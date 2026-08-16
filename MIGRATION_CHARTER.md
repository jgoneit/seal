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

Except for an explicitly approved canonical semantic transition named below,
Python remains authoritative for Acceptance semantics. An explicitly approved
non-semantic resource-limit or invocation-environment divergence may be
retained only when its narrow scope is recorded in the conformance contract and
protected by a regression test. Such a non-semantic exception must not change
Task or Run identity, mechanical state, Scope, required checks, source
stability, Evidence or manifest integrity, or Completion meaning.

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

The Task-create slice retains the frozen exit-1 categories for invalid UTF-8
input or catalog bytes and JSON integer tokens longer than 4,300 decimal digits.
Three writer-only divergences are explicitly approved: an unpaired UTF-16
surrogate exits 1 without creating or truncating a destination, and an input or
catalog that aliases the destination exits 2 without changing either source.
These decisions preserve atomic publication and input/catalog immutability; they
do not alter normalized Task meaning or authorize a broader writer policy.

The third public compatibility slice adds only `seal verify <TASK_ID>`. Verify
executes saved checks, observes S0 and S1 source identity, records layered Git
changes, and publishes one manifest-complete Evidence Run. A recorded Run is a
successful verification operation even when its mechanical result is `fail`.
Verify does not decide Completion, read Verdict, select a latest identity, or
invoke another Toolkit module.

The Go v1 exit table treats exhaustion after 100 true generated Run-id
collisions as publication exit `3` with no new Run or staging residue. This is
an explicit narrow divergence from the frozen implementation's exit `2`
classification; it does not alter any persisted Evidence meaning.

For the Evidence writer only, Go prevalidates the saved Task and check
definitions before S0, writes into a private sibling staging directory, and
publishes the complete directory with a native no-replace rename. Private means
mode `0700` on POSIX and, on Windows, a protected inheritable DACL containing
only the current process-token user and LocalSystem. The writer binds the
retained staging root to the created object identity before writing; Windows
captures that identity from the original creation handle. A replacement in the
create-to-reopen interval is rejected without deleting or modifying the
replacement. It rejects symlinked or non-directory writer ancestors and
repository escape. These are approved
writer-safety and whole-directory atomicity divergences from the frozen writer,
which exposes incomplete Run directories after some failures. They do not
change the recorded Task, check, Scope, source, verification, or manifest
meanings consumed by either frozen Python or Go `run show`.

Atomic publication is claimed only for same-filesystem local POSIX filesystems
with the required native rename primitive and for NTFS. Network filesystems,
FUSE implementations, and filesystems without the required no-replace
operation fail closed or remain outside the support claim. Atomic visibility
does not imply crash-proof storage: artifact file sync is required on every
supported platform, and pre-publication directory sync is additionally
required on POSIX. Windows has no portable directory flush in this writer. A
post-commit parent-directory sync is best effort because returning failure
after a successful rename would contradict the public commit state. An
unrelated process with the same operating-system account can still race
mutable directory names between identity checks on POSIX; that hostile
same-user race is outside the v1 atomic object-binding guarantee. Windows uses
handle-relative publication and has no pathname fallback.

## Approved Go v1 Completion transition

Go v1 Completion is an explicitly approved canonical policy transition, not an
exact reproduction of the frozen Python Completion policy. The frozen
implementation remains authoritative for Task, Evidence, manifest, S0/S1,
Scope, and check meanings consumed through `ValidateRun()`. It is no longer
authoritative for whether a recorded Verdict can satisfy Completion or for the
shape and mutability of a newly written Completion record.

The only supported Go v1 Completion profile is Basic Acceptance with
`verifier.required=false`. A Task with `verifier.required=true` remains valid
for storage, verification, and read-only queries, but `complete` always rejects
it with exit `7`. Go v1 does not read, validate, or decide from
`verdict.raw.json` or `verdict.json`; their presence, absence, validity, and
contents do not affect Completion.

Go v1 writes only immutable schema-version-2 Completion records. It never
upgrades or overwrites the frozen schema-version-1 record. A valid existing v2
record is reusable only after the current invocation revalidates the Run,
collects current source, and reapplies every eligibility gate. Reuse preserves
the record's exact bytes and original timestamp. Static reuse validation
requires both stored source digests to equal the `ValidateRun()`-authoritative
S1 digest before S2 is collected. A legacy v1 record, a corrupt or
contradictory v2 record, or a symlink, directory, or other non-regular
Completion destination is Evidence failure exit `8` and remains untouched.
Absent records are published atomically without replacement; concurrent
eligible creators reconcile an existing winner only by validating and reusing
its exact bytes and timestamp.

The fixed source and policy order is: CLI and identity; canonical stored-Run
validation; existing v2 record validation; stable current-source S2
collection; `S0 == S1 == S2`; the unsupported required-verifier gate; Scope;
required timeout; other required-check failure; and only then immutable record
publication or reuse. This order preserves exits `3`, `9`, `7`, `4`, `6`, and
`5` respectively after stored Evidence and existing-record failures have taken
their earlier classifications. Optional check failure or timeout does not block
Basic Acceptance. A runtime, result-rendering, or stdout failure is exit `1`;
if it occurs after a record was committed or selected for reuse, the immutable
record is preserved and is not rolled back.

This transition does not add Bundle, Verdict, Reviewer, retry, repair, latest
identity, or automatic execution behavior. The complete external contract,
record schema, idempotency rule, and failure precedence are frozen in
[`conformance/complete-contract.md`](conformance/complete-contract.md). The
policy was approved before implementation and is now represented by the public
command and its conformance tests.

## Explicit exclusions

The migration does not bring over Python packaging, private helpers, Codex
Plugin or Skill behavior, UI flows, provider integrations, workflow engines,
automatic repair, or historical phase machinery. It does not add Toolkit
management responsibilities to Seal.
