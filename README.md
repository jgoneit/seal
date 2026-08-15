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
translate the Python package structure and does not claim compatibility with
existing persisted Evidence. Compatibility decisions will be made through
explicit conformance work before any writer is implemented.

## Current CLI

Only two informational operations are available:

```text
seal --help
seal --version
```

The development version is `0.0.0-dev`. Task, Run, Evidence, Verdict, and
completion operations are not implemented in this bootstrap.

To exercise the candidate from a Go checkout:

```bash
go run ./cmd/seal --help
go run ./cmd/seal --version
```

There is no release asset or installer yet. The long-term distribution target
is a standalone `seal` binary that does not require users to install Python or
the Go toolchain.

## Conformance-first development

Future functionality is added only as a narrow vertical slice justified by a
behavioral contract from the Python reference. The goal is reproduction of
established Acceptance meaning and outcomes, not new product design.

The next recommended slice is read-only compatibility for exact `task show`
and `run show` identities using reference fixtures. It must not add Evidence
writes, retries, repair, latest-Run inference, reviewer execution, or workflow
orchestration.

See [MIGRATION_CHARTER.md](MIGRATION_CHARTER.md) for the migration invariants and
[REFERENCE.md](REFERENCE.md) for the frozen reference identity.

## Not part of Seal

Seal does not own agent execution, planning, reviewer selection, knowledge
authoring, security enforcement, deployment, CI orchestration, or a common
Toolkit runtime. It does not impose a model reasoning format, SubAgent count,
or Agent Team topology.
