# Conformance data

The read-only corpus in this directory records black-box behavior observed from
Seal Legacy Core `0.3.0.dev0` at the frozen commit
`94bb931a7934efe31549d4c21dc7153e43f27a08`. The two commands in scope are:

```text
seal-legacy task show <TASK_ID>
seal-legacy run show <TASK_ID> --run-id <RUN_ID>
```

The Go command spelling changes only the executable name to `seal`. The separate
`task-create-contract.md` records the Task snapshot writer boundary exercised by
`cmd/seal/task_create_conformance_test.go`. No check runner, current-source
comparison, Verdict, Completion, latest-id selection, retry, or repair behavior
is represented here.

## Artifact layout

```text
conformance/
├── README.md
├── read-only-contract.md
├── expected/
│   └── reference-results.json
└── fixtures/
    ├── base-pass/
    │   └── .seal/
    │       ├── tasks/SL-CONFIG-TEST-ISOLATION-001.json
    │       └── evidence/SL-CONFIG-TEST-ISOLATION-001/
    │           └── 58afd1bb1b4d4e8397aaabff53a6ae7a/...
    └── cases.json
```

`base-pass` is the only materialized state tree. `cases.json` supplies exact
layout deltas and mutation recipes for 74 observed cases. This keeps the
fixture immutable and avoids duplicating nearly identical Run trees.
`reference-results.json` records the observed exit code, semantic stdout JSON,
stderr category, and normalized stderr message for every case. The case and
result ids have an exact one-to-one correspondence.

To exercise a case, initialize a disposable Git repository, copy
`base-pass/.seal` into it, apply that case's mutations, and invoke the frozen
Reference command. A `rebuild_run_manifest` operation means recomputing raw-byte
sizes, raw-byte SHA-256 values, and the canonical `evidence_sha256`; the
manifest is intentionally left stale unless that operation appears. There is
no committed fixture generator or synchronization script.

## Provenance and sanitization

The base derives from the Reference CLI-generated Run:

```text
.seal/evidence/SL-CONFIG-TEST-ISOLATION-001/
  58afd1bb1b4d4e8397aaabff53a6ae7a
```

It was minimized to one check and no product changes, all check logs and the
diff were made empty, and the absolute check working directory was replaced by
`<repository>`. Source Snapshot digests and the Run manifest were then
recomputed. The resulting fixture was passed back through the exact frozen
Reference CLI and produced the recorded successful summary. The Reference
checkout was not modified. `cases.json` records a SHA-256 for every fixture
file and all path placeholders; no local absolute path or secret is retained.

## Result comparison

For ordinary cases, non-empty stdout parses as exactly one JSON object followed
by a newline. JSON object key order and whitespace are not semantic. Task ids, Run ids,
schema versions, booleans, null versus integer check exit codes, check order,
Scope violations, and Evidence digests are semantic. On handled errors stdout
is empty and stderr is `error: <message>`; compatibility is gated by the
recorded error category and exit code rather than Python-specific parser text.

There are frozen Python edge cases where “JSON stdout” is not strict UTF-8
RFC 8259 JSON: Python may emit `Infinity`, and POSIX surrogateescape code
points U+DC80–U+DCFF are restored to their raw bytes. For any result containing
`stdout_raw_hex`, those exact bytes are authoritative over `stdout_json`.
Malformed raw UTF-8 in a stored Task escapes `task show` as an exit-1
`UnicodeDecodeError`; `run show` catches the same saved-Task decoding failure
and returns the handled invalid-input exit 2. Only the unhandled terminal
exception is portable across Reference installation paths.

CPython's default integer-string conversion guard is also observable. A
positive 4,301-digit decimal integer exceeds the 4,300-digit limit and escapes
the handled boundary as exit 1 with a terminal `ValueError`. `cases.json`
expresses the number as a compact repeat specification rather than storing the
large lexeme in every artifact. `write_repeated_utf8` likewise constructs the
integer-before-syntax fixtures only inside disposable repositories, so the
4,301-digit lexeme is not checked in.

## Approved Go-only divergence regressions

The 74 declarative cases above are exact frozen-Reference observations and Go
parity gates. They do not include these separately approved Go-only divergence
regressions, whose compact inputs are generated only in temporary trees:

- `task show` on a stored Task containing 10,000 nested arrays: the Reference
  returns exit 0 and renders roughly 200 MB, while Go keeps the standard
  `encoding/json` nesting bound and returns runtime exit 1 with empty stdout;
- `run show` with deeply nested matching opaque Task extras: Go keeps that same
  standard-library bound and deterministic classifications instead of
  reproducing CPython's order-dependent `RecursionError` threshold;
- otherwise valid `task show` and `run show` calls from a deleted current
  working directory: the Reference raises `FileNotFoundError` with exit 1,
  while Go returns the stable repository-resolution exit 3; invalid command
  shapes and identities remain exit 2.

The Go regressions verify streams, classification, and no-write behavior
without committing the huge Reference output or materialized depth fixtures.
The exact narrow approvals are recorded in `read-only-contract.md`; they do not
permit different Acceptance-semantic outcomes.

The frozen Reference has two material security limitations represented rather
than repaired here:

- a saved Task JSON symlink may resolve outside the repository and is followed;
- an extra physical file that is absent from both `verification.json` and the
  manifest is ignored. A path explicitly listed as extra is rejected.

These are compatibility facts for this slice, not recommendations for newly
written state.
