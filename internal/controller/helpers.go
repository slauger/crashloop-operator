package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crashloopv1alpha1 "github.com/slauger/crashloop-operator/api/v1alpha1"
)

// updateStatusWithRetry updates an object's status with automatic retry on conflict.
func updateStatusWithRetry(ctx context.Context, c client.Client, obj client.Object, mutate func()) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		mutate()
		return c.Status().Update(ctx, obj)
	})
}

// updatePolicyStatusIfChanged applies mutate to the policy status and writes it
// only when the result differs from what the API server holds. Without this the
// operator rewrites status on every interval even when nothing happened, which
// churns resourceVersion and wakes its own watch. LastEvaluationTime is stamped
// only alongside a real change, so it records the last evaluation that produced
// one rather than the last evaluation that ran.
func updatePolicyStatusIfChanged(ctx context.Context, c client.Client, policy *crashloopv1alpha1.CrashLoopPolicy, mutate func()) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
			return err
		}
		before := policy.Status.DeepCopy()
		mutate()
		if equality.Semantic.DeepEqual(before, &policy.Status) {
			return nil
		}
		now := metav1.Now()
		policy.Status.LastEvaluationTime = &now
		return c.Status().Update(ctx, policy)
	})
}

// podHasFailureReason checks if a pod has any container in a waiting state
// matching one of the watched reasons.
func podHasFailureReason(pod *corev1.Pod, watchReasons []string) (string, bool) {
	reasonSet := make(map[string]struct{}, len(watchReasons))
	for _, r := range watchReasons {
		reasonSet[r] = struct{}{}
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			if _, ok := reasonSet[cs.State.Waiting.Reason]; ok {
				return cs.State.Waiting.Reason, true
			}
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			if _, ok := reasonSet[cs.State.Waiting.Reason]; ok {
				return cs.State.Waiting.Reason, true
			}
		}
	}
	return "", false
}

// podIsRestartLooping reports whether a container keeps dying for one of the
// watched termination reasons, and returns that reason.
//
// This covers the loop that WatchReasons cannot see. Kubelet resets its
// restart backoff once a container has stayed up longer than twice the maximum
// backoff, so a container that survives beyond that between deaths restarts
// immediately every time and never enters CrashLoopBackOff. A memory leak is
// the usual shape: run, grow, get OOM-killed, restart at once, repeat.
//
// The recency bound is what makes this safe. RestartCount is cumulative for a
// pod's whole life and never decays, so without it a workload that misbehaved
// last month would still be acted on.
func podIsRestartLooping(pod *corev1.Pod, reasons []string, threshold int32, window time.Duration) (string, bool) {
	if len(reasons) == 0 {
		return "", false
	}
	// A pod on its way out restarts nothing and would otherwise sweep every
	// replica of a rolling update into the candidate set at once.
	if pod.DeletionTimestamp != nil {
		return "", false
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return "", false
	}

	reasonSet := make(map[string]struct{}, len(reasons))
	for _, r := range reasons {
		reasonSet[r] = struct{}{}
	}

	restartable := restartableInitContainers(pod)
	check := func(statuses []corev1.ContainerStatus, initOnly bool) (string, bool) {
		for _, cs := range statuses {
			if initOnly {
				// A classic init container runs once; only sidecars, which
				// declare restartPolicy Always, can be in a restart loop.
				if _, ok := restartable[cs.Name]; !ok {
					continue
				}
			}
			// The container must still be part of the running pod, and past
			// any startup probe, so a slow start is not mistaken for a loop.
			if cs.State.Terminated != nil {
				continue
			}
			if cs.Started != nil && !*cs.Started {
				continue
			}
			if cs.RestartCount < threshold {
				continue
			}
			term := cs.LastTerminationState.Terminated
			if term == nil || term.Reason == "" {
				continue
			}
			if _, ok := reasonSet[term.Reason]; !ok {
				continue
			}
			if term.FinishedAt.IsZero() || time.Since(term.FinishedAt.Time) > window {
				continue
			}
			return term.Reason, true
		}
		return "", false
	}

	if reason, ok := check(pod.Status.ContainerStatuses, false); ok {
		return reason, true
	}
	return check(pod.Status.InitContainerStatuses, true)
}

