package main

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestPresignExpiry(t *testing.T) {
	tests := []struct {
		name   string
		expiry time.Duration
		want   time.Duration
	}{
		{name: "zero uses the configured default", expiry: 0, want: fakePresignExpiry},
		{name: "negative uses the configured default", expiry: -time.Second, want: fakePresignExpiry},
		{name: "positive is used as is", expiry: 2 * time.Minute, want: 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := presignExpiry(tt.expiry); got != tt.want {
				t.Errorf("presignExpiry(%v) = %v, want %v", tt.expiry, got, tt.want)
			}
		})
	}
	if fakePresignExpiry != 15*time.Minute {
		t.Errorf("fakePresignExpiry = %v, want 15m to mirror s3.DefaultPresignExpiry", fakePresignExpiry)
	}
}

// TestFakeStorage_PresignedURLsHonorExpiry checks that both presigned URL
// methods route their expiry through presignExpiry, by reading the expires
// timestamp back out of the URL.
func TestFakeStorage_PresignedURLsHonorExpiry(t *testing.T) {
	s := newFakeStorage(testFakeBase)
	methods := []struct {
		name    string
		presign func(time.Duration) (string, error)
	}{
		{name: "get", presign: func(d time.Duration) (string, error) {
			return s.PresignedGetURL(context.Background(), "k", d)
		}},
		{name: "put", presign: func(d time.Duration) (string, error) {
			return s.PresignedPutURL(context.Background(), "k", "text/plain", d)
		}},
	}
	expiries := []struct {
		name   string
		expiry time.Duration
		want   time.Duration
	}{
		{name: "default", expiry: 0, want: fakePresignExpiry},
		{name: "explicit", expiry: time.Hour, want: time.Hour},
	}
	for _, m := range methods {
		for _, e := range expiries {
			t.Run(m.name+"/"+e.name, func(t *testing.T) {
				before := time.Now()
				raw, err := m.presign(e.expiry)
				if err != nil {
					t.Fatalf("presign: %v", err)
				}
				after := time.Now()

				u, err := url.Parse(raw)
				if err != nil {
					t.Fatalf("url.Parse(%q): %v", raw, err)
				}
				exp, err := strconv.ParseInt(u.Query().Get("expires"), 10, 64)
				if err != nil {
					t.Fatalf("expires in %q: %v", raw, err)
				}
				// Unix() truncates to the second, so allow one second of slack
				// below the lower bound.
				lo := before.Add(e.want).Unix() - 1
				hi := after.Add(e.want).Unix()
				if exp < lo || exp > hi {
					t.Errorf("expires = %d, want within [%d, %d]", exp, lo, hi)
				}
			})
		}
	}
}
