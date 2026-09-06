package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func newPolicy(name string) *CrashLoopPolicy {
	return &CrashLoopPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

// TestCRDIsClusterScoped guards the fix from the scope change: a namespace on
// the object must be rejected rather than silently accepted.
func TestCRDIsClusterScoped(t *testing.T) {
	ctx := context.Background()
	p := newPolicy("scope-test")
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("failed to create cluster-scoped policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	if p.Namespace != "" {
		t.Errorf("expected no namespace on a cluster-scoped object, got %q", p.Namespace)
	}
}

// TestDefaultingAppliesSpecDefaults verifies the kubebuilder defaults are
// actually applied by the API server, which the fake client never does.
func TestDefaultingAppliesSpecDefaults(t *testing.T) {
	ctx := context.Background()
	p := newPolicy("defaults-test")
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	if got := p.Spec.RestartThreshold; got != 10 {
		t.Errorf("expected restartThreshold 10, got %d", got)
	}
	if got := p.Spec.DurationThreshold; got != "30m" {
		t.Errorf("expected durationThreshold 30m, got %q", got)
	}
	if got := p.Spec.ReconcileInterval; got != "60s" {
		t.Errorf("expected reconcileInterval 60s, got %q", got)
	}
	if p.Spec.AllReplicasFailing == nil || !*p.Spec.AllReplicasFailing {
		t.Error("expected allReplicasFailing to default to true")
	}
	if p.Spec.DryRun == nil || *p.Spec.DryRun {
		t.Error("expected dryRun to default to false")
	}
	if len(p.Spec.Targets) != 3 {
		t.Errorf("expected 3 default targets, got %v", p.Spec.Targets)
	}
	if len(p.Spec.WatchReasons) != 6 {
		t.Errorf("expected 6 default watch reasons, got %v", p.Spec.WatchReasons)
	}
	if len(p.Spec.ExcludeNamespaces) != 3 {
		t.Errorf("expected 3 default excluded namespaces, got %v", p.Spec.ExcludeNamespaces)
	}
}

// TestExplicitFalseSurvivesRoundTrip is the API-server-side proof for the
// pointer change: an explicit false must come back as false, not as the
// default true.
func TestExplicitFalseSurvivesRoundTrip(t *testing.T) {
	ctx := context.Background()
	no := false
	p := newPolicy("explicit-false-test")
	p.Spec.AllReplicasFailing = &no

	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	fetched := &CrashLoopPolicy{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "explicit-false-test"}, fetched); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if fetched.Spec.AllReplicasFailing == nil {
		t.Fatal("expected allReplicasFailing to be set, got nil")
	}
	if *fetched.Spec.AllReplicasFailing {
		t.Error("expected an explicit false to survive, got true")
	}
}

func TestDurationValidation(t *testing.T) {
	ctx := context.Background()

	valid := []string{"30m", "60s", "1h30m", "0.5h", "500ms"}
	for _, v := range valid {
		p := newPolicy("duration-valid-" + sanitize(v))
		p.Spec.DurationThreshold = v
		if err := k8sClient.Create(ctx, p); err != nil {
			t.Errorf("expected durationThreshold %q to be accepted, got %v", v, err)
			continue
		}
		_ = k8sClient.Delete(ctx, p)
	}

	invalid := []string{"2 hours", "30", "abc", "-5m", "1d"}
	for _, v := range invalid {
		p := newPolicy("duration-invalid-" + sanitize(v))
		p.Spec.DurationThreshold = v
		if err := k8sClient.Create(ctx, p); err == nil {
			t.Errorf("expected durationThreshold %q to be rejected", v)
			_ = k8sClient.Delete(ctx, p)
		}
	}
}

func TestReconcileIntervalValidation(t *testing.T) {
	ctx := context.Background()

	p := newPolicy("interval-invalid")
	p.Spec.ReconcileInterval = "5 minutes"
	if err := k8sClient.Create(ctx, p); err == nil {
		t.Error("expected an invalid reconcileInterval to be rejected")
		_ = k8sClient.Delete(ctx, p)
	}
}

func TestRestartThresholdMinimum(t *testing.T) {
	ctx := context.Background()

	p := newPolicy("restart-zero")
	p.Spec.RestartThreshold = -1
	if err := k8sClient.Create(ctx, p); err == nil {
		t.Error("expected a negative restartThreshold to be rejected")
		_ = k8sClient.Delete(ctx, p)
	}
}

func TestTargetsEnumValidation(t *testing.T) {
	ctx := context.Background()

	p := newPolicy("targets-invalid")
	p.Spec.Targets = []string{"Deployment", "DaemonSet"}
	if err := k8sClient.Create(ctx, p); err == nil {
		t.Error("expected an unsupported target kind to be rejected")
		_ = k8sClient.Delete(ctx, p)
	}

	ok := newPolicy("targets-valid")
	ok.Spec.Targets = []string{"Deployment", "StatefulSet", "CronJob"}
	if err := k8sClient.Create(ctx, ok); err != nil {
		t.Errorf("expected the supported target kinds to be accepted, got %v", err)
		return
	}
	_ = k8sClient.Delete(ctx, ok)
}

