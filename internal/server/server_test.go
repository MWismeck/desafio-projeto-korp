package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/MWismeck/desafio-projeto-korp/internal/handler"
	"github.com/MWismeck/desafio-projeto-korp/internal/metrics"
	"github.com/MWismeck/desafio-projeto-korp/internal/middleware"
	"github.com/MWismeck/desafio-projeto-korp/internal/problem"
	"github.com/MWismeck/desafio-projeto-korp/internal/server"
)

// upStub records every transition of the availability gauge.
type upStub struct {
	mu     sync.Mutex
	values []float64
}

func newUpStub() *upStub { return &upStub{} }

func (u *upStub) Set(v float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.values = append(u.values, v)
}

func (u *upStub) last() float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.values) == 0 {
		return -1
	}
	return u.values[len(u.values)-1]
}

// newServer builds a server with the production chain; korp lets a test replace the business handler.
func newServer(t *testing.T, korp http.Handler, up server.UpGauge) *server.Server {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	reg := prometheus.NewRegistry()
	m := metrics.New(reg, "v", "c")
	if korp == nil {
		korp = handler.NewProjetoKorp(time.Now, log)
	}
	return server.New(server.Options{
		Addr:              "127.0.0.1:0",
		ReadHeaderTimeout: 5 * time.Second,
		ShutdownTimeout:   2 * time.Second,
		Logger:            log,
		ProjetoKorp:       korp,
		Metrics:           metrics.Handler(reg),
		Swagger:           handler.Swagger(),
		Middleware: []middleware.Middleware{
			middleware.Recover(log, m.PanicsRecovered), middleware.RequestID(),
			middleware.AccessLog(log, server.RouteHealthz, server.RouteReadyz, server.RouteMetrics), middleware.Metrics(m),
		},
		Up: up,
	})
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
	return rec
}

