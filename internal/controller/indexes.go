package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IndexPodWaitingReason indexes pods by the waiting reasons of their containers.
// It lets the reconciler ask the cache for the handful of pods that are actually
// stuck, instead of listing every pod in the cluster and discarding almost all
// of them.
const IndexPodWaitingReason = "status.waitingReason"

// podWaitingReasons returns the distinct waiting reasons across a pod's
// containers and init containers. A pod with no waiting container yields no
// index entries and is therefore invisible to the indexed lookup.
func podWaitingReasons(obj client.Object) []string {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	seen := make(map[string]struct{})
	collect := func(statuses []corev1.ContainerStatus) {
		for _, cs := range statuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				seen[cs.State.Waiting.Reason] = struct{}{}
			}
		}
	}
	collect(pod.Status.ContainerStatuses)
	collect(pod.Status.InitContainerStatuses)

	if len(seen) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(seen))
	for r := range seen {
		reasons = append(reasons, r)
	}
	return reasons
}

// listPodsByWaitingReasons returns the pods matching any of the given waiting
// reasons, deduplicated by UID since a pod can match several reasons at once.
func listPodsByWaitingReasons(ctx context.Context, c client.Client, reasons []string) ([]corev1.Pod, error) {
	var pods []corev1.Pod
	seen := make(map[types.UID]struct{})

	for _, reason := range reasons {
		podList := &corev1.PodList{}
		if err := c.List(ctx, podList, client.MatchingFields{IndexPodWaitingReason: reason}); err != nil {
			return nil, err
		}
		for i := range podList.Items {
			pod := podList.Items[i]
			if _, dup := seen[pod.UID]; dup {
				continue
			}
			seen[pod.UID] = struct{}{}
			pods = append(pods, pod)
		}
	}
	return pods, nil
}
