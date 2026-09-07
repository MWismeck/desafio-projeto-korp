package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
	"github.com/MWismeck/desafio-projeto-korp/internal/problem"
)

// Counter is the only thing the recovery needs from the metrics: something to increment.
type Counter interface {
	Inc()
}

// Recover turns a panic into a 500 problem response and an ERROR log with the stack, so a bug in
// one request never takes the process down. It must be the outermost middleware.
func Recover(log *slog.Logger, panics Counter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := newRecorder(w)
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				panics.Inc()
				// The request id lives in the inner context; the response header is the copy this layer can see.
				attrs := []slog.Attr{
					slog.Any(logging.KeyPanic, p),
					slog.String(logging.KeyMethod, r.Method),
					slog.String(logging.KeyPath, r.URL.Path),
					slog.String(logging.KeyStack, string(debug.Stack())),
				}
				if id := w.Header().Get(HeaderRequestID); id != "" {
					attrs = append(attrs, slog.String(logging.KeyRequestID, id))
				}
				log.LogAttrs(r.Context(), slog.LevelError, "panic recovered", attrs...)
				if rec.wroteHeader {
					return
				}
				if err := problem.Write(rec, r, http.StatusInternalServerError, "internal error"); err != nil {
					log.LogAttrs(r.Context(), slog.LevelError, "write problem", slog.Any(logging.KeyErr, err))
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}