// restartableInitContainers returns the names of init containers declared with
// restartPolicy Always, which Kubernetes treats as sidecars.
func restartableInitContainers(pod *corev1.Pod) map[string]struct{} {
	names := make(map[string]struct{})
	for _, c := range pod.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			names[c.Name] = struct{}{}
		}
	}
	return names
}

// podExceedsRestartThreshold checks if any container has restarted more than threshold times.
func podExceedsRestartThreshold(pod *corev1.Pod, threshold int32) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount >= threshold {
			return true
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.RestartCount >= threshold {
			return true
		}
	}
	return false
}

// podFailingSince returns the instant from which the pod has been
// continuously not ready, and whether that instant could be determined.
//
// The obvious clock, the container's last termination time, does not work.
// Kubelet restarts a crashing container with exponential backoff capped at a
// few minutes and writes a fresh FinishedAt on every restart, so for a pod in
// a steady crash loop that timestamp is always recent and a threshold above
// the backoff cap can never be reached.
//
// The readiness condition does not bounce that way: it flips to False when the
// container first stops serving and stays there for as long as it keeps
// failing. If the container does recover for a while and then fails again, the
// condition flips too, which is the intended meaning: the pod was healthy in
// between, so the clock should restart.
//
// ContainersReady is preferred over Ready because Ready can also be held false
// by readiness gates, which say nothing about the containers.
func podFailingSince(pod *corev1.Pod) (time.Time, bool) {
	for _, condType := range []corev1.PodConditionType{corev1.ContainersReady, corev1.PodReady} {
		for _, cond := range pod.Status.Conditions {
			if cond.Type != condType || cond.Status != corev1.ConditionFalse {
				continue
			}
			if !cond.LastTransitionTime.IsZero() {
				return cond.LastTransitionTime.Time, true
			}
		}
	}
	// No usable readiness condition. This happens in the first moments of a
	// pod's life before kubelet has reported, so returning "unknown" simply
	// defers the decision to the next evaluation rather than guessing from the
	// creation timestamp, which would fire immediately on an old pod that only
	// just broke.
	return time.Time{}, false
}

// podExceedsDurationThreshold reports whether the pod has been failing for at
// least the given duration.
func podExceedsDurationThreshold(pod *corev1.Pod, duration time.Duration) bool {
	failingSince, ok := podFailingSince(pod)
	if !ok {
		return false
	}
	return time.Since(failingSince) >= duration
}

// ownerWorkload represents a resolved top-level workload that owns a pod.
type ownerWorkload struct {
	Kind      string
	Name      string
	Namespace string
}

// resolveOwnerWorkload walks ownerReferences up to two levels to find the
// top-level workload (Deployment, StatefulSet, or CronJob) that owns a pod.
func resolveOwnerWorkload(ctx context.Context, c client.Client, pod *corev1.Pod) (*ownerWorkload, error) {
	if len(pod.OwnerReferences) == 0 {
		return nil, nil
	}

	// GetControllerOf returns the reference with controller: true. Index 0 is
	// not guaranteed to be it: an object may carry additional non-controller
	// owner references, and several tools add them.
	ownerRef := metav1.GetControllerOf(pod)
	if ownerRef == nil {
		return nil, nil
	}
	ns := pod.Namespace

	switch ownerRef.Kind {
	case "ReplicaSet":
		rs := &appsv1.ReplicaSet{}
		if err := c.Get(ctx, types.NamespacedName{Name: ownerRef.Name, Namespace: ns}, rs); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if rs.UID != ownerRef.UID {
			// The named ReplicaSet was replaced by a new one under the same
			// name, so it is not the object that owns this pod.
			return nil, nil
		}
		if rsOwner := metav1.GetControllerOf(rs); rsOwner != nil && rsOwner.Kind == "Deployment" {
			return &ownerWorkload{Kind: "Deployment", Name: rsOwner.Name, Namespace: ns}, nil
		}
		return nil, nil

	case "StatefulSet":
		return &ownerWorkload{Kind: "StatefulSet", Name: ownerRef.Name, Namespace: ns}, nil

	case "Job":
		job := &batchv1.Job{}
		if err := c.Get(ctx, types.NamespacedName{Name: ownerRef.Name, Namespace: ns}, job); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if job.UID != ownerRef.UID {
			return nil, nil
		}
		if jobOwner := metav1.GetControllerOf(job); jobOwner != nil && jobOwner.Kind == "CronJob" {
			return &ownerWorkload{Kind: "CronJob", Name: jobOwner.Name, Namespace: ns}, nil
		}
		return nil, nil

	default:
		return nil, nil
	}
}

