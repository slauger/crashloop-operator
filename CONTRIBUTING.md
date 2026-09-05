# Contributing

Thanks for your interest in the project. This page covers what you need to get
a change merged.

## Prerequisites

- Go, at the version in [go.mod](go.mod). The Makefile pulls the tools it needs
  (controller-gen, govulncheck, setup-envtest) through the `tool` directive, so
  there is nothing to install by hand.
- Docker or Podman, for building the image and running the e2e tests.
- Helm 3.8 or newer.
- [golangci-lint](https://golangci-lint.run/welcome/install/), the only tool not
  fetched automatically. CI pins the version in
  [.github/workflows/_go.yaml](.github/workflows/_go.yaml); match it locally to
  avoid surprises.
- kind, only if you want to run the e2e tests.

## Before you push

```bash
make ci
```

That runs everything CI runs: linting, `go vet`, tests under the race detector
with a coverage gate, drift checks, `govulncheck`, chart linting and the chart
unit tests. Run `make help` for the individual targets.

Two of those checks catch mistakes that are easy to make:

- **`make check-manifests`** fails when generated code is stale. Run
  `make generate manifests` after touching anything under `api/v1alpha1/` or
  any `+kubebuilder:` marker, and commit the result.
- **`make check-helm-docs`** and **`make check-rbac`** fail when the chart
  README, `values.schema.json` or the chart RBAC no longer match their sources.
  Run `make helm-docs helm-schema` after editing `values.yaml`, and
  `make manifests` after changing an RBAC marker.

## Tests

Unit and integration tests run with `make test`.

- Controller tests use the fake client and live in `internal/controller/`. Build
  fixtures with the helpers in `testutil_test.go` rather than hand-rolling
  objects.
- API tests run against a real API server through envtest and live in
  `api/v1alpha1/`. Anything that depends on defaulting, schema validation or the
  status subresource belongs there, because the fake client applies none of it.
- Chart behaviour is covered by helm-unittest under
  `charts/crashloop-operator/tests/`.

The end-to-end tests use [Chainsaw](https://kyverno.github.io/chainsaw/) against
a kind cluster:

```bash
make e2e-cluster   # create the cluster
make e2e           # build, load, install and run the tests
make e2e-clean     # tear the cluster down
```

## Branching

`develop` is the development branch and the target for all pull requests.
`main` is release-only and is updated by the maintainer.

Branch from `origin/develop` and name the branch after what it does, for
example `feat/pending-pod-detection` or `fix/status-churn`.

## Commits and pull requests

Commits follow [Conventional Commits](https://www.conventionalcommits.org/),
because releases and their notes are generated from the history. Use a
lowercase subject with no trailing period:

```
feat(controller): detect permanently pending pods
fix(api): reject a negative restart threshold
docs: document the multi-policy ordering
```

While the project is pre-1.0, a breaking change releases as a **minor** version,
so mark it with `!` or a `BREAKING CHANGE:` footer and say in the body what
users have to change.

Give the pull request a conventional title, a `## Summary` and a `## Test plan`
section, and a `Closes #<issue>` line where one applies.

Write commit messages, code comments and documentation in plain ASCII. CI
rejects typographic quotes, en and em dashes and similar characters.

## Code conventions

- Use `updateStatusWithRetry` or `updatePolicyStatusIfChanged` for status
  writes. The latter skips the write when nothing changed, which keeps the
  operator from churning the object on every interval.
- Read defaulted spec fields through the `effective*` accessors in
  `internal/controller/policyresolve.go` instead of repeating the fallback.
- Errors inside the reconcile loop should be counted rather than swallowed, so
  the `Ready` condition can report a partial failure.

## Documentation

Documentation is part of the change, not a follow-up:

| You changed | Also update |
|---|---|
| A CRD field | The spec or status table in [README.md](README.md) and the sample in `config/samples/` |
| A `+kubebuilder:default` | The table, and any prose that states the default |
| A chart value | The comment in `values.yaml`, then run `make helm-docs helm-schema` |
| An RBAC marker | Run `make manifests`; the chart templates may need the matching rule |
| Operator behaviour | The relevant README section, and `NOTES.txt` if it affects first use |
