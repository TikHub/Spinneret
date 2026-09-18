package observability

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsNamespace prefixes every Spinneret metric name.
const MetricsNamespace = "spinneret"

// EmptyLabel is the label value used for unknown or empty label values.
const EmptyLabel = "_"

// maxLabelBytes caps label values so that unexpected input cannot bloat series.
const maxLabelBytes = 128

// Histogram bucket layouts (seconds).
var (
	acquireDurationBuckets = []float64{0.0002, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25}
	admissionWaitBuckets   = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1}
	reportLagBuckets       = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10}
	reportProcessBuckets   = []float64{0.0002, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}
	httpDurationBuckets    = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
)

// Breaker state gauge values of spinneret_breaker_state.
const (
	BreakerClosedValue   = 0
	BreakerHalfOpenValue = 1
	BreakerOpenValue     = 2
)

// Metrics holds every Prometheus collector exported by the server. All
// collectors are registered in Registry, which also carries the Go runtime and
// process collectors.
type Metrics struct {
	Registry *prometheus.Registry

	AcquireTotal          *prometheus.CounterVec   // site, group, result
	AcquireDuration       *prometheus.HistogramVec // site
	AcquireScriptDuration prometheus.Histogram
	AcquireAdmissionTotal *prometheus.CounterVec // result
	AcquireAdmissionWait  prometheus.Histogram
	ReportIngestTotal     *prometheus.CounterVec // result
	ReportTotal           *prometheus.CounterVec // site, group, outcome
	ReportLag             prometheus.Histogram
	ReportProcessDuration prometheus.Histogram
	Identities            *prometheus.GaugeVec   // site, type, state
	IdentitiesAvailable   *prometheus.GaugeVec   // site, group
	ActionsTotal          *prometheus.CounterVec // site, action, scope, mode
	BreakerState          *prometheus.GaugeVec   // site, group
	BreakerTransitions    *prometheus.CounterVec // site, group, to
	Proxies               *prometheus.GaugeVec   // site, state
	StreamPending         *prometheus.GaugeVec   // shard
	StreamOwnedShards     prometheus.Gauge
	ConfigWatchers        prometheus.Gauge
	LeaseReaped           *prometheus.CounterVec   // site, kind
	HTTPRequests          *prometheus.CounterVec   // procedure, code
	HTTPRequestDuration   *prometheus.HistogramVec // procedure
	NotifyDeliveries      *prometheus.CounterVec   // kind, result
	DBWriteBatches        *prometheus.CounterVec   // writer, result
}

// NewMetrics creates a fresh registry with the Go runtime and process
// collectors and every Spinneret metric vector registered.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		AcquireTotal: counterVec("acquire_total",
			"Lease acquire attempts by result.", "site", "group", "result"),
		AcquireDuration: histogramVec("acquire_duration_seconds",
			"Lease acquire latency.", acquireDurationBuckets, "site"),
		// The same buckets as acquire_duration_seconds on purpose: the two are
		// directly comparable in one panel, and the gap between them is the
		// wait ladder plus rendering.
		AcquireScriptDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: MetricsNamespace,
			Name:      "acquire_script_seconds",
			Help:      "acquire.lua round-trip time at Redis.",
			Buckets:   acquireDurationBuckets,
		}),
		AcquireAdmissionTotal: counterVec("acquire_admission_total",
			"Acquire admission decisions by result.", "result"),
		AcquireAdmissionWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: MetricsNamespace,
			Name:      "acquire_admission_wait_seconds",
			Help:      "Time an acquire attempt waited for an admission permit.",
			Buckets:   admissionWaitBuckets,
		}),
		ReportIngestTotal: counterVec("report_ingest_total",
			"Reports received by ingest result (accepted, duplicated, rejected).", "result"),
		ReportTotal: counterVec("report_total",
			"Processed reports by classified outcome.", "site", "group", "outcome"),
		ReportLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: MetricsNamespace,
			Name:      "report_lag_seconds",
			Help:      "Delay between report receipt and worker processing.",
			Buckets:   reportLagBuckets,
		}),
		ReportProcessDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: MetricsNamespace,
			Name:      "report_process_duration_seconds",
			Help:      "Worker processing time per report.",
			Buckets:   reportProcessBuckets,
		}),
		Identities: gaugeVec("identities",
			"Identities by type and lifecycle state.", "site", "type", "state"),
		IdentitiesAvailable: gaugeVec("identities_available",
			"Identities currently available for acquisition per endpoint group.", "site", "group"),
		ActionsTotal: counterVec("actions_total",
			"Actions planned by the action policy.", "site", "action", "scope", "mode"),
		BreakerState: gaugeVec("breaker_state",
			"Endpoint group breaker state (0 closed, 1 half-open, 2 open).", "site", "group"),
		BreakerTransitions: counterVec("breaker_transitions_total",
			"Breaker state transitions by target state.", "site", "group", "to"),
		Proxies: gaugeVec("proxies",
			"Proxies by state.", "site", "state"),
		StreamPending: gaugeVec("stream_pending",
			"Pending (unacknowledged) report stream entries per shard.", "shard"),
		StreamOwnedShards: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: MetricsNamespace,
			Name:      "stream_owned_shards",
			Help:      "Report stream shards owned by this instance.",
		}),
		ConfigWatchers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: MetricsNamespace,
			Name:      "config_watchers",
			Help:      "Active configuration watch streams on this instance.",
		}),
		LeaseReaped: counterVec("lease_reaped_total",
			"Leases ended by the reaper (expired, abandoned).", "site", "kind"),
		HTTPRequests: counterVec("http_requests_total",
			"Handled RPC/HTTP requests by procedure and status code.", "procedure", "code"),
		HTTPRequestDuration: histogramVec("http_request_duration_seconds",
			"RPC/HTTP request latency by procedure.", httpDurationBuckets, "procedure"),
		NotifyDeliveries: counterVec("notify_deliveries_total",
			"Notification deliveries by channel kind and result.", "kind", "result"),
		DBWriteBatches: counterVec("db_write_batches_total",
			"Batched database writes by writer and result.", "writer", "result"),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.AcquireTotal,
		m.AcquireDuration,
		m.AcquireScriptDuration,
		m.AcquireAdmissionTotal,
		m.AcquireAdmissionWait,
		m.ReportIngestTotal,
		m.ReportTotal,
		m.ReportLag,
		m.ReportProcessDuration,
		m.Identities,
		m.IdentitiesAvailable,
		m.ActionsTotal,
		m.BreakerState,
		m.BreakerTransitions,
		m.Proxies,
		m.StreamPending,
		m.StreamOwnedShards,
		m.ConfigWatchers,
		m.LeaseReaped,
		m.HTTPRequests,
		m.HTTPRequestDuration,
		m.NotifyDeliveries,
		m.DBWriteBatches,
	)
	return m
}

