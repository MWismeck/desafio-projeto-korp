// Package metrics owns everything registered in the Prometheus registry: the RED metrics of the
// HTTP server, the self-declared availability gauge, build information and runtime collectors.
package metrics

import (
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Label names of the RED metrics; route is always the ServeMux pattern, never the raw path.
const (
	LabelMethod = "method"
	LabelRoute  = "route"
	LabelCode   = "code"
)

// Limits of the /metrics handler: a scrape must never pile up behind a slow one.
const (
	scrapeTimeout      = 5 * time.Second
	maxScrapesInFlight = 3
)

// LatencyBuckets cover 5 ms to 10 s and contain exactly the 0.3 s SLO threshold, so the latency
// SLI is read from a bucket boundary instead of being interpolated.
var LatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.15, 0.2, 0.3, 0.5, 1, 2.5, 5, 10}

// Metrics groups the application metrics that the middlewares and the server update.
type Metrics struct {
	Requests        *prometheus.CounterVec
	Duration        *prometheus.HistogramVec
	InFlight        prometheus.Gauge
	ServiceUp       prometheus.Gauge
	PanicsRecovered prometheus.Counter
}

// New registers the application metrics and the Go/process collectors in reg. The registry is
// injected so tests own their own and nothing ever touches the global one.
func New(reg prometheus.Registerer, version, commit string) *Metrics {
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	f := promauto.With(reg)
	m := &Metrics{
		Requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total de requisições HTTP atendidas, por método, rota template e código de status.",
		}, []string{LabelMethod, LabelRoute, LabelCode}),
		Duration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duração das requisições HTTP em segundos, por método e rota template.",
			Buckets: LatencyBuckets,
		}, []string{LabelMethod, LabelRoute}),
		InFlight: f.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Requisições HTTP em processamento neste instante.",
		}),
		ServiceUp: f.NewGauge(prometheus.GaugeOpts{
			Name: "service_up",
			Help: "1 quando o serviço está pronto para atender; 0 durante boot e drenagem (shutdown).",
		}),
		PanicsRecovered: f.NewCounter(prometheus.CounterOpts{
			Name: "korp_panics_recovered_total",
			Help: "Total de panics recuperados pelo middleware de recovery (cada um virou resposta 500).",
		}),
	}
	buildInfo := f.NewGaugeVec(prometheus.GaugeOpts{
		Name: "korp_build_info",
		Help: "Informações de build do http-server-projeto-korp (valor sempre 1).",
	}, []string{"version", "commit", "go_version"})
	buildInfo.WithLabelValues(version, commit, runtime.Version()).Set(1)
	return m
}

// Handler exposes reg in Prometheus text and OpenMetrics formats (the latter negotiated by Accept).
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:            reg,
		EnableOpenMetrics:   true,
		Timeout:             scrapeTimeout,
		MaxRequestsInFlight: maxScrapesInFlight,
	})
}
