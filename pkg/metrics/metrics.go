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

	// nodesPriceUnresolved reports how many Karpenter-owned nodes currently price
	// at zero, because their zone and capacity-type match no offering of their
	// instance type. Karpenter cannot consolidate a node it prices at zero:
	// nothing is cheaper, so every replacement is rejected and the decision reads
	// as a considered "Can't replace with a cheaper node". A gauge, not a counter,
	// because the question is how many are broken right now.
	nodesPriceUnresolved = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "karpenter_hetzner",
		Name:      "nodes_price_unresolved",
		Help:      "Karpenter-owned nodes whose price does not resolve to any offering, disabling consolidation for them.",
	})

	// priceHealthScanTotal counts scans by result. A gauge reads zero from process
	// start, so "no broken nodes" and "the check has never managed to run" are the
	// same number; only a rising success count says the zero is real.
	priceHealthScanTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "karpenter_hetzner",
		Name:      "price_health_scan_total",
		Help:      "Outcomes of the node price-resolution scan.",
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
		nodesPriceUnresolved,
		priceHealthScanTotal,
	)
}

// SetNodesPriceUnresolved records how many nodes currently fail price
// resolution. Call it on every successful scan, including with zero, so the
// gauge falls back to healthy once the cause is fixed.
func SetNodesPriceUnresolved(n int) {
	nodesPriceUnresolved.Set(float64(n))
}

// RecordPriceHealthScan records whether a price-resolution scan completed.
func RecordPriceHealthScan(result string) {
	priceHealthScanTotal.WithLabelValues(result).Inc()
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
