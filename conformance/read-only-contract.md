# Frozen read-only Acceptance contract

## Authority and boundary

The behavioral authority is `jgoneit/seal-legacy` commit
`94bb931a7934efe31549d4c21dc7153e43f27a08`, version `0.3.0.dev0`. All expected
results in this directory were observed by invoking that checkout as:

```text
PYTHONPATH=<seal-legacy>/src \
  <seal-legacy>/.venv/bin/python -m seal_legacy <arguments>
```

This is an external-contract extraction, not a Python module map. The Go
implementation may choose its own structure but must reproduce the outcomes
for the exact identities and persisted bytes represented here.

| Reference concern | Go reproduces | Reference-only | Excluded |
| --- | --- | --- | --- |
| Stored Task JSON object | Yes, within bounded JSON nesting | | |
| Task normalized snapshot meaning | Yes | | |
| Evidence v2 mechanical documents | Yes | | |
| S0/S1 stored consistency | Yes | | |
| Raw-byte Run manifest integrity | Yes | | |
| Validated Run Summary v1 | Yes | | |
| S2 current source | | | Yes |
| Verdict | | | Yes |
| Completion | | | Yes |
| Bundle | | | Yes |
| Check execution and Evidence writing | | | Yes |
| Codex Plugin and credential profile | | | Yes |
| Historical v0.1/v0.2 compatibility | | | Yes |

## `task show`

`seal task show <TASK_ID>` requires one exact, path-safe Task id. It resolves
the enclosing Git repository, reads only
`.seal/tasks/<TASK_ID>.json`, requires the JSON top level to be an object, and
prints that object with a trailing newline. It does not read
`.seal/checks.json`, inspect the working tree, recalculate a baseline, or infer
another Task.

Most importantly, this command does **not** establish Acceptance validity. It
does not apply `contracts/task.schema.json`, compare the stored `id` with the
requested id, validate `baseline`, or require normalized Task fields. A
syntactically valid object with schema version `99`, unexpected fields, or a
contradictory `id` is returned unchanged. The Task schema is the create-input
contract; normalized storage also adds `baseline`. Strict Task identity and the
fields needed for Acceptance are checked later when canonical Run validation
actually consumes the Task.

Observed Task errors are:

| Condition | Exit | Category |
| --- | ---: | --- |
| invalid or unsafe Task id | 2 | invalid input or identity |
| missing, broken-link, or looping-link Task | 2 | invalid input or identity |
| malformed JSON or non-object JSON | 2 | invalid input or identity |
| raw invalid UTF-8 in stored JSON | 1 | unhandled encoding failure (`UnicodeDecodeError`) |
| decimal integer longer than CPython's 4,300-digit limit | 1 | unhandled numeric conversion failure (`ValueError`) |
| no enclosing Git repository | 3 | repository error |

The frozen implementation follows any Task-file symlink whose target is a
readable regular JSON object. Both a repository-confined target and a target
outside the repository are accepted. This external-target behavior is a known
security limitation preserved only for exact compatibility.

Task rendering also preserves Python number and POSIX surrogateescape
behavior. Numeric spellings are decoded to Python values before pretty
printing (`-0` becomes `0`, `1.2300e+02` becomes `123.0`, and overflow may
render as the non-standard token `Infinity`). An escaped U+DC80–U+DCFF value
is emitted through Python's stdout surrogateescape handler as its original raw
byte. Exact stdout bytes, not UTF-8 JSON semantics, are authoritative for
these recorded edge cases.

CPython's integer-string conversion limit is likewise part of the frozen
behavior. Decoding a positive decimal integer with 4,301 digits raises an
unhandled `ValueError`, produces no stdout, and exits 1. The same failure occurs
when canonical Run validation first decodes a saved Task or Evidence JSON
document containing that value, and it takes precedence when a later token in
the same document has an ordinary JSON syntax error.

## `run show`

`seal run show <TASK_ID> --run-id <RUN_ID>` requires both exact, path-safe
identities. It consumes one canonical `ValidateRun()` result and prints a
`validated-run-summary/v1`-meaning JSON object with:

```text
schema_version, task_id, run_id, evidence_sha256,
mechanical_result, scope_pass, scope_violations,
required_checks_pass, source_stable_during_checks, checks
```

