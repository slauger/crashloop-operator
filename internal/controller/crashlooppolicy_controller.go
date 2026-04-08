package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	crashloopv1alpha1 "github.com/slauger/crashloop-operator/api/v1alpha1"
)

// CrashLoopPolicyReconciler reconciles a CrashLoopPolicy object.
type CrashLoopPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=crashloop-operator.lauger.de,resources=crashlooppolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=crashloop-operator.lauger.de,resources=crashlooppolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=crashloop-operator.lauger.de,resources=crashlooppolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *CrashLoopPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	policy := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Set initial phase
	if policy.Status.Phase == "" {
		if err := updateStatusWithRetry(ctx, r.Client, policy, func() {
			policy.Status.Phase = crashloopv1alpha1.CrashLoopPolicyPhasePending
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Parse duration threshold
	durationThreshold := parseDuration(policy.Spec.DurationThreshold)

	// Use defaults if not set
	watchReasons := policy.Spec.WatchReasons
	if len(watchReasons) == 0 {
		watchReasons = []string{"CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "InvalidImageName", "RunContainerError"}
	}

	restartThreshold := policy.Spec.RestartThreshold
	if restartThreshold == 0 {
		restartThreshold = DefaultRestartThreshold
	}

	targets := policy.Spec.Targets
	if len(targets) == 0 {
		targets = []string{"Deployment", "StatefulSet", "CronJob"}
	}

	// List all pods across all namespaces
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList); err != nil {
		logger.Error(err, "failed to list pods")
		return ctrl.Result{}, err
	}

	// Track which workloads we have already processed
	processed := make(map[string]bool)
	scaledDown := int32(0)

	for i := range podList.Items {
		pod := &podList.Items[i]

		// Skip excluded namespaces
		if isExcludedNamespace(pod.Namespace, policy.Spec.ExcludeNamespaces) {
			continue
		}

		// Check if pod has a matching failure reason
		reason, failing := podHasFailureReason(pod, watchReasons)
		if !failing {
			continue
		}

		// Check thresholds: restart count OR duration
		restartExceeded := podExceedsRestartThreshold(pod, restartThreshold)
		durationExceeded := podExceedsDurationThreshold(pod, durationThreshold)
		if !restartExceeded && !durationExceeded {
			continue
		}

		// Resolve owner workload
		owner, err := resolveOwnerWorkload(ctx, r.Client, pod)
		if err != nil {
			logger.Error(err, "failed to resolve owner workload", "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}
		if owner == nil {
			continue
		}

		// Check if this workload kind is a target
		if !isTargetKind(owner.Kind, targets) {
			continue
		}

		// Skip if already processed
		key := workloadKey(owner)
		if processed[key] {
			continue
		}
		processed[key] = true

		// Check if all replicas are failing (if configured)
		if policy.Spec.AllReplicasFailing {
			allFailing, err := allReplicasFailing(ctx, r.Client, owner, watchReasons)
			if err != nil {
				logger.Error(err, "failed to check all replicas", "workload", key)
				continue
			}
			if !allFailing {
				logger.V(1).Info("not all replicas failing, skipping", "workload", key)
				continue
			}
		}

		// Scale down or suspend the workload
		scaleReason := fmt.Sprintf("pods failing with %s (policy: %s/%s)", reason, policy.Namespace, policy.Name)
		acted, err := scaleDownWorkload(ctx, r.Client, owner, scaleReason, policy.Spec.DryRun)
		if err != nil {
			logger.Error(err, "failed to scale down workload", "workload", key)
			continue
		}

		if acted {
			scaledDown++
			eventReason := EventReasonScaledDown
			eventMsg := fmt.Sprintf("Scaled down %s %s/%s: %s", owner.Kind, owner.Namespace, owner.Name, scaleReason)
			if owner.Kind == "CronJob" {
				eventReason = EventReasonSuspended
				eventMsg = fmt.Sprintf("Suspended CronJob %s/%s: %s", owner.Namespace, owner.Name, scaleReason)
			}
			if policy.Spec.DryRun {
				eventReason = EventReasonDryRun
				eventMsg = "[DRY RUN] " + eventMsg
			}
			r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, eventReason, "Reconcile", eventMsg)
			logger.Info(eventMsg)
		}
	}

	// Update status
	now := metav1.Now()
	if err := updateStatusWithRetry(ctx, r.Client, policy, func() {
		policy.Status.Phase = crashloopv1alpha1.CrashLoopPolicyPhaseActive
		policy.Status.LastEvaluationTime = &now
		policy.Status.ScaledDownWorkloads += scaledDown
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: RequeueIntervalDefault}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CrashLoopPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&crashloopv1alpha1.CrashLoopPolicy{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodToPolicy)).
		Complete(r)
}

// mapPodToPolicy maps a pod event to the CrashLoopPolicy objects that should be reconciled.
func (r *CrashLoopPolicyReconciler) mapPodToPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	policyList := &crashloopv1alpha1.CrashLoopPolicyList{}
	if err := r.List(ctx, policyList); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, policy := range policyList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&policy),
		})
	}
	return requests
}
