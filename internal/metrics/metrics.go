package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	CacheHits       *prometheus.CounterVec
	CacheMisses     *prometheus.CounterVec
	UpstreamBytes   *prometheus.CounterVec
	UpstreamLatency *prometheus.HistogramVec
	RequestsTotal   *prometheus.CounterVec
	ResponsesTotal  *prometheus.CounterVec
	ErrorsTotal     *prometheus.CounterVec
}

// New creates metrics registered with the default prometheus registry.
func New() *Metrics {
	return NewWithRegistry(prometheus.DefaultRegisterer)
}

// NewUnregistered creates metrics without registering them (useful for tests).
func NewUnregistered() *Metrics {
	return NewWithRegistry(nil)
}

// NewWithRegistry creates metrics and registers them with the given registerer.
// Pass nil to skip registration.
func NewWithRegistry(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		CacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smart_git_proxy_cache_hits_total",
			Help: "cache hits by repo and kind",
		}, []string{"repo", "kind"}),
		CacheMisses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smart_git_proxy_cache_misses_total",
			Help: "cache misses by repo and kind",
		}, []string{"repo", "kind"}),
		UpstreamBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smart_git_proxy_upstream_bytes_total",
			Help: "bytes read from upstream",
		}, []string{"repo", "kind"}),
		UpstreamLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "smart_git_proxy_upstream_seconds",
			Help:    "latency for upstream calls",
			Buckets: prometheus.DefBuckets,
		}, []string{"repo", "kind"}),
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smart_git_proxy_requests_total",
			Help: "requests received",
		}, []string{"repo", "kind", "source"}),
		ResponsesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smart_git_proxy_responses_total",
			Help: "responses sent",
		}, []string{"repo", "kind", "status"}),
		ErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smart_git_proxy_errors_total",
			Help: "errors by repo/kind",
		}, []string{"repo", "kind"}),
	}

	if reg != nil {
		reg.MustRegister(
			m.CacheHits,
			m.CacheMisses,
			m.UpstreamBytes,
			m.UpstreamLatency,
			m.RequestsTotal,
			m.ResponsesTotal,
			m.ErrorsTotal,
		)
	}
	return m
}
