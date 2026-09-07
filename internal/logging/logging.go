// Package logging builds the single slog JSON logger of the process and carries the request
// correlation identifier (request_id) from the context into every record.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"
)

// Attribute keys shared by every package that logs, so the log schema lives in one place.
const (
	KeyService    = "service"
	KeyVersion    = "version"
	KeyComponent  = "component"
	KeyErr        = "err"
	KeyRequestID  = "request_id"
	KeyMethod     = "method"
	KeyRoute      = "route"
	KeyPath       = "path"
	KeyStatus     = "status"
	KeyDurationMS = "duration_ms"
	KeyBytesOut   = "bytes_out"
	KeyAddr       = "addr"
	KeyPanic      = "panic"
	KeyStack      = "stack"

	keyTimestamp = "ts"
	redacted     = "[REDACTED]"
)

// sensitiveKeys are redacted no matter who logs them: a safety net for secrets that reach a log call.
var sensitiveKeys = map[string]struct{}{
	"authorization": {}, "cookie": {}, "password": {}, "passwd": {}, "secret": {},
	"token": {}, "api_key": {}, "apikey": {}, "private_key": {},
}

// New builds the JSON logger with the mandatory base attributes and context correlation.
func New(w io.Writer, level slog.Leveler, service, version string) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr})
	return slog.New(ContextHandler{Handler: base}).With(
		slog.String(KeyService, service),
		slog.String(KeyVersion, version),
	)
}

// replaceAttr renames the timestamp to "ts" in UTC and redacts sensitive keys.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.String(keyTimestamp, a.Value.Time().UTC().Format(time.RFC3339Nano))
	}
	if _, sensitive := sensitiveKeys[strings.ToLower(a.Key)]; sensitive {
		a.Value = slog.StringValue(redacted)
	}
	return a
}

type requestIDKey struct{}

// WithRequestID stores the request id in the context for the handlers and the log handler.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request id carried by ctx, or "" outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ContextHandler decorates a slog.Handler so callers using the *Context log methods get the
// request_id without having to remember to add it.
type ContextHandler struct {
	slog.Handler
}

// Handle adds the correlation attributes found in ctx before delegating.
func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		r.AddAttrs(slog.String(KeyRequestID, id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs keeps the decoration on derived handlers.
func (h ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup keeps the decoration on derived handlers.
func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{Handler: h.Handler.WithGroup(name)}
}
