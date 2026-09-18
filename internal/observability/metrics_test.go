package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsVectorsAndLabels(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	require.NotNil(t, m.Registry)

	counters := []struct {
		name   string
		vec    *prometheus.CounterVec
		labels []string
	}{
		{name: "spinneret_acquire_total", vec: m.AcquireTotal, labels: []string{"site", "group", "result"}},
		{name: "spinneret_acquire_admission_total", vec: m.AcquireAdmissionTotal, labels: []string{"result"}},
		{name: "spinneret_report_ingest_total", vec: m.ReportIngestTotal, labels: []string{"result"}},
		{name: "spinneret_report_total", vec: m.ReportTotal, labels: []string{"site", "group", "outcome"}},
		{name: "spinneret_actions_total", vec: m.ActionsTotal, labels: []string{"site", "action", "scope", "mode"}},
		{name: "spinneret_breaker_transitions_total", vec: m.BreakerTransitions, labels: []string{"site", "group", "to"}},
		{name: "spinneret_lease_reaped_total", vec: m.LeaseReaped, labels: []string{"site", "kind"}},
		{name: "spinneret_http_requests_total", vec: m.HTTPRequests, labels: []string{"procedure", "code"}},
		{name: "spinneret_notify_deliveries_total", vec: m.NotifyDeliveries, labels: []string{"kind", "result"}},
		{name: "spinneret_db_write_batches_total", vec: m.DBWriteBatches, labels: []string{"writer", "result"}},
	}
	gauges := []struct {
		name   string
		vec    *prometheus.GaugeVec
		labels []string
	}{
		{name: "spinneret_identities", vec: m.Identities, labels: []string{"site", "type", "state"}},
		{name: "spinneret_identities_available", vec: m.IdentitiesAvailable, labels: []string{"site", "group"}},
		{name: "spinneret_breaker_state", vec: m.BreakerState, labels: []string{"site", "group"}},
		{name: "spinneret_proxies", vec: m.Proxies, labels: []string{"site", "state"}},
		{name: "spinneret_stream_pending", vec: m.StreamPending, labels: []string{"shard"}},
	}
	histograms := []struct {
		name    string
		vec     *prometheus.HistogramVec
		labels  []string
		buckets []float64
	}{
		{name: "spinneret_acquire_duration_seconds", vec: m.AcquireDuration, labels: []string{"site"},
			buckets: []float64{0.0002, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25}},
		{name: "spinneret_http_request_duration_seconds", vec: m.HTTPRequestDuration, labels: []string{"procedure"},
			buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}},
	}

	for _, c := range counters {
		_, err := c.vec.GetMetricWithLabelValues(append(values(len(c.labels)), "extra")...)
		require.Error(t, err, "%s must reject extra labels", c.name)
		ctr, err := c.vec.GetMetricWith(labelMap(c.labels))
		require.NoError(t, err, c.name)
		ctr.Inc()
	}
	for _, g := range gauges {
		_, err := g.vec.GetMetricWithLabelValues(append(values(len(g.labels)), "extra")...)
		require.Error(t, err, "%s must reject extra labels", g.name)
		gg, err := g.vec.GetMetricWith(labelMap(g.labels))
		require.NoError(t, err, g.name)
		gg.Set(3)
	}
	for _, h := range histograms {
		obs, err := h.vec.GetMetricWith(labelMap(h.labels))
		require.NoError(t, err, h.name)
		obs.Observe(0.003)
	}
	m.ReportLag.Observe(0.3)
	m.ReportProcessDuration.Observe(0.001)
	m.AcquireScriptDuration.Observe(0.0004)
	m.AcquireAdmissionWait.Observe(0.0004)
	m.StreamOwnedShards.Set(4)
	m.ConfigWatchers.Set(7)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	byName := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		byName[f.GetName()] = f
	}

	for _, c := range counters {
		requireFamily(t, byName, c.name, dto.MetricType_COUNTER, c.labels)
	}
	for _, g := range gauges {
		requireFamily(t, byName, g.name, dto.MetricType_GAUGE, g.labels)
	}
	for _, h := range histograms {
		f := requireFamily(t, byName, h.name, dto.MetricType_HISTOGRAM, h.labels)
		requireBuckets(t, f, h.buckets)
	}
	requireBuckets(t, requireFamily(t, byName, "spinneret_report_lag_seconds", dto.MetricType_HISTOGRAM, nil),
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10})
	requireFamily(t, byName, "spinneret_report_process_duration_seconds", dto.MetricType_HISTOGRAM, nil)
	requireBuckets(t, requireFamily(t, byName, "spinneret_acquire_script_seconds", dto.MetricType_HISTOGRAM, nil),
		[]float64{0.0002, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25})
	requireBuckets(t, requireFamily(t, byName, "spinneret_acquire_admission_wait_seconds", dto.MetricType_HISTOGRAM, nil),
		[]float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1})
	requireFamily(t, byName, "spinneret_stream_owned_shards", dto.MetricType_GAUGE, nil)
	requireFamily(t, byName, "spinneret_config_watchers", dto.MetricType_GAUGE, nil)
	require.Contains(t, byName, "go_goroutines", "Go collector must be registered")

	require.InDelta(t, 4, metricValue(t, m.StreamOwnedShards), 0)
}