// isTargetKind checks if a workload kind is in the targets list.
func isTargetKind(kind string, targets []string) bool {
	return slices.Contains(targets, kind)
}

// isExcludedNamespace checks if a namespace is in the exclude list.
func isExcludedNamespace(ns string, excluded []string) bool {
	return slices.Contains(excluded, ns)
}

// workloadKey returns a unique string key for a workload.
func workloadKey(w *ownerWorkload) string {
	return fmt.Sprintf("%s/%s/%s", w.Namespace, w.Kind, w.Name)
}

// parseDuration parses a duration string, falling back to the caller's default
// and reporting whether the input was usable. The fallback is a parameter
// because the two duration fields have different defaults; hardcoding one of
// them made an invalid reconcileInterval silently run at the 30m threshold
// default instead of 60s.
//
// The API server rejects malformed values through the Pattern on both fields,
// so a false return means either an object written before that validation
// existed or a value the pattern admits but time.ParseDuration does not.
func parseDuration(s string, fallback time.Duration) (time.Duration, bool) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback, false
	}
	return d, true
}

// allReplicasFailing reports whether every replica of the workload is failing
// in a way the policy acts on. It takes the whole policy rather than a reason
// list so that it cannot disagree with the reconcile loop about what counts as
// failing, which would silently make an opted-in restart loop inert whenever
// allReplicasFailing is true, as it is by default.
func allReplicasFailing(ctx context.Context, c client.Client, owner *ownerWorkload, policy *crashloopv1alpha1.CrashLoopPolicy) (bool, error) {
	switch owner.Kind {
	case "Deployment":
		deploy := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, deploy); err != nil {
			return false, err
		}
		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		if desired == 0 {
			return false, nil
		}
		// List pods via the deployment's selector
		podList := &corev1.PodList{}
		selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
		if err != nil {
			return false, err
		}
		if err := c.List(ctx, podList, client.InNamespace(owner.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return false, err
		}
		if len(podList.Items) == 0 {
			return false, nil
		}
		for i := range podList.Items {
			if !podMatchesPolicy(policy, &podList.Items[i]) {
				return false, nil
			}
		}
		return true, nil

	case "StatefulSet":
		sts := &appsv1.StatefulSet{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, sts); err != nil {
			return false, err
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if desired == 0 {
			return false, nil
		}
		podList := &corev1.PodList{}
		selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
		if err != nil {
			return false, err
		}
		if err := c.List(ctx, podList, client.InNamespace(owner.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return false, err
		}
		if len(podList.Items) == 0 {
			return false, nil
		}
		for i := range podList.Items {
			if !podMatchesPolicy(policy, &podList.Items[i]) {
				return false, nil
			}
		}
		return true, nil

	case "CronJob":
		cj := &batchv1.CronJob{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, cj); err != nil {
			return false, err
		}
		// List jobs owned by this CronJob
		jobList := &batchv1.JobList{}
		if err := c.List(ctx, jobList, client.InNamespace(owner.Namespace)); err != nil {
			return false, err
		}
		// Find active jobs belonging to this CronJob
		var ownedJobs []batchv1.Job
		for i := range jobList.Items {
			for _, ref := range jobList.Items[i].OwnerReferences {
				if ref.Kind == "CronJob" && ref.Name == owner.Name {
					ownedJobs = append(ownedJobs, jobList.Items[i])
					break
				}
			}
		}
		if len(ownedJobs) == 0 {
			return false, nil
		}
		// Check pods of the most recent job. List order is not defined, so
		// sort rather than trusting the last element.
		slices.SortFunc(ownedJobs, func(a, b batchv1.Job) int {
			return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
		})
		latestJob := ownedJobs[len(ownedJobs)-1]
		podList := &corev1.PodList{}
		if err := c.List(ctx, podList, client.InNamespace(owner.Namespace)); err != nil {
			return false, err
		}
		var jobPods []corev1.Pod
		for i := range podList.Items {
			for _, ref := range podList.Items[i].OwnerReferences {
				if ref.Kind == "Job" && ref.Name == latestJob.Name {
					jobPods = append(jobPods, podList.Items[i])
					break
				}
			}
		}
		if len(jobPods) == 0 {
			return false, nil
		}
		for i := range jobPods {
			if !podMatchesPolicy(policy, &jobPods[i]) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

// currentReplicas reads spec.replicas from a Deployment or StatefulSet.
func currentReplicas(obj client.Object) (int32, bool) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		if o.Spec.Replicas == nil {
			return 1, true
		}
		return *o.Spec.Replicas, true
	case *appsv1.StatefulSet:
		if o.Spec.Replicas == nil {
			return 1, true
		}
		return *o.Spec.Replicas, true
	default:
		return 0, false
	}
}

// scaleWorkloadToZero records why the workload is being stopped and then sets
// its replica count to zero through the scale subresource.
//
// The replica change goes through /scale rather than a full object update
// because something else may also be managing the count. A HorizontalPodAutoscaler
// writes through the same subresource, so a narrow write competes with it
// cleanly, whereas a full-object update replays the entire spec on a conflict
// retry.
//
// The annotations are written first on purpose. If the scale call then fails,
// the workload is still running and the next evaluation retries it, so the
// mistake corrects itself. In the other order a failed annotation write would
// leave a workload stopped with no record of its previous replica count, and
// the guard above would stop the operator ever revisiting it.
func scaleWorkloadToZero(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	key types.NamespacedName,
	reason, policyName, now string,
	dryRun bool,
) (bool, error) {
	if err := c.Get(ctx, key, obj); err != nil {
		return false, err
	}
	replicas, ok := currentReplicas(obj)
	if !ok {
		return false, fmt.Errorf("unsupported workload type %T", obj)
	}
	if replicas == 0 {
		return false, nil
	}
	if dryRun {
		return true, nil
	}

	var prevReplicas int32
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, key, obj); err != nil {
			return err
		}
		current, _ := currentReplicas(obj)
		if current == 0 {
			return nil
		}
		prevReplicas = current

		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[AnnotationScaledDownReason] = reason
		annotations[AnnotationScaledDownAt] = now
		annotations[AnnotationScaledDownBy] = policyName
		annotations[AnnotationPreviousReplicas] = fmt.Sprintf("%d", prevReplicas)
		obj.SetAnnotations(annotations)
		return c.Update(ctx, obj)
	}); err != nil {
		return false, err
	}
	if prevReplicas == 0 {
		return false, nil
	}

	scale := &autoscalingv1.Scale{}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.SubResource("scale").Get(ctx, obj, scale); err != nil {
			return err
		}
		if scale.Spec.Replicas == 0 {
			return nil
		}
		scale.Spec.Replicas = 0
		return c.SubResource("scale").Update(ctx, obj, client.WithSubResourceBody(scale))
	}); err != nil {
		return false, err
	}
	return true, nil
}

