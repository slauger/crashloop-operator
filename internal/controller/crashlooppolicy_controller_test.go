package controller

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
}

func TestReconcile_ScalesDownDeployment(t *testing.T) {
	policy := newCrashLoopPolicy("test-policy", withAllReplicasFailing(false))
	deploy := newDeployment("my-app", testNamespace, 3)
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app", "deploy-uid-1")
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
	rs := newReplicaSet("my-app-rs", "kube-system", "my-app", "deploy-uid-1")
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
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app", "deploy-uid-1")
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
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app", "deploy-uid-1")
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
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app", "deploy-uid-1")
	failingPod := newFailingPod("my-app-pod-1", testNamespace, rsOwnerRef(), "CrashLoopBackOff", 15)
	healthyPod := newHealthyPod("my-app-pod-2", testNamespace, rsOwnerRef())
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
	devRs := newReplicaSet("dev-app-rs", "dev-team", "dev-app", "deploy-uid-1")
	devPod := newFailingPod("dev-app-pod", "dev-team", metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       "dev-app-rs",
		UID:        "rs-uid-1",
	}, "CrashLoopBackOff", 15)

	// Deployment in prod namespace (should NOT be scaled down)
	prodDeploy := newDeployment("prod-app", "prod-team", 1)
	prodDeploy.UID = "deploy-uid-2"
	prodRs := newReplicaSet("prod-app-rs", "prod-team", "prod-app", "deploy-uid-2")
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
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app", "deploy-uid-1")
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
	rs := newReplicaSet("my-app-rs", testNamespace, "my-app", "deploy-uid-1")
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
	healthyPod := newHealthyPod("my-cj-pod-2", testNamespace, jobOwnerRef())

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

func TestPodExceedsDurationThreshold_CrashLoopWithTermination(t *testing.T) {
	// A pod in CrashLoopBackOff with LastTerminationState should use
	// the termination time as the failure start.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "crashloop",
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(metav1.Now().Add(-3 * time.Hour)),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
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
							FinishedAt: metav1.NewTime(metav1.Now().Add(-1 * time.Hour)),
							ExitCode:   1,
						},
					},
				},
			},
		},
	}
	if !podExceedsDurationThreshold(pod, 30*time.Minute) {
		t.Error("expected crashlooping pod with 1h-old termination to exceed 30m duration threshold")
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
