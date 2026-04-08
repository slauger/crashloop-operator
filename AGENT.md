# Agent Instructions

## Git Workflow

- **Development branch:** `develop`
- **Release branch:** `main` (only receives merges from `develop` via auto-PR)
- **Always branch from `origin/develop`**, never from `main`
- **PRs target `develop`** (`--base develop`)
- Branch naming: `feat/<topic>` or `fix/<topic>`

```bash
git fetch origin develop
git checkout -b feat/my-feature origin/develop
# ... work ...
gh pr create --base develop
```

## Merge Strategy

- **Feature/fix PRs into `develop`:** Use standard merge by default. Use squash only when the branch has many noisy commits that logically belong together (e.g. lots of trial-and-error or CI fix-ups).
- **`develop` into `main`:** Always rebase, so that `develop` and `main` stay identical after merge.

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/). Semantic-release on `main` uses these to determine version bumps.

```
feat: add CrashLoopPolicy CRD and controller
fix: handle nil ownerReferences in pod watcher
docs: update README for installation
ci: add container build workflow
chore: update dependencies
refactor: simplify owner resolution logic
test: add unit tests for pod failure detection
```

- Lowercase subject, no trailing period
- `feat:` triggers a minor version bump
- `fix:` triggers a patch version bump
- Append `BREAKING CHANGE:` in the body for major bumps

## Pull Requests

- Title follows conventional commit format
- Keep the title short (under 72 chars)
- Body should include a `## Summary` with bullet points and a `## Test plan`
- Reference related issues with `Closes #<number>`
- PRs always target `develop`, not `main`

## Build & Test

```bash
make generate manifests   # regenerate deepcopy + CRD YAML + Helm CRDs
go build ./...
go vet ./...
go test ./internal/controller/ -v
make ci                   # full CI: lint + test + helm-lint + check-manifests
```

Always run `make generate manifests` after modifying `api/v1alpha1/*_types.go`.
Always run `make check-manifests` before committing to verify generated files are up to date.

## Project Structure

```
api/v1alpha1/              CRD type definitions (Go structs)
internal/controller/       Reconciler, helpers, tests
config/crd/bases/          Generated CRD YAML
charts/crashloop-operator/ Helm chart (CRDs copied from config/crd/bases/)
images/                    Containerfiles
cmd/                       Entrypoint
```

## Code Conventions

- Controller tests use the builder pattern from `testutil_test.go` (`newCrashLoopPolicy()` with option funcs)
- Use `updateStatusWithRetry` for all status updates
- Follow existing patterns for reconciler structure

## Text Quality

- **No Unicode em-dashes, smart quotes, or other non-ASCII punctuation.** Use plain ASCII (`-`, `--`, `'`, `"`).
- CI runs a unicode-lint check that rejects zero-width characters, soft hyphens, word joiners, and similar invisible characters.
- When in doubt, stick to plain ASCII in all files (code, docs, comments, commit messages).

## Documentation

- No CHANGELOG -- release notes are auto-generated from commit messages by semantic-release
