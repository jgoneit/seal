# Seal

Seal is an evidence-backed completion tool for coding agents.

Its one-sentence responsibility in the Acceptance plane is: determine whether a
specific Task and Run have intact Evidence, still match the relevant Source, and
satisfy the declared completion conditions.

## Place in the Jgoneit Agent Toolkit

Seal is an independent Acceptance module. It can be installed, used, versioned,
and removed without a Toolkit manager or another module.

The architecture keeps execution outside Seal:

- The Native Agent owns planning, implementation, tool choice, and execution.
- The user, Native Agent, or CI owns composition between independent tools.
- Seal exposes state, not transitions.
- Seal never invokes another Toolkit module or chooses what runs next.

## Migration status

This repository is an **experimental Go successor candidate**. The canonical
behavioral reference remains [Seal Legacy](https://github.com/jgoneit/seal-legacy),
the preserved Python implementation.

The canonical implementation has not switched to Go. This repository does not
translate the Python package structure. Its current compatibility surface is
limited to read-only queries for an exact stored Task or Evidence Run under
`.seal`. The Python reference remains canonical, and no writer has been
introduced.

## Current CLI

Four read-only operations are available:

```text
seal --help
seal --version
seal task show <TASK_ID>
seal run show <TASK_ID> --run-id <RUN_ID>
```

The development version is `0.0.0-dev`.

`task show` reproduces the frozen reference's lookup behavior: it requires an
exact path-safe Task ID and returns a syntactically valid stored JSON object
without assigning Schema validity or Acceptance meaning to its fields.

`run show` requires exact Task and Run IDs, validates the stored Evidence v2
documents and raw-byte Run Manifest through one canonical read boundary, and
then returns the transient `validated-run-summary/v1` view. A structurally
valid failed Run still returns exit 0; missing, unsafe, unsupported, tampered,
or contradictory Evidence returns exit 8.

This slice does not create Tasks, run checks, verify work, collect current
source, read Verdict or Completion state, complete Tasks, infer a latest
identity, retry, repair, or write persisted Evidence. It reads `.seal` only and
has no `.harness` fallback.

To exercise the candidate from a Go checkout:

```bash
go run ./cmd/seal --help
go run ./cmd/seal --version
go run ./cmd/seal task show <TASK_ID>
go run ./cmd/seal run show <TASK_ID> --run-id <RUN_ID>
```

There is no release asset or installer yet. The long-term distribution target
is a standalone `seal` binary that does not require users to install Python or
the Go toolchain.

## Conformance-first development

Future functionality is added only as a narrow vertical slice justified by a
behavioral contract from the Python reference. The goal is reproduction of
established Acceptance meaning and outcomes, not new product design.

The first compatibility slice covers exact read-only `task show` and `run show`
identities using fixtures derived from the frozen reference. See
[the read-only contract](conformance/read-only-contract.md) and
[fixture provenance](conformance/README.md). It does not add Evidence writes,
retries, repair, latest-Run inference, reviewer execution, or workflow
orchestration.

See [MIGRATION_CHARTER.md](MIGRATION_CHARTER.md) for the migration invariants and
[REFERENCE.md](REFERENCE.md) for the frozen reference identity.

## Not part of Seal

Seal does not own agent execution, planning, reviewer selection, knowledge
authoring, security enforcement, deployment, CI orchestration, or a common
Toolkit runtime. It does not impose a model reasoning format, SubAgent count,
or Agent Team topology.
