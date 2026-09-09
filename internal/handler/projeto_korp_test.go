package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MWismeck/desafio-projeto-korp/internal/handler"
)

// tickingClock advances one second per call, proving per-request resolution without sleeping.
type tickingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tickingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// get performs GET /projeto-korp and decodes the body rejecting any field outside the contract;
// the raw body is kept in the recorder for byte-exact comparisons.
func get(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, handler.ProjetoKorpResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projeto-korp", http.NoBody))

	var got handler.ProjetoKorpResponse
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, got
}

func TestProjetoKorp_ReturnsContractInUTC(t *testing.T) {
	t.Parallel()
	// A -03:00 clock proves the handler converts to UTC instead of relying on the host zone.
	saoPaulo := time.FixedZone("America/Sao_Paulo", -3*60*60)
	fixed := time.Date(2026, 9, 5, 9, 30, 0, 0, saoPaulo)
	h := handler.NewProjetoKorp(func() time.Time { return fixed }, slog.New(slog.DiscardHandler))

	rec, got := get(t, h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != handler.ContentTypeJSON {
		t.Fatalf("Content-Type = %q, want %q", ct, handler.ContentTypeJSON)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if body, want := rec.Body.String(), `{"nome":"Projeto Korp","horario":"2026-09-05T12:30:00Z"}`+"\n"; body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if got.Nome != handler.ProjectName || got.Horario != "2026-09-05T12:30:00Z" {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestProjetoKorp_ResolvesTimePerRequest(t *testing.T) {
	t.Parallel()
	clock := &tickingClock{t: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)}
	h := handler.NewProjetoKorp(clock.Now, slog.New(slog.DiscardHandler))

	_, first := get(t, h)
	_, second := get(t, h)

	if first.Horario == second.Horario {
		t.Fatalf("horario must change between requests, both = %q", first.Horario)
	}
	if !strings.HasSuffix(first.Horario, "Z") || !strings.HasSuffix(second.Horario, "Z") {
		t.Fatalf("horario without Z suffix: %q, %q", first.Horario, second.Horario)
	}
}

// TestProjetoKorp_RealClockChangesAcrossSeconds is the evidence with the production clock:
// the second request is issued after the wall clock crosses a second boundary (timer, not Sleep).
func TestProjetoKorp_RealClockChangesAcrossSeconds(t *testing.T) {
	t.Parallel()
	h := handler.NewProjetoKorp(time.Now, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, first := get(t, h)
	firstTime, err := time.Parse(time.RFC3339, first.Horario)
	if err != nil {
		t.Fatalf("horario %q is not RFC 3339: %v", first.Horario, err)
	}
	nextSecond := time.NewTimer(time.Until(firstTime.Add(time.Second)) + 50*time.Millisecond)
	defer nextSecond.Stop()
	select {
	case <-nextSecond.C:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for the next second: %v", ctx.Err())
	}
	_, second := get(t, h)

	if first.Horario == second.Horario {
		t.Fatalf("horario did not change across a second boundary: %q", first.Horario)
	}
	secondTime, err := time.Parse(time.RFC3339, second.Horario)
	if err != nil || !secondTime.After(firstTime) || secondTime.Location() != time.UTC {
		t.Fatalf("second horario %q invalid or not after %q: %v", second.Horario, first.Horario, err)
	}
}

// failingWriter fails every Write so the encode error branch is exercised.
type failingWriter struct {
	http.ResponseWriter
}

func (failingWriter) Write([]byte) (int, error) { return 0, http.ErrAbortHandler }

func TestProjetoKorp_LogsEncodeFailure(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := handler.NewProjetoKorp(time.Now, log)

	h.ServeHTTP(failingWriter{ResponseWriter: httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/projeto-korp", http.NoBody))

	if !strings.Contains(buf.String(), "encode response") {
		t.Fatalf("encode failure not logged: %q", buf.String())
	}
}

func TestSwagger_ServesUI(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	handler.Swagger().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, handler.SwaggerPath+"index.html", http.NoBody))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "swagger") {
		t.Fatalf("swagger index: status = %d, body = %.80q", rec.Code, rec.Body.String())
	}
}

func BenchmarkProjetoKorp(b *testing.B) {
	h := handler.NewProjetoKorp(time.Now, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodGet, "/projeto-korp", http.NoBody)
	b.ReportAllocs()
	for b.Loop() {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
