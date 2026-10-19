package logger

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNew_RedactsSensitiveTopLevelKeys_JSON(t *testing.T) {
	var buf bytes.Buffer
	New("production", &buf).Info("login",
		"password", "hunter2",
		"Authorization", "Bearer abc",
		"x_api_key", "k-1",
		"db_password", "pw",
		"refreshToken", "rt-1",
		"username", "alice",
	)

	decoded := decodeJSONLine(t, &buf)
	for _, key := range []string{"password", "Authorization", "x_api_key", "db_password", "refreshToken"} {
		if decoded[key] != RedactedValue {
			t.Errorf("%s = %v, want %q", key, decoded[key], RedactedValue)
		}
	}
	if decoded["username"] != "alice" {
		t.Errorf("username = %v, want %q", decoded["username"], "alice")
	}
	for _, secret := range []string{"hunter2", "Bearer abc", "k-1", "rt-1"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("line %q leaks %q", buf.String(), secret)
		}
	}
}

func TestNew_RedactsSensitiveTopLevelKeys_Text(t *testing.T) {
	var buf bytes.Buffer
	New("development", &buf).Info("login", "X-Api-Key", "k-1", "token", "t-1", "username", "alice")

	line := buf.String()
	for _, want := range []string{"X-Api-Key=[REDACTED]", "token=[REDACTED]", "username=alice"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q does not contain %q", line, want)
		}
	}
	for _, secret := range []string{"k-1", "t-1"} {
		if strings.Contains(line, secret) {
			t.Errorf("line %q leaks %q", line, secret)
		}
	}
}

// The matching rules are audit's: case-insensitive, ignoring '_', '-', '.'
// and spaces, and substring. TestDefaultRedactedKeys_MatchAudit in package
// audit pins the default list itself.
func TestRedaction_MatchingRules(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"Authorization", true},
		{"x_api_key", true},
		{"X-Api-Key", true},
		{"apiKey", true},
		{"db_password", true},
		{"access.token", true},
		{"client secret", true},
		{"private-key", true},
		{"aws_credentials", true},
		{"Set-Cookie", true},
		{"passwd", true},
		{"session_id", false},
		{"username", false},
		{"email", false},
		{"api", false},
		{"key", false},
	}
	m := newKeyMatcher(DefaultRedactedKeys())
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := m.matches(tt.key); got != tt.want {
				t.Errorf("matches(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestNew_RedactsAttrsBoundWithWith(t *testing.T) {
	for _, env := range []string{"production", "development"} {
		t.Run(env, func(t *testing.T) {
			var buf bytes.Buffer
			New(env, &buf).With("api_key", "k-1", "component", "billing").Info("hello")

			if strings.Contains(buf.String(), "k-1") {
				t.Errorf("line %q leaks the bound api_key", buf.String())
			}
			if !strings.Contains(buf.String(), RedactedValue) || !strings.Contains(buf.String(), "billing") {
				t.Errorf("line %q: want api_key redacted and component kept", buf.String())
			}
		})
	}
}

func TestNew_RedactsInsideGroups_JSON(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	l.Logger.WithGroup("req").With("secret", "s-1", "path", "/a").Info("hello",
		slog.Group("headers", slog.String("Cookie", "c-1"), slog.String("Accept", "json"),
			slog.Group("deep", slog.String("private_key", "pk-1"))),
	)

	decoded := decodeJSONLine(t, &buf)
	req, _ := decoded["req"].(map[string]any)
	if req["secret"] != RedactedValue || req["path"] != "/a" {
		t.Errorf("req = %v, want secret redacted and path kept", req)
	}
	headers, _ := req["headers"].(map[string]any)
	if headers["Cookie"] != RedactedValue || headers["Accept"] != "json" {
		t.Errorf("headers = %v, want Cookie redacted and Accept kept", headers)
	}
	deep, _ := headers["deep"].(map[string]any)
	if deep["private_key"] != RedactedValue {
		t.Errorf("deep = %v, want private_key redacted", deep)
	}
}

func TestNew_RedactsInsideGroups_Text(t *testing.T) {
	var buf bytes.Buffer
	New("development", &buf).Logger.WithGroup("req").With("secret", "s-1").Info("hello",
		slog.Group("headers", slog.String("Cookie", "c-1"), slog.String("Accept", "json")))

	line := buf.String()
	for _, want := range []string{"req.secret=[REDACTED]", "req.headers.Cookie=[REDACTED]", "req.headers.Accept=json"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q does not contain %q", line, want)
		}
	}
}

