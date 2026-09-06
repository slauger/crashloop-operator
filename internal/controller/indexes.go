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

// IndexPodTerminationReason indexes pods by the termination reason of their
// last container exit. It is a separate index from the waiting one because the
// two describe different states: a container that is currently down versus one
// that keeps dying but is running right now.
const IndexPodTerminationReason = "status.terminationReason"

// podTerminationReasons returns the distinct last-termination reasons across a
// pod's containers, for containers that have actually restarted. A pod that
// has never restarted yields nothing, so the index stays small.
func podTerminationReasons(obj client.Object) []string {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	seen := make(map[string]struct{})
	collect := func(statuses []corev1.ContainerStatus) {
		for _, cs := range statuses {
			if cs.RestartCount == 0 {
				continue
			}
			if term := cs.LastTerminationState.Terminated; term != nil && term.Reason != "" {
				seen[term.Reason] = struct{}{}
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

// listPodsByIndex returns the pods matching any of the given values on the
// given index, deduplicated by UID since a pod can match several at once.
func listPodsByIndex(ctx context.Context, c client.Client, index string, values []string) ([]corev1.Pod, error) {
	var pods []corev1.Pod
	seen := make(map[types.UID]struct{})

	for _, value := range values {
		podList := &corev1.PodList{}
		if err := c.List(ctx, podList, client.MatchingFields{index: value}); err != nil {
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

// listCandidatePods returns the pods a policy could act on: those waiting with
// a watched reason, plus, when the policy opts in, those that keep restarting
// with a watched termination reason.
func listCandidatePods(
	ctx context.Context, c client.Client, waitingReasons, terminationReasons []string,
) ([]corev1.Pod, error) {
	pods, err := listPodsByIndex(ctx, c, IndexPodWaitingReason, waitingReasons)
	if err != nil {
		return nil, err
	}
	if len(terminationReasons) == 0 {
		return pods, nil
	}

	restarting, err := listPodsByIndex(ctx, c, IndexPodTerminationReason, terminationReasons)
	if err != nil {
		return nil, err
	}

	seen := make(map[types.UID]struct{}, len(pods))
	for i := range pods {
		seen[pods[i].UID] = struct{}{}
	}
	for i := range restarting {
		if _, dup := seen[restarting[i].UID]; dup {
			continue
		}
		pods = append(pods, restarting[i])
	}
	return pods, nil
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