func TestServer_Routes(t *testing.T) {
	t.Parallel()
	h := newServer(t, nil, newUpStub()).Handler()
	tests := []struct {
		name        string
		method      string
		path        string
		wantCode    int
		wantCT      string
		wantBodyHas string
		wantAllow   string
	}{
		{name: "GET /projeto-korp", method: http.MethodGet, path: "/projeto-korp", wantCode: 200, wantCT: handler.ContentTypeJSON, wantBodyHas: `"nome":"Projeto Korp"`},
		{name: "HEAD /projeto-korp has no body", method: http.MethodHead, path: "/projeto-korp", wantCode: 200, wantCT: handler.ContentTypeJSON},
		{name: "POST /projeto-korp is 405 JSON with Allow", method: http.MethodPost, path: "/projeto-korp", wantCode: 405, wantCT: problem.ContentType, wantBodyHas: `"status":405`, wantAllow: "GET"},
		{name: "DELETE /projeto-korp is 405", method: http.MethodDelete, path: "/projeto-korp", wantCode: 405, wantCT: problem.ContentType, wantAllow: "GET"},
		{name: "unknown route is 404 JSON", method: http.MethodGet, path: "/nope", wantCode: 404, wantCT: problem.ContentType, wantBodyHas: `"title":"Not Found"`},
		{name: "trailing slash is another route", method: http.MethodGet, path: "/projeto-korp/", wantCode: 404, wantCT: problem.ContentType},
		{name: "GET /healthz", method: http.MethodGet, path: "/healthz", wantCode: 200, wantCT: handler.ContentTypeJSON, wantBodyHas: `{"status":"ok"}`},
		{name: "GET /metrics", method: http.MethodGet, path: "/metrics", wantCode: 200, wantCT: "text/plain", wantBodyHas: "# TYPE http_requests_total counter"},
		{name: "GET /swagger/index.html", method: http.MethodGet, path: "/swagger/index.html", wantCode: 200, wantCT: "text/html", wantBodyHas: "swagger"},
		{name: "GET /swagger/doc.json", method: http.MethodGet, path: "/swagger/doc.json", wantCode: 200, wantCT: "application/json", wantBodyHas: `"/projeto-korp"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, h, tc.method, tc.path)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantCT) {
				t.Fatalf("Content-Type = %q, want prefix %q", ct, tc.wantCT)
			}
			if tc.wantBodyHas != "" && !strings.Contains(rec.Body.String(), tc.wantBodyHas) {
				t.Fatalf("body = %q, want containing %q", rec.Body.String(), tc.wantBodyHas)
			}
			if tc.wantAllow != "" && !strings.Contains(rec.Header().Get("Allow"), tc.wantAllow) {
				t.Fatalf("Allow = %q, want containing %q", rec.Header().Get("Allow"), tc.wantAllow)
			}
			if rec.Header().Get(middleware.HeaderRequestID) == "" {
				t.Fatal("X-Request-ID missing on response")
			}
		})
	}
}

func TestServer_ProblemCarriesRequestID(t *testing.T) {
	t.Parallel()
	h := newServer(t, nil, newUpStub()).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", http.NoBody)
	req.Header.Set(middleware.HeaderRequestID, "corr-42")

	h.ServeHTTP(rec, req)

	var p problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.RequestID != "corr-42" || p.Instance != "/missing" {
		t.Fatalf("problem = %+v", p)
	}
}

func TestServer_HandlerNotFoundIsNotIntercepted(t *testing.T) {
	t.Parallel()
	own404 := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "custom", http.StatusNotFound)
	})
	h := newServer(t, own404, newUpStub()).Handler()

	rec := do(t, h, http.MethodGet, "/projeto-korp")

	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("matched handler's 404 must pass through untouched: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestServer_ReadyzBeforeListenIs503(t *testing.T) {
	t.Parallel()
	h := newServer(t, nil, newUpStub()).Handler()

	rec := do(t, h, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("readyz before listen: %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// slowHandler blocks until released, to keep a request in flight during shutdown.
type slowHandler struct {
	started  chan struct{}
	release  chan struct{}
	finished atomic.Bool
}

func (s *slowHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	close(s.started)
	<-s.release
	w.WriteHeader(http.StatusOK)
	s.finished.Store(true)
}

func TestServer_GracefulShutdownDrainsInFlightAndFlipsAvailability(t *testing.T) {
	t.Parallel()
	slow := &slowHandler{started: make(chan struct{}), release: make(chan struct{})}
	up := newUpStub()
	srv := newServer(t, slow, up)
	if err := srv.Listen(context.Background()); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + srv.Addr()

	waitReady(t, client, base)
	if up.last() != 1 {
		t.Fatalf("service_up after listen = %v, want 1", up.last())
	}

	inFlight := make(chan *http.Response, 1)
	go func() {
		resp, err := client.Get(base + "/projeto-korp")
		if err != nil {
			t.Errorf("in-flight request: %v", err)
			inFlight <- nil
			return
		}
		inFlight <- resp
	}()
	<-slow.started
	cancel()

	// The drain flips availability before waiting for the in-flight request.
	waitUntil(t, func() bool { return up.last() == 0 })
	if slow.finished.Load() {
		t.Fatal("in-flight request finished before it was released")
	}
	close(slow.release)
	resp := <-inFlight
	if resp == nil {
		t.Fatal("in-flight request failed")
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("in-flight request status = %d, want 200 (drained, not killed)", resp.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil after graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if up.last() != 0 {
		t.Fatalf("service_up after shutdown = %v, want 0", up.last())
	}
}

func TestServer_ShutdownDeadlineForcesClose(t *testing.T) {
	t.Parallel()
	slow := &slowHandler{started: make(chan struct{}), release: make(chan struct{})}
	log := slog.New(slog.DiscardHandler)
	srv := server.New(server.Options{
		Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second, ShutdownTimeout: 50 * time.Millisecond,
		Logger: log, ProjetoKorp: slow, Metrics: http.NotFoundHandler(), Swagger: http.NotFoundHandler(), Up: newUpStub(),
	})
	if err := srv.Listen(context.Background()); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	client := &http.Client{Timeout: 5 * time.Second}
	waitReady(t, client, "http://"+srv.Addr())
	go func() {
		resp, err := client.Get("http://" + srv.Addr() + "/projeto-korp")
		if err == nil {
			_ = resp.Body.Close() // the connection is force-closed by the server under test
		}
	}()
	<-slow.started
	cancel()

	err := <-done
	close(slow.release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Serve err = %v, want DeadlineExceeded after forced close", err)
	}
}

func TestServer_ListenFailsOnBusyPort(t *testing.T) {
	t.Parallel()
	first := newServer(t, nil, newUpStub())
	if err := first.Listen(context.Background()); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	log := slog.New(slog.DiscardHandler)
	second := server.New(server.Options{
		Addr: first.Addr(), ReadHeaderTimeout: time.Second, ShutdownTimeout: time.Second, Logger: log,
		ProjetoKorp: http.NotFoundHandler(), Metrics: http.NotFoundHandler(), Swagger: http.NotFoundHandler(), Up: newUpStub(),
	})

	if err := second.Run(context.Background()); err == nil {
		t.Fatal("Run on a busy port must fail")
	}
}

// waitReady polls /healthz until it answers 200, without sleeping between attempts.
func waitReady(t *testing.T, client *http.Client, base string) {
	t.Helper()
	waitUntil(t, func() bool {
		resp, err := client.Get(base + "/healthz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// waitUntil retries cond on a short ticker with an overall deadline.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("condition not met before deadline")
		}
	}
}
