---
description: Pick the next open GitHub issue and implement it end-to-end (branch, code, tests, PR, CI, merge)
---

# Autodev

Work through the project backlog autonomously. Handle exactly one issue per
invocation. For continuous mode run `/loop /autodev`.

## Select the next issue

1. List open issues: `gh issue list --state open --json number,title,labels --limit 100`.
2. Skip the Renovate "Dependency Dashboard" issue and anything labeled
   `blocked`, `question` or `discussion`.
3. Skip issues that already have an open PR working on them: check open PR
   bodies for `Closes #<number>` / `Fixes #<number>` references.
4. Read the roadmap tracking issue #34 and pick the first unchecked item
   whose linked issue is open and not skipped above.
5. Otherwise pick by priority: label `bug` first, then `ci`, then
   `enhancement`, then lowest issue number.
6. If no eligible issue remains, report that the backlog is done and stop.

## Implement

1. Read the full issue including comments: `gh issue view <n> --comments`.
   Respect design decisions recorded there. Known project decisions: no
   automatic scale-up recovery (GitOps concern), no notification integrations,
   the most restrictive policy wins when multiple policies match, metrics are
   low priority.
2. `git fetch origin` and branch from `origin/develop` using `feat/<topic>`
   or `fix/<topic>` naming per AGENT.md.
3. Implement the change including tests. Follow AGENT.md: builder-pattern
   test helpers from `testutil_test.go`, `updateStatusWithRetry` for status
   writes, plain ASCII in all files and commit messages.
4. After touching `api/v1alpha1/*_types.go` run `make generate manifests`.
5. Update documentation in the same change: README tables for new or changed
   CRD fields, chart values comments for new Helm values.
6. Run `make ci` and fix everything it reports before committing.

## Ship

1. Commit with a Conventional Commit message, GPG-signed and signed off:
   `git commit -S --signoff -m "<type>: <subject>"`.
2. Push the branch and open a PR against develop with a `## Summary` and
   `## Test plan` section and a `Closes #<n>` line:
   `gh pr create --base develop`.
3. Wait for CI: `gh pr checks --watch`. If checks fail, fix the problem,
   push, and wait again.
4. When all checks pass, merge with a merge commit and delete the branch:
   `gh pr merge --merge --delete-branch`.
5. Tick the matching checklist entry in the roadmap issue #34 via
   `gh issue edit`.

## When blocked

If the issue needs a human decision, external credentials, or repository
settings you cannot change: leave a comment on the issue describing exactly
what is missing, add the `blocked` label, and stop without picking another
issue.

## Guardrails

- One issue per run.
- Never force-push, never push to main directly, never create tags or
  releases, never merge the develop-to-main release PR.
- Never commit secrets or tokens.
- Keep commit messages and code free of any AI or assistant references.
