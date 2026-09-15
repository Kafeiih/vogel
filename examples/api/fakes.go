package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	"github.com/kafeiih/vogel/notification"
	"github.com/kafeiih/vogel/storage"
)

// fakeAuthenticator implements auth.Authenticator in memory, for this
// example only. It stands in for auth/zitadel.Authenticator -- swapping it
// out for a real Zitadel-backed one is a one-line change in main.go's
// composition root, because every downstream consumer (the Authenticate
// middleware, RequirePermission, audit.Recorder) depends only on the
// auth.Authenticator port, never on this type.
//
// Tokens are of the form "<userID>:<username>:<role1,role2>", e.g.
// "u1:alice:author,reviewer". An empty or malformed token is unauthenticated.
type fakeAuthenticator struct{}

var _ auth.Authenticator = (*fakeAuthenticator)(nil)

func (fakeAuthenticator) Authenticate(_ context.Context, token string) (*auth.Principal, error) {
	parts := strings.SplitN(token, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return nil, auth.ErrUnauthenticated
	}
	var roles []string
	if parts[2] != "" {
		roles = strings.Split(parts[2], ",")
	}
	return &auth.Principal{UserID: parts[0], Username: parts[1], Roles: roles}, nil
}

// fakeChecker implements authz.Checker in memory, for this example only. It
// stands in for authz/cerbos.Checker -- swapping it out for a real
// Cerbos-backed one is a one-line change in main.go's composition root.
//
// Policy: a principal with the "admin" role is allowed everything. Every
// other principal is allowed "read" actions unconditionally, and "delete"
// only on a document it owns AND that is not a "report" -- see the "delete"
// case below and DocumentHandler.Delete for why that second condition is
// the one that actually needs access.Guard's per-instance check rather than
// RequirePermission's coarse one.
type fakeChecker struct{}

var _ authz.Checker = (*fakeChecker)(nil)

func (fakeChecker) IsAllowed(_ context.Context, p authz.Principal, res authz.Resource, action string) (bool, error) {
	for _, role := range p.Roles {
		if role == "admin" {
			return true, nil
		}
	}

	if action == "delete" {
		// "report" documents may only be deleted by an admin (handled by the
		// short-circuit above), regardless of ownership -- res.Attr["kind"]
		// is only populated once DocumentHandler.Delete has actually loaded
		// the row, which is exactly the information RequirePermission's
		// coarse, pre-load check could never have supplied.
		if kind, _ := res.Attr["kind"].(string); kind == "report" {
			return false, nil
		}
		// owned_document_ids comes from access.PrincipalAttributes, resolved
		// once per request from DocumentStore.OwnedIDs -- see main.go.
		owned, _ := p.Attr["owned_document_ids"].([]string)
		return slices.Contains(owned, res.ID), nil
	}

	return action == "read", nil
}

// fakeStorage implements storage.Storage over a mutex-guarded in-memory map,
// for this example only. It stands in for storage/s3.S3Storage -- swapping
// it out for a real S3 (or MinIO) backed one is a one-line change in
// main.go's composition root.
type fakeStorage struct {
	mu    sync.Mutex
	files map[string][]byte
}

var _ storage.Storage = (*fakeStorage)(nil)

func newFakeStorage() *fakeStorage {
	return &fakeStorage{files: make(map[string][]byte)}
}

func (s *fakeStorage) Upload(_ context.Context, input *storage.UploadInput) error {
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return fmt.Errorf("fake storage: upload %q: %w", input.Key, err)
	}
	s.mu.Lock()
	s.files[input.Key] = data
	s.mu.Unlock()
	return nil
}

func (s *fakeStorage) Download(_ context.Context, key string) (*storage.DownloadOutput, error) {
	s.mu.Lock()
	data, ok := s.files[key]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake storage: download %q: not found", key)
	}
	return &storage.DownloadOutput{
		Body:        io.NopCloser(bytes.NewReader(data)),
		ContentType: "application/octet-stream",
	}, nil
}

func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.files, key)
	s.mu.Unlock()
	return nil
}

func (s *fakeStorage) PresignedGetURL(_ context.Context, key string, expiry time.Duration) (string, error) {
	return fmt.Sprintf("http://localhost/fake-storage/%s?expires=%d", key, time.Now().Add(expiry).Unix()), nil
}

func (s *fakeStorage) PresignedPutURL(_ context.Context, key string, _ string, expiry time.Duration) (string, error) {
	return fmt.Sprintf("http://localhost/fake-storage/%s?expires=%d", key, time.Now().Add(expiry).Unix()), nil
}

// fakeNotifier implements notification.Notifier by logging the message
// instead of sending it, for this example only. It stands in for
// notification/smtp.SMTPNotifier or notification/sendgrid.SendGridNotifier
// -- swapping it out for a real one is a one-line change in main.go's
// composition root.
type fakeNotifier struct {
	logger *slog.Logger
}

var _ notification.Notifier = (*fakeNotifier)(nil)

func (n *fakeNotifier) Send(ctx context.Context, msg *notification.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	n.logger.InfoContext(ctx, "fake notifier: email not actually sent",
		"to", msg.To, "from", msg.From, "subject", msg.Subject)
	return nil
}
