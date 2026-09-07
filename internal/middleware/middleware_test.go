package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
	"github.com/MWismeck/desafio-projeto-korp/internal/metrics"
	"github.com/MWismeck/desafio-projeto-korp/internal/middleware"
	"github.com/MWismeck/desafio-projeto-korp/internal/problem"
)

const (
	keyLevel = "level"
	keyMsg   = "msg"
)

// newMux registers the production-like routes: a normal handler, a panicking one and a probe.
func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projeto-korp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"ok":true,"request_id":"` + logging.RequestIDFrom(r.Context()) + `"}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// logLines decodes every JSON record in buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		lines = append(lines, m)
	}
	return lines
}

type stubCounter struct{ n int }

func (c *stubCounter) Inc() { c.n++ }

func TestChain_FirstMiddlewareIsOutermost(t *testing.T) {
	t.Parallel()
	var order []string
	mark := func(name string) middleware.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := middleware.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") }), mark("a"), mark("b"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if got := strings.Join(order, ","); got != "a,b,handler" {
		t.Fatalf("order = %s, want a,b,handler", got)
	}
}

func TestRecover_PanicBecomes500ProblemAndErrorLog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v")
	panics := &stubCounter{}
	h := middleware.Chain(newMux(t), middleware.Recover(log, panics), middleware.RequestID())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", http.NoBody)
	req.Header.Set(middleware.HeaderRequestID, "req-panic")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != problem.ContentType {
		t.Fatalf("Content-Type = %q, want problem+json", ct)
	}
	var p problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != 500 || p.Detail != "internal error" {
		t.Fatalf("body = %s (%v)", rec.Body.String(), err)
	}
	if panics.n != 1 {
		t.Fatalf("panics counter = %d, want 1", panics.n)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0][keyLevel] != "ERROR" || lines[0][keyMsg] != "panic recovered" {
		t.Fatalf("log = %v", lines)
	}
	if lines[0][logging.KeyRequestID] != "req-panic" || lines[0][logging.KeyStack] == "" || lines[0][logging.KeyPanic] != "kaboom" {
		t.Fatalf("log lacks correlation/stack/panic: %v", lines[0])
	}
}

func TestRecover_LeavesStartedResponseAlone(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	started := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("after header")
	})
	h := middleware.Recover(log, &stubCounter{})(started)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("started response was overwritten: %d %q", rec.Code, rec.Body.String())
	}
}

func TestRequestID_AcceptsValidGeneratesOtherwise(t *testing.T) {
	t.Parallel()
	generated := regexp.MustCompile(`^[A-Z2-7]{26}$`)
	tests := []struct {
		name     string
		incoming string
		wantEcho bool
	}{
		{name: "valid incoming id is echoed", incoming: "abc-123_x.y", wantEcho: true},
		{name: "missing id is generated", incoming: "", wantEcho: false},
		{name: "id with spaces is replaced", incoming: "bad id", wantEcho: false},
		{name: "id too long is replaced", incoming: strings.Repeat("a", 65), wantEcho: false},
		{name: "id with newline is replaced", incoming: "a\nb", wantEcho: false},
	}
	h := middleware.Chain(newMux(t), middleware.RequestID())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/projeto-korp", http.NoBody)
			if tc.incoming != "" {
				req.Header.Set(middleware.HeaderRequestID, tc.incoming)
			}
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(middleware.HeaderRequestID)
			if tc.wantEcho && got != tc.incoming {
				t.Fatalf("X-Request-ID = %q, want echo of %q", got, tc.incoming)
			}
			if !tc.wantEcho && !generated.MatchString(got) {
				t.Fatalf("X-Request-ID = %q, want generated id", got)
			}
			if !strings.Contains(rec.Body.String(), `"request_id":"`+got+`"`) {
				t.Fatalf("handler did not see the id in context: %s", rec.Body.String())
			}
		})
	}
}

