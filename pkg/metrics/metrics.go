// Package metrics defines and registers Prometheus metrics for the Hetzner
// Karpenter provider under the "karpenter_hetzner_" namespace.
//
// All metrics are registered once in an init() against controller-runtime's
// shared Registry so they coexist safely with karpenter-core metrics.
// Callers import this package for its side-effects and then invoke the helper
// functions (RecordServerCreate, RecordServerDelete, RecordDrift,
// RecordCacheHit, RecordCacheMiss) to instrument hot paths.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metric label values for the "result" label.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

var (
	// serverCreateTotal counts server create attempts by result (success|error).
	serverCreateTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "karpenter_hetzner",
		Name:      "server_create_total",
		Help:      "Total number of Hetzner server create calls by result.",
	}, []string{"result"})

	// serverCreateDurationSeconds measures how long server creates take (wall
	// time from Create call through action-wait completion).
	serverCreateDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "karpenter_hetzner",
		Name:      "server_create_duration_seconds",
		Help:      "Duration of Hetzner server create operations in seconds.",
		Buckets:   []float64{1, 5, 10, 20, 30, 60, 120},
	})

	// serverDeleteTotal counts server delete attempts by result (success|error).
	serverDeleteTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "karpenter_hetzner",
		Name:      "server_delete_total",
		Help:      "Total number of Hetzner server delete calls by result.",
	}, []string{"result"})

	// hcloudAPICallsTotal counts hcloud API calls by operation and result. We
	// scope it to the operations we actually instrument (server_create,
	// server_delete, placement_group, image_list) to keep label cardinality
	// predictable without threading a counter through every internal helper.
	hcloudAPICallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "karpenter_hetzner",
		Name:      "hcloud_api_calls_total",
		Help:      "Total number of hcloud API calls by operation and result.",
	}, []string{"operation", "result"})

	// driftDetectedTotal counts drift detections by reason.
	driftDetectedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "karpenter_hetzner",
		Name:      "drift_detected_total",
		Help:      "Total number of drift detections by reason.",
	}, []string{"reason"})

	// instanceTypeCacheTotal counts instance-type cache lookups by result (hit|miss).
	instanceTypeCacheTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "karpenter_hetzner",
		Name:      "instance_type_cache_total",
		Help:      "Total number of instance-type cache lookups by result.",
	}, []string{"result"})
)

func init() {
	crmetrics.Registry.MustRegister(
		serverCreateTotal,
		serverCreateDurationSeconds,
		serverDeleteTotal,
		hcloudAPICallsTotal,
		driftDetectedTotal,
		instanceTypeCacheTotal,
	)
}

// RecordServerCreate records a server create result and its duration.
// Call this once per Create() return, passing "success" or "error" and
// the wall-clock duration measured from the call's entry point.
func RecordServerCreate(result string, dur time.Duration) {
	serverCreateTotal.WithLabelValues(result).Inc()
	serverCreateDurationSeconds.Observe(dur.Seconds())
	hcloudAPICallsTotal.WithLabelValues("server_create", result).Inc()
}

// RecordServerDelete records a server delete result.
func RecordServerDelete(result string) {
	serverDeleteTotal.WithLabelValues(result).Inc()
	hcloudAPICallsTotal.WithLabelValues("server_delete", result).Inc()
}

// RecordDrift increments the drift counter for the given reason string
// (e.g. "ImageDrift", "NetworkDrift").
func RecordDrift(reason string) {
	driftDetectedTotal.WithLabelValues(reason).Inc()
}

// RecordCacheHit records an instance-type cache hit.
func RecordCacheHit() {
	instanceTypeCacheTotal.WithLabelValues("hit").Inc()
}

// RecordCacheMiss records an instance-type cache miss (triggers a fresh API fetch).
func RecordCacheMiss() {
	instanceTypeCacheTotal.WithLabelValues("miss").Inc()
}
