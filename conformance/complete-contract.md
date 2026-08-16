# Go v1 Completion contract

This contract defines the implemented Go v1 behavior of:

```text
seal complete <TASK_ID> --run-id <RUN_ID>
```

The stored-Run authority remains `ValidateRun()`, whose Task, Evidence,
manifest, S0/S1, Scope, and check meanings reproduce Seal Legacy Core
`0.3.0.dev0` at `94bb931a7934efe31549d4c21dc7153e43f27a08`.
Completion policy itself makes the explicit canonical transition recorded in
[`MIGRATION_CHARTER.md`](../MIGRATION_CHARTER.md): Go v1 does not consume Manual
Verdict artifacts, and it writes an immutable schema-version-2 record rather
than the mutable Legacy schema-version-1 record.

The policy was approved before implementation and is now frozen by the command,
core, and conformance tests.

## Public result and exits

The command requires one exact Task identity and an explicit exact Run
identity. It never selects a latest Run. Successful stdout retains the Legacy
three-field shape as one UTF-8 JSON object followed by one newline; stderr is
empty.

The public option spellings are exact: only `-h`, `--help`, and `--run-id` are
accepted. Go does not reproduce `argparse`'s incidental long-option prefix
abbreviations such as `--r` or its position-dependent trailing-`--` quirk.
`--run-id=<RUN_ID>`, option-first order, and repeated `--run-id` with the last
value winning remain supported. A `--` terminator stops option and help
recognition.

```json
{
  "completion_path": "<absolute path to completion.json>",
  "run_id": "<RUN_ID>",
  "task_id": "<TASK_ID>"
}
```

Exit `0` means that the supplied Task/Run is eligible against the source
observed by this invocation and that an immutable v2 record was either
published or safely reused. The handled policy categories are:

| Exit | Meaning |
| ---: | --- |
| `0` | current Basic Acceptance passed and a v2 record was published or reused |
| `1` | runtime, success-result rendering, or stdout write failure |
| `2` | CLI shape, Task/Run identity, or saved Task validation failure |
| `3` | Git/repository failure or unstable current-source collection |
| `4` | stored Evidence records a Scope violation |
| `5` | a non-timeout required check failed or could not launch |
| `6` | a required check timed out |
| `7` | the Task has `verifier.required=true`, which Go v1 does not complete |
| `8` | Evidence, manifest, Completion-record, or Completion-publication failure |
| `9` | valid stored or current source does not satisfy `S0 == S1 == S2` |

Handled failures write no success JSON. A refusal never rewrites or removes an
existing Completion record, including a historical record from an earlier
successful invocation.

## Fixed decision order

Completion applies exactly this fail-closed order:

1. Validate CLI shape and the supplied Task and Run identities. Invalid input
   is exit `2`; repository resolution failure is exit `3`.
2. Call `ValidateRun()` as the sole authority for saved Task, exact Run,
   mechanical Evidence, manifest, and persisted S0/S1 integrity. Identity or
   saved-Task failure is exit `2`, repository failure is exit `3`, and missing,
   corrupt, contradictory, unsupported, or unsafe Evidence is exit `8`. A
   preserved runtime-representation failure is exit `1`.
3. If `completion.json` already exists, validate its v2 schema, exact identity,
   manifest digest, pass result, and timestamp before observing current source.
   Both stored source digests must equal the `ValidateRun()`-authoritative S1
   digest. An invalid or non-regular destination is exit `8`. Completion-record
   decoding never leaks an invalid-UTF-8, numeric-conversion, nesting-depth, or
   other generic runtime category; every such record failure is exit `8`.
4. Collect S2 with the same source collector used for S0 and S1. S2 is observed
   twice without retry; disagreement between those observations or another
   collection failure is exit `3`.
5. Require `S0 == S1 == S2`. A stable unequal Snapshot is exit `9`, including
   an S0/S1 mismatch already represented by an otherwise valid failed Run.
6. If the saved Task has `verifier.required=true`, return exit `7`.
7. If the validated Run has a Scope violation, return exit `4`.
8. If any required check timed out, return exit `6`.
9. If any other required check failed, including launch failure, return exit
   `5`.
10. Only after all gates pass, atomically publish a new v2 record without
    replacement or reuse the already validated v2 record.
11. Render and write the three-field success object. Rendering or stdout
    failure is exit `1`. A record already committed or selected for reuse stays
    intact and is not rolled back.

Earlier steps always take precedence over later steps. In particular, corrupt
Evidence or an invalid existing Completion record wins over current source;
source mismatch wins over verifier, Scope, and check gates; the unsupported
required-verifier gate wins over Scope and check outcomes; and required timeout
wins over other required-check failure.

`mechanical_result` is not an additional independently ordered gate. Its value
is validated against source stability, Scope, and required-check aggregates by
`ValidateRun()`, and the component Completion gates above provide the public
refusal exits.

## Basic-only verifier policy

`verifier.required=false` is the only official Go v1 Basic Profile.
`verifier.required=true` remains a valid Task value so existing Task and Run
storage and read-only compatibility are preserved, but it always reaches exit
`7` after stored and current source binding succeeds. A pass Verdict does not
make that Task eligible.

`verdict.raw.json` and `verdict.json` are not Completion inputs. Complete does
not open, parse, validate, digest, or project them. Missing, malformed,
contradictory, symlinked, or otherwise arbitrary Verdict artifacts do not alter
the decision for either verifier setting. They remain outside the mechanical
manifest and outside the v2 Completion record.

