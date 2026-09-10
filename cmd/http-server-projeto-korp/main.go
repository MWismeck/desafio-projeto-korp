// Command http-server-projeto-korp serves GET /projeto-korp (Desafio DevOps "Projeto Korp") with
// Prometheus metrics, structured JSON logs and a Swagger UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/MWismeck/desafio-projeto-korp/internal/config"
	"github.com/MWismeck/desafio-projeto-korp/internal/handler"
	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
	"github.com/MWismeck/desafio-projeto-korp/internal/metrics"
	"github.com/MWismeck/desafio-projeto-korp/internal/middleware"
	"github.com/MWismeck/desafio-projeto-korp/internal/server"
)

// serviceName is the canonical name shared by the log field, the Prometheus job and the compose service.
const serviceName = "http-server-projeto-korp"

// Filled by the linker: -ldflags "-X main.version=… -X main.commit=… -X main.buildDate=…".
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// probeTimeout matches the healthcheck timeout declared in compose.yml.
const probeTimeout = 2 * time.Second

// Keys of the boot log line; sloglint requires constants so the schema stays greppable.
const (
	keyPort              = "port"
	keyEnv               = "env"
	keyLogLevel          = "log_level"
	keyShutdownTimeout   = "shutdown_timeout"
	keyReadHeaderTimeout = "read_header_timeout"
)

// @title           http-server-projeto-korp
// @version         1.0.0
// @description     Serviço HTTP do Desafio DevOps "Projeto Korp": GET /projeto-korp devolve o nome do projeto e o horário atual em UTC, resolvido a cada requisição.
// @BasePath        /
func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		slog.Error("fatal", slog.Any(logging.KeyErr, err))
		os.Exit(1)
	}
}

// flags are the only inputs not taken from the environment: they select a mode of the binary.
type flags struct {
	version     bool
	healthcheck bool
}

func parseFlags(args []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet(serviceName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&f.version, "version", false, "print version, commit and build date, then exit")
	fs.BoolVar(&f.healthcheck, "healthcheck", false, "probe GET /healthz on the configured port and exit 0 when healthy")
	if err := fs.Parse(args); err != nil {
		return flags{}, fmt.Errorf("parse flags: %w", err)
	}
	return f, nil
}

// run is main without os.Exit: it returns the error so tests can drive every mode of the binary.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	level := new(slog.LevelVar)
	ver, com, date := buildInfo()
	log := logging.New(stdout, level, serviceName, ver)
	slog.SetDefault(log)

	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.version {
		return printVersion(stdout, ver, com, date)
	}
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	level.Set(cfg.LogLevel)
	if f.healthcheck {
		return probe(ctx, cfg.Port)
	}
	return serve(ctx, log, cfg, ver, com)
}

// serve builds the dependency graph by hand and runs the server until SIGTERM/SIGINT.
func serve(ctx context.Context, log *slog.Logger, cfg config.Config, ver, com string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	reg := prometheus.NewRegistry()
	m := metrics.New(reg, ver, com)
	// Probes and scrape are traffic the platform makes against itself: they stay at DEBUG in the
	// access log and out of the request metrics, which exist to describe what users do.
	quietRoutes := []string{server.RouteHealthz, server.RouteReadyz, server.RouteMetrics}
	srv := server.New(server.Options{
		Addr:              cfg.Addr(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ShutdownTimeout:   cfg.ShutdownTimeout,
		Logger:            log,
		ProjetoKorp:       handler.NewProjetoKorp(time.Now, log),
		Metrics:           metrics.Handler(reg),
		Swagger:           handler.Swagger(),
		// Order matters: recovery outermost, then request id, then the observers closest to the mux.
		Middleware: []middleware.Middleware{
			middleware.Recover(log, m.PanicsRecovered),
			middleware.RequestID(),
			middleware.AccessLog(log, quietRoutes...),
			middleware.Metrics(m, quietRoutes...),
		},
		Up: m.ServiceUp,
	})
	log.Info("config loaded",
		slog.Int(keyPort, cfg.Port),
		slog.String(keyEnv, cfg.Env),
		slog.String(keyLogLevel, cfg.LogLevel.String()),
		slog.String(keyShutdownTimeout, cfg.ShutdownTimeout.String()),
		slog.String(keyReadHeaderTimeout, cfg.ReadHeaderTimeout.String()),
	)
	return srv.Run(ctx)
}

// buildInfo falls back to the VCS metadata embedded by `go build` when the linker flags were not passed.
func buildInfo() (ver, com, date string) {
	ver, com, date = version, commit, buildDate
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ver, com, date
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if com == "none" {
				com = s.Value
			}
		case "vcs.time":
			if date == "unknown" {
				date = s.Value
			}
		}
	}
	return ver, com, date
}

func printVersion(w io.Writer, ver, com, date string) error {
	if _, err := fmt.Fprintf(w, "%s version=%s commit=%s built=%s go=%s\n", serviceName, ver, com, date, runtime.Version()); err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	return nil
}

// probe is the container healthcheck: GET /healthz on the loopback port, exit 0 only on 200.
func probe(ctx context.Context, port int) (err error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	target := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: server.RouteHealthz}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	resp, err := newProbeClient().Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", target.String(), err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", target.String(), resp.StatusCode)
	}
	return nil
}

// newProbeClient is a one-shot loopback client; every timeout is explicit so a stuck server
// cannot hang the healthcheck beyond its budget.
func newProbeClient() *http.Client {
	return &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: probeTimeout}).DialContext,
			ResponseHeaderTimeout: probeTimeout,
			DisableKeepAlives:     true,
		},
	}
}
