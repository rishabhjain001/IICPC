package metrics_test

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/iicpc/dbhp/control-plane-api/internal/metrics"
)

// TestRegisterMetrics_AllFiveMetricsPresent verifies that all five required
// metrics are registered in the registry. Vec metrics (GaugeVec, CounterVec)
// are exercised with a label value before gathering so Prometheus includes
// them in the output (Prometheus only serialises vec metrics once they have
// at least one labelled observation).
func TestRegisterMetrics_AllFiveMetricsPresent(t *testing.T) {
	reg := metrics.NewRegistry()
	m := metrics.RegisterMetrics(reg)

	// Touch vec metrics so they appear in Gather output.
	m.KafkaConsumerLag.WithLabelValues("telemetry.raw.test").Set(0)
	m.RequestsTotal.WithLabelValues("/test.Service/Method").Add(0)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	want := map[string]bool{
		"dbhp_active_sandboxes":              false,
		"dbhp_active_bots":                   false,
		"dbhp_kafka_consumer_lag":            false,
		"dbhp_timescaledb_write_latency_p99": false,
		"dbhp_control_plane_requests_total":  false,
	}

	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("expected metric %q not found in registry", name)
		}
	}
}

// TestRegisterMetrics_CounterIncrementsCorrectly verifies that incrementing
// RequestsTotal is reflected in the gathered metric value.
func TestRegisterMetrics_CounterIncrementsCorrectly(t *testing.T) {
	reg := metrics.NewRegistry()
	m := metrics.RegisterMetrics(reg)

	const method = "/dbhp.control.v1.BenchmarkService/CreateRun"

	m.RequestsTotal.WithLabelValues(method).Inc()
	m.RequestsTotal.WithLabelValues(method).Inc()
	m.RequestsTotal.WithLabelValues(method).Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var found bool
	for _, mf := range families {
		if mf.GetName() != "dbhp_control_plane_requests_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			// Find the metric with the matching label.
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "method" && lp.GetValue() == method {
					val := metric.GetCounter().GetValue()
					if val != 3 {
						t.Errorf("expected counter value 3, got %v", val)
					}
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("metric dbhp_control_plane_requests_total with method=%q not found", method)
	}

	// Suppress unused import warning.
	_ = dto.MetricFamily{}
}

// TestRegisterMetrics_GaugeSetAndRead verifies that setting a Gauge is
// reflected in the gathered metric value.
func TestRegisterMetrics_GaugeSetAndRead(t *testing.T) {
	reg := metrics.NewRegistry()
	m := metrics.RegisterMetrics(reg)

	m.ActiveSandboxes.Set(42)
	m.ActiveBots.Set(1337)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	checks := map[string]float64{
		"dbhp_active_sandboxes": 42,
		"dbhp_active_bots":      1337,
	}

	for _, mf := range families {
		if want, ok := checks[mf.GetName()]; ok {
			for _, metric := range mf.GetMetric() {
				got := metric.GetGauge().GetValue()
				if got != want {
					t.Errorf("metric %q: want %v, got %v", mf.GetName(), want, got)
				}
			}
		}
	}
}
