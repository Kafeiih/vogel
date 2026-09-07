package cerbos

import "testing"

func TestNew_EmptyHost_ReturnsError(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("expected error for empty host, got nil")
	}
}

func TestChecker_Close_IsANoOpThatReturnsNil(t *testing.T) {
	c, err := New(Config{Host: "localhost:3593", UseTLS: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}
