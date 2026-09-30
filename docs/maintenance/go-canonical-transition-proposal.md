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

Validating an existing store needs three separate things from `pyjson`:

1. **Digest encoding.** `evidence_sha256` (manifest records) and the Source
   Snapshot digest are recomputed over canonical JSON. Both payloads hold only
   strings and integers, so old-store validation needs the encoder's key order,
   separators, ASCII escapes, and integer normalization (a stored `-0` size is
   recomputed as `0`), but not Python float `repr`.
2. **Decoding.** Run validation decodes the saved Task and every Evidence
   document. Those documents may contain Python-only tokens (valid Runs can hold
   `NaN` in `verification.json` `duration` or `checks.json` `duration_seconds`),
   lone-surrogate escapes, integers of any length, or bytes the explicit UTF-8
   guard classifies.
3. **Python equality.** `pyjson.Equal` (`true == 1`, `1 == 1.0`) decides the
   saved-Task versus Evidence `task.json` comparison and the Evidence checks for
   check records (including `effective_timeout`), `required_checks_pass`,
   changed files, Scope results, and Source Snapshot stability. Documents are
   compared, not re-encoded; the manifest hashes their raw bytes.

Task rendering (`task show`) and the bytes of newly written documents are not
old-store requirements. Keeping them stable is a policy choice within option A.

The examples below assume `pyjson` is replaced by `encoding/json` with
`Decoder.UseNumber` and Go `==`/`reflect.DeepEqual` comparisons. The default
`float64` decoding would change more (for example, it rejects a 4,301-digit
integer and loses integer spelling). The list is illustrative, not exhaustive;
see required step 7.

| Stored content | Outcome |
|---|---|
| `NaN`/`Infinity` token in a saved Task | decode fails in `readSavedTask`: identity error, exit 2 |
| `NaN`/`Infinity` token in Run documents, including today's valid `NaN` durations | Evidence decode fails, exit 8 |
| Lone-surrogate escape such as `\udcff` | not rejected: replaced with U+FFFD. Matching saved and Evidence copies can still compare equal, so the Run stays valid, but `task show` bytes change (see `task_surrogateescape_dcff`) |
| Invalid raw UTF-8 | replaced with U+FFFD instead of today's exit 1 unless the explicit guard is kept |
| Saved Task and Evidence `task.json` equal only under Python equality | Task mismatch, exit 2 |
| Evidence document equal to its expected value only under Python equality (such as `effective_timeout: 300.0` for a Task timeout of `300`) | Evidence error, exit 8 |
| Integer longer than 4,300 digits | today exit 1; accepted (exit 0) |
| Manifest or Source Snapshot record with a non-normal integer such as `-0` | digest mismatch, exit 8, unless the encoder normalizes integers |
| Ordinary floats rendered with Python `repr` | no effect on validation; changes only new bytes and `task show`/`run show` output |

Seal has no migration tool and the charter forbids automatic repair, and it
cannot tell ahead of time which stores fall into which rows. Old-store
validation therefore needs the decoder features, Python equality, and the
string/integer rules of the canonical encoder. Option A additionally keeps float
`repr` and Task rendering so that new writes and public output do not change.

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

Additionally introduce Task schema v2 and Evidence schema v3 restricted to
RFC 8259 values (no NaN/Infinity, no lone surrogates), while keeping v1/v2
readers. RFC 8259 fixes syntax, not bytes, so the new versions must also define
a versioned canonical encoding for digest payloads (key order, separators,
escaping, number spelling) and how readers select it from the schema version.

- Simplifies what new Runs contain and can later retire parity code once no
  supported store holds old versions.
- Adds a second writer and reader path now, so code grows first; needs a
  public retention/support policy for old stores.

### C. Hard cut

Drop Python parity and declare earlier stores unsupported.

- Deletes most `pyjson` decoding edge cases, Python equality, the runtime
  exit-1 categories, the parity-specific corpus cases, and the Reference-capture
  machinery. Corpus cases for Acceptance semantics (Task/Run identity, manifest
  hash and size corruption, missing Evidence, Scope, symlinks) must stay as
  Go-owned scenarios, so the saving is smaller than the corpus size.
- Existing Tasks, Runs, and Completions in the affected rows above stop
  validating or change outcome. Beyond exact lookup and Completion, `verify`
  reads the saved Task first and can no longer produce a Run for an affected
  Task. `run export` reports affected Tasks as `invalid_task` and excludes
  affected Runs as `invalid_evidence`, so every export of that store exits 8
  for periodic consumers.
- New invocations change too, unless the mappings are kept independently of
  old-store support: `task-create-contract.md` requires exit 1 for invalid
  UTF-8 Task or catalog input and for over-limit integer tokens, which would
  otherwise become ordinary invalid input or be accepted after replacement
  decoding.
- Because affected stores cannot be
  identified in advance, this contradicts the current migration invariants and
  the RC acceptance history.

## Recommendation

Approve option A now. Revisit B only if parity code blocks a concrete feature.
Do not choose C while any user depends on existing `.seal` state.

## Required steps if approved

1. Record the transition in `MIGRATION_CHARTER.md` (scope: behavioral authority,
   not schema) and update `REFERENCE.md` to mark Python as historical. Update
   every other statement that names Python as the behavioral authority or
   reference in the same change, found by a repository-wide search rather than
   a fixed list. Today these include the headers of
   `conformance/read-only-contract.md`, `conformance/task-create-contract.md`,
   and `conformance/verify-contract.md`; the "Approved divergences" paragraph
   of `read-only-contract.md`; the authority statements in
   `MIGRATION_CHARTER.md`; and the Reference paragraph in `README.md`.
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
7. Before choosing option B or C, inventory every `pyjson` call site that
   affects decoding, equality, or digest encoding for stored documents, with a
   regression scenario for each affected document class and its exit code.

## Verification

- The Go golden tests keep today's corpus checks: exact exit codes, stderr
  category and normalized message shape, and stdout compared as decoded JSON
  (`reflect.DeepEqual`). That establishes semantic, normalized parity, not
  identical bytes. Key-order or whitespace changes would still pass.
- Byte identity is established only by the raw goldens from step 6, plus the
  existing `stdout_raw_hex` cases.
- Representative Runs created before the transition, both Go- and
  Python-written, keep their current `run show` and `complete` outcomes in a
  regression scenario, including exit 7 for a Task with `verifier.required`
  and exit 9 when current source differs from S1. Successful `complete` is
  required only for eligible Basic Runs with matching source.