// RegisterAcquireGate registers scrape-time gauges of the acquire admission
// gate. Callers may pass nil functions for gauges they cannot supply, and
// calling it twice is a no-op (a duplicate registration is swallowed), so two
// services sharing one Metrics in a test binary do not panic.
func (m *Metrics) RegisterAcquireGate(limit, inFlight, queued, peers func() int) {
	if m == nil {
		return
	}
	reg := func(name, help string, fn func() int) {
		if fn == nil {
			return
		}
		m.registerCollector(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: MetricsNamespace, Name: name, Help: help,
		}, func() float64 { return float64(fn()) }))
	}
	reg("acquire_inflight", "Acquire script calls currently in flight on this instance.", inFlight)
	reg("acquire_queued", "Acquire attempts waiting for an admission permit on this instance.", queued)
	reg("acquire_inflight_limit", "Acquire admission limit of this instance.", limit)
	reg("acquire_peers", "Live API instances the acquire admission limit is divided among.", peers)
}

// RegisterAcquirePeerRegistry registers scrape-time series of the API instance
// heartbeat that divides the fleet-wide acquire budget. Without them a
// permanently failing heartbeat is indistinguishable from a genuine
// single-instance deployment, while every instance quietly admits the whole
// fleet budget. Nil functions are skipped and a duplicate registration is
// swallowed, as in RegisterAcquireGate.
func (m *Metrics) RegisterAcquirePeerRegistry(beatAge func() time.Duration, beatFailures func() int64) {
	if m == nil {
		return
	}
	if beatAge != nil {
		m.registerCollector(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: MetricsNamespace,
			Name:      "acquire_peer_beat_age_seconds",
			Help:      "Time since the last successful API instance heartbeat, or since process start.",
		}, func() float64 { return beatAge().Seconds() }))
	}
	if beatFailures != nil {
		m.registerCollector(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Namespace: MetricsNamespace,
			Name:      "acquire_peer_beat_failures_total",
			Help:      "API instance heartbeats that failed; the live peer count is frozen while they do.",
		}, func() float64 { return float64(beatFailures()) }))
	}
}

// registerCollector registers c, swallowing a duplicate registration so that
// two services sharing one Metrics in a test binary do not panic.
func (m *Metrics) registerCollector(c prometheus.Collector) {
	if err := m.Registry.Register(c); err != nil {
		var dup prometheus.AlreadyRegisteredError
		if !errors.As(err, &dup) {
			panic(err)
		}
	}
}

// Handler serves the registry in the Prometheus exposition format. Collection
// errors are reported to the scraper but do not abort the response.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Registry:      m.Registry,
	})
}

// Label normalizes a label value: empty values become "_", invalid UTF-8 is
// replaced (Prometheus rejects it) and values are truncated to 128 bytes on a
// rune boundary.
func Label(v string) string {
	if v == "" {
		return EmptyLabel
	}
	if !utf8.ValidString(v) {
		v = strings.ToValidUTF8(v, "�")
	}
	if len(v) > maxLabelBytes {
		cut := maxLabelBytes
		for cut > 0 && !utf8.RuneStart(v[cut]) {
			cut--
		}
		v = v[:cut]
	}
	return v
}

// BreakerStateValue maps a breaker state name to its gauge value. Unknown
// states map to closed.
func BreakerStateValue(state string) float64 {
	switch state {
	case "open":
		return BreakerOpenValue
	case "half_open":
		return BreakerHalfOpenValue
	default:
		return BreakerClosedValue
	}
}

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      name,
		Help:      help,
	}, labels)
}

func gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      name,
		Help:      help,
	}, labels)
}

func histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: MetricsNamespace,
		Name:      name,
		Help:      help,
		Buckets:   buckets,
	}, labels)
}
