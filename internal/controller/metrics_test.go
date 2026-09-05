package controller

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// resetMetrics clears the collectors so each test starts from a known state.
// The collectors are package-level and registered once in init, so they carry
// over between tests otherwise.
func resetMetrics() {
	scaledDownTotal.Reset()
	workloadsScaledDown.Reset()
	policyEvaluationErrors.Reset()
	policyReady.Reset()
}

func TestMetrics_ScaleDownIsCounted(t *testing.T) {
	resetMetrics()

	policy := newCrashLoopPolicy("metrics-policy", withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("metrics-policy")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `
# HELP crashloop_scaled_down_total Total workloads scaled down or suspended, including dry-run actions.
# TYPE crashloop_scaled_down_total counter
crashloop_scaled_down_total{dry_run="false",kind="Deployment",namespace="default",policy="metrics-policy",reason="CrashLoopBackOff"} 1
`
	if err := testutil.CollectAndCompare(scaledDownTotal, strings.NewReader(expected)); err != nil {
		t.Errorf("unexpected metric: %v", err)
	}

	if got := testutil.ToFloat64(workloadsScaledDown.WithLabelValues("metrics-policy")); got != 1 {
		t.Errorf("expected 1 workload held down, got %v", got)
	}
	if got := testutil.ToFloat64(policyReady.WithLabelValues("metrics-policy")); got != 1 {
		t.Errorf("expected policy to report ready, got %v", got)
	}
}

// TestMetrics_DryRunIsDistinguishable is the reason the dry_run label exists:
// a simulated action must not look like a real one.
func TestMetrics_DryRunIsDistinguishable(t *testing.T) {
	resetMetrics()

	policy := newCrashLoopPolicy("dry-policy", withDryRun(true), withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("dry-policy")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := testutil.ToFloat64(scaledDownTotal.WithLabelValues(
		"dry-policy", testNamespace, "Deployment", "CrashLoopBackOff", "true")); got != 1 {
		t.Errorf("expected the dry-run action to be counted with dry_run=true, got %v", got)
	}
	if got := testutil.ToFloat64(scaledDownTotal.WithLabelValues(
		"dry-policy", testNamespace, "Deployment", "CrashLoopBackOff", "false")); got != 0 {
		t.Errorf("a dry-run action must not be counted as a real one, got %v", got)
	}
}

// TestMetrics_PartialFailureIsVisible covers the blind spot the counter exists
// for: controller-runtime sees a successful reconcile here.
func TestMetrics_PartialFailureIsVisible(t *testing.T) {
	resetMetrics()

	policy := newCrashLoopPolicy("failing-policy", withAllReplicasFailing(false))
	pod := newFailingPod("orphan-pod", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)
	c := &failingOwnerClient{Client: setupTestClient(policy, pod)}
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("failing-policy")); err != nil {
		t.Fatalf("expected the reconcile to succeed despite the workload error: %v", err)
	}

	if got := testutil.ToFloat64(policyEvaluationErrors.WithLabelValues("failing-policy")); got == 0 {
		t.Error("expected the per-workload failure to be counted")
	}
	if got := testutil.ToFloat64(policyReady.WithLabelValues("failing-policy")); got != 0 {
		t.Errorf("expected policy to report not ready, got %v", got)
	}
}

// TestMetrics_DeletedPolicyIsForgotten guards the easiest thing to miss:
// without cleanup the gauges keep reporting on an object that is gone.
func TestMetrics_DeletedPolicyIsForgotten(t *testing.T) {
	resetMetrics()

	policy := newCrashLoopPolicy("gone-policy")
	c := setupTestClient(policy)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("gone-policy")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := testutil.CollectAndCount(policyReady); got != 1 {
		t.Fatalf("expected 1 series before deletion, got %d", got)
	}

	if err := c.Delete(testCtx(), policy); err != nil {
		t.Fatalf("failed to delete policy: %v", err)
	}
	if _, err := r.Reconcile(testCtx(), testRequest("gone-policy")); err != nil {
		t.Fatalf("unexpected error reconciling a deleted policy: %v", err)
	}

	if got := testutil.CollectAndCount(policyReady); got != 0 {
		t.Errorf("expected the ready gauge to be dropped for a deleted policy, got %d series", got)
	}
	if got := testutil.CollectAndCount(workloadsScaledDown); got != 0 {
		t.Errorf("expected the workload gauge to be dropped for a deleted policy, got %d series", got)
	}
}
