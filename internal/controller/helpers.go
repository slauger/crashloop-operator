package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// podExceedsDurationThreshold checks if the pod has been in a failing state
// longer than the given duration.
func podExceedsDurationThreshold(pod *corev1.Pod, duration time.Duration) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionFalse {
			return time.Since(condition.LastTransitionTime.Time) >= duration
		}
	}
	// If the pod has been pending for too long, that also counts
	if pod.Status.Phase == corev1.PodPending && !pod.CreationTimestamp.IsZero() {
		return time.Since(pod.CreationTimestamp.Time) >= duration
	}
	return false
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

	ownerRef := pod.OwnerReferences[0]
	ns := pod.Namespace

	switch ownerRef.Kind {
	case "ReplicaSet":
		rs := &appsv1.ReplicaSet{}
		if err := c.Get(ctx, types.NamespacedName{Name: ownerRef.Name, Namespace: ns}, rs); err != nil {
			if errors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if len(rs.OwnerReferences) > 0 && rs.OwnerReferences[0].Kind == "Deployment" {
			return &ownerWorkload{Kind: "Deployment", Name: rs.OwnerReferences[0].Name, Namespace: ns}, nil
		}
		return nil, nil

	case "StatefulSet":
		return &ownerWorkload{Kind: "StatefulSet", Name: ownerRef.Name, Namespace: ns}, nil

	case "Job":
		job := &batchv1.Job{}
		if err := c.Get(ctx, types.NamespacedName{Name: ownerRef.Name, Namespace: ns}, job); err != nil {
			if errors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if len(job.OwnerReferences) > 0 && job.OwnerReferences[0].Kind == "CronJob" {
			return &ownerWorkload{Kind: "CronJob", Name: job.OwnerReferences[0].Name, Namespace: ns}, nil
		}
		return nil, nil

	default:
		return nil, nil
	}
}

// isTargetKind checks if a workload kind is in the targets list.
func isTargetKind(kind string, targets []string) bool {
	for _, t := range targets {
		if t == kind {
			return true
		}
	}
	return false
}

// isExcludedNamespace checks if a namespace is in the exclude list.
func isExcludedNamespace(ns string, excluded []string) bool {
	for _, e := range excluded {
		if e == ns {
			return true
		}
	}
	return false
}

// workloadKey returns a unique string key for a workload.
func workloadKey(w *ownerWorkload) string {
	return fmt.Sprintf("%s/%s/%s", w.Namespace, w.Kind, w.Name)
}

// parseDuration parses a duration string, falling back to the default.
func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		d, _ = time.ParseDuration(DefaultDurationThreshold)
	}
	return d
}

// allReplicasFailing checks if all pods of a workload are in a failing state.
func allReplicasFailing(ctx context.Context, c client.Client, owner *ownerWorkload, watchReasons []string) (bool, error) {
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
			if _, failing := podHasFailureReason(&podList.Items[i], watchReasons); !failing {
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
			if _, failing := podHasFailureReason(&podList.Items[i], watchReasons); !failing {
				return false, nil
			}
		}
		return true, nil

	case "CronJob":
		// CronJobs create Jobs which create Pods; if we got here the pod is already failing
		return true, nil
	}
	return false, nil
}

// scaleDownWorkload scales a workload to zero or suspends it.
func scaleDownWorkload(ctx context.Context, c client.Client, owner *ownerWorkload, reason string, dryRun bool) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	switch owner.Kind {
	case "Deployment":
		deploy := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, deploy); err != nil {
			return false, err
		}
		if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas == 0 {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		prevReplicas := int32(1)
		if deploy.Spec.Replicas != nil {
			prevReplicas = *deploy.Spec.Replicas
		}
		deploy.Spec.Replicas = int32Ptr(0)
		if deploy.Annotations == nil {
			deploy.Annotations = make(map[string]string)
		}
		deploy.Annotations[AnnotationScaledDownReason] = reason
		deploy.Annotations[AnnotationScaledDownAt] = now
		deploy.Annotations[AnnotationPreviousReplicas] = fmt.Sprintf("%d", prevReplicas)
		return true, c.Update(ctx, deploy)

	case "StatefulSet":
		sts := &appsv1.StatefulSet{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, sts); err != nil {
			return false, err
		}
		if sts.Spec.Replicas != nil && *sts.Spec.Replicas == 0 {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		prevReplicas := int32(1)
		if sts.Spec.Replicas != nil {
			prevReplicas = *sts.Spec.Replicas
		}
		sts.Spec.Replicas = int32Ptr(0)
		if sts.Annotations == nil {
			sts.Annotations = make(map[string]string)
		}
		sts.Annotations[AnnotationScaledDownReason] = reason
		sts.Annotations[AnnotationScaledDownAt] = now
		sts.Annotations[AnnotationPreviousReplicas] = fmt.Sprintf("%d", prevReplicas)
		return true, c.Update(ctx, sts)

	case "CronJob":
		cj := &batchv1.CronJob{}
		if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: owner.Namespace}, cj); err != nil {
			return false, err
		}
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		cj.Spec.Suspend = boolPtr(true)
		if cj.Annotations == nil {
			cj.Annotations = make(map[string]string)
		}
		cj.Annotations[AnnotationScaledDownReason] = reason
		cj.Annotations[AnnotationScaledDownAt] = now
		return true, c.Update(ctx, cj)
	}

	return false, nil
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
