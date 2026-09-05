package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metrics exported by the operator. controller-runtime already registers
// reconcile counts, errors, durations and the workqueue metrics, so nothing
// here duplicates those: these cover what only this operator knows.
//
// No metric carries a workload name. Namespaces are bounded by the cluster,
// but workload names are not, and a series for a workload that is later
// deleted would never go away. Workload identity lives in
// status.activeScaledDown and the scaled-down-by annotation instead.
var (
	// scaledDownTotal counts actions taken. The dry_run label matters: while a
	// policy is in dry-run you specifically want to see what it would have
	// done, and without the label that is indistinguishable from a real
	// action.
	scaledDownTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "crashloop_scaled_down_total",
			Help: "Total workloads scaled down or suspended, including dry-run actions.",
		},
		[]string{"policy", "namespace", "kind", "reason", "dry_run"},
	)

	// workloadsScaledDown reports how many workloads a policy is currently
	// holding at zero. A value that stays above zero for a long time is the
	// interesting alert: something has been down and nobody noticed.
	workloadsScaledDown = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "crashloop_workloads_scaled_down",
			Help: "Workloads currently scaled down or suspended by this policy.",
		},
		[]string{"policy"},
	)

	// policyEvaluationErrors covers a blind spot. Per-workload failures are
	// counted and surfaced through the Ready condition but deliberately do not
	// fail the reconcile, so controller_runtime_reconcile_errors_total stays
	// at zero while a policy quietly fails on every workload it looks at.
	policyEvaluationErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "crashloop_policy_evaluation_errors_total",
			Help: "Per-workload errors during evaluation that did not fail the reconcile.",
		},
		[]string{"policy"},
	)

	// policyReady mirrors the Ready condition so it can be alerted on directly.
	policyReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "crashloop_policy_ready",
			Help: "1 when the policy's last evaluation completed without errors, 0 otherwise.",
		},
		[]string{"policy"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		scaledDownTotal,
		workloadsScaledDown,
		policyEvaluationErrors,
		policyReady,
	)
}

// forgetPolicyMetrics drops the per-policy series for a policy that no longer
// exists. Without this the gauges keep reporting on a deleted object forever.
func forgetPolicyMetrics(name string) {
	workloadsScaledDown.DeleteLabelValues(name)
	policyReady.DeleteLabelValues(name)
	policyEvaluationErrors.DeleteLabelValues(name)
}

// boolToFloat maps a boolean onto the 1/0 convention Prometheus gauges use.
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
