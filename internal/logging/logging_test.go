package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
)

const (
	keyPassword = "password"
	keyUser     = "user"
	keyTS       = "ts"
	keyLevel    = "level"
	keyMsg      = "msg"
)

// lastLine decodes the last JSON record written to buf.
func lastLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	var got map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &got); err != nil {
		t.Fatalf("decode log line %q: %v", lines[len(lines)-1], err)
	}
	return got
}

func TestNew_BaseFieldsAndTimestamp(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "1.2.3")

	log.Info("boot")
	got := lastLine(t, &buf)

	if got[logging.KeyService] != "svc" || got[logging.KeyVersion] != "1.2.3" {
		t.Fatalf("service/version = %v/%v, want svc/1.2.3", got[logging.KeyService], got[logging.KeyVersion])
	}
	if got[keyLevel] != "INFO" || got[keyMsg] != "boot" {
		t.Fatalf("level/msg = %v/%v", got[keyLevel], got[keyMsg])
	}
	ts, ok := got[keyTS].(string)
	if !ok {
		t.Fatalf("ts missing: %v", got)
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || parsed.Location() != time.UTC {
		t.Fatalf("ts %q is not RFC 3339 UTC: %v", ts, err)
	}
}

func TestNew_LevelFiltersDebug(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v")

	log.Debug("hidden")

	if buf.Len() != 0 {
		t.Fatalf("debug record written at info level: %s", buf.String())
	}
}

func TestReplaceAttr_RedactsSensitiveKeys(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v")

	log.Info("login", slog.String(keyPassword, "hunter2"), slog.String(keyUser, "alice"))
	got := lastLine(t, &buf)

	if got[keyPassword] != "[REDACTED]" {
		t.Fatalf("password = %v, want [REDACTED]", got[keyPassword])
	}
	if got[keyUser] != "alice" {
		t.Fatalf("user = %v, want alice", got[keyUser])
	}
}

func TestContextHandler_AddsRequestID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v")
	ctx := logging.WithRequestID(context.Background(), "req-1")

	log.InfoContext(ctx, "handled")
	got := lastLine(t, &buf)

	if got[logging.KeyRequestID] != "req-1" {
		t.Fatalf("request_id = %v, want req-1", got[logging.KeyRequestID])
	}
}

func TestContextHandler_OmitsCorrelationOutsideRequest(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v")

	log.InfoContext(context.Background(), "boot")
	got := lastLine(t, &buf)

	if _, present := got[logging.KeyRequestID]; present {
		t.Fatalf("request_id must be absent outside a request, got %v", got[logging.KeyRequestID])
	}
}

func TestContextHandler_SurvivesWithAttrsAndWithGroup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v").
		With(slog.String(logging.KeyComponent, "http")).
		WithGroup("req")
	ctx := logging.WithRequestID(context.Background(), "req-2")

	log.InfoContext(ctx, "handled", slog.String(logging.KeyMethod, "GET"))
	got := lastLine(t, &buf)

	if got[logging.KeyComponent] != "http" {
		t.Fatalf("component lost after With: %v", got)
	}
	group, ok := got["req"].(map[string]any)
	if !ok || group[logging.KeyMethod] != "GET" {
		t.Fatalf("group attrs lost: %v", got)
	}
	if group[logging.KeyRequestID] != "req-2" {
		t.Fatalf("request_id lost after WithGroup: %v", got)
	}
}

func TestRequestIDFrom_EmptyWithoutValue(t *testing.T) {
	t.Parallel()
	if id := logging.RequestIDFrom(context.Background()); id != "" {
		t.Fatalf("RequestIDFrom(empty ctx) = %q, want empty", id)
	}
}