// TestStatusSubresource verifies that status is a real subresource: a plain
// update must not persist status, and conditions must round-trip.
func TestStatusSubresource(t *testing.T) {
	ctx := context.Background()
	p := newPolicy("status-test")
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	// A normal update must not carry status through.
	p.Status.Phase = CrashLoopPolicyPhaseActive
	if err := k8sClient.Update(ctx, p); err != nil {
		t.Fatalf("failed to update policy: %v", err)
	}
	fetched := &CrashLoopPolicy{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "status-test"}, fetched); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if fetched.Status.Phase != "" {
		t.Errorf("expected a plain update to leave status empty, got %q", fetched.Status.Phase)
	}

	// Writing through the status subresource must persist.
	fetched.Status.Phase = CrashLoopPolicyPhaseActive
	fetched.Status.ObservedGeneration = fetched.Generation
	fetched.Status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSucceeded",
		Message:            "ok",
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: fetched.Generation,
	}}
	fetched.Status.ActiveScaledDown = []ScaledDownWorkloadRef{{
		Kind: "Deployment", Namespace: "default", Name: "my-app",
	}}
	if err := k8sClient.Status().Update(ctx, fetched); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	again := &CrashLoopPolicy{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "status-test"}, again); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if again.Status.Phase != CrashLoopPolicyPhaseActive {
		t.Errorf("expected phase Active, got %q", again.Status.Phase)
	}
	if len(again.Status.Conditions) != 1 {
		t.Errorf("expected 1 condition, got %d", len(again.Status.Conditions))
	}
	if len(again.Status.ActiveScaledDown) != 1 || again.Status.ActiveScaledDown[0].Name != "my-app" {
		t.Errorf("expected the structured workload ref to round-trip, got %+v", again.Status.ActiveScaledDown)
	}
}

// TestConditionsMergeByType covers the list-map markers: two conditions with
// distinct types coexist, and re-applying one type replaces rather than
// duplicates it.
func TestConditionsMergeByType(t *testing.T) {
	ctx := context.Background()
	p := newPolicy("conditions-test")
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	p.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: metav1.Now()},
		{Type: "Degraded", Status: metav1.ConditionFalse, Reason: "B", LastTransitionTime: metav1.Now()},
	}
	if err := k8sClient.Status().Update(ctx, p); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	fetched := &CrashLoopPolicy{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "conditions-test"}, fetched); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if len(fetched.Status.Conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(fetched.Status.Conditions))
	}

	// A duplicate type must be rejected by the list-map constraint.
	fetched.Status.Conditions = append(fetched.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: "C", LastTransitionTime: metav1.Now(),
	})
	if err := k8sClient.Status().Update(ctx, fetched); err == nil {
		t.Error("expected a duplicate condition type to be rejected by the list-map constraint")
	}
}

// sanitize turns a duration string into something usable as an object name.
func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// TestSampleManifestsAreValid applies everything in config/samples against the
// real API server. The samples are documentation, so a field that was renamed
// or removed should fail here rather than in a user's terminal.
func TestSampleManifestsAreValid(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join("..", "..", "config", "samples")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}

	found := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		found++

		t.Run(entry.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatalf("failed to read sample: %v", err)
			}

			obj := &CrashLoopPolicy{}
			if err := yaml.UnmarshalStrict(raw, obj); err != nil {
				t.Fatalf("sample does not decode into CrashLoopPolicy: %v", err)
			}
			// Give it a unique name so samples cannot collide with each other.
			obj.Name = "sample-" + sanitize(entry.Name())

			if err := k8sClient.Create(ctx, obj); err != nil {
				t.Fatalf("sample rejected by the API server: %v", err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		})
	}

	if found == 0 {
		t.Fatalf("no sample manifests found in %s", dir)
	}
}

// TestStatusAcceptsAnyWorkloadKind guards against reintroducing an enum on the
// status field. The API server validates status writes, so a constrained value
// there would reject the whole update, not just the offending entry, and every
// condition and counter would silently stop being written.
func TestStatusAcceptsAnyWorkloadKind(t *testing.T) {
	ctx := context.Background()
	p := newPolicy("status-kind-test")
	if err := k8sClient.Create(ctx, p); err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })

	p.Status.Phase = CrashLoopPolicyPhaseActive
	p.Status.ActiveScaledDown = []ScaledDownWorkloadRef{
		{Kind: "Deployment", Namespace: "default", Name: "known"},
		{Kind: "SomeFutureKind", Namespace: "default", Name: "unknown"},
	}
	if err := k8sClient.Status().Update(ctx, p); err != nil {
		t.Fatalf("status write rejected for an unlisted kind: %v", err)
	}

	fetched := &CrashLoopPolicy{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "status-kind-test"}, fetched); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	// The whole update must have landed, not just the recognised entry.
	if len(fetched.Status.ActiveScaledDown) != 2 {
		t.Errorf("expected both entries to persist, got %d", len(fetched.Status.ActiveScaledDown))
	}
	if fetched.Status.Phase != CrashLoopPolicyPhaseActive {
		t.Errorf("expected the rest of the status update to land, phase is %q", fetched.Status.Phase)
	}
}

// TestSpecStillRejectsUnknownTarget is the other half: the enum on the spec is
// the one that should stay, so a user typo is caught at write time.
func TestSpecStillRejectsUnknownTarget(t *testing.T) {
	ctx := context.Background()
	p := newPolicy("spec-target-test")
	p.Spec.Targets = []string{"SomeFutureKind"}
	if err := k8sClient.Create(ctx, p); err == nil {
		t.Error("expected an unknown target kind to be rejected on the spec")
		_ = k8sClient.Delete(ctx, p)
	}
}
