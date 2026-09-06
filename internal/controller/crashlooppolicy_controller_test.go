package controller

import (
	"regexp"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crashloopv1alpha1 "github.com/slauger/crashloop-operator/api/v1alpha1"
)

func TestReconcile_NoPolicy(t *testing.T) {
	c := setupTestClient()
	r := newReconciler(c)

	result, err := r.Reconcile(testCtx(), testRequest("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}
}

func TestReconcile_SetsInitialPhase(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy")
	c := setupTestClient(policy)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKeyFromObject(policy), updated); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if updated.Status.Phase != crashloopv1alpha1.CrashLoopPolicyPhaseActive {
		t.Errorf("expected phase Active, got %s", updated.Status.Phase)
	}
	if updated.Status.LastEvaluationTime == nil {
		t.Error("expected lastEvaluationTime to be set")
	}

	// Verify conditions are set
	readyCond := meta.FindStatusCondition(updated.Status.Conditions, ConditionReady)
	if readyCond == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if readyCond.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True, got %s", readyCond.Status)
	}

	degradedCond := meta.FindStatusCondition(updated.Status.Conditions, ConditionDegraded)
	if degradedCond == nil {
		t.Fatal("expected Degraded condition to be set")
	}
	if degradedCond.Status != metav1.ConditionFalse {
		t.Errorf("expected Degraded=False when no workloads are failing, got %s", degradedCond.Status)
	}
}

func TestReconcile_ScalesDownDeployment(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Errorf("expected replicas=0, got %v", updated.Spec.Replicas)
	}
	if updated.Annotations[AnnotationScaledDownReason] == "" {
		t.Error("expected scaled-down-reason annotation to be set")
	}
	if updated.Annotations[AnnotationPreviousReplicas] != "3" {
		t.Errorf("expected previous-replicas=3, got %s", updated.Annotations[AnnotationPreviousReplicas])
	}

	// Verify ActiveScaledDown status
	updatedPolicy := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKeyFromObject(policy), updatedPolicy); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if len(updatedPolicy.Status.ActiveScaledDown) != 1 {
		t.Errorf("expected 1 active scaled down workload, got %d", len(updatedPolicy.Status.ActiveScaledDown))
	} else {
		want := crashloopv1alpha1.ScaledDownWorkloadRef{
			Kind:      "Deployment",
			Namespace: "default",
			Name:      "my-app",
		}
		if updatedPolicy.Status.ActiveScaledDown[0] != want {
			t.Errorf("expected %+v, got %+v", want, updatedPolicy.Status.ActiveScaledDown[0])
		}
	}

	// Verify Degraded condition is True when workloads are scaled down
	degradedCond := meta.FindStatusCondition(updatedPolicy.Status.Conditions, ConditionDegraded)
	if degradedCond == nil {
		t.Fatal("expected Degraded condition to be set")
	}
	if degradedCond.Status != metav1.ConditionTrue {
		t.Errorf("expected Degraded=True when workloads are scaled down, got %s", degradedCond.Status)
	}
}

func TestReconcile_ScalesDownStatefulSet(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	sts := newStatefulSet("my-sts", testNamespace, 2)
	pod := newFailingPod("my-sts-0", testNamespace, stsOwnerRef(), "ImagePullBackOff", 15)

	c := setupTestClient(policy, sts, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.StatefulSet{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-sts", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get statefulset: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Errorf("expected replicas=0, got %v", updated.Spec.Replicas)
	}
}

func TestReconcile_SuspendsCronJob(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	cj := newCronJob("my-cj", testNamespace)
	job := newJob("my-cj-job", testNamespace, "my-cj")
	pod := newFailingPod("my-cj-job-pod", testNamespace, jobOwnerRef(), "CreateContainerConfigError", 5)

	c := setupTestClient(policy, cj, job, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &batchv1.CronJob{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-cj", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get cronjob: %v", err)
	}
	if updated.Spec.Suspend == nil || !*updated.Spec.Suspend {
		t.Error("expected cronjob to be suspended")
	}
}

func TestReconcile_SkipsExcludedNamespace(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	deploy := newDeployment("my-app", "kube-system", 1)
	rs := newReplicaSet("my-app-rs", "kube-system", "my-app")
	pod := newFailingPod("my-app-pod-1", "kube-system", rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: "kube-system"}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas == 0 {
		t.Error("expected deployment in kube-system to NOT be scaled down")
	}
}

func TestReconcile_DryRunDoesNotScale(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withDryRun(true), withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas == 0 {
		t.Error("expected deployment to NOT be scaled down in dry run mode")
	}
}

func withDurationThreshold(d string) policyOption {
	return func(p *crashloopv1alpha1.CrashLoopPolicy) {
		p.Spec.DurationThreshold = d
	}
}

func TestReconcile_BelowThresholdDoesNotScale(t *testing.T) {
	// Both restart threshold (20) and duration threshold (24h) are set high
	// so the pod with 5 restarts and 1h age does not exceed either.
	policy := newCrashLoopPolicy("test-policy", withRestartThreshold(20), withDurationThreshold("24h"), withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 1)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	// Pod has only 5 restarts and was created 1h ago (below 24h duration threshold)
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 5)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas == 0 {
		t.Error("expected deployment to NOT be scaled down when below restart threshold")
	}
}