// scaleDownWorkload scales a workload to zero or suspends it.
// It uses RetryOnConflict to handle concurrent updates safely.
func scaleDownWorkload(ctx context.Context, c client.Client, owner *ownerWorkload, reason, policyName string, dryRun bool) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	key := types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}

	switch owner.Kind {
	case "Deployment":
		return scaleWorkloadToZero(ctx, c, &appsv1.Deployment{}, key, reason, policyName, now, dryRun)

	case "StatefulSet":
		return scaleWorkloadToZero(ctx, c, &appsv1.StatefulSet{}, key, reason, policyName, now, dryRun)

	case "CronJob":
		cj := &batchv1.CronJob{}
		if err := c.Get(ctx, key, cj); err != nil {
			return false, err
		}
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := c.Get(ctx, key, cj); err != nil {
				return err
			}
			if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
				return nil
			}
			cj.Spec.Suspend = new(true)
			if cj.Annotations == nil {
				cj.Annotations = make(map[string]string)
			}
			cj.Annotations[AnnotationScaledDownReason] = reason
			cj.Annotations[AnnotationScaledDownAt] = now
			cj.Annotations[AnnotationScaledDownBy] = policyName
			return c.Update(ctx, cj)
		})
		if err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

// findActiveScaledDownWorkloads returns workload keys for all workloads that carry
// the crashloop-operator scaled-down annotation, filtered by namespace and targets.
func findActiveScaledDownWorkloads(ctx context.Context, c client.Client, allowedNamespaces map[string]bool, excludeNamespaces, targets []string, policyName string) ([]crashloopv1alpha1.ScaledDownWorkloadRef, error) {
	var active []crashloopv1alpha1.ScaledDownWorkloadRef

	if isTargetKind("Deployment", targets) {
		deployList := &appsv1.DeploymentList{}
		if err := c.List(ctx, deployList); err != nil {
			return nil, err
		}
		for i := range deployList.Items {
			d := &deployList.Items[i]
			if d.Annotations[AnnotationScaledDownReason] == "" {
				continue
			}
			// Only report workloads this policy scaled down itself.
			if d.Annotations[AnnotationScaledDownBy] != policyName {
				continue
			}
			if isExcludedNamespace(d.Namespace, excludeNamespaces) {
				continue
			}
			if allowedNamespaces != nil && !allowedNamespaces[d.Namespace] {
				continue
			}
			active = append(active, crashloopv1alpha1.ScaledDownWorkloadRef{
				Kind:      "Deployment",
				Namespace: d.Namespace,
				Name:      d.Name,
			})
		}
	}

	if isTargetKind("StatefulSet", targets) {
		stsList := &appsv1.StatefulSetList{}
		if err := c.List(ctx, stsList); err != nil {
			return nil, err
		}
		for i := range stsList.Items {
			s := &stsList.Items[i]
			if s.Annotations[AnnotationScaledDownReason] == "" {
				continue
			}
			// Only report workloads this policy scaled down itself.
			if s.Annotations[AnnotationScaledDownBy] != policyName {
				continue
			}
			if isExcludedNamespace(s.Namespace, excludeNamespaces) {
				continue
			}
			if allowedNamespaces != nil && !allowedNamespaces[s.Namespace] {
				continue
			}
			active = append(active, crashloopv1alpha1.ScaledDownWorkloadRef{
				Kind:      "StatefulSet",
				Namespace: s.Namespace,
				Name:      s.Name,
			})
		}
	}

	if isTargetKind("CronJob", targets) {
		cjList := &batchv1.CronJobList{}
		if err := c.List(ctx, cjList); err != nil {
			return nil, err
		}
		for i := range cjList.Items {
			cj := &cjList.Items[i]
			if cj.Annotations[AnnotationScaledDownReason] == "" {
				continue
			}
			// Only report workloads this policy scaled down itself.
			if cj.Annotations[AnnotationScaledDownBy] != policyName {
				continue
			}
			if isExcludedNamespace(cj.Namespace, excludeNamespaces) {
				continue
			}
			if allowedNamespaces != nil && !allowedNamespaces[cj.Namespace] {
				continue
			}
			active = append(active, crashloopv1alpha1.ScaledDownWorkloadRef{
				Kind:      "CronJob",
				Namespace: cj.Namespace,
				Name:      cj.Name,
			})
		}
	}

	return active, nil
}