func metricValue(t *testing.T, c prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	require.NoError(t, c.Write(&out))
	switch {
	case out.Counter != nil:
		return out.GetCounter().GetValue()
	case out.Gauge != nil:
		return out.GetGauge().GetValue()
	default:
		t.Fatalf("unsupported metric type")
		return 0
	}
}

func values(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "v"
	}
	return out
}

func labelMap(labels []string) prometheus.Labels {
	out := prometheus.Labels{}
	for _, l := range labels {
		out[l] = "v"
	}
	return out
}

func requireFamily(t *testing.T, byName map[string]*dto.MetricFamily, name string, typ dto.MetricType, labels []string) *dto.MetricFamily {
	t.Helper()
	f, ok := byName[name]
	require.True(t, ok, "metric %s not registered", name)
	require.Equal(t, typ, f.GetType(), name)
	require.Len(t, f.GetMetric(), 1, name)
	got := make([]string, 0)
	for _, lp := range f.GetMetric()[0].GetLabel() {
		got = append(got, lp.GetName())
	}
	require.ElementsMatch(t, labels, got, name)
	return f
}

func requireBuckets(t *testing.T, f *dto.MetricFamily, want []float64) {
	t.Helper()
	var got []float64
	for _, b := range f.GetMetric()[0].GetHistogram().GetBucket() {
		got = append(got, b.GetUpperBound())
	}
	require.Equal(t, want, got, f.GetName())
}

func TestNewMetricsIndependentRegistries(t *testing.T) {
	t.Parallel()
	a, b := NewMetrics(), NewMetrics()
	a.AcquireTotal.WithLabelValues("s", "g", "ok").Inc()
	require.InDelta(t, 1, metricValue(t, a.AcquireTotal.WithLabelValues("s", "g", "ok")), 0)
	require.InDelta(t, 0, metricValue(t, b.AcquireTotal.WithLabelValues("s", "g", "ok")), 0)
}

func TestMetricsHandler(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.ReportIngestTotal.WithLabelValues("accepted").Add(3)
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, `spinneret_report_ingest_total{result="accepted"} 3`)
	require.Contains(t, text, "go_goroutines")
}

func TestLabel(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 127) + "世界"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "_"},
		{name: "plain", in: "shop", want: "shop"},
		{name: "unicode kept", in: "示例站点", want: "示例站点"},
		{name: "invalid utf8 replaced", in: "a\xffb", want: "a�b"},
		{name: "exactly max", in: strings.Repeat("x", 128), want: strings.Repeat("x", 128)},
		{name: "truncated on rune boundary", in: long, want: strings.Repeat("a", 127)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Label(tt.in)
			require.Equal(t, tt.want, got)
			require.True(t, utf8.ValidString(got))
			require.LessOrEqual(t, len(got), 128)
		})
	}
	// A normalized value must always be accepted by a vector.
	m := NewMetrics()
	require.NotPanics(t, func() { m.ReportIngestTotal.WithLabelValues(Label("\xff\xfe")).Inc() })
}

