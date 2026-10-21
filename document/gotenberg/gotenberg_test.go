package gotenberg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/document/gotenberg"
)

var _ document.Renderer = (*gotenberg.Renderer)(nil)

func TestRender_SendsChromiumHTMLMultipartRequest(t *testing.T) {
	html := []byte("<html><body>hola</body></html>")
	pdf := []byte("%PDF-1.7 rendered")

	var gotPath, gotMethod, gotFilename string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		require.NoError(t, r.ParseMultipartForm(1<<20))
		files := r.MultipartForm.File["files"]
		require.Len(t, files, 1)
		gotFilename = files[0].Filename
		f, err := files[0].Open()
		require.NoError(t, err)
		defer f.Close()
		gotFile, _ = io.ReadAll(f)
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	}))
	defer srv.Close()

	r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL + "/", HTTPClient: srv.Client()})
	require.NoError(t, err)

	out, err := r.Render(context.Background(), html)
	require.NoError(t, err)
	assert.Equal(t, pdf, out)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/forms/chromium/convert/html", gotPath)
	assert.Equal(t, "index.html", gotFilename)
	assert.True(t, bytes.Equal(html, gotFile))
}

func TestRender_Non2xx_ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Invalid HTTP status code from the main page", http.StatusConflict)
	}))
	defer srv.Close()

	r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = r.Render(context.Background(), []byte("<p>x</p>"))
	require.ErrorIs(t, err, gotenberg.ErrUnexpectedStatus)
	assert.Contains(t, err.Error(), "409")
	assert.Contains(t, err.Error(), "Invalid HTTP status code from the main page")
}

func TestRender_OversizedResponse_ReturnsErrResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 11)))
	}))
	defer srv.Close()

	r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL, MaxResponseBytes: 10})
	require.NoError(t, err)

	_, err = r.Render(context.Background(), []byte("<p>x</p>"))
	require.ErrorIs(t, err, gotenberg.ErrResponseTooLarge)
}

func TestRender_ResponseAtLimit_IsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 10)))
	}))
	defer srv.Close()

	r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL, MaxResponseBytes: 10})
	require.NoError(t, err)

	out, err := r.Render(context.Background(), []byte("<p>x</p>"))
	require.NoError(t, err)
	assert.Len(t, out, 10)
}

func TestRender_Timeout_ReturnsError(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	require.NoError(t, err)

	_, err = r.Render(context.Background(), []byte("<p>x</p>"))
	require.Error(t, err)
}

func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	for _, base := range []string{"", "localhost:3000", "ftp://gotenberg", "http://"} {
		_, err := gotenberg.New(gotenberg.Config{BaseURL: base})
		assert.Error(t, err, base)
	}
}

func TestRender_StatusDecidesWhetherARetryCanHelp(t *testing.T) {
	for _, tc := range []struct {
		status   int
		rejected bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusForbidden, true},
		{http.StatusConflict, true},
		{http.StatusRequestEntityTooLarge, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusUnauthorized, false},
		{http.StatusNotFound, false},
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "nope", tc.status)
			}))
			defer srv.Close()
			r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL})
			require.NoError(t, err)

			_, err = r.Render(context.Background(), []byte("<p>x</p>"))

			require.ErrorIs(t, err, gotenberg.ErrUnexpectedStatus)
			assert.Equal(t, tc.rejected, errors.Is(err, document.ErrRenderRejected))
		})
	}
}

func TestRender_OversizedResponse_IsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 11)))
	}))
	defer srv.Close()
	r, err := gotenberg.New(gotenberg.Config{BaseURL: srv.URL, MaxResponseBytes: 10})
	require.NoError(t, err)

	_, err = r.Render(context.Background(), []byte("<p>x</p>"))

	require.ErrorIs(t, err, gotenberg.ErrResponseTooLarge)
	assert.ErrorIs(t, err, document.ErrRenderRejected)
	assert.Equal(t, "gotenberg: response exceeds the configured size limit (10 bytes)", err.Error())
}
