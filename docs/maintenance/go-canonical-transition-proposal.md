# Proposal: Go canonical transition for Legacy byte parity

Status: proposal for decision. It changes no code or contract until an
explicit approval is recorded in [`MIGRATION_CHARTER.md`](../../MIGRATION_CHARTER.md).

## Question

The frozen Python Reference (`REFERENCE.md`) is still the behavioral
authority for Task, Evidence, Manifest, source, Scope, and check meanings.
Go reproduces its externally observable bytes, including edge cases that a
Go-first design would not choose. Which of that parity can be removed if Go
becomes canonical, and at what cost?

## What parity code exists today

Sizes are after the simplification stack in [`simplification.md`](simplification.md).

| Area | Where | Size | Why it exists |
|---|---|---:|---|
| Python JSON decoding: NaN/Infinity constants, lone-surrogate escapes, CPython 4300-digit integers | `internal/pyjson/decode.go` | ~390 lines | `json.load` accepts inputs Go rejects or alters |
| Python rendering and equality: sorted keys, `repr` floats, surrogateescape bytes, bool/int/float `==` | `internal/pyjson/encode.go`, `value.go` | ~400 lines | recomputed digests (key order, separators, ASCII escapes), newly written bytes, public output, and saved-Task comparisons must match Python |
| Runtime exit-1 categories for invalid UTF-8, integer limit, unencodable surrogates | `taskstate`, `runstate` error mapping | ~80 lines | Reference exits 1 on these uncaught exceptions |
| `argparse`-shaped option handling and help text | `cmd/seal/main.go` parsers | ~200 lines | frozen CLI category parity |
| Frozen read semantics: `task show`/`run show` follow a symlinked saved Task; `run show`/`complete` walk up to `.git` | `runstate`, `TRUST_MODEL.md` | small | Reference compatibility, documented as not a security boundary |
| Conformance corpus and its tests | `conformance/fixtures`, `conformance/expected`, `cmd/seal/conformance_test.go`, `task_create_conformance_test.go` | ~3,500 JSON + ~2,350 test lines | normalized comparison with Reference captures: exit codes, stderr shape, decoded stdout; raw bytes only for `stdout_raw_hex` cases |
| "Every Go Run must be accepted by frozen Python `run show`" | `conformance/verify-contract.md` | rule | cross-implementation acceptance |

JSON nesting depth is not in this table because it is not Reference parity.
Go keeps the standard `encoding/json` depth bound as a Go-owned resource policy.
For 10,000 nested arrays, `task show` exits 1 where the Reference exits 0, and
deep matching Task extras in `run show` get deterministic classifications
instead of CPython's order-dependent `RecursionError`. Both are approved
divergences in [`read-only-contract.md`](../../conformance/read-only-contract.md).
`task create` classifies its depth failure as exit 2, a gap already listed in
[`simplification.md`](simplification.md). An authority transfer cannot remove
these rules; any change to them must be evaluated separately as Go policy.

The same holds for Completion. Immutable schema-version-2 records, and never
upgrading or overwriting Legacy v1 records, are an already approved canonical
policy transition (see "Approved Go v1 Completion transition" in
[`MIGRATION_CHARTER.md`](../../MIGRATION_CHARTER.md) and
[`complete-contract.md`](../../conformance/complete-contract.md)), not
parity that a transfer could remove.

## What a transition cannot remove

Stored Evidence is validated by recomputing digests over canonical JSON:
`evidence_sha256` (manifest records) and the Source Snapshot digest (ASCII,
sorted, Python `json.dumps` bytes). Both payloads hold only strings and integers;
floats such as check durations live in artifacts whose raw bytes are hashed,
not re-encoded. Every existing `.seal/evidence` Run, whether
Python- or Go-written, can only be re-validated by an encoder that reproduces
those exact bytes. The same holds for saved Task snapshots that already contain
Python-specific values.

Therefore the canonical encoder and the decoder features that stored documents
can contain must stay for as long as existing stores must validate. Seal has no
migration tool and the charter forbids automatic repair. What removal would
change depends on the document and the value:

| Stored content | Effect of replacing `pyjson` with a strict Go decoder/encoder |
|---|---|
| Saved Task with a Python-only value (NaN/Infinity, lone surrogate) | `readSavedTask` fails before Evidence is read: identity error, exit 2 |
| Run documents (`task.json`, manifest, Source Snapshot) with such values, or a digest payload rendered differently (key order, separators, ASCII escapes) | Evidence validation fails, exit 8 |
| Floats rendered with Python `repr` | no effect on existing stores; only newly written bytes and public output would change |
| Integer longer than 4,300 digits | today runtime exit 1; without the guard it would be accepted (exit 0), which changes the outcome the other way |
| Only the JSON subset both implementations render identically | stays valid, provided the canonical encoder keeps the same bytes |

Seal cannot tell ahead of time which stores fall into which class, so option A
keeps the whole layer.

## Options

### A. Authority transfer only (recommended)

Go becomes the behavioral authority; Python remains a historical record.

- Keep `internal/pyjson` and every stored schema byte-for-byte; existing and
  new Evidence stays valid.
- Convert the conformance corpus into Go-owned golden tests: keep the fixtures
  and expected results, drop the requirement to re-capture from Python and the
  rule that Go Runs must pass Python `run show`.
- Future behavior changes are justified by a Go contract and regression test
  instead of a Reference scenario.
- Removes the process cost (Python environment, dual capture), not much code.

### B. New schema versions for new writes

Additionally introduce Task schema v2 and Evidence schema v3 written with plain
RFC 8259 JSON (no NaN/Infinity, no lone surrogates, no Python float `repr`),
while keeping v1/v2 readers.

- Simplifies what new Runs contain and can later retire parity code once no
  supported store holds old versions.
- Adds a second writer and reader path now, so code grows first; needs a
  public retention/support policy for old stores.

### C. Hard cut

Drop Python parity and declare earlier stores unsupported.

- Deletes most of `pyjson` decoding edge cases, runtime exit-1 categories, and
  the corpus (several thousand lines).
- Existing Tasks, Runs, and Completions with Python-specific values or
  rendering stop validating, with the exit categories shown above; exact lookup
  and Completion from those states are lost. Because affected stores cannot be
  identified in advance, this contradicts the current migration invariants and
  the RC acceptance history.

## Recommendation

Approve option A now. Revisit B only if parity code blocks a concrete feature.
Do not choose C while any user depends on existing `.seal` state.

## Required steps if approved

1. Record the transition in `MIGRATION_CHARTER.md` (scope: behavioral authority,
   not schema) and update `REFERENCE.md` to mark Python as historical.
2. Update `AGENTS.md` migration rules that require Reference justification.
3. Re-home the corpus as Go golden tests; keep fixture provenance in
   `conformance/README.md`.
4. Remove the "accepted by frozen Python `run show`" rule from
   `conformance/verify-contract.md`.
5. Cut a new RC; the Acceptance surface digest changes, so the stable gate
   needs a fresh 20-Task report.
6. Before relaxing any rule, add raw golden bytes for every public output and
   stored document that must stay byte-exact. Today only cases with
   `stdout_raw_hex` are compared byte for byte.

## Verification

- The Go golden tests keep today's corpus checks: exact exit codes, stderr
  category and normalized message shape, and stdout compared as decoded JSON
  (`reflect.DeepEqual`). That establishes semantic, normalized parity, not
  identical bytes. Key-order or whitespace changes would still pass.
- Byte identity is established only by the raw goldens from step 6, plus the
  existing `stdout_raw_hex` cases.
- Runs created before the transition, both Go- and Python-written, still pass
  `run show` and `complete` in a regression scenario.
