package s3

import (
	"log/slog"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

func TestNewS3Storage_HTTPTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "zero uses the default", timeout: 0, want: DefaultHTTPTimeout},
		{name: "explicit override", timeout: 5 * time.Minute, want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewS3Storage(Config{Bucket: "b", Region: "us-east-1", HTTPTimeout: tt.timeout}, slog.Default())
			if err != nil {
				t.Fatalf("NewS3Storage: %v", err)
			}
			hc, ok := s.client.Options().HTTPClient.(*awshttp.BuildableClient)
			if !ok {
				t.Fatalf("HTTPClient = %T, want *awshttp.BuildableClient (keeps the SDK transport defaults)", s.client.Options().HTTPClient)
			}
			if got := hc.GetTimeout(); got != tt.want {
				t.Errorf("HTTPClient timeout = %v, want %v", got, tt.want)
			}
		})
	}
	if DefaultHTTPTimeout != 60*time.Second {
		t.Errorf("DefaultHTTPTimeout = %v, want 60s", DefaultHTTPTimeout)
	}
}

func TestNewS3Storage_NegativeHTTPTimeout_ReturnsError(t *testing.T) {
	if _, err := NewS3Storage(Config{Bucket: "b", HTTPTimeout: -time.Second}, slog.Default()); err == nil {
		t.Fatal("expected an error for a negative HTTPTimeout, got nil")
	}
}