// A sensitive key whose value is a group is redacted as a whole, as audit
// redacts a whole map under a sensitive key: every attribute inside it is
// replaced, whatever its own key.
func TestNew_RedactsEverythingUnderASensitiveGroup(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	l.Info("hello", slog.Group("credentials", slog.String("user", "alice"), slog.String("pass", "p-1")))
	l.Logger.WithGroup("auth_token").Info("hello", "value", "t-1")

	if strings.Contains(buf.String(), "alice") || strings.Contains(buf.String(), "p-1") || strings.Contains(buf.String(), "t-1") {
		t.Errorf("output %q leaks a value under a sensitive group", buf.String())
	}
}

func TestNew_WithRedactedKeys_AddsToTheDefaults(t *testing.T) {
	var buf bytes.Buffer
	New("production", &buf, WithRedactedKeys("ssn", "session")).Info("hello",
		"customer_SSN", "123", "session_id", "sid-1", "password", "pw", "email", "a@b.c")

	decoded := decodeJSONLine(t, &buf)
	for _, key := range []string{"customer_SSN", "session_id", "password"} {
		if decoded[key] != RedactedValue {
			t.Errorf("%s = %v, want %q", key, decoded[key], RedactedValue)
		}
	}
	if decoded["email"] != "a@b.c" {
		t.Errorf("email = %v, want it kept", decoded["email"])
	}
}

// Built-in keys are never redacted, even by a denylist entry that matches
// them, so a log line keeps its time, level and message.
func TestNew_WithRedactedKeys_NeverRedactsBuiltInKeys(t *testing.T) {
	var buf bytes.Buffer
	New("production", &buf, WithRedactedKeys("e", "msg", "")).Info("hello")

	decoded := decodeJSONLine(t, &buf)
	if decoded["msg"] != "hello" || decoded["level"] != "INFO" {
		t.Errorf("line = %v, want msg and level kept", decoded)
	}
	if _, err := time.Parse(time.RFC3339Nano, decoded["time"].(string)); err != nil {
		t.Errorf("time = %v, want a timestamp: %v", decoded["time"], err)
	}
}

func TestDefaultRedactedKeys_ReturnsACopy(t *testing.T) {
	keys := DefaultRedactedKeys()
	keys[0] = "mutated"
	if slices.Contains(DefaultRedactedKeys(), "mutated") {
		t.Error("mutating the returned slice changed the defaults")
	}
}

func TestNew_Redaction_KeepsContextKeysAndUTC(t *testing.T) {
	withLocalZone(t)
	var buf bytes.Buffer
	New("production", &buf).InfoContext(requestContext("req-1", "user-1"), "hello", "token", "t-1")

	decoded := decodeJSONLine(t, &buf)
	if decoded["request_id"] != "req-1" || decoded["user_id"] != "user-1" {
		t.Errorf("line = %v, want request_id and user_id from the context", decoded)
	}
	if decoded["token"] != RedactedValue {
		t.Errorf("token = %v, want %q", decoded["token"], RedactedValue)
	}
	ts, _ := decoded["time"].(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("time = %q, want UTC (Z suffix)", ts)
	}
}

// A record with no sensitive key renders byte for byte as it did before
// redaction existed: a contextHandler over a handler whose only ReplaceAttr
// is utcTime.
func TestNew_NonSensitiveRecord_IsByteIdenticalToBefore(t *testing.T) {
	ts := time.Date(2026, 10, 18, 12, 0, 0, 0, time.FixedZone("x", 3*3600))
	for _, env := range []string{"production", "development"} {
		t.Run(env, func(t *testing.T) {
			var before, after bytes.Buffer
			opts := &slog.HandlerOptions{Level: getLogLevel(env), ReplaceAttr: utcTime}
			var baseline slog.Handler = slog.NewTextHandler(&before, opts)
			if env == "production" {
				baseline = slog.NewJSONHandler(&before, opts)
			}
			handlers := []slog.Handler{&contextHandler{inner: baseline}, New(env, &after).Handler()}

			for _, h := range handlers {
				r := slog.NewRecord(ts, slog.LevelInfo, "hello", 0)
				r.AddAttrs(slog.String("component", "billing"), slog.Int("count", 3),
					slog.Group("http", slog.String("path", "/a"), slog.Int("status", 200)))
				h = h.WithAttrs([]slog.Attr{slog.String("service", "api")}).WithGroup("g")
				if err := h.Handle(requestContext("req-1", "user-1"), r); err != nil {
					t.Fatalf("Handle: %v", err)
				}
			}
			if before.String() != after.String() {
				t.Errorf("output changed:\nbefore %q\nafter  %q", before.String(), after.String())
			}
		})
	}
}
