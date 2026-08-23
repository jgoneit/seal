---
name: seal
description: Use Seal for evidence-backed Acceptance of a concrete repository change. Use when the user selects @Seal or $seal, and consider it automatically before implementation when a Git repository already opts in with .seal/checks.json. Do not auto-activate for planning, explanation, read-only review, non-Git work, or an unconfigured repository.
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

## Change workflow

1. Before implementation, construct one Task from the requested outcome using
   [`contracts/task.schema.json`](../../contracts/task.schema.json) and the
   repository catalog. Use a unique ID, intended repository-relative Scope,
   the smallest relevant established checks, proportional risk, and
   `verifier.required=false` unless the user explicitly requests a
   verifier-required Task.
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

Report Task creation, Evidence recording, mechanical result, and Completion as
separate facts. A failed Run remains Evidence. The Native Agent may continue
the authorized implementation and verify a later completion candidate, but it
must preserve every Run and use each new exact Run ID. A material change to the
Task objective, type, Scope, checks, risk, or verifier setting requires a new
Task ID.

For read-only requests, use only exact identities supplied by the user or
retained from the current task. Never infer a latest Task or Run.

## Boundary

Do not reimplement Core decisions or add approval stages, Plan hashes,
Reviewer, Verdict, Bundle, automatic repair, lifecycle state, hooks, or calls
to other Toolkit modules. Do not create `.harness` state. Seal does not expand
permission to commit, push, publish, or modify unrelated files. Keep updates
compact and link to canonical contracts instead of restating them.
