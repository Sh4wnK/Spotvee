package metrics

import (
	"fmt"
	"strings"
)

// AppMetrics renders the apply-latency histogram as API JSON and Prometheus
// text. It wraps a *Histogram supplied by the caller (the sync engine's).
type AppMetrics struct {
	latency *Histogram
	exposed bool
}

// NewAppMetrics builds an aggregator over the given latency histogram.
func NewAppMetrics(latency *Histogram) *AppMetrics {
	return &AppMetrics{latency: latency, exposed: true}
}

// SetExposed toggles /metrics exposure.
func (m *AppMetrics) SetExposed(on bool) { m.exposed = on }

// Exposed reports whether /metrics should be served.
func (m *AppMetrics) Exposed() bool { return m.exposed }

// Histogram returns the underlying latency histogram.
func (m *AppMetrics) Histogram() *Histogram { return m.latency }

// Summary returns the percentile snapshot.
func (m *AppMetrics) Summary() Summary { return m.latency.Summary() }

// Prometheus renders the latency distribution as Prometheus text format.
func (m *AppMetrics) Prometheus() []byte {
	s := m.latency.Summary()
	var b strings.Builder
	b.WriteString("# HELP spotvee_sync_latency_ms End-to-end track-color apply latency.\n")
	b.WriteString("# TYPE spotvee_sync_latency_ms gauge\n")
	fmt.Fprintf(&b, "spotvee_sync_latency_ms_p50 %s\n", fmtFloat(s.P50))
	fmt.Fprintf(&b, "spotvee_sync_latency_ms_p95 %s\n", fmtFloat(s.P95))
	fmt.Fprintf(&b, "spotvee_sync_latency_ms_p99 %s\n", fmtFloat(s.P99))
	fmt.Fprintf(&b, "spotvee_sync_latency_ms_max %s\n", fmtFloat(s.Max))
	fmt.Fprintf(&b, "spotvee_sync_latency_count %d\n", s.Count)
	return []byte(b.String())
}

func fmtFloat(f float64) string {
	if f != f { // NaN
		return "0"
	}
	return fmt.Sprintf("%.3f", f)
}
