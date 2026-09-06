package controller

import (
	"context"
	"fmt"
	"math"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

func (r *CrashLoopPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	policy := &crashloopv1alpha1.CrashLoopPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			// Drop the per-policy series, otherwise the gauges keep reporting
			// on an object that no longer exists.
			forgetPolicyMetrics(req.Name)
		}
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

	// Both duration fields are pattern-validated by the API server, so an
	// unparseable value here means an object that predates that validation.
	// Say so rather than quietly running on the default.
	if _, ok := parseDuration(policy.Spec.DurationThreshold, DefaultDurationThresholdDuration); policy.Spec.DurationThreshold != "" && !ok {
		logger.Info("invalid durationThreshold, using default",
			"value", policy.Spec.DurationThreshold, "default", DefaultDurationThreshold)
	}
	if _, ok := effectiveReconcileInterval(policy); !ok {
		logger.Info("invalid reconcileInterval, using default",
			"value", policy.Spec.ReconcileInterval, "default", RequeueIntervalDefault)
	}

	durationThreshold := effectiveDurationThreshold(policy)
	watchReasons := effectiveWatchReasons(policy)
	restartThreshold := effectiveRestartThreshold(policy)
	targets := effectiveTargets(policy)
	requireAllReplicasFailing := effectiveAllReplicasFailing(policy)
	dryRun := effectiveDryRun(policy)
	restartWindow := effectiveRestartWindow(policy)

	// Every policy sees every pod, so overlapping policies have to agree on who
	// acts. Load the full set once and let the most restrictive matching policy
	// win each workload.
	policyList := &crashloopv1alpha1.CrashLoopPolicyList{}
	if err := r.List(ctx, policyList); err != nil {
		logger.Error(err, "failed to list policies")
		return ctrl.Result{}, err
	}
	nsResolver := newNamespaceResolver(r.Client)

	// Resolve allowed namespaces from namespaceSelector
	allowedNamespaces, err := nsResolver.allowed(ctx, policy)
	if err != nil {
		logger.Error(err, "failed to resolve namespace selector")
		return ctrl.Result{}, err
	}

	// Ask the cache only for pods waiting on one of the reasons this policy
	// watches. Listing every pod in the cluster and discarding the healthy
	// ones does not scale with cluster size.
	pods, err := listCandidatePods(ctx, r.Client, watchReasons, effectiveTerminationReasons(policy))
	if err != nil {
		logger.Error(err, "failed to list failing pods")
		return ctrl.Result{}, err
	}

	// Track which workloads we have already processed
	processed := make(map[string]bool)
	scaledDown := int32(0)
	// Errors inside the loop are per-workload and must not abort the whole
	// evaluation, but they must not vanish either: they are counted here and
	// reported through the Ready condition.
	loopErrors := 0

	for i := range pods {
		pod := &pods[i]

		// Skip namespaces not matching the selector (if set)
		if allowedNamespaces != nil {
			if !allowedNamespaces[pod.Namespace] {
				continue
			}
		}

		// Skip excluded namespaces
		if isExcludedNamespace(pod.Namespace, policy.Spec.ExcludeNamespaces) {
			continue
		}

		// Check if pod has a matching failure reason. A restart loop already
		// carries its own restart and recency thresholds, so only the waiting
		// path is gated on restartThreshold and durationThreshold here.
		reason, waitingFailing := podHasFailureReason(pod, watchReasons)
		loopReason, looping := podIsRestartLooping(pod,
			effectiveTerminationReasons(policy), restartThreshold, restartWindow)
		if !waitingFailing && !looping {
			continue
		}
		if !waitingFailing {
			reason = loopReason
		} else if !podExceedsRestartThreshold(pod, restartThreshold) &&
			!podExceedsDurationThreshold(pod, durationThreshold) && !looping {
			continue
		}

		// Resolve owner workload
		owner, err := resolveOwnerWorkload(ctx, r.Client, pod)
		if err != nil {
			logger.Error(err, "failed to resolve owner workload", "pod", pod.Name, "namespace", pod.Namespace)
			loopErrors++
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

		// Check exclude workload selector
		if policy.Spec.ExcludeWorkloadSelector != nil {
			excluded, err := isWorkloadExcludedBySelector(ctx, r.Client, owner, policy.Spec.ExcludeWorkloadSelector)
			if err != nil {
				logger.Error(err, "failed to check workload selector", "workload", key)
				loopErrors++
				continue
			}
			if excluded {
				logger.V(1).Info("workload excluded via selector, skipping", "workload", key)
				continue
			}
		}

		// Check if all replicas are failing (if configured)
		if requireAllReplicasFailing {
			allFailing, err := allReplicasFailing(ctx, r.Client, owner, policy)
			if err != nil {
				logger.Error(err, "failed to check all replicas", "workload", key)
				loopErrors++
				continue
			}
			if !allFailing {
				logger.V(1).Info("not all replicas failing, skipping", "workload", key)
				continue
			}
		}

		// Several policies may match this workload. The most restrictive one
		// owns the action; the others leave it alone so that the workload is
		// scaled down once and by an identifiable policy.
		winner, err := winningPolicy(ctx, r.Client, nsResolver, policyList.Items, pod, owner)
		if err != nil {
			logger.Error(err, "failed to resolve winning policy", "workload", key)
			loopErrors++
			continue
		}
		if winner != nil && winner.Name != policy.Name {
			logger.V(1).Info("another policy is more restrictive for this workload, skipping",
				"workload", key, "winner", winner.Name)
			continue
		}

		// Scale down or suspend the workload
		scaleReason := fmt.Sprintf("pods failing with %s (policy: %s)", reason, policy.Name)
		acted, err := scaleDownWorkload(ctx, r.Client, owner, scaleReason, policy.Name, dryRun)
		if err != nil {
			logger.Error(err, "failed to scale down workload", "workload", key)
			loopErrors++
			continue
		}

		if acted {
			scaledDown++
			scaledDownTotal.WithLabelValues(
				policy.Name, owner.Namespace, owner.Kind, reason, strconv.FormatBool(dryRun),
			).Inc()
			eventReason := EventReasonScaledDown
			eventMsg := fmt.Sprintf("Scaled down %s %s/%s: %s", owner.Kind, owner.Namespace, owner.Name, scaleReason)
			if owner.Kind == "CronJob" {
				eventReason = EventReasonSuspended
				eventMsg = fmt.Sprintf("Suspended CronJob %s/%s: %s", owner.Namespace, owner.Name, scaleReason)
			}
			if dryRun {
				eventReason = EventReasonDryRun
				eventMsg = "[DRY RUN] " + eventMsg
			}
			r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, eventReason, "Reconcile", eventMsg)
			logger.Info(eventMsg)
		}
	}

	// Collect currently active scaled-down workloads. A failure here leaves the
	// list incomplete rather than empty, so it counts as a partial failure
	// instead of silently truncating status.
	activeScaledDown, err := findActiveScaledDownWorkloads(ctx, r.Client, allowedNamespaces, policy.Spec.ExcludeNamespaces, targets, policy.Name)
	if err != nil {
		logger.Error(err, "failed to find active scaled-down workloads")
		loopErrors++
	}

	truncated := int32(0)
	if len(activeScaledDown) > MaxActiveScaledDown {
		omitted := min(len(activeScaledDown)-MaxActiveScaledDown, math.MaxInt32)
		truncated = int32(omitted)
		logger.Info("active scaled-down list truncated",
			"limit", MaxActiveScaledDown, "omitted", omitted)
		activeScaledDown = activeScaledDown[:MaxActiveScaledDown]
	}

	workloadsScaledDown.WithLabelValues(policy.Name).Set(float64(len(activeScaledDown)) + float64(truncated))
	policyReady.WithLabelValues(policy.Name).Set(boolToFloat(loopErrors == 0))
	if loopErrors > 0 {
		policyEvaluationErrors.WithLabelValues(policy.Name).Add(float64(loopErrors))
	}

	// Update status with conditions
	generation := policy.Generation
	if err := updatePolicyStatusIfChanged(ctx, r.Client, policy, func() {
		policy.Status.Phase = crashloopv1alpha1.CrashLoopPolicyPhaseActive
		policy.Status.ObservedGeneration = generation
		policy.Status.ScaledDownWorkloads += scaledDown
		policy.Status.ActiveScaledDown = activeScaledDown
		policy.Status.ActiveScaledDownTruncated = truncated

		// Ready reflects whether the evaluation actually completed. It used to
		// be set to True unconditionally, which hid every per-workload failure.
		readyStatus := metav1.ConditionTrue
		readyReason := "ReconcileSucceeded"
		readyMessage := "Policy evaluation completed successfully"
		if loopErrors > 0 {
			readyStatus = metav1.ConditionFalse
			readyReason = "ReconcilePartiallyFailed"
			readyMessage = fmt.Sprintf("Policy evaluation completed with %d error(s); see operator logs", loopErrors)
		}
		meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
			Type:               ConditionReady,
			Status:             readyStatus,
			ObservedGeneration: generation,
			Reason:             readyReason,
			Message:            readyMessage,
		})

		// Set Degraded condition based on whether workloads are scaled down
		if len(activeScaledDown) > 0 {
			meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
				Type:               ConditionDegraded,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: generation,
				Reason:             "WorkloadsScaledDown",
				Message:            fmt.Sprintf("%d workload(s) currently scaled down", len(activeScaledDown)),
			})
		} else {
			meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
				Type:               ConditionDegraded,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: generation,
				Reason:             "NoFailingWorkloads",
				Message:            "No workloads are currently scaled down",
			})
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Use per-policy reconcile interval if configured
	requeueInterval, _ := effectiveReconcileInterval(policy)

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CrashLoopPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &corev1.Pod{}, IndexPodWaitingReason, podWaitingReasons,
	); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &corev1.Pod{}, IndexPodTerminationReason, podTerminationReasons,
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&crashloopv1alpha1.CrashLoopPolicy{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToPolicy),
			// Healthy pods vastly outnumber stuck ones and cannot trigger an
			// action, so filtering them out here keeps the queue quiet.
			builder.WithPredicates(predicate.NewPredicateFuncs(podIsInteresting)),
		).
		Complete(r)
}