func TestReconcile_AllReplicasFailingRequired(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(true))
	deploy := newDeployment("my-app", testNamespace, 2)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	failingPod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)
	healthyPod := newHealthyPod("my-app-pod-2", rsOwnerRef())
	// Set labels so the deployment selector matches
	failingPod.Labels = map[string]string{"app": "my-app"}
	healthyPod.Labels = map[string]string{"app": "my-app"}

	c := setupTestClient(policy, deploy, rs, failingPod, healthyPod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas == 0 {
		t.Error("expected deployment to NOT be scaled down when not all replicas are failing")
	}
}

func TestReconcile_RequeuesAfterInterval(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy")
	c := setupTestClient(policy)
	r := newReconciler(c)

	result, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != RequeueIntervalDefault {
		t.Errorf("expected requeue after %v, got %v", RequeueIntervalDefault, result.RequeueAfter)
	}
}

func TestReconcile_CustomReconcileInterval(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withReconcileInterval("5m"))
	c := setupTestClient(policy)
	r := newReconciler(c)

	result, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := 5 * time.Minute
	if result.RequeueAfter != expected {
		t.Errorf("expected requeue after %v, got %v", expected, result.RequeueAfter)
	}
}

func TestPodHasFailureReason(t *testing.T) {
	tests := []struct {
		name         string
		reason       string
		watchReasons []string
		want         bool
	}{
		{"matching reason", "CrashLoopBackOff", []string{"CrashLoopBackOff"}, true},
		{"no match", "Running", []string{"CrashLoopBackOff"}, false},
		{"empty reasons", "CrashLoopBackOff", nil, false},
		{"ImagePullBackOff", "ImagePullBackOff", []string{"ImagePullBackOff", "ErrImagePull"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{Reason: tt.reason},
							},
						},
					},
				},
			}
			_, got := podHasFailureReason(pod, tt.watchReasons)
			if got != tt.want {
				t.Errorf("podHasFailureReason() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsExcludedNamespace(t *testing.T) {
	tests := []struct {
		ns       string
		excluded []string
		want     bool
	}{
		{"kube-system", []string{"kube-system"}, true},
		{"default", []string{"kube-system"}, false},
		{"default", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.ns, func(t *testing.T) {
			if got := isExcludedNamespace(tt.ns, tt.excluded); got != tt.want {
				t.Errorf("isExcludedNamespace(%q) = %v, want %v", tt.ns, got, tt.want)
			}
		})
	}
}

func TestReconcile_NamespaceSelectorFilters(t *testing.T) {
	// Policy only watches namespaces with label env=dev
	policy := newCrashLoopPolicy("test-policy",
		withAllReplicasFailing(false),
		withExcludeNamespaces(), // no exclusions
		withNamespaceSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"env": "dev"},
		}),
	)

	devNs := newNamespace("dev-team", map[string]string{"env": "dev"})
	prodNs := newNamespace("prod-team", map[string]string{"env": "prod"})

	// Deployment in dev namespace (should be scaled down)
	devDeploy := newDeployment("dev-app", "dev-team", 1)
	devRs := newReplicaSet("dev-app-rs", "dev-team", "dev-app")
	devPod := newFailingPod("dev-app-pod", "dev-team", metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       "dev-app-rs",
		UID:        "rs-uid-1",
	}, "CrashLoopBackOff", 15)

	// Deployment in prod namespace (should NOT be scaled down)
	prodDeploy := newDeployment("prod-app", "prod-team", 1)
	prodDeploy.UID = "deploy-uid-2"
	prodRs := newReplicaSet("prod-app-rs", "prod-team", "prod-app")
	prodRs.OwnerReferences[0].UID = "deploy-uid-2"
	prodPod := newFailingPod("prod-app-pod", "prod-team", metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       "prod-app-rs",
		UID:        "rs-uid-1",
	}, "CrashLoopBackOff", 15)

	c := setupTestClient(policy, devNs, prodNs, devDeploy, devRs, devPod, prodDeploy, prodRs, prodPod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dev deployment should be scaled down
	devUpdated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "dev-app", Namespace: "dev-team"}, devUpdated); err != nil {
		t.Fatalf("failed to get dev deployment: %v", err)
	}
	if devUpdated.Spec.Replicas == nil || *devUpdated.Spec.Replicas != 0 {
		t.Error("expected dev deployment to be scaled down")
	}

	// Prod deployment should NOT be scaled down
	prodUpdated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "prod-app", Namespace: "prod-team"}, prodUpdated); err != nil {
		t.Fatalf("failed to get prod deployment: %v", err)
	}
	if prodUpdated.Spec.Replicas != nil && *prodUpdated.Spec.Replicas == 0 {
		t.Error("expected prod deployment to NOT be scaled down (namespace not matching selector)")
	}
}

