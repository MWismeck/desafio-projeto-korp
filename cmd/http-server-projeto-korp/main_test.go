package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// envVars read by the binary; cleared so host values never leak into the tests.
var envVars = []string{"APP_PORT", "APP_ENV", "LOG_LEVEL", "SHUTDOWN_TIMEOUT", "READ_HEADER_TIMEOUT"}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envVars {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// healthServer binds a loopback port and answers /healthz with status; returns the port.
func healthServer(t *testing.T, status int) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(status)
		}),
	}}
	srv.Start()
	t.Cleanup(srv.Close)
	return ln.Addr().(*net.TCPAddr).Port
}

// freePort asks the kernel for an unused loopback port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port
}

func TestRun_VersionFlagPrintsAndExitsZero(t *testing.T) {
	clearEnv(t)
	var out bytes.Buffer

	if err := run(context.Background(), []string{"-version"}, &out); err != nil {
		t.Fatalf("run(-version) err = %v", err)
	}

	line := out.String()
	for _, want := range []string{serviceName + " version=", " commit=", " built=", " go=go"} {
		if !strings.Contains(line, want) {
			t.Fatalf("version output %q lacks %q", line, want)
		}
	}
}

func TestRun_UnknownFlagFails(t *testing.T) {
	clearEnv(t)
	if err := run(context.Background(), []string{"-bogus"}, &bytes.Buffer{}); err == nil {
		t.Fatal("run(-bogus) err = nil, want error")
	}
}

func TestRun_InvalidConfigFailsFast(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_PORT", "0")

	err := run(context.Background(), nil, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "APP_PORT") {
		t.Fatalf("run() err = %v, want config error naming APP_PORT", err)
	}
}

func TestRun_HealthcheckFlag(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "healthy server exits 0", status: http.StatusOK, wantErr: false},
		{name: "unhealthy server exits 1", status: http.StatusServiceUnavailable, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("APP_PORT", strconv.Itoa(healthServer(t, tc.status)))

			err := run(context.Background(), []string{"-healthcheck"}, &bytes.Buffer{})

			if (err != nil) != tc.wantErr {
				t.Fatalf("run(-healthcheck) err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestProbe_NoServerFailsWithinTimeout(t *testing.T) {
	t.Parallel()
	start := time.Now()

	err := probe(context.Background(), freePort(t))

	if err == nil {
		t.Fatal("probe without server must fail")
	}
	if elapsed := time.Since(start); elapsed > probeTimeout+time.Second {
		t.Fatalf("probe took %s, want <= %s", elapsed, probeTimeout)
	}
}

func TestPrintVersion_ReportsWriteFailure(t *testing.T) {
	t.Parallel()
	if err := printVersion(failingWriter{}, "v", "c", "d"); err == nil {
		t.Fatal("printVersion must surface the write error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestBuildInfo_KeepsLinkerValues(t *testing.T) {
	t.Parallel()
	ver, com, date := buildInfo()
	if ver != version {
		t.Fatalf("version = %q, want %q", ver, version)
	}
	if com == "" || date == "" {
		t.Fatalf("commit/date must never be empty: %q %q", com, date)
	}
}

// TestRun_ServesUntilCanceled boots the real binary path on a free port, checks the endpoints
// and then cancels the context, expecting a clean exit.
func TestRun_ServesUntilCanceled(t *testing.T) {
	clearEnv(t)
	port := freePort(t)
	t.Setenv("APP_PORT", strconv.Itoa(port))
	t.Setenv("SHUTDOWN_TIMEOUT", "2s")
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, &logs) }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for probe(ctx, port) != nil {
		select {
		case <-tick.C:
		case <-deadline.C:
			cancel()
			t.Fatalf("server did not become healthy; logs:\n%s", logs.String())
		}
	}

	resp, err := client.Get(base + "/projeto-korp")
	if err != nil {
		t.Fatalf("GET /projeto-korp: %v", err)
	}
	var body struct {
		Nome    string `json:"nome"`
		Horario string `json:"horario"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if body.Nome != "Projeto Korp" || !strings.HasSuffix(body.Horario, "Z") {
		t.Fatalf("body = %+v", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() err = %v after cancel, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return after cancel")
	}
	for _, want := range []string{`"msg":"config loaded"`, `"msg":"server listening"`, `"msg":"shutdown complete"`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs lack %s:\n%s", want, logs.String())
		}
	}
}
