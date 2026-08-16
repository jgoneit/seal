# Frozen Task creation contract

## Authority and extraction method

The behavioral authority is `jgoneit/seal-legacy` commit
`94bb931a7934efe31549d4c21dc7153e43f27a08`, version `0.3.0.dev0`. The
contract below was extracted by invoking that checkout as a command-line
program in disposable Git repositories:

```text
PYTHONPATH=<seal-legacy>/src \
  <seal-legacy>/.venv/bin/python -m seal_legacy task create \
  --file <TASK_JSON> [--force]
```

This document records externally observable input, output, normalization,
storage, and error behavior. It is not a map of Python modules or helpers.

## Command result

On success, the command exits 0, leaves stderr empty, and writes the normalized
Task snapshot as sorted, two-space-indented UTF-8 JSON followed by one newline.
The same JSON bytes are stored at `.seal/tasks/<TASK_ID>.json`.

Handled Task, catalog, duplicate, and ordinary file-input failures exit 2,
leave stdout empty, and write one `error: <message>` line to stderr. Git
executable, repository discovery, and missing-current-HEAD failures exit 3.
Argument-parser failures also exit 2 but use the parser's usage/error rendering;
Go compatibility is based on the command category and exit code, not
Python-specific usage text.

The Reference pipeline fixes observable error precedence:

1. Parse the command shape.
2. Resolve the enclosing Git repository.
3. Read the Task Spec JSON object.
4. Read and validate `.seal/checks.json`.
5. Validate and normalize the Task.
6. Resolve the current full `HEAD` commit id.
7. Save the snapshot.
8. Render the returned snapshot to stdout.

No step runs a check, reads or writes Evidence, inspects worktree cleanliness,
or advances an Acceptance lifecycle.

## Task Spec input

The input must be one JSON object with exactly these fields:

```text
schema_version, id, type, objective, scope, checks, risk, verifier
```

The contract is:

- `schema_version` is the exact JSON integer `1`. Boolean and floating-point
  representations such as `true`, `1.0`, and `1e0` are rejected.
- `id` is a non-empty string matching
  `^[A-Za-z0-9][A-Za-z0-9_-]*$`.
- `type` is one of `bugfix`, `feature`, `refactor`, `test`, `docs`, or
  `config-infra`.
- `objective` is a non-empty string. It is not trimmed; whitespace-only and
  leading or trailing whitespace are preserved.
- `risk` is one of `low`, `medium`, or `high`.
- `scope` is a non-empty array of non-empty strings.
- `checks` is a non-empty array of catalog-name strings or inline check
  objects.
- `verifier` is an object with required exact-boolean `required` and optional
  non-empty-string `preferred_runner`. Other fields are rejected.

The imported `contracts/task.schema.json` records this input shape, but a
Draft 2020-12 validator alone is not exact: the Schema considers numerically
integral JSON values such as `1.0` integers, while the Reference rejects them.
Its regular expressions also accept a trailing newline in an otherwise valid
Task id and do not reject non-letter drive-like Scope prefixes such as `1:`;
the frozen runtime validator rejects both.
The saved snapshot also contains `baseline`, which is intentionally absent
from the input Schema.

## Scope normalization

Each Scope entry is normalized independently and input order and duplicates
are preserved:

1. Replace `\` with `/`.
2. Reject POSIX absolute, Windows absolute, drive-prefixed, and `..` component
   paths.
3. Remove empty and `.` components.
4. Join the remaining components with `/`, or use `.` if none remain.

Normalization changes examples such as `./src//seal/` to `src/seal` and
`tests\unit` to `tests/unit`. Scope is repository-relative metadata; Task
creation does not require the referenced paths to exist.

## Check catalog and normalized checks

The catalog path is exactly `.seal/checks.json`. It is required even when all
Task checks are inline. The input catalog is read-only and no normalized copy
is written.

The catalog top level must be an object containing required `checks` and
optional `schema_version`; other fields are rejected. If present,
`schema_version` must be the exact JSON integer `1`. `checks` accepts:

- a list of full check definitions; or
- an object whose key supplies the check name. An absent embedded `name` is
  injected, a matching `name` is accepted, and a different `name` is rejected.

An empty catalog is valid when the Task uses only inline checks. Duplicate
names in list form are rejected. Object-member order has no effect on resolved
Task order. Catalog reads follow ordinary filesystem symlinks in the frozen
Reference; the new writer confinement rule does not change that read contract.

A check definition contains exactly required `name`, `argv`, and `required`,
plus optional `timeout_seconds`:

- `name` is a non-empty string.
- `argv` is a non-empty array of non-empty strings. It is stored as data and is
  not executed by Task creation.
- `required` is an exact boolean.
- `timeout_seconds`, when present, is an exact positive JSON integer. The
  Reference accepts arbitrary-precision values up to its JSON integer token
  limit; boolean and floating-point values are rejected.

Catalog string references are replaced by full normalized definitions. Inline
definitions are normalized by the same rules. Task check order and duplicate
Task references are preserved. Unknown references are rejected.

## Baseline and deterministic snapshot

After Task and catalog validation, the Reference invokes the semantic
equivalent of:

```text
git -C <repository> rev-parse HEAD
```

The full current commit id is stored as `baseline`. A dirty index, dirty
worktree, untracked files, a nested invocation directory, and an input file
outside the repository are accepted and left unchanged. An unborn repository
or otherwise unavailable current HEAD exits 3. There is no baseline override.

The normalized snapshot contains exactly:

```text
schema_version, id, type, objective, scope, checks, risk, verifier, baseline
```

JSON object keys are sorted during rendering; array order is semantic and
preserved. Unicode is emitted directly rather than ASCII-escaped when it is a
valid Unicode scalar sequence.