func TestReconcile_ExcludeWorkloadSelectorSkipsWorkload(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy",
		withAllReplicasFailing(false),
		withExcludeWorkloadSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"argocd.argoproj.io/instance": "my-app"},
		}),
	)

	// Deployment with matching label should be skipped
	deploy := newDeployment("my-app", testNamespace, 3)
	deploy.Labels = map[string]string{
		"app":                         "my-app",
		"argocd.argoproj.io/instance": "my-app",
	}
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas == 0 {
		t.Error("expected deployment to NOT be scaled down (excluded via workload selector)")
	}
}

func TestReconcile_ExcludeWorkloadSelectorAllowsNonMatching(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy",
		withAllReplicasFailing(false),
		withExcludeWorkloadSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"argocd.argoproj.io/instance": "other-app"},
		}),
	)

	// Deployment without matching label should be scaled down
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, deploy, rs, pod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Error("expected deployment to be scaled down (workload selector does not match)")
	}
}

func TestReconcile_CronJobAllReplicasFailing(t *testing.T) {
	// With allReplicasFailing=true, a CronJob should only be suspended
	// if the pods of its latest job are actually failing.
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(true))
	cj := newCronJob("my-cj", testNamespace)
	job := newJob("my-cj-job", testNamespace, "my-cj")
	failingPod := newFailingPod("my-cj-pod-1", testNamespace, jobOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(policy, cj, job, failingPod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &batchv1.CronJob{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-cj", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get cronjob: %v", err)
	}
	if updated.Spec.Suspend == nil || !*updated.Spec.Suspend {
		t.Error("expected cronjob to be suspended when all job pods are failing")
	}
}

func TestReconcile_CronJobNotAllReplicasFailing(t *testing.T) {
	// With allReplicasFailing=true, a CronJob should NOT be suspended
	// when some job pods are healthy.
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(true))
	cj := newCronJob("my-cj", testNamespace)
	job := newJob("my-cj-job", testNamespace, "my-cj")
	failingPod := newFailingPod("my-cj-pod-1", testNamespace, jobOwnerRef(), "CrashLoopBackOff", 15)
	healthyPod := newHealthyPod("my-cj-pod-2", jobOwnerRef())

	c := setupTestClient(policy, cj, job, failingPod, healthyPod)
	r := newReconciler(c)

	_, err := r.Reconcile(testCtx(), testRequest("test-policy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &batchv1.CronJob{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-cj", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get cronjob: %v", err)
	}
	if updated.Spec.Suspend != nil && *updated.Spec.Suspend {
		t.Error("expected cronjob NOT to be suspended when not all job pods are failing")
	}
}

func TestPodExceedsDurationThreshold_SlowStartingPod(t *testing.T) {
	// A pod that just started and has no restarts or termination state
	// should NOT exceed the duration threshold, even if PodReady=False.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "slow-starter",
			Namespace:         testNamespace,
			CreationTimestamp: metav1.Now(), // just created
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.Now(),
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 0,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}
	if podExceedsDurationThreshold(pod, 30*time.Minute) {
		t.Error("expected slow-starting pod to NOT exceed duration threshold")
	}
}

