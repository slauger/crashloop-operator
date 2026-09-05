# crashloop-operator

![Version: 0.0.0](https://img.shields.io/badge/Version-0.0.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.0.0](https://img.shields.io/badge/AppVersion-0.0.0-informational?style=flat-square)

Kubernetes operator that scales down workloads stuck in CrashLoopBackOff and similar terminal failure states

**Homepage:** <https://github.com/slauger/crashloop-operator>

## Prerequisites

- Kubernetes 1.29 or newer
- Helm 3.8 or newer, for OCI registry support

## Installation

```bash
helm install crashloop-operator \
  oci://ghcr.io/slauger/charts/crashloop-operator \
  --namespace crashloop-system \
  --create-namespace
```

The chart installs the `CrashLoopPolicy` CRD from its `crds/` directory. Helm
does not upgrade or delete CRDs installed that way, so a chart upgrade that
changes the CRD needs it applied manually:

```bash
kubectl apply -f https://raw.githubusercontent.com/slauger/crashloop-operator/main/config/crd/bases/crashloop-operator.lauger.de_crashlooppolicies.yaml
```

No policy is created by the chart. The operator does nothing until you create a
`CrashLoopPolicy`; see the
[project README](https://github.com/slauger/crashloop-operator#quick-start) for
an example.

## Scope

`CrashLoopPolicy` is cluster-scoped, so access to it is always granted through a
ClusterRole. `scope.mode` controls how far the operator's **workload** access
reaches:

- `cluster` (default) grants workload access across all namespaces.
- `namespace` confines workload access to `scope.watchNamespace`, defaulting to
  the release namespace, and passes `--watch-namespace` to the operator.

## Uninstalling

```bash
helm uninstall crashloop-operator --namespace crashloop-system
```

Workloads the operator scaled down stay at zero replicas. Scale them back up
using the recorded previous replica count:

```bash
kubectl scale deployment my-app \
  --replicas="$(kubectl get deployment my-app \
    -o jsonpath='{.metadata.annotations.crashloop-operator\.lauger\.de/previous-replicas}')"
```

The CRD is not removed by `helm uninstall`. Delete it explicitly if you want the
policies gone too.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Simon Lauger | <simon@lauger.de> | <https://lauger.de> |

## Requirements

Kubernetes: `>=1.29.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for the operator pod. |
| automountServiceAccountToken | bool | `true` | Mount the ServiceAccount token. The operator talks to the API server, so it needs this; the value exists for policies that require it to be explicit. |
| extraArgs | list | `[]` | Extra command line arguments for the operator. |
| extraEnv | list | `[]` | Extra environment variables for the operator container. |
| fullnameOverride | string | `""` | Override the full resource name prefix. |
| image.digest | string | `""` | Image digest. Takes precedence over tag when set, for example `sha256:abc...`. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. IfNotPresent is safe now that the default tag is the immutable appVersion rather than a floating latest. |
| image.repository | string | `"ghcr.io/slauger/crashloop-operator"` | Operator image repository. |
| image.tag | string | `""` | Image tag. Defaults to the chart appVersion, which pins each chart release to the operator build it was tested with. |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries. |
| leaderElect | bool | `true` | Enable leader election so that only one replica reconciles at a time. |
| metrics.service.annotations | object | `{}` | Annotations to add to the metrics Service. |
| metrics.service.enabled | bool | `false` | Expose the controller-runtime metrics endpoint through a Service. |
| metrics.service.port | int | `8080` | Service port for metrics. |
| metrics.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor. Requires metrics.service.enabled and the monitoring.coreos.com CRDs. |
| metrics.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| metrics.serviceMonitor.labels | object | `{}` | Extra labels for the ServiceMonitor, for Prometheus selector matching. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| nodeSelector | object | `{}` | Node selector for the operator pod. |
| podAnnotations | object | `{}` | Annotations to add to the operator pod. |
| podDisruptionBudget.enabled | bool | `false` | Create a PodDisruptionBudget. Only meaningful with replicaCount above 1, since leader election already keeps a single replica from being active twice. |
| podDisruptionBudget.maxUnavailable | string | `""` | Maximum unavailable pods. Mutually exclusive with minAvailable. |
| podDisruptionBudget.minAvailable | int | `1` | Minimum available pods. Mutually exclusive with maxUnavailable. |
| podLabels | object | `{}` | Extra labels to add to the operator pod. |
| priorityClassName | string | `""` | PriorityClass for the operator pod. |
| replicaCount | int | `1` | Number of operator replicas. Leader election makes only one of them active. |
| resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Resource requests and limits for the operator container. |
| revisionHistoryLimit | int | `3` | How many old ReplicaSets to retain for the operator Deployment. |
| scope.mode | string | `"cluster"` | Operator scope. `cluster` watches all namespaces via ClusterRole. `namespace` confines workload access to a single namespace via Role. CrashLoopPolicy itself is cluster-scoped either way, so policy access is always granted through a ClusterRole. |
| scope.watchNamespace | string | `""` | Namespace to watch when mode is `namespace`. Defaults to the release namespace when empty. |
| serviceAccount.annotations | object | `{}` | Annotations to add to the ServiceAccount. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount for the operator. |
| serviceAccount.name | string | `""` | Name of the ServiceAccount. Generated from the release name when empty. |
| tolerations | list | `[]` | Tolerations for the operator pod. |
| topologySpreadConstraints | list | `[]` | Topology spread constraints for the operator pod. |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
