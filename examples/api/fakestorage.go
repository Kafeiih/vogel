package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kafeiih/vogel/storage"
)

// fakeStoragePath is where the fake bucket is served, on this example's own
// server and outside /api/v1: a real bucket is its own origin, reached with
// a presigned URL and no bearer token.
const fakeStoragePath = "/fake-storage/"

// fakePresignExpiry stands in for an adapter's configured default lifetime
// (storage/s3 uses Config.PresignExpiry, or s3.DefaultPresignExpiry = 15m).
const fakePresignExpiry = 15 * time.Minute

// fakeMaxPresignExpiry mirrors s3.DefaultMaxPresignExpiry: the fake refuses
// to sign a URL that lives longer, as the storage.Storage contract requires.
const fakeMaxPresignExpiry = time.Hour

// fakeObject is one stored object and the Content-Type it was stored with.
type fakeObject struct {
	data        []byte
	contentType string
}

// fakeStorage implements storage.Storage over a mutex-guarded in-memory map,
// for this example only. It stands in for storage/s3.S3Storage -- swapping
// it out for a real S3 (or MinIO) backed one is a one-line change in
// main.go's composition root.
//
// Unlike a plain map, it also plays the bucket's HTTP endpoint (Handler), so
// the presigned-upload flow runs end to end without a real store: the URLs
// it presigns point at baseURL + fakeStoragePath and carry an HMAC over
// what they allow (method, key, expiry, and the signed Content-Type and
// Content-Length, or the response headers for a GET). Handler refuses any
// request whose signature, expiry, Content-Type or Content-Length does not
// match, the way S3 answers 403 to a request that breaks its SigV4
// signature. The HMAC key is random per process, so URLs die on restart.
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]fakeObject

	baseURL string
	secret  []byte
	now     func() time.Time
}

var _ storage.Storage = (*fakeStorage)(nil)

// newFakeStorage builds a fake whose presigned URLs point at baseURL (for
// example "http://localhost:8080"), where Handler must be mounted under
// fakeStoragePath.
func newFakeStorage(baseURL string) *fakeStorage {
	secret := make([]byte, 32)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(secret)
	return &fakeStorage{
		objects: make(map[string]fakeObject),
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		now:     time.Now,
	}
}

func (s *fakeStorage) Upload(_ context.Context, input *storage.UploadInput) error {
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return fmt.Errorf("fake storage: upload %q: %w", input.Key, err)
	}
	s.put(input.Key, data, input.ContentType)
	return nil
}

func (s *fakeStorage) put(key string, data []byte, contentType string) {
	s.mu.Lock()
	s.objects[key] = fakeObject{data: data, contentType: contentType}
	s.mu.Unlock()
}

func (s *fakeStorage) get(key string) (fakeObject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[key]
	return obj, ok
}

func (s *fakeStorage) Download(_ context.Context, key string) (*storage.DownloadOutput, error) {
	obj, ok := s.get(key)
	if !ok {
		// The port's sentinel, as storage/s3 maps S3's NoSuchKey to it: the
		// validation job checks storage.ErrNotFound and works unchanged with
		// either adapter.
		return nil, fmt.Errorf("fake storage: download %q: %w", key, storage.ErrNotFound)
	}
	return &storage.DownloadOutput{
		Body:        io.NopCloser(bytes.NewReader(obj.data)),
		ContentType: obj.contentType,
	}, nil
}

func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}

// presignExpiry applies the storage.Storage contract's default: a
// non-positive expiry means "use the adapter's configured default"; a
// positive one is returned unchanged, never clamped. expiresAt then refuses
// one above fakeMaxPresignExpiry with storage.ErrPresignExpiryTooLong.
func presignExpiry(expiry time.Duration) time.Duration {
	if expiry <= 0 {
		return fakePresignExpiry
	}
	return expiry
}

// expiresAt resolves expiry to the Unix second the URL dies at, or
// storage.ErrPresignExpiryTooLong.
func (s *fakeStorage) expiresAt(key string, expiry time.Duration) (int64, error) {
	d := presignExpiry(expiry)
	if d > fakeMaxPresignExpiry {
		return 0, fmt.Errorf("fake storage: presign %q for %s (max %s): %w", key, d, fakeMaxPresignExpiry, storage.ErrPresignExpiryTooLong)
	}
	return s.now().Add(d).Unix(), nil
}

