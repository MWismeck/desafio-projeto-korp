// Package problem writes RFC 9457 error responses (application/problem+json) so every error the
// service produces has the same shape and carries the request id that links it to the logs.
package problem

import (
	"encoding/json"
	"net/http"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
)

// ContentType is the media type of every error body produced by the service.
const ContentType = "application/problem+json; charset=utf-8"

// typeBlank is the RFC 9457 type for errors fully described by their HTTP status.
const typeBlank = "about:blank"

// Problem is the error body. Detail never carries internal error text: the request id is the link
// to the diagnostic in the logs.
type Problem struct {
	Type      string `json:"type" example:"about:blank"`
	Title     string `json:"title" example:"Not Found"`
	Status    int    `json:"status" example:"404"`
	Detail    string `json:"detail,omitempty" example:"no route for GET /x"`
	Instance  string `json:"instance,omitempty" example:"/x"`
	RequestID string `json:"request_id,omitempty" example:"7f2c1a1e-3b0e-4d8f-9a44-1d2f0c3b6a90"`
}

// Write sends a problem with the given status; the returned error only reports a failed encode,
// which the caller logs because the status line has already left.
func Write(w http.ResponseWriter, r *http.Request, status int, detail string) error {
	p := Problem{
		Type:      typeBlank,
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: logging.RequestIDFrom(r.Context()),
	}
	h := w.Header()
	h.Set("Content-Type", ContentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(p)
}