Each projected check contains exactly `name`, `required`, `passed`,
`timed_out`, and `exit_code`; a launch failure uses JSON `null` for
`exit_code`. Each Scope violation projects `source`, `status`, `path`, and
`previous_path`.

Canonical validation reads these persisted files from the exact Run:

```text
task.json
changed-files.json
diff.patch
checks.json
checks/<declared stdout and stderr logs>
source-before-checks.json
source-after-checks.json
verification.json
run-manifest.json
```

It also reads the exact saved `.seal/tasks/<TASK_ID>.json`. The repository
check catalog is not read. Verdict and Completion files, if present, are not
part of this authority and are not projected.

The validator checks Task/Run identity, Task snapshot equality, a full Git
baseline id, normalized Scope and check definitions, exact cross-document
fields, check/log correspondence, `passed == (!timed_out && exit_code == 0)`,
Scope recomputation, required-check aggregation, S0/S1 Snapshot self-digests
and stability, mechanical-result recomputation, the logical Evidence file
list, raw-byte sizes and SHA-256 values, and canonical Evidence digest. It does
not execute checks, collect S2, compare current source, or evaluate completion.

Required-check failure, timeout, launch failure, Scope failure, or S0/S1
instability is valid stored state. Such a Run returns exit 0 and a summary with
`mechanical_result: "fail"`. An optional check may fail while the Run remains
mechanically passing because optional outcomes do not enter
`required_checks_pass`.

## Symlink and path contract

The exact observed conditions are:

- `.seal`, `.seal/evidence`, `.seal/evidence/<TASK_ID>`, and the exact Run
  directory must be real directories, not symlinks. A symlink at any of these
  positions is rejected with exit 8 even if it points to a directory inside
  the repository.
- Every Evidence path must be a non-empty relative POSIX path without an
  absolute root, drive prefix, backslash, NUL, empty component, `.`, or `..`.
- Frozen Scope and changed-file validation treats any path whose second byte is
  `:` as drive-prefixed, so `1:file`, `_:file`, `C:file`, and `C:/file` are all
  rejected there. Source Snapshot path validation accepts the same four byte
  strings. This Reference asymmetry is preserved; neither value is opened as a
  filesystem path by these validation steps.
- An Evidence file may be a symlink when strict resolution succeeds, the
  resolved target is a regular file, and the target remains inside the
  resolved Run directory.
- A confined Evidence symlink is still checked against the logical path's
  manifest size and SHA-256. Different target bytes therefore fail manifest
  validation.
- An outside-Run target, broken link, loop, directory, or other non-regular
  target is rejected with exit 8.
- Saved Task files are outside this Evidence-file confinement rule and, as
  noted above, may resolve outside the repository in the frozen Reference.

The logical mechanical file set is exact between the validator-derived paths,
`verification.json`, and `run-manifest.json`: missing, duplicate, unsafe, or
extra **listed** paths fail. The Reference does not enumerate the physical Run
directory, so an unlisted extra physical file is ignored and the valid summary
is returned. This is another known frozen security limitation.

## Exit and stream contract

| Exit | Meaning for these commands |
| ---: | --- |
| 0 | Stored Task object or structurally valid Run was read; a valid Run may have mechanical result `pass` or `fail`. |
| 1 | A stored-Task encoding or numeric conversion failure, or a runtime failure recorded below. |
| 2 | Invalid CLI identity/input or stored Task/Run identity contradiction. |
| 3 | Repository-root resolution failed. |
| 8 | Evidence is missing, malformed, contradictory, unsupported, unsafe, or tampered. |

Success writes one JSON object plus a trailing newline to stdout and leaves
stderr empty. Handled failures leave stdout empty and write one `error:` line
to stderr. In the frozen numeric/surrogateescape edge cases above, the bytes
are authoritative even when they are not strict UTF-8 JSON. Completion-only
exits are not used by `run show`.

## Approved divergences

These exceptions are narrow compatibility decisions for non-semantic resource
or invocation-environment failures. Python remains authoritative for
Acceptance semantics, and the exceptions do not permit different Task or Run
identity, mechanical result, Scope, required-check result, source stability,
Evidence digest, manifest integrity, path confinement, or Completion meaning.

### Bounded `task show` JSON nesting/resource limit

