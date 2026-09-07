package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
	"github.com/MWismeck/desafio-projeto-korp/internal/problem"
)

func TestWrite_FillsRFC9457Fields(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing?secret=1", http.NoBody)
	req = req.WithContext(logging.WithRequestID(req.Context(), "req-9"))

	if err := problem.Write(rec, req, http.StatusNotFound, "no route"); err != nil {
		t.Fatalf("Write() err = %v", err)
	}

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != problem.ContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, problem.ContentType)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var got problem.Problem
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := problem.Problem{
		Type: "about:blank", Title: "Not Found", Status: 404, Detail: "no route",
		Instance: "/missing", RequestID: "req-9",
	}
	if got != want {
		t.Fatalf("body = %+v, want %+v", got, want)
	}
}

// failingWriter reports an error on Write so the encode failure path is exercised.
type failingWriter struct {
	http.ResponseWriter
}

func (failingWriter) Write([]byte) (int, error) { return 0, http.ErrAbortHandler }

func TestWrite_ReturnsEncodeError(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/x", http.NoBody)

	err := problem.Write(failingWriter{ResponseWriter: httptest.NewRecorder()}, req, http.StatusInternalServerError, "")

	if err == nil {
		t.Fatal("Write() err = nil, want encode error")
	}
}
