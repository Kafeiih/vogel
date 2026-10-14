package storage

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

var pngHeader = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0x0D, 'I', 'H', 'D', 'R'}

func TestSniffContentType(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 2000)...)
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")

	tests := []struct {
		name     string
		content  []byte
		allowed  []string
		wantType string
		wantErr  bool
	}{
		{name: "allowed type longer than the sniff window", content: pdf, allowed: []string{"application/pdf"}, wantType: "application/pdf"},
		{name: "allowed binary type", content: pngHeader, allowed: []string{"application/pdf", "image/png"}, wantType: "image/png"},
		{name: "detected parameters are ignored when matching", content: []byte("hello"), allowed: []string{"text/plain"}, wantType: "text/plain; charset=utf-8"},
		{name: "allowlist parameters and case are ignored", content: []byte("hello"), allowed: []string{"Text/Plain; charset=iso-8859-1"}, wantType: "text/plain; charset=utf-8"},
		{name: "short input is sniffed", content: []byte("%PDF-"), allowed: []string{"application/pdf"}, wantType: "application/pdf"},
		{name: "disallowed type is rejected", content: html, allowed: []string{"application/pdf", "text/plain"}, wantType: "text/html; charset=utf-8", wantErr: true},
		{name: "empty allowlist rejects everything", content: pdf, allowed: nil, wantType: "application/pdf", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, body, err := SniffContentType(bytes.NewReader(tt.content), tt.allowed...)
			if got != tt.wantType {
				t.Errorf("content type = %q, want %q", got, tt.wantType)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrContentTypeNotAllowed) {
					t.Fatalf("err = %v, want ErrContentTypeNotAllowed", err)
				}
				if body != nil {
					t.Error("body is non-nil on rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			full, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if !bytes.Equal(full, tt.content) {
				t.Errorf("body has %d bytes, want the full %d-byte content", len(full), len(tt.content))
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestSniffContentType_ReadErrorIsReturned(t *testing.T) {
	_, body, err := SniffContentType(failingReader{}, "application/pdf")
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("err = %v, want the read error", err)
	}
	if errors.Is(err, ErrContentTypeNotAllowed) {
		t.Error("a read error must not look like a disallowed type")
	}
	if body != nil {
		t.Error("body is non-nil on a read error")
	}
}