This is an intentional policy difference from the frozen Reference, which
validates recorded Verdict artifacts, allows a passing required Verdict to
satisfy the verifier gate, and can reject an optional-verifier Task because of
a recorded failing Verdict.

## Current source and saved outcomes

Complete never reruns checks or regenerates Evidence. It reuses the source
collector from Verify only to make a bounded S2 observation. Source remains
unlocked; exit `0` asserts equality at the S2 observation, not that files cannot
change after the observation or after the command returns.

S0 and S1 come only from the manifest-validated Run. The v2 record binds the
validated manifest digest, the S1 source digest, and the newly observed S2
digest. Success requires all three Snapshots to be equal. A collector that
cannot obtain two identical S2 observations fails closed at exit `3`; two
stable but unequal Snapshots are exit `9`.

Optional check failure, launch failure, or timeout remains recorded Evidence
but does not block Completion. Required outcomes retain timeout precedence:
any required timeout is exit `6`; only when none timed out can another required
failure return exit `5`.

## Immutable completion v2

The exact new record has no fields beyond:

```json
{
  "schema_version": 2,
  "task_id": "<TASK_ID>",
  "run_id": "<RUN_ID>",
  "evidence_sha256": "<validated manifest digest>",
  "verified_source_sha256": "<S1 digest>",
  "current_source_sha256": "<S2 digest>",
  "final_result": "pass",
  "completed_at": "<UTC timestamp>"
}
```

The digest fields are 64-character lowercase SHA-256 hex strings.
`completed_at` uses exactly the UTC microsecond grammar
`YYYY-MM-DDTHH:MM:SS.ffffffZ`. On success,
`verified_source_sha256 == current_source_sha256` because S1 equals S2. New
records are written as deterministic UTF-8 JSON with a trailing newline.

The v2 record deliberately omits the Legacy `mechanical_result`, verifier
runner/verdict, verifier-required flag, and finding counts. Its
`evidence_sha256` is the digest already established by the validated
`run-manifest.json`; the Completion record is a later consumer artifact and is
not added to that mechanical manifest.

An existing record is valid for reuse only when it has the exact v2 field set
and types, exact requested identities, `final_result: "pass"`, the validated
manifest digest, a `completed_at` value in the exact UTC microsecond grammar,
and both
`verified_source_sha256 == current_source_sha256 == <validated S1 digest>`.
These are static exit-`8` record checks performed before S2. The current
invocation must then collect S2 and pass every gate. If newly observed S2
differs from S1, the result is current source drift at exit `9`, not a rewrite
of or exit-`8` repair attempt on the existing record. Reuse returns the normal
success object without rewriting the record, preserving its exact bytes and
original `completed_at` value.

The following existing destinations are exit `8` and are never overwritten,
removed, truncated, converted, or repaired:

- a Legacy schema-version-1 Completion record;
- invalid UTF-8, malformed JSON, duplicate keys, excessive nesting, invalid
  numeric representation, missing or additional fields, invalid field types,
  or an unsupported schema version;
- a record whose Task, Run, Evidence, source, or final-result content
  contradicts the validated Run or current eligibility, or whose
  `completed_at` does not match the exact UTC microsecond grammar;
- a symlink, directory, or other non-regular `completion.json` destination.

These Completion-record failures are all exit `8`, including representation
failures that other commands may classify as runtime exit `1`. A semantically
valid exact-v2 record may use different insignificant JSON whitespace or key
order; reuse preserves its original bytes instead of canonicalizing them.

An earlier valid v2 record does not waive current eligibility. Later source
drift or any later verifier, Scope, timeout, or required-check refusal returns
its current exit while preserving that record.

## Atomic no-clobber publication

When `completion.json` is absent, Complete constructs the entire v2 record in
the exact validated Run directory and publishes it with an atomic no-replace
operation. It does not use a replace rename and does not temporarily truncate
the destination. A pre-commit write, sync, validation, or publication failure
is exit `8`, creates no final record, and cleans its private temporary artifact
under the tested ordinary filesystem conditions.

Concurrent eligible calls are idempotent. At most one call creates the final
name. A losing no-replace call handles the existing-destination result by
reading the winner and applying the same v2 record validation. When the winner
is the identical eligible record, the loser preserves its exact bytes and
timestamp, returns the normal success object, and exits `0`. A conflicting
winner or unsafe destination is exit `8`.

No-clobber publication protects an existing record from Complete itself. It
does not make the repository immutable against another process running as the
same operating-system user.

The implementation contains native no-replace publication primitives targeting
ordinary local Linux, macOS, and Windows filesystems. A platform is not claimed
as release-supported until its native CI gate passes; cross-compilation alone
is insufficient. Atomic visibility is not a crash-durability claim. POSIX
targets synchronize the staged file and directory before publication, Windows
synchronizes the staged file but does not provide directory sync here, and the
post-publication directory sync is best-effort. No atomic-rename or durability
guarantee is claimed for network filesystems, FUSE, or filesystems whose
semantics differ from ordinary local POSIX filesystems or NTFS.

## Explicit exclusions

Complete does not rerun checks, recollect S0 or S1, regenerate changes or diff,
create or select another Run, retry or repair work, invoke a reviewer, consume
or produce Verdict, infer a latest identity, install Git, call another Toolkit
module, or make a future completion ineligible by deleting an earlier record.

This slice adds no Bundle, Verdict, Reviewer, Knowledge, Security runtime,
automatic execution, workflow engine, hook, event bus, provider integration,
or Agent orchestration behavior.
