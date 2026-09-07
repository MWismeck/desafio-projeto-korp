package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/MWismeck/desafio-projeto-korp/internal/metrics"
)

func TestNew_RegistersEverythingAndPassesLint(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewPedanticRegistry()
	m := metrics.New(reg, "1.0.0", "abc123")
	m.Requests.WithLabelValues("GET", "/projeto-korp", "200").Inc()
	m.Duration.WithLabelValues("GET", "/projeto-korp").Observe(0.01)
	m.ServiceUp.Set(1)

	problems, err := testutil.GatherAndLint(reg)
	if err != nil {
		t.Fatalf("GatherAndLint: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("promlint problems: %v", problems)
	}

	for _, name := range []string{
		"http_requests_total", "http_request_duration_seconds", "http_requests_in_flight",
		"service_up", "korp_build_info", "korp_panics_recovered_total", "go_goroutines", "process_start_time_seconds",
	} {
		if n := testutil.CollectAndCount(reg, name); n == 0 {
			t.Errorf("metric %s not registered", name)
		}
	}
}

func TestNew_BuildInfoCarriesVersionCommitAndGo(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	metrics.New(reg, "1.2.3", "deadbeef")

	want := `
# HELP korp_build_info Informações de build do http-server-projeto-korp (valor sempre 1).
# TYPE korp_build_info gauge
korp_build_info{commit="deadbeef",go_version="` + runtime.Version() + `",version="1.2.3"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "korp_build_info"); err != nil {
		t.Fatalf("korp_build_info mismatch: %v", err)
	}
}

func TestLatencyBuckets_ContainSLOThreshold(t *testing.T) {
	t.Parallel()
	for _, b := range metrics.LatencyBuckets {
		if b == 0.3 {
			return
		}
	}
	t.Fatalf("LatencyBuckets %v lack the 0.3 s SLO threshold", metrics.LatencyBuckets)
}

func TestHandler_ServesPrometheusAndOpenMetrics(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.New(reg, "v", "c")
	m.Requests.WithLabelValues("GET", "/projeto-korp", "200").Inc()
	m.Duration.WithLabelValues("GET", "/projeto-korp").Observe(0.02)
	h := metrics.Handler(reg)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, re := range []string{
		`(?m)^# HELP http_requests_total `, `(?m)^# TYPE http_requests_total counter`,
		`(?m)^http_requests_total\{code="200",method="GET",route="/projeto-korp"\} 1`,
		`(?m)^# TYPE service_up gauge`, `(?m)^# TYPE korp_build_info gauge`,
		`(?m)^http_request_duration_seconds_bucket\{.*le="0.3"\}`,
	} {
		if !regexp.MustCompile(re).MatchString(body) {
			t.Errorf("exposition lacks %s:\n%s", re, body)
		}
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Fatalf("OpenMetrics not negotiated: Content-Type = %q", ct)
	}
	if !strings.HasSuffix(strings.TrimSpace(rec.Body.String()), "# EOF") {
		t.Fatalf("OpenMetrics exposition must end with # EOF")
	}
}