func TestPodExceedsDurationThreshold_ImagePullBackOff(t *testing.T) {
	// A pod stuck in ImagePullBackOff with no restarts should use
	// creation timestamp as the failure start.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "bad-image",
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(metav1.Now().Add(-2 * time.Hour)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			// Kubelet publishes this as soon as it starts syncing the pod, so
			// a pod that has been stuck for two hours carries a two-hour-old
			// transition.
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.ContainersReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(metav1.Now().Add(-2 * time.Hour)),
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 0,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "ImagePullBackOff",
						},
					},
				},
			},
		},
	}
	if !podExceedsDurationThreshold(pod, 30*time.Minute) {
		t.Error("expected pod with ImagePullBackOff for 2h to exceed 30m duration threshold")
	}
}

func TestPodExceedsDurationThreshold_SteadyCrashLoop(t *testing.T) {
	// The state a live kubelet actually produces for a steady crash loop.
	// Backoff is capped at a few minutes and every restart rewrites
	// FinishedAt, so the last termination is always recent no matter how long
	// the pod has been broken. Measuring from it can never reach a threshold
	// above the cap, which is why readiness is the clock instead.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "crashloop",
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(metav1.Now().Add(-3 * time.Hour)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.ContainersReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(metav1.Now().Add(-1 * time.Hour)),
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 15,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							FinishedAt: metav1.NewTime(metav1.Now().Add(-90 * time.Second)),
							ExitCode:   1,
						},
					},
				},
			},
		},
	}
	if !podExceedsDurationThreshold(pod, 30*time.Minute) {
		t.Error("expected a pod crash looping for an hour to exceed the 30m duration threshold")
	}
}

func TestPodExceedsDurationThreshold_RecoveredPodResetsTheClock(t *testing.T) {
	// A container that became ready again and only just failed must not count
	// as having been broken for the whole time since its first ever failure.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "recovered",
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(metav1.Now().Add(-8 * time.Hour)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.ContainersReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(metav1.Now().Add(-2 * time.Minute)),
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 20,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				},
			},
		},
	}
	if podExceedsDurationThreshold(pod, 30*time.Minute) {
		t.Error("expected a pod that was healthy two minutes ago not to exceed the threshold")
	}
}

func TestPodExceedsDurationThreshold_NoReadinessConditionYet(t *testing.T) {
	// Very early in a pod's life kubelet has not published readiness. Deciding
	// from the creation timestamp would fire immediately on an old pod that
	// only just broke, so the answer is "not yet" and the next evaluation
	// decides.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "no-conditions",
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(metav1.Now().Add(-5 * time.Hour)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 0,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
					},
				},
			},
		},
	}
	if podExceedsDurationThreshold(pod, 30*time.Minute) {
		t.Error("expected no duration verdict without a readiness condition")
	}
}

func TestMapPodToPolicy_FiltersExcludedNamespace(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy")
	pod := newFailingPod("my-pod", "kube-system", rsOwnerRef(), "CrashLoopBackOff", 5)

	c := setupTestClient(policy, pod)
	r := newReconciler(c)

	requests := r.mapPodToPolicy(testCtx(), pod)
	if len(requests) != 0 {
		t.Errorf("expected 0 requests for pod in excluded namespace, got %d", len(requests))
	}
}

func TestMapPodToPolicy_IncludesMatchingNamespace(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withExcludeNamespaces())
	pod := newFailingPod("my-pod", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 5)

	c := setupTestClient(policy, pod)
	r := newReconciler(c)

	requests := r.mapPodToPolicy(testCtx(), pod)
	if len(requests) != 1 {
		t.Errorf("expected 1 request for pod in non-excluded namespace, got %d", len(requests))
	}
}

func TestMapPodToPolicy_NamespaceSelectorFilters(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy",
		withExcludeNamespaces(),
		withNamespaceSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"env": "dev"},
		}),
	)
	devNs := newNamespace("dev-ns", map[string]string{"env": "dev"})
	prodNs := newNamespace("prod-ns", map[string]string{"env": "prod"})
	devPod := newFailingPod("dev-pod", "dev-ns", rsOwnerRef(), "CrashLoopBackOff", 5)
	prodPod := newFailingPod("prod-pod", "prod-ns", rsOwnerRef(), "CrashLoopBackOff", 5)

	c := setupTestClient(policy, devNs, prodNs, devPod, prodPod)
	r := newReconciler(c)

	devRequests := r.mapPodToPolicy(testCtx(), devPod)
	if len(devRequests) != 1 {
		t.Errorf("expected 1 request for pod in dev namespace, got %d", len(devRequests))
	}

	// The map function deliberately does not resolve the namespaceSelector:
	// doing so cost a namespace list per policy per pod event. It only decides
	// whether to enqueue, and Reconcile applies the selector for real, as
	// TestReconcile_NamespaceSelectorFilters asserts.
	prodRequests := r.mapPodToPolicy(testCtx(), prodPod)
	if len(prodRequests) != 1 {
		t.Errorf("expected the pod to be enqueued and filtered during reconcile, got %d requests", len(prodRequests))
	}
}

