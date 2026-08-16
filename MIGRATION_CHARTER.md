# Go Seal Migration Charter

This charter constrains the transition from the preserved Python Seal to its Go
successor.

## Invariants

- Python Seal is the behavioral reference.
- Go Seal reproduces contracts and outcomes, not Python structure.
- No new product feature is added during migration.
- Every behavior change must be justified by conformance.
- Seal owns Acceptance only.
- Seal never invokes another Toolkit module.
- Seal does not select or execute a reviewer.
- Seal contains no Knowledge- or Security-plane functionality.
- Seal does not retry, repair, or infer a latest Task or Run.
- Seal does not create or depend on a common Toolkit runtime.
- Native Agents retain ownership of planning, implementation, and execution.
- Composition remains the responsibility of the user, Native Agent, or CI.

## Compatibility method

Migration work proceeds as small vertical slices:

1. Identify one externally observable Python reference contract.
2. Capture deterministic conformance scenarios without copying Python
   implementation structure.
3. Implement the narrow Go behavior needed for those scenarios.
4. Compare output, exit codes, artifacts, integrity rules, and failure cases.
5. Keep the Python implementation canonical until the transition criteria are
   separately reviewed and approved.

Python remains authoritative for Acceptance semantics. An explicitly approved
non-semantic resource-limit or invocation-environment divergence may be
retained only when its narrow scope is recorded in the conformance contract and
protected by a regression test. Such an exception must not change Task or Run
identity, mechanical state, Scope, required checks, source stability, Evidence
or manifest integrity, or Completion meaning.

The Task snapshot destination decision below is the narrow persisted-root
decision for the first Go writer. It does not revise or generalize the frozen
Evidence compatibility contract. The bootstrap created no runtime data and
made no automatic migration claim.

The first implemented slice is exact-identity, read-only `.seal` Task and Run
query compatibility; it introduces no writer or lifecycle transition.

The second implemented slice adds only the Task snapshot writer. For that
writer destination alone, Go rejects symlinked, non-directory, traversing, or
repository-escaping paths and publishes complete snapshots from the destination
directory with atomic no-clobber and replacement operations on the supported
local filesystems. This approved confinement does not change existing `task
show`, `run show`, or Evidence symlink semantics, and it is not a blanket
symlink policy for other `.seal` paths.

## Explicit exclusions

The migration does not bring over Python packaging, private helpers, Codex
Plugin or Skill behavior, UI flows, provider integrations, workflow engines,
automatic repair, or historical phase machinery. It does not add Toolkit
management responsibilities to Seal.
