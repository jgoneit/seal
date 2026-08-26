---
name: seal
description: Use Seal for evidence-backed Acceptance of a concrete repository change. Use when the user selects @Seal or $seal, and consider it automatically before implementation only for a clean, opted-in Git repository whose selected checks pass the Agent-safe preflight. Do not auto-activate for planning, explanation, read-only review, non-Git work, dirty worktrees, or unconfigured repositories.
metadata:
  short-description: Record evidence-backed completion
---

# Seal

Seal is an Acceptance tool. The Native Agent owns planning, implementation,
tool choice, tests, and iteration. Treat the documented `seal` CLI as the only
authority for Task, Evidence, Run, and Completion results.

## Activation

Explicit `@Seal` or `$seal` selection loads the Skill. A discussion, plan, or
audit remains read-only and creates no Seal lifecycle state. For an exact
read-only query, use only Task and Run identities supplied by the user or
retained in this task; never infer a latest identity. A missing catalog does
not block an exact `task show` or `run show` query.

Implicit use requires all of the following:

- The user requested a concrete repository change.
- The Git root already contains `.seal/checks.json`.
- This request has not changed the repository yet.
- `git status --porcelain=v1 --untracked-files=all --ignore-submodules=none`, run
  at that root, succeeds with completely empty output.
- The selected checks pass the Agent-safe catalog preflight below.

If the status output is nonempty, say `Seal: skipped — worktree is not clean.`
and continue the requested work without asking about Seal. This skip does not
override ordinary dirty-worktree conflict handling for the requested change.
For another implicit failure, use `Seal: skipped — <reason>.` and continue.
Never create the catalog, clean, reset, stash, or commit merely to activate
Seal, and never activate it retroactively after implementation began.

For explicit change work, a dirty worktree is allowed only when all existing
product changes clearly belong to the requested outcome and Scope. Disclose
that attribution before Task creation. If it is unclear, ask before creating a
Task and do not alter the existing work.

## Preflight

Before any Seal command:

1. Resolve the exact Git root and use it as the working directory for every
   repository-bound `task create`, `task show`, `verify`, `run show`, and
   `complete` invocation.
2. Prefer `seal` on `PATH`; otherwise accept the documented installer path
   (`$HOME/.local/bin/seal` on Linux or macOS, or
   `%LOCALAPPDATA%\Programs\Seal\bin\seal.exe` on Windows) only when it is an
   executable regular file. Reuse that exact executable.
3. Run `--version` and compare it with the Plugin version in
   `.codex-plugin/plugin.json`, removing only a `+codex.*` suffix.

The Plugin does not install Core. On an implicit CLI or version failure, skip
Seal and continue. On explicit use, report the prerequisite failure before
continuing without Seal. If the user required Seal as a condition of the work,
stop instead.

For a new Task, read the catalog, HEAD, and worktree status. Select the smallest
relevant established checks without duplicate selections. A missing catalog,
malformed or unresolved selected definition, or an effective timeout above
Core's 300-second maximum blocks Task creation. The effective timeout is the
declared positive integer or 300 seconds when absent. Report the exact hard
block; implicit use skips without asking, while explicit use stops before an
unusable Task. Create a missing catalog only for an explicit Seal setup request,
using checks already established by the repository.

For implicit use, further require every effective timeout to be at most 120
seconds and their sum to be at most 300 seconds. Selected checks must be
noninteractive and must not intentionally change tracked or nonignored product
Source. Ignored cache, build, and temporary output is allowed. Treat an argv as
unsafe when it clearly starts an interactive, watch, server, REPL, or pager
process; asks a shell or evaluator to interpret a command string; or performs a
formatter write/fix, code generation, snapshot update, migration, dependency
update, or configuration update. Do not broaden these examples into guesses
about an otherwise ordinary check. Never rewrite an argv or timeout to pass
preflight. These additional heuristics do not gate explicit use; ordinary
authorization and Scope still apply.

## Basic Task

