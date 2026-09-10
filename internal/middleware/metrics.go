package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MWismeck/desafio-projeto-korp/internal/metrics"
)

// methodOther is the label for methods outside the HTTP RFC set, so a scanner cannot mint series.
const methodOther = "other"

var knownMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {}, http.MethodPut: {},
	http.MethodPatch: {}, http.MethodDelete: {}, http.MethodOptions: {},
}

// Metrics records the RED metrics of every request. It must sit directly around the mux (no
// request clone in between) because the route label is read from r.Pattern after the match.
// Routes listed in quiet are the same ones the access log keeps at DEBUG (probes and scrape).
func Metrics(m *metrics.Metrics, quiet ...string) Middleware {
	quietRoutes := make(map[string]struct{}, len(quiet))
	for _, route := range quiet {
		quietRoutes[route] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			m.InFlight.Inc()
			rec := newRecorder(w)
			completed := false
			defer func() {
				m.InFlight.Dec()
				// http_requests_total measures user traffic and is also the denominator of the
				// availability SLI: the healthcheck (every 10s) and the scrape (every 15s) would
				// show volume nobody asked for and dilute the ratio with requests of our own.
				if isQuiet(quietRoutes, routeFromPattern(r.Pattern)) {
					return
				}
				status := rec.status
				// A handler that did not return normally is unwinding a panic; the recovery answers 500.
				if !completed {
					status = http.StatusInternalServerError
				}
				observe(m, r, status, time.Since(start))
			}()
			next.ServeHTTP(rec, r)
			completed = true
		})
	}
}

// observe updates the counter and the histogram with the normalized method and the route template.
func observe(m *metrics.Metrics, r *http.Request, status int, elapsed time.Duration) {
	method := normalizeMethod(r.Method)
	route := routeFromPattern(r.Pattern)
	m.Requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.Duration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

func normalizeMethod(method string) string {
	if _, known := knownMethods[method]; known {
		return method
	}
	return methodOther
}