func TestIsTargetKind(t *testing.T) {
	tests := []struct {
		kind    string
		targets []string
		want    bool
	}{
		{"Deployment", []string{"Deployment", "StatefulSet"}, true},
		{"CronJob", []string{"Deployment"}, false},
		{"StatefulSet", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			if got := isTargetKind(tt.kind, tt.targets); got != tt.want {
				t.Errorf("isTargetKind(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestReconcile_ExplicitAllReplicasFailingFalse(t *testing.T) {
	// An explicit false must survive: with the old non-pointer field it was
	// indistinguishable from unset and the default true silently applied,
	// which would have kept this deployment running.
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 2)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	failingPod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)
	healthyPod := newHealthyPod("my-app-pod-2", rsOwnerRef())
	failingPod.Labels = map[string]string{"app": "my-app"}
	healthyPod.Labels = map[string]string{"app": "my-app"}

	c := setupTestClient(policy, deploy, rs, failingPod, healthyPod)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("test-policy")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Error("expected deployment to be scaled down when allReplicasFailing is explicitly false")
	}
}

func TestReconcile_StatusNotRewrittenWhenUnchanged(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy")
	c := setupTestClient(policy)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("test-policy")); err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}
	first := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKeyFromObject(policy), first); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}

	if _, err := r.Reconcile(testCtx(), testRequest("test-policy")); err != nil {
		t.Fatalf("unexpected error on second reconcile: %v", err)
	}
	second := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKeyFromObject(policy), second); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}

	if first.ResourceVersion != second.ResourceVersion {
		t.Errorf("expected no status write on an unchanged reconcile, resourceVersion moved from %s to %s",
			first.ResourceVersion, second.ResourceVersion)
	}
}

func TestReconcile_SetsObservedGeneration(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy")
	policy.Generation = 7
	c := setupTestClient(policy)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("test-policy")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKeyFromObject(policy), updated); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	if updated.Status.ObservedGeneration != 7 {
		t.Errorf("expected observedGeneration 7, got %d", updated.Status.ObservedGeneration)
	}
}

func TestReconcile_MostRestrictivePolicyWins(t *testing.T) {
	// Two policies match the same workload. The one with the lower restart
	// threshold is more restrictive and must own the action; the other must
	// leave the workload alone so it is not attributed twice.
	strict := newCrashLoopPolicy("strict", withAllReplicasFailing(false), withRestartThreshold(5))
	lax := newCrashLoopPolicy("lax", withAllReplicasFailing(false), withRestartThreshold(10))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(strict, lax, deploy, rs, pod)
	r := newReconciler(c)

	// Reconcile the less restrictive policy first: it must defer.
	if _, err := r.Reconcile(testCtx(), testRequest("lax")); err != nil {
		t.Fatalf("unexpected error reconciling lax: %v", err)
	}
	updated := &appsv1.Deployment{}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas == 0 {
		t.Fatal("expected the less restrictive policy to defer to the more restrictive one")
	}

	// The more restrictive policy acts and records itself.
	if _, err := r.Reconcile(testCtx(), testRequest("strict")); err != nil {
		t.Fatalf("unexpected error reconciling strict: %v", err)
	}
	if err := c.Get(testCtx(), types.NamespacedName{Name: "my-app", Namespace: testNamespace}, updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 0 {
		t.Error("expected the most restrictive policy to scale the deployment down")
	}
	if got := updated.Annotations[AnnotationScaledDownBy]; got != "strict" {
		t.Errorf("expected scaled-down-by=strict, got %q", got)
	}
}

func TestReconcile_ActiveScaledDownAttributedToOwningPolicy(t *testing.T) {
	strict := newCrashLoopPolicy("strict", withAllReplicasFailing(false), withRestartThreshold(5))
	lax := newCrashLoopPolicy("lax", withAllReplicasFailing(false), withRestartThreshold(10))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app")
	pod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)

	c := setupTestClient(strict, lax, deploy, rs, pod)
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("strict")); err != nil {
		t.Fatalf("unexpected error reconciling strict: %v", err)
	}
	if _, err := r.Reconcile(testCtx(), testRequest("lax")); err != nil {
		t.Fatalf("unexpected error reconciling lax: %v", err)
	}

	owning := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKey{Name: "strict"}, owning); err != nil {
		t.Fatalf("failed to get strict policy: %v", err)
	}
	if len(owning.Status.ActiveScaledDown) != 1 {
		t.Errorf("expected the owning policy to list 1 workload, got %d", len(owning.Status.ActiveScaledDown))
	}
	if owning.Status.ScaledDownWorkloads != 1 {
		t.Errorf("expected the owning policy counter to be 1, got %d", owning.Status.ScaledDownWorkloads)
	}

	other := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKey{Name: "lax"}, other); err != nil {
		t.Fatalf("failed to get lax policy: %v", err)
	}
	if len(other.Status.ActiveScaledDown) != 0 {
		t.Errorf("expected the non-owning policy to list no workloads, got %d", len(other.Status.ActiveScaledDown))
	}
	if other.Status.ScaledDownWorkloads != 0 {
		t.Errorf("expected the non-owning policy counter to stay 0, got %d", other.Status.ScaledDownWorkloads)
	}
}

