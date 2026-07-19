// Package metrics exposes Prometheus counters/histograms/gauges for the
// proxy's job lifecycle, registered against the default global registry.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	jobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "spf_jobs_total",
		Help: "Total number of service-attempt outcomes by terminal status and service (a single job may increment multiple services' counters if it uses fallback).",
	}, []string{"status", "service"})

	downloadDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "spf_download_duration_seconds",
		Help:    "Download duration in seconds by service and quality.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s .. ~68min
	}, []string{"service", "quality"})

	queueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "spf_queue_depth",
		Help: "Current number of jobs in the active queue by status.",
	}, []string{"status"})
)

func RecordJobResult(status, service string) {
	jobsTotal.WithLabelValues(status, service).Inc()
}

func RecordDownloadDuration(service, quality string, seconds float64) {
	downloadDuration.WithLabelValues(service, quality).Observe(seconds)
}

func SetQueueDepth(status string, count int) {
	queueDepth.WithLabelValues(status).Set(float64(count))
}

// PromHTTPHandler returns the standard net/http handler for /metrics.
func PromHTTPHandler() http.Handler {
	return promhttp.Handler()
}