func TestBreakerStateValue(t *testing.T) {
	t.Parallel()
	require.InDelta(t, 0, BreakerStateValue("closed"), 0)
	require.InDelta(t, 1, BreakerStateValue("half_open"), 0)
	require.InDelta(t, 2, BreakerStateValue("open"), 0)
	require.InDelta(t, 0, BreakerStateValue("bogus"), 0)
}

func TestRegisterAcquireGate(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	gauges := map[string]float64{
		"spinneret_acquire_inflight":       3,
		"spinneret_acquire_queued":         7,
		"spinneret_acquire_inflight_limit": 32,
		"spinneret_acquire_peers":          2,
	}
	before, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range before {
		require.NotContains(t, gauges, f.GetName(), "gate gauges are registered lazily")
	}

	value := func(v int) func() int { return func() int { return v } }
	m.RegisterAcquireGate(value(32), value(3), value(7), value(2))
	// Registering twice is how two services sharing one Metrics behave; it must
	// not panic and must not duplicate a series.
	require.NotPanics(t, func() { m.RegisterAcquireGate(value(32), value(3), value(7), value(2)) })

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	seen := map[string]int{}
	for _, f := range families {
		if want, ok := gauges[f.GetName()]; ok {
			seen[f.GetName()]++
			require.Equal(t, dto.MetricType_GAUGE, f.GetType(), f.GetName())
			require.Len(t, f.GetMetric(), 1, f.GetName())
			require.InDelta(t, want, f.GetMetric()[0].GetGauge().GetValue(), 0, f.GetName())
		}
	}
	for name := range gauges {
		require.Equal(t, 1, seen[name], "%s must be registered exactly once", name)
	}

	// Nil functions are skipped and a nil *Metrics is a no-op.
	m2 := NewMetrics()
	m2.RegisterAcquireGate(nil, value(1), nil, nil)
	names := map[string]bool{}
	f2, err := m2.Registry.Gather()
	require.NoError(t, err)
	for _, f := range f2 {
		names[f.GetName()] = true
	}
	require.True(t, names["spinneret_acquire_inflight"])
	require.False(t, names["spinneret_acquire_inflight_limit"])

	var nilMetrics *Metrics
	require.NotPanics(t, func() { nilMetrics.RegisterAcquireGate(value(1), value(1), value(1), value(1)) })
}

func TestRegisterAcquirePeerRegistry(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	const (
		ageName      = "spinneret_acquire_peer_beat_age_seconds"
		failuresName = "spinneret_acquire_peer_beat_failures_total"
	)
	before, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, f := range before {
		require.NotEqual(t, ageName, f.GetName(), "registry series are registered lazily")
		require.NotEqual(t, failuresName, f.GetName(), "registry series are registered lazily")
	}

	age := func() time.Duration { return 1500 * time.Millisecond }
	failures := func() int64 { return 4 }
	m.RegisterAcquirePeerRegistry(age, failures)
	require.NotPanics(t, func() { m.RegisterAcquirePeerRegistry(age, failures) },
		"two services sharing one Metrics must not panic")

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	seen := map[string]int{}
	for _, f := range families {
		switch f.GetName() {
		case ageName:
			seen[ageName]++
			require.Equal(t, dto.MetricType_GAUGE, f.GetType())
			require.InDelta(t, 1.5, f.GetMetric()[0].GetGauge().GetValue(), 0)
		case failuresName:
			seen[failuresName]++
			require.Equal(t, dto.MetricType_COUNTER, f.GetType())
			require.InDelta(t, 4, f.GetMetric()[0].GetCounter().GetValue(), 0)
		}
	}
	require.Equal(t, 1, seen[ageName])
	require.Equal(t, 1, seen[failuresName])

	// Nil functions are skipped and a nil *Metrics is a no-op.
	m2 := NewMetrics()
	m2.RegisterAcquirePeerRegistry(nil, failures)
	names := map[string]bool{}
	f2, err := m2.Registry.Gather()
	require.NoError(t, err)
	for _, f := range f2 {
		names[f.GetName()] = true
	}
	require.False(t, names[ageName])
	require.True(t, names[failuresName])

	var nilMetrics *Metrics
	require.NotPanics(t, func() { nilMetrics.RegisterAcquirePeerRegistry(age, failures) })
}
