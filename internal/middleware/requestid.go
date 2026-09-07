package middleware

import (
	"crypto/rand"
	"net/http"
	"regexp"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
)

// HeaderRequestID is the correlation header accepted from the proxy and always echoed back.
const HeaderRequestID = "X-Request-ID"

// validRequestID bounds what is accepted from outside: a client must not inject log content.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID accepts a well-formed incoming X-Request-ID or generates one, stores it in the
// context for logs and problems, and echoes it in the response.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if !validRequestID.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
		})
	}
}

// newRequestID returns 130 bits of cryptographic randomness as base32; rand.Text has no error
// path, unlike hand-rolled UUIDs built on rand.Read.
func newRequestID() string {
	return rand.Text()
}
