package s3

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

func TestNewS3Storage_ResponseHeaderTimeout(t *testing.T) {
	tests := []struct {
		name        string
		timeout     time.Duration
		wantHeader  time.Duration
		wantDial    time.Duration
		wantTLSHand time.Duration
	}{
		{
			name:        "zero uses the default and keeps the SDK connect bounds",
			timeout:     0,
			wantHeader:  DefaultResponseHeaderTimeout,
			wantDial:    awshttp.DefaultDialConnectTimeout,
			wantTLSHand: awshttp.DefaultHTTPTransportTLSHandleshakeTimeout,
		},
		{
			name:        "a larger override only raises the header wait",
			timeout:     5 * time.Minute,
			wantHeader:  5 * time.Minute,
			wantDial:    awshttp.DefaultDialConnectTimeout,
			wantTLSHand: awshttp.DefaultHTTPTransportTLSHandleshakeTimeout,
		},
		{
			name:        "a smaller override also tightens dial and TLS handshake",
			timeout:     2 * time.Second,
			wantHeader:  2 * time.Second,
			wantDial:    2 * time.Second,
			wantTLSHand: 2 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewS3Storage(Config{Bucket: "b", Region: "us-east-1", ResponseHeaderTimeout: tt.timeout}, slog.Default())
			if err != nil {
				t.Fatalf("NewS3Storage: %v", err)
			}
			hc, ok := s.client.Options().HTTPClient.(*awshttp.BuildableClient)
			if !ok {
				t.Fatalf("HTTPClient = %T, want *awshttp.BuildableClient (keeps the SDK transport defaults)", s.client.Options().HTTPClient)
			}
			if got := hc.GetTimeout(); got != 0 {
				t.Errorf("http.Client.Timeout = %v, want 0 (a whole-request timeout would cap body streaming)", got)
			}
			tr := hc.GetTransport()
			if tr.ResponseHeaderTimeout != tt.wantHeader {
				t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, tt.wantHeader)
			}
			if tr.TLSHandshakeTimeout != tt.wantTLSHand {
				t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, tt.wantTLSHand)
			}
			if got := hc.GetDialer().Timeout; got != tt.wantDial {
				t.Errorf("dialer Timeout = %v, want %v", got, tt.wantDial)
			}
		})
	}
	if DefaultResponseHeaderTimeout != 60*time.Second {
		t.Errorf("DefaultResponseHeaderTimeout = %v, want 60s", DefaultResponseHeaderTimeout)
	}
}

func TestNewS3Storage_NegativeResponseHeaderTimeout_ReturnsError(t *testing.T) {
	if _, err := NewS3Storage(Config{Bucket: "b", ResponseHeaderTimeout: -time.Second}, slog.Default()); err == nil {
		t.Fatal("expected an error for a negative ResponseHeaderTimeout, got nil")
	}
}

// newEndpointStorage builds an adapter pointed at a local httptest endpoint.
func newEndpointStorage(t *testing.T, endpoint string, bound time.Duration) *S3Storage {
	t.Helper()
	s, err := NewS3Storage(Config{
		Bucket:                "docs",
		Region:                "us-east-1",
		Endpoint:              endpoint,
		AccessKey:             "test-access-key",
		SecretKey:             "test-secret-key",
		PathStyle:             true,
		ResponseHeaderTimeout: bound,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return s
}

// A healthy endpoint that streams a body for longer than the bound must not
// be cut off: the bound covers the wait for headers, not the transfer.
func TestDownload_SlowBodyLongerThanBound_Succeeds(t *testing.T) {
	const bound = 200 * time.Millisecond
	chunks := []string{"alpha-", "bravo-", "charlie-", "delta"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Length", "25")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for _, c := range chunks {
			time.Sleep(bound / 2)
			_, _ = io.WriteString(w, c)
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	s := newEndpointStorage(t, srv.URL, bound)
	out, err := s.Download(context.Background(), "report.pdf")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer out.Body.Close()

	start := time.Now()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("reading a body streamed for %v with a %v bound: %v", time.Since(start), bound, err)
	}
	if want := strings.Join(chunks, ""); string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if time.Since(start) <= bound {
		t.Fatalf("body streamed in %v, test needs it to outlast the %v bound", time.Since(start), bound)
	}
}

// An endpoint that accepts the request but never sends headers fails once the
// bound expires instead of hanging a caller whose context has no deadline.
func TestDownload_StalledHeaders_FailsAfterBound(t *testing.T) {
	const bound = 100 * time.Millisecond
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	s := newEndpointStorage(t, srv.URL, bound)
	done := make(chan error, 1)
	go func() {
		_, err := s.Download(context.Background(), "report.pdf")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Download succeeded against an endpoint that never sends headers")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Download did not return: the header wait is unbounded")
	}
}
