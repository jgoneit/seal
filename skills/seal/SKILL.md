---
name: seal
description: Use Seal for evidence-backed Acceptance of a concrete repository change. Use when the user selects @Seal or $seal, and consider it automatically before implementation only when an opted-in Git repository's selected checks pass the Agent-safe preflight. Do not auto-activate for planning, explanation, read-only review, non-Git work, or an unconfigured repository.
metadata:
  short-description: Record evidence-backed completion
---

# Seal

Seal is an Acceptance tool. The Native Agent owns planning, implementation,
tool choice, tests, and iteration. Treat the documented `seal` CLI as the only
authority for Task, Evidence, Run, and Completion results.

## Activation

Implicit use requires all of the following:

- The user requested a concrete repository change.
- The Git repository already contains `.seal/checks.json` at its root.
- The current request has not changed the repository yet.
- Existing unrelated changes will not be attributed to this Task.
- The selected checks pass the Agent-safe preflight below.

Otherwise continue the requested work without Seal. Never create the catalog
merely to activate the Skill.

Explicit `@Seal` or `$seal` selection loads the Skill, but a discussion, plan,
audit, or read-only query remains read-only. Create a missing catalog only when
the user requested Seal setup, using checks already established by the
repository rather than inventing policy.

## Preflight

Before any Seal command:

1. Resolve the exact Git repository root. Use that root as the working
   directory for every repository-bound `task create`, `task show`, `verify`,
   `run show`, and `complete` invocation.
2. Prefer `seal` on `PATH`; otherwise accept the documented installer path
   (`$HOME/.local/bin/seal` on Linux or macOS, or
   `%LOCALAPPDATA%\Programs\Seal\bin\seal.exe` on Windows) only when it is an
   executable regular file. Reuse that exact executable.
3. Run it with `--version`. Compare the output with the Plugin version in
   `.codex-plugin/plugin.json`, removing only a `+codex.*` build suffix.

The Plugin does not install the CLI. On implicit CLI or version failure, say
briefly that Seal was skipped and continue the requested work. On explicit use,
report the prerequisite failure before proceeding without Seal.

For a new Task, also read `.seal/checks.json`, `HEAD`, and worktree status.
Dirty worktrees are supported, but create the Task only when all existing
product changes belong to its requested outcome and Scope. If attribution is
unclear, skip implicit use or ask before explicit Task creation; never clean,
reset, stash, or commit existing work merely to activate Seal. A missing
catalog blocks Task creation but not an exact `task show` or `run show` query.

### Agent-safe catalog preflight

Inspect only the checks selected for this Task. Resolve every selected catalog
name to its definition; count optional checks and duplicate selections because
Core executes them all. The effective timeout is the declared positive integer
or Core's 300-second default when it is absent. A selected effective timeout
above Core's 300-second execution maximum is unsafe for every Agent-facing Task.
For implicit use, further require every effective timeout to be at most 120
seconds and their exact sum to be at most 300 seconds. A malformed or unresolved
selected definition is unsafe.

Checks used through the Agent-facing workflow must be noninteractive and must
not intentionally change tracked or nonignored product Source. Ignored cache,
build, and temporary output is allowed. Treat an argv as unsafe when it clearly
starts an interactive, watch, server, REPL, or pager process; asks a shell or
evaluator to interpret a command string; or performs a formatter write/fix,
code generation, snapshot update, migration, dependency update, or
configuration update. Do not extend these examples into guesses about an
otherwise ordinary check; rely on Core's execution bounds when no unsafe intent
is clear.

Never rewrite a catalog definition, argv, or timeout to make it pass. For an
unsafe implicit candidate, give a one-line reason that Seal was skipped and
continue the requested Native Agent work without asking. For explicit use,
stop before Task creation, report the exact unsafe or unresolved condition, and
wait for the user. Seal's S0/S1 comparison detects Source mutation only after a
check ran; it is not prevention or rollback.

## Change workflow

1. Before implementation, construct one Task from the requested outcome using
   [`contracts/task.schema.json`](../../contracts/task.schema.json) and the
   repository catalog. Use a unique ID, intended repository-relative Scope,
   the smallest relevant established checks, Agent-judged proportional risk,
   and `verifier.required=false`. Risk is required descriptive metadata, not a
   user approval or a Core gate. Do not add `preferred_runner` to an
   Agent-constructed Task.
2. Keep the temporary Task input outside the repository and run
   `seal task create --file <TASK_JSON>`. Do not use `--force` without an
   explicit request to replace that exact Task. Retain its ID and baseline.
3. Implement normally. No additional Seal-specific approval is required for
   the already requested change.
4. At a completion candidate, run `seal verify <TASK_ID>`, retain its returned
   Run ID, then run `seal run show <TASK_ID> --run-id <RUN_ID>` for that exact
   Run.
5. Verify and run-show exit `0` mean valid Evidence was recorded or read, not
   that it passed. Inspect `mechanical_result`, `scope_pass`,
   `required_checks_pass`, and `source_stable_during_checks`.
6. For a mechanically passing Basic Task, run
   `seal complete <TASK_ID> --run-id <RUN_ID>`. Only complete exit `0` is an
   accepted Basic Completion.

`verifier.required=true` is allowed through this Skill only when the user
explicitly requests Evidence-only recording and explicitly accepts that the Go
v1 Basic Profile cannot complete it and that `complete` must not run. Otherwise
stop before Task creation, do not silently downgrade the Task to
`required=false`, and state exactly:

```text
Seal Go v1 Basic Profile cannot complete verifier.required=true. No Task or Evidence was created.
```

For an accepted Evidence-only request, create the Task, run `verify`, and run
exact `run show` for the returned Run ID. Never call `complete`, invoke or imply
a Reviewer, or claim Acceptance Completion. End the result with this literal
line:

```text
Completion: unsupported and not attempted
```

Report Task creation, Evidence recording, mechanical result, and Completion as
separate facts. A failed Run remains Evidence. The Native Agent may continue
the authorized implementation and verify a later completion candidate, but it
must preserve every Run and use each new exact Run ID. A material change to the
Task objective, type, Scope, checks, or verifier setting requires a new Task
ID. A material risk metadata change also requires a new Task ID so the immutable
snapshot remains accurate, but risk stays Agent-owned descriptive context and
does not trigger user approval or a Core decision.

For read-only requests, use only exact identities supplied by the user or
retained from the current task. Never infer a latest Task or Run.

## Boundary

Do not reimplement Core decisions or add approval stages, Plan hashes,
Reviewer, Verdict, Bundle, automatic repair, lifecycle state, hooks, or calls
to other Toolkit modules. Do not create `.harness` state. Seal does not expand
permission to commit, push, publish, or modify unrelated files. Keep updates
compact and link to canonical contracts instead of restating them.