// isWorkloadExcludedBySelector checks if a workload's labels match the given label selector.
func isWorkloadExcludedBySelector(ctx context.Context, c client.Client, owner *ownerWorkload, selector *metav1.LabelSelector) (bool, error) {
	if selector == nil {
		return false, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, fmt.Errorf("parsing workload exclude selector: %w", err)
	}

	var workloadLabels map[string]string

	switch owner.Kind {
	case "Deployment":
		obj := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		workloadLabels = obj.Labels
	case "StatefulSet":
		obj := &appsv1.StatefulSet{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		workloadLabels = obj.Labels
	case "CronJob":
		obj := &batchv1.CronJob{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		workloadLabels = obj.Labels
	}

	return sel.Matches(labels.Set(workloadLabels)), nil
}

// resolveNamespaces returns a set of namespace names matching the given label selector.
// If the selector is nil, it returns nil (meaning all namespaces are allowed).
func resolveNamespaces(ctx context.Context, c client.Client, selector *metav1.LabelSelector) (map[string]bool, error) {
	if selector == nil {
		return nil, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("parsing namespace selector: %w", err)
	}

	// Empty selector matches everything
	if sel.Empty() {
		return nil, nil
	}

	nsList := &corev1.NamespaceList{}
	if err := c.List(ctx, nsList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	result := make(map[string]bool, len(nsList.Items))
	for i := range nsList.Items {
		result[nsList.Items[i].Name] = true
	}
	return result, nil
}
