// Package middleware provides the pure func(http.Handler) http.Handler decorators of the HTTP
// edge: recovery, request id, access log and RED metrics.
package middleware

import (
	"net/http"
	"strings"
)

// Middleware decorates a handler without knowing what is behind it.
type Middleware func(http.Handler) http.Handler

// Chain applies mws to h so that the first middleware becomes the outermost layer.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// routeUnmatched is the route label of requests the mux did not match (404/405): one series,
// however many paths a scanner tries.
const routeUnmatched = "unmatched"

// routeFromPattern turns the ServeMux pattern ("GET /projeto-korp") into the route label.
func routeFromPattern(pattern string) string {
	if pattern == "" {
		return routeUnmatched
	}
	if _, path, found := strings.Cut(pattern, " "); found {
		return path
	}
	return pattern
}

// responseRecorder captures the status and bytes written; Unwrap keeps http.ResponseController
// able to reach the original writer.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