func TestIsMoreRestrictive(t *testing.T) {
	tests := []struct {
		name string
		a    *crashloopv1alpha1.CrashLoopPolicy
		b    *crashloopv1alpha1.CrashLoopPolicy
		want bool
	}{
		{
			name: "lower restart threshold wins",
			a:    newCrashLoopPolicy("a", withRestartThreshold(3)),
			b:    newCrashLoopPolicy("b", withRestartThreshold(9)),
			want: true,
		},
		{
			name: "shorter duration wins when restarts tie",
			a:    newCrashLoopPolicy("a", withDurationThreshold("5m")),
			b:    newCrashLoopPolicy("b", withDurationThreshold("1h")),
			want: true,
		},
		{
			name: "allReplicasFailing false wins",
			a:    newCrashLoopPolicy("a", withAllReplicasFailing(false)),
			b:    newCrashLoopPolicy("b", withAllReplicasFailing(true)),
			want: true,
		},
		{
			name: "real action outranks dry run",
			a:    newCrashLoopPolicy("a", withDryRun(false)),
			b:    newCrashLoopPolicy("b", withDryRun(true)),
			want: true,
		},
		{
			name: "name breaks a full tie",
			a:    newCrashLoopPolicy("aaa"),
			b:    newCrashLoopPolicy("bbb"),
			want: true,
		},
		{
			name: "not more restrictive when higher threshold",
			a:    newCrashLoopPolicy("a", withRestartThreshold(20)),
			b:    newCrashLoopPolicy("b", withRestartThreshold(2)),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMoreRestrictive(tc.a, tc.b); got != tc.want {
				t.Errorf("isMoreRestrictive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	fallback := 42 * time.Second
	tests := []struct {
		name  string
		input string
		want  time.Duration
		wantO bool
	}{
		{name: "simple", input: "30m", want: 30 * time.Minute, wantO: true},
		{name: "compound", input: "1h30m", want: 90 * time.Minute, wantO: true},
		{name: "fractional", input: "0.5h", want: 30 * time.Minute, wantO: true},
		{name: "milliseconds", input: "500ms", want: 500 * time.Millisecond, wantO: true},
		{name: "empty falls back", input: "", want: fallback, wantO: false},
		{name: "prose falls back", input: "2 hours", want: fallback, wantO: false},
		{name: "unit missing falls back", input: "30", want: fallback, wantO: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseDuration(tc.input, fallback)
			if got != tc.want || ok != tc.wantO {
				t.Errorf("parseDuration(%q) = (%v, %v), want (%v, %v)", tc.input, got, ok, tc.want, tc.wantO)
			}
		})
	}
}

func TestEffectiveReconcileInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
		wantO bool
	}{
		{name: "unset uses default", value: "", want: RequeueIntervalDefault, wantO: true},
		{name: "valid is honoured", value: "5m", want: 5 * time.Minute, wantO: true},
		// Regression: the old code fell back to the 30m durationThreshold
		// default here, so a typo silently produced a 30 minute loop.
		{name: "invalid uses the reconcile default", value: "5 minutes", want: RequeueIntervalDefault, wantO: false},
		{name: "zero uses the reconcile default", value: "0s", want: RequeueIntervalDefault, wantO: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newCrashLoopPolicy("p", withReconcileInterval(tc.value))
			got, ok := effectiveReconcileInterval(p)
			if got != tc.want || ok != tc.wantO {
				t.Errorf("effectiveReconcileInterval(%q) = (%v, %v), want (%v, %v)", tc.value, got, ok, tc.want, tc.wantO)
			}
		})
	}
}