// podIsInteresting reports whether a pod could matter to any policy: it is
// either waiting with a reason, or it has restarted with a recorded
// termination reason. A pod in a slow restart loop is running when observed,
// so the waiting check alone would drop its events.
func podIsInteresting(obj client.Object) bool {
	return len(podWaitingReasons(obj)) > 0 || len(podTerminationReasons(obj)) > 0
}

// mapPodToPolicy maps a pod event to the CrashLoopPolicy objects that should be reconciled.
// It filters out policies whose namespace configuration excludes the pod's namespace.
func (r *CrashLoopPolicyReconciler) mapPodToPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)
	podNamespace := obj.GetNamespace()

	policyList := &crashloopv1alpha1.CrashLoopPolicyList{}
	if err := r.List(ctx, policyList); err != nil {
		logger.Error(err, "failed to list policies in mapPodToPolicy")
		return nil
	}

	var requests []reconcile.Request
	for i := range policyList.Items {
		policy := &policyList.Items[i]

		// Skip if pod is in an excluded namespace for this policy
		if isExcludedNamespace(podNamespace, policy.Spec.ExcludeNamespaces) {
			continue
		}

		// Deliberately no namespaceSelector check here. Resolving it costs a
		// namespace list per policy per event, and this function only decides
		// whether to enqueue: Reconcile applies the selector properly anyway.
		// Enqueuing a reconcile that finds nothing is far cheaper, especially
		// since the watch predicate already filters out healthy pods.
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(policy),
		})
	}
	return requests
}
