package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	routingAPIRequests = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vsr_routing_api_requests_total",
		Help: "Total number of HTTP routing API requests",
	})

	routingAPIErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vsr_routing_api_errors_total",
		Help: "Total number of HTTP routing API errors by reason",
	}, []string{"reason"})

	routingAPILatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vsr_routing_api_latency_seconds",
		Help:    "HTTP routing API request latency in seconds",
		Buckets: prometheus.DefBuckets,
	})
)

// RecordRoutingAPIRequest records an incoming routing API request.
func RecordRoutingAPIRequest() {
	routingAPIRequests.Inc()
}

// RecordRoutingAPIError records a routing API error by reason.
func RecordRoutingAPIError(reason string) {
	routingAPIErrors.WithLabelValues(reason).Inc()
}

// RecordRoutingAPILatency records the latency of a routing API request.
func RecordRoutingAPILatency(seconds float64) {
	routingAPILatency.Observe(seconds)
}
