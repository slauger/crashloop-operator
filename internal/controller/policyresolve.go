package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crashloopv1alpha1 "github.com/slauger/crashloop-operator/api/v1alpha1"
)

// Effective spec accessors. The API server applies the kubebuilder defaults,
// but objects can reach the controller without having gone through defaulting
// (an old object, or a unit test), so every read of a defaulted field goes
// through one of these rather than repeating the fallback at each call site.

func effectiveWatchReasons(p *crashloopv1alpha1.CrashLoopPolicy) []string {
	if len(p.Spec.WatchReasons) > 0 {
		return p.Spec.WatchReasons
	}
	return DefaultWatchReasons
}

func effectiveTargets(p *crashloopv1alpha1.CrashLoopPolicy) []string {
	if len(p.Spec.Targets) > 0 {
		return p.Spec.Targets
	}
	return DefaultTargets
}

func effectiveRestartThreshold(p *crashloopv1alpha1.CrashLoopPolicy) int32 {
	if p.Spec.RestartThreshold > 0 {
		return p.Spec.RestartThreshold
	}
	return DefaultRestartThreshold
}

func effectiveDurationThreshold(p *crashloopv1alpha1.CrashLoopPolicy) time.Duration {
	return parseDuration(p.Spec.DurationThreshold)
}

func effectiveAllReplicasFailing(p *crashloopv1alpha1.CrashLoopPolicy) bool {
	if p.Spec.AllReplicasFailing != nil {
		return *p.Spec.AllReplicasFailing
	}
	return true
}

func effectiveDryRun(p *crashloopv1alpha1.CrashLoopPolicy) bool {
	return p.Spec.DryRun != nil && *p.Spec.DryRun
}

// isMoreRestrictive reports whether a would act on a workload sooner, or more
// forcefully, than b. The comparison is a total order so that the winner among
// a set of matching policies is deterministic and independent of list order:
//
//  1. lower restartThreshold
//  2. shorter durationThreshold
//  3. allReplicasFailing false before true (acts on partial failure too)
//  4. dryRun false before true (a real action outranks a simulated one)
//  5. name ascending, purely to break remaining ties
func isMoreRestrictive(a, b *crashloopv1alpha1.CrashLoopPolicy) bool {
	if ra, rb := effectiveRestartThreshold(a), effectiveRestartThreshold(b); ra != rb {
		return ra < rb
	}
	if da, db := effectiveDurationThreshold(a), effectiveDurationThreshold(b); da != db {
		return da < db
	}
	if aa, ab := effectiveAllReplicasFailing(a), effectiveAllReplicasFailing(b); aa != ab {
		return !aa
	}
	if wa, wb := effectiveDryRun(a), effectiveDryRun(b); wa != wb {
		return !wa
	}
	return a.Name < b.Name
}

// namespaceResolver caches namespaceSelector lookups for one reconcile pass, so
// that comparing N policies does not issue N namespace lists.
type namespaceResolver struct {
	client client.Client
	cache  map[string]map[string]bool
}

func newNamespaceResolver(c client.Client) *namespaceResolver {
	return &namespaceResolver{client: c, cache: make(map[string]map[string]bool)}
}

// allowed returns the namespaces admitted by the policy's selector, or nil when
// the policy has no selector and therefore admits all of them.
func (nr *namespaceResolver) allowed(ctx context.Context, p *crashloopv1alpha1.CrashLoopPolicy) (map[string]bool, error) {
	if p.Spec.NamespaceSelector == nil {
		return nil, nil
	}
	if cached, ok := nr.cache[p.Name]; ok {
		return cached, nil
	}
	resolved, err := resolveNamespaces(ctx, nr.client, p.Spec.NamespaceSelector)
	if err != nil {
		return nil, err
	}
	nr.cache[p.Name] = resolved
	return resolved, nil
}

// policyWouldAct reports whether the policy would scale down owner right now,
// given the failing pod that led to it. It runs the same checks the reconcile
// loop applies to its own policy, so that the winner is chosen among policies
// that would genuinely act rather than merely reference the same namespace.
func policyWouldAct(
	ctx context.Context,
	c client.Client,
	nr *namespaceResolver,
	policy *crashloopv1alpha1.CrashLoopPolicy,
	pod *corev1.Pod,
	owner *ownerWorkload,
) (bool, error) {
	allowedNamespaces, err := nr.allowed(ctx, policy)
	if err != nil {
		return false, err
	}
	if allowedNamespaces != nil && !allowedNamespaces[pod.Namespace] {
		return false, nil
	}
	if isExcludedNamespace(pod.Namespace, policy.Spec.ExcludeNamespaces) {
		return false, nil
	}
	if !isTargetKind(owner.Kind, effectiveTargets(policy)) {
		return false, nil
	}

	watchReasons := effectiveWatchReasons(policy)
	if _, failing := podHasFailureReason(pod, watchReasons); !failing {
		return false, nil
	}
	if !podExceedsRestartThreshold(pod, effectiveRestartThreshold(policy)) &&
		!podExceedsDurationThreshold(pod, effectiveDurationThreshold(policy)) {
		return false, nil
	}

	if policy.Spec.ExcludeWorkloadSelector != nil {
		excluded, err := isWorkloadExcludedBySelector(ctx, c, owner, policy.Spec.ExcludeWorkloadSelector)
		if err != nil {
			return false, err
		}
		if excluded {
			return false, nil
		}
	}

	if effectiveAllReplicasFailing(policy) {
		allFailing, err := allReplicasFailing(ctx, c, owner, watchReasons)
		if err != nil {
			return false, err
		}
		if !allFailing {
			return false, nil
		}
	}

	return true, nil
}

// winningPolicy returns the most restrictive policy that would act on owner for
// the given pod. It returns nil when no policy would act. Policies other than
// the winner leave the workload alone, so a workload is scaled down once, by one
// identifiable policy, no matter how many policies overlap.
func winningPolicy(
	ctx context.Context,
	c client.Client,
	nr *namespaceResolver,
	policies []crashloopv1alpha1.CrashLoopPolicy,
	pod *corev1.Pod,
	owner *ownerWorkload,
) (*crashloopv1alpha1.CrashLoopPolicy, error) {
	var winner *crashloopv1alpha1.CrashLoopPolicy
	for i := range policies {
		candidate := &policies[i]
		acts, err := policyWouldAct(ctx, c, nr, candidate, pod, owner)
		if err != nil {
			return nil, err
		}
		if !acts {
			continue
		}
		if winner == nil || isMoreRestrictive(candidate, winner) {
			winner = candidate
		}
	}
	return winner, nil
}
