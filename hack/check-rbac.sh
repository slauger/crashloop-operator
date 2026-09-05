#!/usr/bin/env bash
# Compare the RBAC rules the chart ships against the rules controller-gen
# derives from the +kubebuilder:rbac markers.
#
# The chart splits its rules across two roles, one always cluster-scoped for
# the CrashLoopPolicy itself and one that follows scope.mode for workloads, so
# a plain file diff against config/rbac/role.yaml is not possible. Instead both
# sides are flattened into a sorted "apiGroup resource verb" triple list and
# compared as sets, which also makes the check insensitive to how rules happen
# to be grouped or ordered.
set -euo pipefail

CHART="${1:-charts/crashloop-operator}"
GENERATED="${2:-config/rbac/role.yaml}"

# Flatten every rule into one line per apiGroup/resource/verb combination.
# The empty core group is spelled out so it cannot be confused with a blank
# field, and duplicates are collapsed by sort -u.
normalise() {
  yq eval-all --output-format=json '. | select(.kind == "ClusterRole" or .kind == "Role")' - \
    | jq -r '
        (.rules // [])[]
        | . as $rule
        | (($rule.apiGroups // [""])[]) as $group
        | (($rule.resources // [])[]) as $resource
        | (($rule.verbs // [])[]) as $verb
        | "\(if $group == "" then "core" else $group end) \($resource) \($verb)"
      ' \
    | sort -u
}

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

# Render in cluster mode: it grants the full set, whereas namespace mode
# deliberately narrows workload access to a single namespace.
helm template rbac-check "${CHART}" --set scope.mode=cluster \
  | normalise > "${TMP}/chart.txt"
normalise < "${GENERATED}" > "${TMP}/generated.txt"

if diff -u "${TMP}/generated.txt" "${TMP}/chart.txt" > "${TMP}/diff.txt"; then
  echo "OK: chart RBAC matches the kubebuilder markers"
  exit 0
fi

echo "error: chart RBAC and the +kubebuilder:rbac markers disagree."
echo "Lines prefixed '-' are granted by the markers but missing from the chart."
echo "Lines prefixed '+' are granted by the chart but not by the markers."
echo
cat "${TMP}/diff.txt"
exit 1