func (s *fakeStorage) PresignedGetURL(_ context.Context, key string, expiry time.Duration, opts ...storage.PresignGetOption) (string, error) {
	expires, err := s.expiresAt(key, expiry)
	if err != nil {
		return "", err
	}
	// Fold the options the same way a real adapter does so the URL asks for
	// the disposition and type the frontend would get.
	o := storage.ApplyPresignGetOptions(opts...)
	exp := strconv.FormatInt(expires, 10)
	q := url.Values{}
	q.Set("expires", exp)
	q.Set("disposition", o.ContentDisposition)
	q.Set("response_content_type", o.ContentType)
	q.Set("sig", s.sign(http.MethodGet, key, exp, o.ContentDisposition, o.ContentType))
	return s.objectURL(key, q), nil
}

func (s *fakeStorage) PresignedPutURL(_ context.Context, key string, contentType string, expiry time.Duration, opts ...storage.PresignPutOption) (string, error) {
	o := storage.ApplyPresignPutOptions(opts...)
	if o.HasContentLength && o.ContentLength <= 0 {
		return "", fmt.Errorf("fake storage: presign put %q: %w", key, storage.ErrInvalidContentLength)
	}
	expires, err := s.expiresAt(key, expiry)
	if err != nil {
		return "", err
	}
	exp := strconv.FormatInt(expires, 10)
	length := ""
	if o.HasContentLength {
		length = strconv.FormatInt(o.ContentLength, 10)
	}
	q := url.Values{}
	q.Set("expires", exp)
	q.Set("content_type", contentType)
	if length != "" {
		q.Set("content_length", length)
	}
	q.Set("sig", s.sign(http.MethodPut, key, exp, contentType, length))
	return s.objectURL(key, q), nil
}

// objectURL is baseURL + fakeStoragePath + key, each key segment escaped.
func (s *fakeStorage) objectURL(key string, q url.Values) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return s.baseURL + fakeStoragePath + strings.Join(segments, "/") + "?" + q.Encode()
}

// sign returns the hex HMAC-SHA256 of fields. Each field is length-prefixed
// so no value can shift into its neighbour (for example a content type
// ending in what looks like a length).
func (s *fakeStorage) sign(fields ...string) string {
	mac := hmac.New(sha256.New, s.secret)
	for _, f := range fields {
		_, _ = fmt.Fprintf(mac, "%d:%s;", len(f), f)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// Handler serves the fake bucket: GET and PUT on fakeStoragePath + key,
// authorized only by the presigned query string. It never logs or echoes
// the URL; the access log in front of it records the path only.
func (s *fakeStorage) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, fakeStoragePath)
		if key == "" || key == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPut:
			s.servePut(w, r, key)
		case http.MethodGet:
			s.serveGet(w, r, key)
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// authorize checks the signature over fields and the URL's expiry, writing
// a 403 and returning false when either fails.
func (s *fakeStorage) authorize(w http.ResponseWriter, q url.Values, fields ...string) bool {
	if !hmac.Equal([]byte(q.Get("sig")), []byte(s.sign(fields...))) {
		http.Error(w, "signature does not match", http.StatusForbidden)
		return false
	}
	expires, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err != nil || s.now().Unix() > expires {
		http.Error(w, "request has expired", http.StatusForbidden)
		return false
	}
	return true
}

func (s *fakeStorage) servePut(w http.ResponseWriter, r *http.Request, key string) {
	q := r.URL.Query()
	contentType := q.Get("content_type")
	length := q.Get("content_length")
	if !s.authorize(w, q, http.MethodPut, key, q.Get("expires"), contentType, length) {
		return
	}
	// The signature covers the headers the client must send, as SigV4 does:
	// a request with another Content-Type or Content-Length is refused.
	if r.Header.Get("Content-Type") != contentType {
		http.Error(w, "Content-Type does not match the signed value", http.StatusForbidden)
		return
	}
	limit := int64(maxUploadBytes)
	if length != "" {
		n, err := strconv.ParseInt(length, 10, 64)
		if err != nil || r.ContentLength != n {
			http.Error(w, "Content-Length does not match the signed value", http.StatusForbidden)
			return
		}
		limit = n
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "object too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read the body", http.StatusBadRequest)
		return
	}
	if length != "" && int64(len(data)) != limit {
		http.Error(w, "body does not match the signed Content-Length", http.StatusForbidden)
		return
	}
	s.put(key, data, contentType)
	w.WriteHeader(http.StatusOK)
}

func (s *fakeStorage) serveGet(w http.ResponseWriter, r *http.Request, key string) {
	q := r.URL.Query()
	disposition := q.Get("disposition")
	contentType := q.Get("response_content_type")
	if !s.authorize(w, q, http.MethodGet, key, q.Get("expires"), disposition, contentType) {
		return
	}
	obj, ok := s.get(key)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(obj.data)
}