## Frozen storage behavior and limitations

The Reference creates `.seal/tasks` as needed. Without `--force`, opening the
destination exclusively protects an existing Task and exits 2 without changing
its bytes. With `--force`, the Reference truncates and rewrites the destination
in place. It creates no backup, history, lock, latest pointer, or other Task.

The frozen writer is neither confined nor atomic:

- `.seal`, `.seal/tasks`, and destination symlinks may be followed, including
  targets outside the repository.
- force replacement can truncate the old snapshot before all output has been
  encoded and written.
- directory and file modes inherit process umask rather than a fixed contract.

These are Reference security and durability limitations, not behavior to copy
into the new Go writer.

## Approved Go writer behavior

For this writer only, Go rejects an unsafe destination with exit 2. It rejects
a symlink at `.seal`, `.seal/tasks`, or the destination (including broken and
looping links), a destination resolving outside the repository, traversal, and
a non-directory ancestor. This approval does not alter `task show`, `run show`,
catalog reads, or Evidence symlink semantics.

Go prepares the complete snapshot bytes before any write, uses a same-directory
temporary regular file with deterministic permissions, and publishes it
atomically. Under ordinary supported filesystem operations, a failed create
leaves an existing destination unchanged and removes temporary residue.
No-force publication must not overwrite a winner in a race; force may replace
only the exact target Task.

The implementation uses a same-directory hard link for no-force, followed by
removal of the temporary name, and a same-directory rename for force. This was
validated on macOS/APFS. A filesystem without hard-link support cannot provide
the implemented no-clobber publication path, and identical force-replacement
semantics are not claimed for every non-POSIX platform.

The static symlink rejection is validated for a stable filesystem layout.
`os.Root` confines ordinary symlink resolution to the repository, but its
component opens follow repository-internal symlinks and do not prohibit bind
mounts. A hostile concurrent ancestor swap can therefore redirect publication
within the repository even though it cannot use the tested ordinary symlink
escape to write outside. Race-proof no-follow component opens would require
platform-specific primitives beyond this standard-library slice.

There is one post-publication filesystem-fault window that is not transactional:
if the no-force hard link succeeds but every attempt to remove the temporary
name fails, the complete destination already exists and temporary residue may
remain after the command reports success. Link creation is therefore the
no-force commit point; reporting failure afterward would falsely claim that no
Task was created, while removing the destination to roll back could delete a
concurrent force replacement. Normal, duplicate, validation, and
pre-publication failure paths do not use this exception; the residual is
reported rather than hidden behind a generic transaction abstraction.
An independent failure of temporary-name cleanup can likewise leave residue
after a pre-publication failure, although the destination remains unchanged.
This double-fault path is best-effort cleanup rather than a transactional
guarantee and was not fault-injected in this slice.

## Blocked Reference edge cases

Five newly observed cases combine incompatible requirements and are not claimed
as exact parity in this slice:

- A JSON string containing an unpaired UTF-16 surrogate passes the frozen
  structural validator, then the Reference exits 1 while encoding output. A
  first create can leave an empty destination and force can truncate existing
  bytes. Reproducing that artifact contradicts the required atomic failure and
  destination-preservation contract. Go must not publish a partial snapshot;
  the exact compatibility classification remains blocked pending an explicit
  canonical decision.
- With `--force`, an input file that is itself the destination Task path is
  overwritten by the normalized snapshot in the Reference. Reproducing that
  behavior contradicts unconditional input-file immutability. Exact alias-case
  behavior remains blocked; ordinary distinct input paths are immutable.
- A catalog symlink may resolve to the destination Task file. The Reference
  reads that file as the catalog and then force-replaces it with the Task
  snapshot, which changes the bytes visible through `.seal/checks.json`.
  Reproducing this aliases the otherwise immutable catalog to the allowed
  destination write. This exact alias case is blocked with the input/destination
  alias pending the same policy decision; ordinary catalog symlink reads remain
  compatible.

Additional parser-limit classifications are also blocked rather than counted as
ordinary exact parity:

- Raw invalid UTF-8 in the input or catalog escapes the Reference as an
  unhandled `UnicodeDecodeError` with exit 1, while the requested public table
  classifies malformed input and catalog data as exit 2.
- A 4,301-digit JSON integer token escapes CPython's configured conversion
  limit as an unhandled `ValueError` with exit 1; 4,300 digits can be accepted
  for a positive timeout. The requested public table otherwise names only
  exits 0, 2, and 3 for Task creation.

The Reference's modes are umask-dependent (`0755`/`0644` under umask `022` and
`0700`/`0600` under umask `077`), so there are no exact deterministic Reference
modes to reproduce. This slice fixes newly created Task directories at `0755`
and snapshots at `0644`, the common Reference result under umask `022`, and
records that choice as writer hardening rather than exact byte metadata parity.

The current Go candidate fails closed with handled exit 2 for the blocked
encoding, integer-limit, surrogate, and input/catalog alias cases. Those
outcomes preserve the requested no-partial-write and input/catalog immutability
properties, but they are excluded from the exact Reference-parity count until a
canonical transition decides their public classification.

## Write set and exclusions

For a successful, distinct-input creation, the allowed write set is only:

```text
.seal/tasks/                    # create missing real directories as needed
.seal/tasks/<TASK_ID>.json      # create or explicitly force-replace
```

The input Task JSON, `.seal/checks.json`, Git metadata, HEAD, index, tracked or
untracked source, Evidence, other Tasks, Seal Legacy, and Harness Toolkit are
unchanged. Verification, S0/S1/S2, Run ids, manifests, Completion, Bundle,
Verdict, Reviewer execution, latest-id inference, retry, and repair are outside
this slice.
