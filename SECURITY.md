# Security Policy

## Supported Versions

| Version        | Supported          |
|----------------|--------------------|
| Latest release | :white_check_mark: |
| Older releases | :x:                |

The project is pre-1.0 and under active development. Security fixes go into the
next release rather than being backported.

## Reporting a Vulnerability

Please report vulnerabilities privately:

1. **Do not** open a public GitHub issue.
2. Email [simon@lauger.de](mailto:simon@lauger.de) describing the issue.
3. Include reproduction steps and the affected version if you can.

You should get a response within 7 days. Once confirmed, a fix is developed and
released as soon as practical.

## Security Considerations

This operator handles no secrets, certificates or credentials. Its security
relevance is different: **it can take workloads down.**

### Blast radius

The operator sets `replicas` to `0` on Deployments and StatefulSets and
suspends CronJobs. Anyone who can create or edit a `CrashLoopPolicy` can
therefore cause an outage, whether by mistake or on purpose.

`CrashLoopPolicy` is deliberately **cluster-scoped** so that creating one is an
administrative act. Treat write access to it the way you would treat any
cluster-wide permission, and do not grant it to workload teams by default.

Three mechanisms limit the damage a policy can do:

- `dryRun: true` logs and emits events for the workloads a policy would act on
  without touching them. Use it whenever a policy is new or has been changed.
- `excludeNamespaces` defaults to `kube-system`, `kube-public` and
  `kube-node-lease`, so a default policy will not disable cluster components.
  Setting the field replaces that default rather than adding to it.
- `namespaceSelector` and `excludeWorkloadSelector` narrow which namespaces and
  workloads a policy considers at all.

The operator never scales anything back up. Recovery is deliberately left to
whatever manages your workloads, so a scale-down cannot be silently undone.

### RBAC

Permissions follow least privilege and are generated from the
`+kubebuilder:rbac` markers in the controller, with a CI check
(`make check-rbac`) that fails if the chart grants more or less than the
markers declare.

The chart splits access in two. Policy, namespace and event access is always a
ClusterRole, because those are cluster-scoped resources. Workload access
follows `scope.mode`: `cluster` grants it everywhere, `namespace` confines it to
a single namespace via a Role, which is the tightest deployment the operator
supports.

### Container hardening

The operator pod runs with:

- `runAsNonRoot: true`, UID 1001
- `readOnlyRootFilesystem: true`
- `allowPrivilegeEscalation: false`
- all capabilities dropped
- `seccompProfile: RuntimeDefault`

### Metrics endpoint

The controller-runtime metrics endpoint listens on port 8080 **without
authentication**. The chart does not expose it: `metrics.service.enabled`
defaults to `false`, so it is reachable only from inside the pod network to
whatever can already reach the pod. If you enable the Service, restrict access
accordingly. The metrics contain no secrets, but they do reveal namespace and
workload names.

### Supply chain

Released images and Helm charts are signed with cosign using keyless Sigstore
signing, carry SLSA provenance and an SBOM, and are validated against a
[Conforma](https://conforma.dev/) policy at release time. Verification commands
are in the [README](README.md#supply-chain-security).