func TestAccessLog_FieldsLevelsAndQuietRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		path      string
		wantLevel string
		wantMsg   string
		wantRoute string
		wantCode  float64
	}{
		{name: "matched route logged at INFO with pattern not path", path: "/projeto-korp?x=1", wantLevel: "INFO", wantMsg: "request handled", wantRoute: "/projeto-korp", wantCode: 200},
		{name: "unmatched route is INFO with unmatched label", path: "/nope", wantLevel: "INFO", wantMsg: "request handled", wantRoute: "unmatched", wantCode: 404},
		{name: "probe route is DEBUG", path: "/healthz", wantLevel: "DEBUG", wantMsg: "request handled", wantRoute: "/healthz", wantCode: 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := logging.New(&buf, slog.LevelDebug, "svc", "v")
			h := middleware.Chain(newMux(t), middleware.RequestID(), middleware.AccessLog(log, "/healthz"))

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tc.path, http.NoBody))

			lines := logLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("want exactly one access log line, got %d: %v", len(lines), lines)
			}
			line := lines[0]
			if line[keyLevel] != tc.wantLevel || line[keyMsg] != tc.wantMsg {
				t.Fatalf("level/msg = %v/%v, want %s/%s", line[keyLevel], line[keyMsg], tc.wantLevel, tc.wantMsg)
			}
			if line[logging.KeyRoute] != tc.wantRoute || line[logging.KeyStatus] != tc.wantCode || line[logging.KeyMethod] != "GET" {
				t.Fatalf("route/status/method = %v/%v/%v", line[logging.KeyRoute], line[logging.KeyStatus], line[logging.KeyMethod])
			}
			if _, ok := line[logging.KeyDurationMS].(float64); !ok {
				t.Fatalf("duration_ms missing: %v", line)
			}
			if _, ok := line[logging.KeyRequestID].(string); !ok {
				t.Fatalf("request_id missing: %v", line)
			}
			if _, hasPath := line[logging.KeyPath]; hasPath != (tc.wantRoute == "unmatched") {
				t.Fatalf("path presence = %v, want only for unmatched: %v", hasPath, line)
			}
		})
	}
}

func TestAccessLog_ServerErrorIsError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, "svc", "v")
	failing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	h := middleware.AccessLog(log)(failing)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0][keyLevel] != "ERROR" || lines[0][keyMsg] != "request failed" {
		t.Fatalf("log = %v", lines)
	}
}

func TestMetrics_CountsByPatternAndHandlesPanics(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewPedanticRegistry()
	m := metrics.New(reg, "v", "c")
	h := middleware.Chain(newMux(t), middleware.Recover(slog.New(slog.DiscardHandler), m.PanicsRecovered), middleware.Metrics(m))

	for _, p := range []string{"/projeto-korp", "/projeto-korp?x=1", "/nope", "/boom"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, http.NoBody))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PROPFIND", "/projeto-korp", http.NoBody))

	tests := []struct {
		labels []string
		want   float64
	}{
		{labels: []string{"GET", "/projeto-korp", "200"}, want: 2},
		{labels: []string{"GET", "unmatched", "404"}, want: 1},
		{labels: []string{"GET", "/boom", "500"}, want: 1},
		{labels: []string{"other", "unmatched", "405"}, want: 1},
	}
	for _, tc := range tests {
		if got := testutil.ToFloat64(m.Requests.WithLabelValues(tc.labels...)); got != tc.want {
			t.Errorf("http_requests_total%v = %v, want %v", tc.labels, got, tc.want)
		}
	}
	if got := testutil.ToFloat64(m.InFlight); got != 0 {
		t.Fatalf("http_requests_in_flight = %v after requests, want 0", got)
	}
	if got := testutil.ToFloat64(m.PanicsRecovered); got != 1 {
		t.Fatalf("korp_panics_recovered_total = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(m.Duration, "http_request_duration_seconds"); got == 0 {
		t.Fatal("histogram has no observations")
	}
	if problems, err := testutil.GatherAndLint(reg); err != nil || len(problems) > 0 {
		t.Fatalf("promlint: %v %v", err, problems)
	}
}
