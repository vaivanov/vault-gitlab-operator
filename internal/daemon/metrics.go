package daemon

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/scentbird/vault-gitlab-operator/internal/sync"
)

// Metrics holds the operator's Prometheus instruments on a private
// registry (so tests can build many without collisions).
type Metrics struct {
	Registry *prometheus.Registry

	runs        *prometheus.CounterVec
	actions     *prometheus.CounterVec
	lastSuccess prometheus.Gauge
	duration    prometheus.Histogram
}

func NewMetrics() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	m.runs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vgo_sync_runs_total",
		Help: "Reconcile runs by result (success = no failed actions or target errors).",
	}, []string{"result"})
	m.actions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vgo_actions_total",
		Help: "Planned/applied actions by operation and target kind.",
	}, []string{"op", "target_kind"})
	m.lastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vgo_last_sync_success_timestamp_seconds",
		Help: "Unix timestamp of the last fully successful reconcile run.",
	})
	m.duration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vgo_sync_duration_seconds",
		Help:    "Duration of reconcile runs.",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
	})
	m.Registry.MustRegister(m.runs, m.actions, m.lastSuccess, m.duration)
	return m
}

// Observe records one finished reconcile run.
func (m *Metrics) Observe(report *sync.Report) {
	result := "success"
	if report.HasErrors() {
		result = "error"
	} else {
		m.lastSuccess.SetToCurrentTime()
	}
	m.runs.WithLabelValues(result).Inc()
	m.duration.Observe(report.Duration.Seconds())

	for _, t := range report.Targets {
		kind := string(t.Target.Kind)
		for _, a := range t.Actions {
			op := string(a.Op)
			if a.Err != nil {
				op = "failed"
			}
			m.actions.WithLabelValues(op, kind).Inc()
		}
	}
}
