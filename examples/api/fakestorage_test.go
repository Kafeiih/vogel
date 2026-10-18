package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/storage"
)

const testFakeBase = "http://storage.test"

// sendSigned issues method against the fake bucket's handler at the
// presigned rawURL, the way a browser would follow it: the URL's path and
// query go to the handler, the headers and body are the client's.
func sendSigned(t *testing.T, s *fakeStorage, method, rawURL, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	var reader io.Reader
	if method == http.MethodPut {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, u.RequestURI(), reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// tamper returns rawURL with query parameter name set to value, keeping the
// original signature.
func tamper(t *testing.T, rawURL, name, value string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	q := u.Query()
	q.Set(name, value)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestFakeStorage_PresignedPutURLReflectsWhatItSigns(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	raw, err := s.PresignedPutURL(context.Background(), "documents/a/uploads/b", "application/pdf", 5*time.Minute, storage.WithContentLength(42))
	if err != nil {
		t.Fatalf("PresignedPutURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if got := u.Scheme + "://" + u.Host; got != testFakeBase {
		t.Errorf("URL origin = %q, want the configured base %q", got, testFakeBase)
	}
	if u.Path != "/fake-storage/documents/a/uploads/b" {
		t.Errorf("URL path = %q, want /fake-storage/documents/a/uploads/b", u.Path)
	}
	q := u.Query()
	if q.Get("content_type") != "application/pdf" || q.Get("content_length") != "42" || q.Get("sig") == "" {
		t.Errorf("query = %v, want content_type=application/pdf, content_length=42 and a signature", q)
	}
}

func TestFakeStorage_PresignRejectsWhatTheContractRejects(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	ctx := context.Background()

	if _, err := s.PresignedPutURL(ctx, "k", "text/plain", time.Minute, storage.WithContentLength(0)); !errors.Is(err, storage.ErrInvalidContentLength) {
		t.Errorf("PresignedPutURL with length 0 error = %v, want ErrInvalidContentLength", err)
	}
	if _, err := s.PresignedPutURL(ctx, "k", "text/plain", fakeMaxPresignExpiry+time.Second); !errors.Is(err, storage.ErrPresignExpiryTooLong) {
		t.Errorf("PresignedPutURL above the maximum error = %v, want ErrPresignExpiryTooLong", err)
	}
	if _, err := s.PresignedGetURL(ctx, "k", fakeMaxPresignExpiry+time.Second); !errors.Is(err, storage.ErrPresignExpiryTooLong) {
		t.Errorf("PresignedGetURL above the maximum error = %v, want ErrPresignExpiryTooLong", err)
	}
}

// The bucket endpoint accepts a PUT only when it carries a valid, unexpired
// signature and exactly the Content-Type and Content-Length that were signed.
func TestFakeStorage_HandlerEnforcesTheSignedPut(t *testing.T) {
	const key = "documents/a/uploads/b"
	const body = "hello"

	tests := []struct {
		name        string
		mutateURL   func(t *testing.T, raw string) string
		contentType string
		body        string
		advance     time.Duration
		wantStatus  int
	}{
		{name: "signed request is stored", contentType: "text/plain", body: body, wantStatus: http.StatusOK},
		{name: "bad signature is 403", mutateURL: func(t *testing.T, raw string) string { return tamper(t, raw, "sig", strings.Repeat("0", 64)) }, contentType: "text/plain", body: body, wantStatus: http.StatusForbidden},
		{name: "a widened signed length is 403", mutateURL: func(t *testing.T, raw string) string { return tamper(t, raw, "content_length", "999") }, contentType: "text/plain", body: body, wantStatus: http.StatusForbidden},
		{name: "a key moved to another document is 403", mutateURL: func(_ *testing.T, raw string) string { return strings.Replace(raw, "documents/a/", "documents/z/", 1) }, contentType: "text/plain", body: body, wantStatus: http.StatusForbidden},
		{name: "expired URL is 403", contentType: "text/plain", body: body, advance: 6 * time.Minute, wantStatus: http.StatusForbidden},
		{name: "wrong Content-Type is 403", contentType: "text/html", body: body, wantStatus: http.StatusForbidden},
		{name: "missing Content-Type is 403", contentType: "", body: body, wantStatus: http.StatusForbidden},
		{name: "longer body than signed is 403", contentType: "text/plain", body: body + "!", wantStatus: http.StatusForbidden},
		{name: "shorter body than signed is 403", contentType: "text/plain", body: "hell", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeStorage(testFakeBase)
			start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return start }

			raw, err := s.PresignedPutURL(context.Background(), key, "text/plain", 5*time.Minute, storage.WithContentLength(int64(len(body))))
			if err != nil {
				t.Fatalf("PresignedPutURL: %v", err)
			}
			if tt.mutateURL != nil {
				raw = tt.mutateURL(t, raw)
			}
			s.now = func() time.Time { return start.Add(tt.advance) }

			rec := sendSigned(t, s, http.MethodPut, raw, tt.contentType, tt.body)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			out, dlErr := s.Download(context.Background(), key)
			if tt.wantStatus != http.StatusOK {
				if dlErr == nil {
					t.Error("a rejected PUT stored the object anyway")
				}
				return
			}
			if dlErr != nil {
				t.Fatalf("Download after a stored PUT: %v", dlErr)
			}
			defer func() { _ = out.Body.Close() }()
			got, _ := io.ReadAll(out.Body)
			if string(got) != body || out.ContentType != "text/plain" {
				t.Errorf("stored %q as %q, want %q as text/plain", got, out.ContentType, body)
			}
		})
	}
}

// A PUT URL signed without a length still cannot store more than
// maxUploadBytes: the handler bounds the body like a bucket policy would.
func TestFakeStorage_HandlerBoundsAnUnsignedLength(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	raw, err := s.PresignedPutURL(context.Background(), "k", "text/plain", time.Minute)
	if err != nil {
		t.Fatalf("PresignedPutURL: %v", err)
	}
	rec := sendSigned(t, s, http.MethodPut, raw, "text/plain", strings.Repeat("a", maxUploadBytes+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if _, err := s.Download(context.Background(), "k"); err == nil {
		t.Error("an oversized PUT stored the object anyway")
	}
}

func TestFakeStorage_HandlerServesTheSignedGet(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	ctx := context.Background()
	if err := s.Upload(ctx, &storage.UploadInput{Key: "k", Body: strings.NewReader("%PDF-1.4"), ContentType: "text/html"}); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	raw, err := s.PresignedGetURL(ctx, "k", time.Minute, storage.WithResponseContentType("application/pdf"))
	if err != nil {
		t.Fatalf("PresignedGetURL: %v", err)
	}
	rec := sendSigned(t, s, http.MethodGet, raw, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "%PDF-1.4" {
		t.Errorf("body = %q, want the stored object", rec.Body.String())
	}
	// The signed type wins over the stored one, and the default disposition
	// is attachment.
	if got := rec.Header().Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type = %q, want the signed application/pdf", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != storage.DefaultContentDisposition {
		t.Errorf("Content-Disposition = %q, want %q", got, storage.DefaultContentDisposition)
	}

	if rec := sendSigned(t, s, http.MethodGet, tamper(t, raw, "response_content_type", "text/html"), "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("GET with a tampered response type = %d, want 403", rec.Code)
	}
	if rec := sendSigned(t, s, http.MethodGet, strings.Replace(raw, "/fake-storage/k", "/fake-storage/missing", 1), "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("GET of another key with this signature = %d, want 403", rec.Code)
	}

	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if rec := sendSigned(t, s, http.MethodGet, raw, "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("expired GET = %d, want 403", rec.Code)
	}
}

func TestFakeStorage_HandlerRejectsOtherMethodsAndMissingObjects(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	raw, err := s.PresignedGetURL(context.Background(), "nothing-here", time.Minute)
	if err != nil {
		t.Fatalf("PresignedGetURL: %v", err)
	}
	if rec := sendSigned(t, s, http.MethodGet, raw, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("signed GET of a missing object = %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodDelete, "/fake-storage/k", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want 405", rec.Code)
	}
}

// Download of a key that was never stored wraps errObjectNotFound, so the
// validation job can tell "the client never uploaded" from a transient
// failure worth retrying.
func TestFakeStorage_DownloadMissingIsObjectNotFound(t *testing.T) {
	_, err := newFakeStorage(testFakeBase).Download(context.Background(), "nope")
	if !errors.Is(err, errObjectNotFound) {
		t.Errorf("Download error = %v, want errObjectNotFound", err)
	}
}

// The expires parameter is Unix seconds, so the URL shows its own lifetime.
func TestFakeStorage_ExpiresIsUnixSeconds(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return start }
	raw, err := s.PresignedGetURL(context.Background(), "k", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignedGetURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if got := u.Query().Get("expires"); got != strconv.FormatInt(start.Add(5*time.Minute).Unix(), 10) {
		t.Errorf("expires = %s, want %d", got, start.Add(5*time.Minute).Unix())
	}
}
