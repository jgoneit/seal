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
resource-limit divergence may be retained only when its narrow scope is
recorded in the conformance contract and protected by a regression test.

Persisted root and Evidence compatibility must be decided before the first Go
writer is introduced. This bootstrap creates no runtime data and makes no
automatic migration claim.

The first implemented slice is exact-identity, read-only `.seal` Task and Run
query compatibility; it introduces no writer or lifecycle transition.

## Explicit exclusions

The migration does not bring over Python packaging, private helpers, Codex
Plugin or Skill behavior, UI flows, provider integrations, workflow engines,
automatic repair, or historical phase machinery. It does not add Toolkit
management responsibilities to Seal.