func TestDurationPatternMatchesAcceptedValues(t *testing.T) {
	// Mirrors the kubebuilder Pattern on durationThreshold and
	// reconcileInterval. Anything the pattern admits must also parse, and the
	// documented defaults must be admitted.
	pattern := regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+$`)

	valid := []string{"30m", "60s", "1h", "1h30m", "0.5h", "500ms", "100us", "10ns"}
	for _, v := range valid {
		if !pattern.MatchString(v) {
			t.Errorf("pattern should accept %q", v)
			continue
		}
		if _, err := time.ParseDuration(v); err != nil {
			t.Errorf("pattern accepts %q but time.ParseDuration rejects it: %v", v, err)
		}
	}

	invalid := []string{"2 hours", "30", "", "abc", "-5m", "5 m", "1d"}
	for _, v := range invalid {
		if pattern.MatchString(v) {
			t.Errorf("pattern should reject %q", v)
		}
	}
}

func TestPodWaitingReasons(t *testing.T) {
	healthy := newHealthyPod("healthy", rsOwnerRef())
	if got := podWaitingReasons(healthy); len(got) != 0 {
		t.Errorf("expected no reasons for a healthy pod, got %v", got)
	}

	failing := newFailingPod("failing", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 3)
	got := podWaitingReasons(failing)
	if len(got) != 1 || got[0] != "CrashLoopBackOff" {
		t.Errorf("expected [CrashLoopBackOff], got %v", got)
	}

	// A non-pod object must not panic the index function.
	if got := podWaitingReasons(newDeployment("d", testNamespace, 1)); got != nil {
		t.Errorf("expected nil for a non-pod object, got %v", got)
	}
}

func TestPodHasWaitingContainer(t *testing.T) {
	if podHasWaitingContainer(newHealthyPod("healthy", rsOwnerRef())) {
		t.Error("healthy pod should not pass the watch predicate")
	}
	if !podHasWaitingContainer(newFailingPod("failing", testNamespace, rsOwnerRef(), "ImagePullBackOff", 1)) {
		t.Error("failing pod should pass the watch predicate")
	}
}

func TestListPodsByWaitingReasons_DeduplicatesAndFilters(t *testing.T) {
	failing := newFailingPod("failing", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 3)
	healthy := newHealthyPod("healthy", rsOwnerRef())
	c := setupTestClient(failing, healthy)

	// Passing the reason twice must not yield the pod twice.
	pods, err := listPodsByWaitingReasons(testCtx(), c, []string{"CrashLoopBackOff", "CrashLoopBackOff", "ImagePullBackOff"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 {
		t.Fatalf("expected exactly 1 pod, got %d", len(pods))
	}
	if pods[0].Name != "failing" {
		t.Errorf("expected the failing pod, got %s", pods[0].Name)
	}
}

func TestReconcile_ReadyFalseOnPartialFailure(t *testing.T) {
	// A pod owned by a ReplicaSet whose Deployment cannot be resolved makes
	// resolveOwnerWorkload fail. The evaluation continues, but Ready must say
	// so instead of reporting unconditional success.
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	pod := newFailingPod("orphan-pod", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)
	c := &failingOwnerClient{Client: setupTestClient(policy, pod)}
	r := newReconciler(c)

	if _, err := r.Reconcile(testCtx(), testRequest("test-policy")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := c.Get(testCtx(), client.ObjectKey{Name: "test-policy"}, updated); err != nil {
		t.Fatalf("failed to get policy: %v", err)
	}
	ready := meta.FindStatusCondition(updated.Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("expected Ready=False after a partial failure, got %s", ready.Status)
	}
	if ready.Reason != "ReconcilePartiallyFailed" {
		t.Errorf("expected reason ReconcilePartiallyFailed, got %s", ready.Reason)
	}
}