Before implementation, construct one Task from
[`contracts/task.schema.json`](../../contracts/task.schema.json) and the
catalog. Use a unique ID, intended repository-relative Scope, the selected
checks, Agent-judged proportional risk, and `verifier.required=false`. Risk is
descriptive metadata, not an approval or Core gate. Do not add
`preferred_runner`.

Keep the input outside the repository and run
`seal task create --file <TASK_JSON>`. Never use `--force` without an explicit
request to replace that exact Task. Start implementation only after successful
Task creation with usable output. On exit `2` or `3`, report the Task as not
created. On exit `1` or unusable success output, report it as indeterminate
because Core may already have published the Task; do not retry, infer its
identity, or start implementation.

New Agent Tasks are Basic-only. If `verifier.required=true` is requested, do
not run repository or Core preflight, create or silently downgrade the Task,
or use the activated-workflow report block. Stop with exactly this response:

```text
Seal Go v1 Basic Profile cannot complete verifier.required=true. No Task or Evidence was created.
```

Keep this identity capsule only in the current task and reuse every value
verbatim:

```text
repo_root: <resolved Git root>
task_id: <created Task ID>
run_id: <successful verify result; absent until then>
```

Do not persist Plugin state or use the returned Evidence path as a lifecycle
input.

## Acceptance workflow

At a completion candidate, run `seal verify <TASK_ID>` exactly once. Exit `0`
means Evidence was recorded, not that it passed. Capture the returned exact Run
ID. On nonzero exit or unusable output, report Evidence as indeterminate and do
not call `run show` or `complete`; never infer an ID or inspect raw Evidence.

When every selected check is required, minimize the happy path:

1. Run `seal complete <TASK_ID> --run-id <RUN_ID>` directly after successful
   `verify`.
2. On exit `0`, report accepted Basic Completion and do not call `run show`.
3. On exit `4`, `5`, `6`, or `9`, call exact `run show` once for diagnostics.
4. On any other nonzero exit, do not call `run show`. Treat exit `1` as
   indeterminate because a Completion record may already have been published.
   Treat exit `7` as a policy rejection and exits `2`, `3`, and `8` as
   fail-closed operational failures. Only exit `0` is accepted Completion.

When any selected check is optional, run exact `run show` once after successful
`verify` so its result can be reported. A run-show error stops before
Completion. If the validated summary has `mechanical_result=fail`, preserve the
Run and do not call `complete`. If it passes, report every optional outcome and
call exact `complete`; an optional failure is non-blocking under Basic policy.
Only complete exit `0` is accepted. Exit `1` is indeterminate; exits `4`, `5`,
`6`, `7`, and `9` are policy rejections; and exits `2`, `3`, and `8` are
fail-closed operational failures. Do not repeat `run show` in this branch.

A failed Run remains Evidence. The Native Agent may continue the authorized
implementation and verify a later completion candidate only after a relevant
change, preserving every prior Run and using each returned exact Run ID. A
material change to objective, type, Scope, checks, risk, or verifier requires a
new Task ID. Never automatically retry, repair, replace Evidence, read raw
Evidence for routing, or fall back to a latest identity.

## Reporting

For an activated workflow, report these labels in order:

```text
Task: created <TASK_ID> | not created | indeterminate
Evidence: recorded <RUN_ID> | indeterminate | not recorded
Mechanical: pass | fail | unavailable
Optional checks: none selected | <name>: pass/fail (non-blocking)
Completion: accepted | rejected (exit N: policy reason) | failed (exit N: operational reason) | not attempted | indeterminate
Not proven: <relevant limitation, when needed>
```

When required-only Completion exits `0`, `Mechanical: pass (validated during
Completion)` is sufficient. Keep Task creation, Evidence recording, mechanical
state, optional outcomes, and Completion distinct; do not imply all checks
passed when an optional check failed.

## Boundary

Do not reimplement Core decisions or add approval stages, Plan hashes,
Reviewer, Verdict, Bundle, automatic repair, lifecycle state, hooks, or calls
to other Toolkit modules. Do not create `.harness` state. Seal does not expand
permission to commit, push, publish, or modify unrelated files.
