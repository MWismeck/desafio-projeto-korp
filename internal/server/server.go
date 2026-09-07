// Package server assembles the routes, the middleware chain and the http.Server, and runs the
// listen/serve/drain lifecycle; it reads no configuration and creates no dependency itself.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/MWismeck/desafio-projeto-korp/internal/handler"
	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
	"github.com/MWismeck/desafio-projeto-korp/internal/middleware"
	"github.com/MWismeck/desafio-projeto-korp/internal/problem"
)

// Routes served besides the business endpoint; exported so the access log can quiet the probes.
const (
	RouteProjetoKorp = "/projeto-korp"
	RouteHealthz     = "/healthz"
	RouteReadyz      = "/readyz"
	RouteMetrics     = "/metrics"
)

// Timeouts of the http.Server, chained with the NGINX ones: header < client_header_timeout (10s),
// write <= proxy_read_timeout, idle > keepalive_timeout so the proxy closes first.
const (
	readTimeout    = 10 * time.Second
	writeTimeout   = 10 * time.Second
	idleTimeout    = 60 * time.Second
	maxHeaderBytes = 1 << 20
	// retryAfter tells the proxy when to probe again while the service drains.
	retryAfter = "1"
)

// UpGauge is the availability signal the server flips: 1 once listening, 0 when draining.
type UpGauge interface {
	Set(float64)
}

// Options are the explicit dependencies of the server.
type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
	Logger            *slog.Logger
	ProjetoKorp       http.Handler
	Metrics           http.Handler
	Swagger           http.Handler
	Middleware        []middleware.Middleware
	Up                UpGauge
}

// Server owns the http.Server, its listener and the readiness state.
type Server struct {
	httpServer      *http.Server
	listener        net.Listener
	log             *slog.Logger
	up              UpGauge
	ready           atomic.Bool
	shutdownTimeout time.Duration
}

// health is the body of the liveness and readiness probes.
type health struct {
	Status string `json:"status"`
}

// New wires the routes and middlewares; it does no I/O.
func New(o Options) *Server {
	s := &Server{log: o.Logger, up: o.Up, shutdownTimeout: o.ShutdownTimeout}
	mux := http.NewServeMux()
	mux.Handle("GET "+RouteProjetoKorp, o.ProjetoKorp)
	mux.HandleFunc("GET "+RouteHealthz, s.healthz)
	mux.HandleFunc("GET "+RouteReadyz, s.readyz)
	mux.Handle("GET "+RouteMetrics, o.Metrics)
	mux.Handle("GET "+handler.SwaggerPath, o.Swagger)

	s.httpServer = &http.Server{
		Addr:              o.Addr,
		Handler:           middleware.Chain(problemFallback(mux, o.Logger), o.Middleware...),
		ReadHeaderTimeout: o.ReadHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(o.Logger.Handler(), slog.LevelError),
	}
	return s
}

// Handler exposes the full chain for in-process tests.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Listen binds the address; it is separate from Serve so callers can learn the bound port.
func (s *Server) Listen(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.httpServer.Addr, err)
	}
	s.listener = ln
	return nil
}

// Addr is the bound address, valid after Listen.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// Run is Listen followed by Serve.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(ctx); err != nil {
		return err
	}
	return s.Serve(ctx)
}

// Serve accepts connections until ctx is canceled, then drains within ShutdownTimeout. Requests
// see ctx through BaseContext, so in-flight work is canceled together with the process.
func (s *Server) Serve(ctx context.Context) error {
	s.httpServer.BaseContext = func(net.Listener) context.Context { return ctx }
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		s.setReady(true)
		s.log.Info("server listening", slog.String(logging.KeyAddr, s.Addr()))
		if err := s.httpServer.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		return s.shutdown(gctx)
	})
	if err := g.Wait(); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}

// shutdown flips readiness first so the proxy and Prometheus see the drain, then waits for
// in-flight requests. Close is the last resort when the deadline passes.
func (s *Server) shutdown(ctx context.Context) error {
	s.setReady(false)
	s.log.Info("shutdown started")
	// ctx is already canceled; detaching it keeps the deadline meaningful for the drain.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	err := s.httpServer.Shutdown(drainCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(err, s.httpServer.Close())
	}
	if err != nil {
		return fmt.Errorf("drain connections: %w", err)
	}
	s.log.Info("shutdown complete")
	return nil
}

func (s *Server) setReady(ready bool) {
	s.ready.Store(ready)
	if ready {
		s.up.Set(1)
		return
	}
	s.up.Set(0)
}

// healthz is liveness: 200 whenever the process answers, even while draining.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeHealth(w, r, http.StatusOK, "ok")
}

// readyz is readiness: 503 while draining so the proxy stops sending new traffic.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		w.Header().Set("Retry-After", retryAfter)
		s.logIfErr(r, problem.Write(w, r, http.StatusServiceUnavailable, "draining"))
		return
	}
	s.writeHealth(w, r, http.StatusOK, "ready")
}

func (s *Server) writeHealth(w http.ResponseWriter, r *http.Request, status int, state string) {
	h := w.Header()
	h.Set("Content-Type", handler.ContentTypeJSON)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	s.logIfErr(r, json.NewEncoder(w).Encode(health{Status: state}))
}

// logIfErr records an encode failure once the status has already been sent.
func (s *Server) logIfErr(r *http.Request, err error) {
	if err != nil {
		s.log.ErrorContext(r.Context(), "write response", slog.Any(logging.KeyErr, err))
	}
}

// problemFallback converts the text/plain 404 and 405 generated by the ServeMux into
// problem+json while keeping the Allow header; only responses with no matched pattern are
// touched, so handlers that legitimately answer 404 are left alone.
func problemFallback(mux *http.ServeMux, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&fallbackWriter{ResponseWriter: w, r: r, log: log}, r)
	})
}

type fallbackWriter struct {
	http.ResponseWriter
	r           *http.Request
	log         *slog.Logger
	intercepted bool
}

func (f *fallbackWriter) WriteHeader(code int) {
	if f.r.Pattern == "" && (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) {
		f.intercepted = true
		if err := problem.Write(f.ResponseWriter, f.r, code, "no route for "+f.r.Method+" "+f.r.URL.Path); err != nil {
			f.log.ErrorContext(f.r.Context(), "write problem", slog.Any(logging.KeyErr, err))
		}
		return
	}
	f.ResponseWriter.WriteHeader(code)
}

// Write drops the text/plain body of an intercepted response; the problem body already went out.
func (f *fallbackWriter) Write(b []byte) (int, error) {
	if f.intercepted {
		return len(b), nil
	}
	return f.ResponseWriter.Write(b)
}

func (f *fallbackWriter) Unwrap() http.ResponseWriter {
	return f.ResponseWriter
}
