package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
)

// microsPerMilli converts the measured duration into fractional milliseconds for the log.
const microsPerMilli = 1000.0

// AccessLog emits one record per request after the handler ran. Routes listed in quiet (probes,
// scrapes) are logged at DEBUG so they do not drown the real traffic.
func AccessLog(log *slog.Logger, quiet ...string) Middleware {
	log = log.With(slog.String(logging.KeyComponent, "http"))
	quietRoutes := make(map[string]struct{}, len(quiet))
	for _, route := range quiet {
		quietRoutes[route] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := newRecorder(w)
			// Not deferred on purpose: on a panic the recovery middleware writes the one ERROR line.
			next.ServeHTTP(rec, r)

			route := routeFromPattern(r.Pattern)
			attrs := []slog.Attr{
				slog.String(logging.KeyMethod, r.Method),
				slog.String(logging.KeyRoute, route),
				slog.Int(logging.KeyStatus, rec.status),
				slog.Float64(logging.KeyDurationMS, float64(time.Since(start).Microseconds())/microsPerMilli),
				slog.Int(logging.KeyBytesOut, rec.bytes),
			}
			if route == routeUnmatched {
				attrs = append(attrs, slog.String(logging.KeyPath, r.URL.Path))
			}
			level, msg := slog.LevelInfo, "request handled"
			switch {
			case rec.status >= http.StatusInternalServerError:
				level, msg = slog.LevelError, "request failed"
			case isQuiet(quietRoutes, route):
				level = slog.LevelDebug
			}
			log.LogAttrs(r.Context(), level, msg, attrs...)
		})
	}
}

func isQuiet(quiet map[string]struct{}, route string) bool {
	_, ok := quiet[route]
	return ok
}
