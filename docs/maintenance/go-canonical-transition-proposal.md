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
| Python JSON decoding: NaN/Infinity constants, lone-surrogate escapes, CPython 4300-digit integers, depth classification | `internal/pyjson/decode.go` | ~360 lines | `json.load` accepts inputs Go rejects or alters |
| Python rendering and equality: sorted keys, `repr` floats, surrogateescape bytes, bool/int/float `==` | `internal/pyjson/encode.go`, `value.go` | ~400 lines | stored bytes, digests, and saved-Task comparisons must match Python |
| Runtime exit-1 categories for invalid UTF-8, integer limit, nesting depth, unencodable surrogates | `taskstate`, `runstate` error mapping | ~80 lines | Reference exits 1 on these uncaught exceptions |
| `argparse`-shaped option handling and help text | `cmd/seal/main.go` parsers | ~200 lines | frozen CLI category parity |
| Frozen read semantics: `task show`/`run show` follow a symlinked saved Task; `run show`/`complete` walk up to `.git` | `runstate`, `TRUST_MODEL.md` | small | Reference compatibility, documented as not a security boundary |
| Legacy Completion v1 records rejected but never rewritten | `runstate/complete.go` | small | coexistence with Python-written stores |
| Conformance corpus and its tests | `conformance/fixtures`, `conformance/expected`, `cmd/seal/conformance_test.go`, `task_create_conformance_test.go` | ~3,500 JSON + ~2,350 test lines | byte-exact comparison with Reference captures |
| "Every Go Run must be accepted by frozen Python `run show`" | `conformance/verify-contract.md` | rule | cross-implementation acceptance |

## What a transition cannot remove

Stored Evidence is validated by recomputing digests over canonical JSON:
`evidence_sha256` (manifest records) and the Source Snapshot digest (ASCII,
sorted, Python `json.dumps` bytes). Every existing `.seal/evidence` Run, whether
Python- or Go-written, can only be re-validated by an encoder that reproduces
those exact bytes. The same holds for saved Task snapshots that already contain
Python-specific values.

Therefore the canonical encoder and the decoder features that stored documents
can contain must stay for as long as existing stores must validate. Seal has no
migration tool and the charter forbids automatic repair. Removing them would make
existing Runs fail with exit 8.

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
- Every existing Run and Completion stops validating; exact lookup and
  Completion from those states are lost. Contradicts the current migration
  invariants and the RC acceptance history.

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

## Verification

- All existing conformance fixtures still produce identical bytes and exit
  codes under the Go golden tests before any rule is relaxed.
- Runs created before the transition, both Go- and Python-written, still pass
  `run show` and `complete` in a regression scenario.