The frozen Reference accepts a stored Task object whose value contains 10,000
nested arrays, returns exit 0, leaves stderr empty, and renders roughly 200 MB
of pretty-printed stdout. The Go Candidate deliberately retains the standard
`encoding/json` nesting bound: it returns exit 1, leaves stdout empty, and
writes one deterministic `error:` line identifying the supported JSON nesting
depth limit.

This approval applies only to this `task show` case when an extreme nesting
depth is rejected by the Go standard JSON decoder's max-depth limit. It does
not establish a general parser or encoder exception, and it does not permit
divergence in Task/Run identity, mechanical result, Scope, required checks,
source stability, Evidence digest, manifest validity, path confinement,
ordinary supported-input exit categories, or Acceptance and Completion
meaning.

The difference affects only opaque `task show` display for a pathological
resource input. It does not participate in stored Run integrity, mechanical
state, source identity, Evidence digest, or Completion. Reproducing the
Reference's roughly 200 MB rendering would require a custom parser or formatter
whose implementation, security, and maintenance cost is not justified by this
Acceptance slice. The regression generates the input in a temporary directory;
the huge input/output is not stored in the repository corpus.

### Bounded Run Task-extra JSON nesting/resource limit

For matching saved and Evidence Task snapshots, the frozen Reference traverses
opaque extra fields while comparing the two objects. On the pinned Python
runtime, otherwise valid nested extras succeed or raise an unhandled
`RecursionError` at an order-dependent boundary: the observed threshold changes
between approximately 989 and 993 nested containers according to object-key
position and traversal state. At 10,000 levels the Reference exits 1 before it
can return a Run summary.

The Go Candidate does not reproduce that CPython call-stack and key-order
accident with a custom order-preserving parser. Matching opaque Task extras are
accepted up to the Go standard JSON implementation's nesting bound. When that
bound is exceeded while decoding the saved Task or the Run's `task.json`, Go
returns runtime exit 1 with empty stdout and a deterministic handled error.
The same depth failure in checks, verification, a Source Snapshot, or the Run
manifest remains an Evidence error with exit 8. A saved/Evidence Task mismatch
that can be decoded remains an identity error with exit 2.

This policy changes only when an opaque matching Task extra becomes too costly
to decode. The fields that establish Acceptance meaning are still validated,
and their identity, mechanical, Scope, check, source, and integrity outcomes
may not diverge under this approval. Regression inputs are generated in a
temporary directory instead of committing huge nested documents.

### Deleted or unlinked current working directory

When a process starts in a directory that is subsequently unlinked, the frozen
Reference's `task show` and `run show` let `Path.cwd()` raise an unhandled
`FileNotFoundError`, producing exit 1 and a Python traceback. The Go Candidate
instead classifies this invocation precondition as repository resolution: both
state commands with otherwise valid identities and options return exit 3,
leave stdout empty, and write exactly
`error: Task commands must run inside a Git repository.` followed by a newline.
Informational `--help` and `--version` calls do not resolve the working
directory and remain exit 0 with the same bytes as in a normal directory.
Invalid identities and command shapes are still rejected first with exit 2.

This is an invocation-environment classification only. No Task or Run is read,
validated, selected, repaired, or advanced when the current directory cannot
be resolved. In particular, this stable error does not direct a model, choose a
tool, retry work, infer a latest Run, execute a Reviewer, or perform a lifecycle
transition. The user, Native Agent, or CI remains responsible for composition.

## Read-only and trust properties

Both commands are observers. They must not create or alter `.seal` files,
cache files, locks, latest pointers, summaries, migrations, Git worktree/index
state, or HEAD. Content/file-set hashes before and after execution are the
write invariant; mtimes are not relied on.

The manifest is a local corruption/tamper detector, not a signature, remote
attestation, authorization decision, or malicious-maintainer defense. This
slice deliberately preserves the external Task-symlink and unlisted-file
limitations above. Raw Task fields are returned as data only and must never be
executed, resolved as paths, or treated as lifecycle decisions by `task show`.

## Intentionally unsupported

There is no `.harness` fallback, state converter, Writer, Task creation,
verification runner, S0/S1 generation, S2 collection, Verdict, Completion,
Bundle, Reviewer, Plugin workflow, latest Task/Run selection, retry, repair,
next-action recommendation, central Toolkit runtime, or new persisted schema
in this slice.
