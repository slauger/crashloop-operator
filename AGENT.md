# Agent Instructions

[CONTRIBUTING.md](CONTRIBUTING.md) is the source of truth for prerequisites,
the branching model, commit conventions, the test layout and the documentation
mapping table. Read it first. This file covers only what an agent needs beyond
it, so the two cannot drift apart.

## Before you finish

```bash
make ci
```

Everything CI runs. It is not optional: several checks exist precisely because
generated files silently fall out of step, and they fail the build rather than
warn.

- `make generate manifests` after touching `api/v1alpha1/*_types.go` or any
  `+kubebuilder:` marker, including the RBAC markers.
- `make helm-docs helm-schema` after editing `charts/crashloop-operator/values.yaml`.
- `go mod tidy` after adding an import. `make check-tidy` covers it, but only
  since it was added; older habits assumed CI alone would catch it.

## Release model

`develop` is the only branch you work on. **Do not create or push to `main`,
do not merge into it, and do not cut releases** - the maintainer owns that path
deliberately. The auto-PR workflow targets `main` and will fail while the branch
does not exist; that is expected and not a defect to fix.

While the project is pre-1.0, `.releaserc.json` releases a breaking change as a
**minor** version, not a major. Mark breaking changes with `!` or a
`BREAKING CHANGE:` footer anyway, and say in the body what users must change.

## Documentation scope

There is deliberately **no mkdocs site**. The project has a single CRD, so
documentation lives in `README.md`, `CONTRIBUTING.md`, `SECURITY.md` and the
generated chart README. Do not add a `docs/` tree.

Documentation is part of the change, not a follow-up. The mapping table in
CONTRIBUTING.md says which document each kind of change touches.

## Project structure

```
api/v1alpha1/              CRD types, plus the envtest suite that runs them
                           against a real API server
internal/controller/       Reconciler, helpers, policy resolution, tests
config/crd/bases/          Generated CRD YAML
config/rbac/               Generated RBAC, compared against the chart by
                           hack/check-rbac.sh
config/samples/            Hand-written examples, validated by envtest
charts/crashloop-operator/ Helm chart; CRDs copied from config/crd/bases/
tests/e2e/                 Chainsaw tests, run on kind in CI
hack/                      Coverage and RBAC check scripts
images/                    Containerfiles
cmd/                       Entrypoint
```

## Code conventions

- Read defaulted spec fields through the `effective*` accessors in
  `internal/controller/policyresolve.go`. Repeating a fallback inline creates a
  second source of truth next to the kubebuilder default.
- Use `updatePolicyStatusIfChanged` for status writes. `updateStatusWithRetry`
  still exists for the initial phase write, but the former skips the write when
  nothing changed, which is what keeps the operator from rewriting its own
  object every interval.
- Count errors inside the reconcile loop rather than swallowing them, so the
  `Ready` condition can report `ReconcilePartiallyFailed`. A loop that logs and
  continues without counting makes a broken policy look healthy.
- Controller tests use the builder pattern in `testutil_test.go`. Anything that
  depends on defaulting, schema validation or the status subresource belongs in
  the envtest suite instead, because the fake client applies none of it.

## Text quality

Plain ASCII everywhere: code, comments, documentation and commit messages. No
em-dashes, smart quotes, or other non-ASCII punctuation. CI rejects zero-width
characters, soft hyphens, word joiners and similar invisible characters, and
`asciicheck`/`bidichk` enforce the same in Go sources.

## Linting

`.golangci.yml` is a byte-identical copy of `golang/examples/.golangci.yml` from
[slauger/coding-standards](https://github.com/slauger/coding-standards). Do not
hand-edit it; update it by copying the standard again, so the next update stays
a copy rather than a merge.

`modernize` rewrites real code, not just formatting. After
`golangci-lint run --fix`, run the full build and test suite before committing.

## No CHANGELOG

Release notes are generated from commit messages by semantic-release.
