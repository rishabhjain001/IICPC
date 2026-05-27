// Package metrics defines and registers Prometheus metrics for the Control
// Plane API service (Requirement 11.5).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metrics exposed by the Control Plane API.
type Metrics struct {
	// ActiveSandboxes is a Gauge tracking the number of currently active
	// sandboxes across all benchmark runs.
	ActiveSandboxes prometheus.Gauge

	// ActiveBots is a Gauge tracking the number of currently active synthetic
	// trading bots across all benchmark runs.
	ActiveBots prometheus.Gauge

	// KafkaConsumerLag is a GaugeVec tracking the per-topic consumer lag for
	// the control-plane Kafka consumer group. Label: "topic".
	KafkaConsumerLag *prometheus.GaugeVec

	// TSDBWriteLatency is a Gauge tracking the p99 write latency to
	// TimescaleDB in milliseconds.
	TSDBWriteLatency prometheus.Gauge

	// RequestsTotal is a CounterVec tracking the total number of gRPC requests
	// handled by the Control Plane API. Label: "method".
	RequestsTotal *prometheus.CounterVec
}

// NewRegistry creates a new, empty Prometheus registry. Using a custom
// registry (rather than the default global one) keeps tests isolated.
func NewRegistry() *prometheus.Registry {
	return prometheus.NewRegistry()
}

// RegisterMetrics creates all Control Plane metrics, registers them with reg,
// and returns a populated *Metrics. Panics on registration failure (which
// indicates a programming error such as a duplicate metric name).
func RegisterMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		ActiveSandboxes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dbhp_active_sandboxes",
			Help: "Number of currently active sandbox containers.",
		}),

		ActiveBots: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dbhp_active_bots",
			Help: "Number of currently active synthetic trading bots.",
		}),

		KafkaConsumerLag: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "dbhp_kafka_consumer_lag",
				Help: "Kafka consumer lag per topic for the control-plane consumer group.",
			},
			[]string{"topic"},
		),

		TSDBWriteLatency: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dbhp_timescaledb_write_latency_p99",
			Help: "p99 write latency to TimescaleDB in milliseconds.",
		}),

		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbhp_control_plane_requests_total",
				Help: "Total number of gRPC requests handled by the Control Plane API.",
			},
			[]string{"method"},
		),
	}

	reg.MustRegister(
		m.ActiveSandboxes,
		m.ActiveBots,
		m.KafkaConsumerLag,
		m.TSDBWriteLatency,
		m.RequestsTotal,
	)

	return m
}
