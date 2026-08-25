# Verify compatibility contract

This contract freezes the Go v1 behavior of:

```text
seal verify <TASK_ID>
```

The behavioral reference is Seal Legacy Core `0.3.0.dev0` at
`94bb931a7934efe31549d4c21dc7153e43f27a08`. The Go implementation reproduces
the persisted Task, checks, source, Scope, verification, and manifest meanings,
with the explicitly approved execution-resource, writer-safety, and
whole-directory publication differences below.

## Public result and exits

A successful call prints one sorted, indented UTF-8 JSON object followed by one
newline. Stderr is empty.

```json
{
  "evidence_path": "<absolute .seal/evidence/TASK_ID/RUN_ID path>",
  "run_id": "<32 lowercase hexadecimal characters>"
}
```

Exit `0` means that a complete, self-validated, manifest-valid Run was
published. It does not mean that checks, Scope, or source stability passed.
The handled exit categories are:

| Exit | Meaning |
| --- | --- |
| `0` | one complete Run was committed |
| `2` | CLI, Task identity, saved Task, or check-definition failure, including a saved effective timeout above 300 seconds |
| `3` | Git, source observation, check infrastructure, execution-bound, or publication failure |
| `1` | supported runtime-representation or success-stdout failure |

Handled state and execution failures write no stdout and one `error: ...` line
to stderr. CLI shape failures write that error followed by the shared usage
synopsis. A stdout failure after commit returns `1` and does not roll back the
already published Run. Verify never returns Completion exits `4` through `9`
for mechanical outcomes.

The Basic execution boundary uses these exact handled messages before the CLI
adds its `error: ` prefix:

```text
saved Task snapshot checks[N].timeout_seconds must be at most 300 seconds.
Verification exceeded the 600-second wall-clock safety budget.
checks[N].stdout exceeded the 8388608-byte safety limit.
checks[N].stderr exceeded the 8388608-byte safety limit.
Verification check logs exceeded the 33554432-byte aggregate safety limit.
checks[N] output pipes did not close after process termination.
```

The command accepts one positional Task identity and no feature flags.
`-h`/`--help` short-circuits like the frozen parser. `--base-ref`, latest-Run
selection, retry, repair, and extra positional arguments are rejected.

## Phase order

The externally meaningful order is:

1. validate CLI identity and resolve the enclosing Git worktree;
2. read and fully prevalidate the saved Task and resolved check definitions;
3. collect stable source snapshot S0;
4. allocate a private staging Run and write the saved Task snapshot;
5. run every check in saved order, continuing after failure, timeout, or launch
   failure;
6. collect stable source snapshot S1;
7. collect layered changes and binary diff after S1;
8. write source, change, check, and verification artifacts;
9. write the manifest last and self-validate with the same authority used by
   `run show`;
10. atomically publish the complete Run directory and print its identity.

S0 and S1 each observe final product source exactly twice without retry.
Disagreement within either bounded collection is a repository failure. A
stable S0 that differs from a stable S1 is instead recorded as
`source_stable_during_checks: false`; the Run still publishes with exit `0`.

Prevalidating Task/check definitions and withholding the entire Run until the
final rename are approved Go writer divergences. The frozen writer may allocate
an incomplete public Run before detecting some Task or later repository
failures.

One cumulative 600-second Verify deadline starts immediately after the saved
Task and all saved check definitions pass admission validation, before S0. It
covers source observation, every check, change collection, artifact creation,
and all other work before native publication begins. Deadline expiry before
that commit boundary terminates any current managed check tree, aborts the
private staging Run, and returns exit `3` without publishing a Run. Once native
publication begins, the commit result takes precedence over a concurrently
expiring deadline. Generated Evidence self-validation reads and hashes files in
bounded chunks with deadline checks between chunks.

## Check execution

- Checks execute sequentially in saved order with explicit argv and no implicit
  shell. A saved argv may itself name a shell, but the runner never constructs
  or interprets a shell command string.
- The repository root is cwd. The caller's environment is inherited unchanged.
  Stdin is the platform null device, so reads receive noninteractive EOF rather
  than caller input.
- The default and maximum effective timeout are exactly 300 seconds. A saved
  positive timeout at or below 300 remains exact; a larger saved effective
  timeout is rejected before S0 or private staging with exit `2`. It is never
  clamped or recorded as a timed-out check.
- Stdout and stderr remain raw byte streams in private `0600` files. Each
  stream accepts at most 8,388,608 bytes, and all check log streams in one
  Verify accept at most 33,554,432 bytes in aggregate. Exactly the cap is
  valid. The first byte beyond either cap terminates the current managed
  process tree and aborts Verify with exit `3`, no published Run, and no
  truncated log or truncation marker.
- A nonzero exit, timeout, or launch failure is a failed check result and does
  not stop later checks. A launch failure records `exit_code: null` and an
  explanatory raw stderr line.
- A per-check timeout is an ordinary recorded check outcome and later checks
  continue. The cumulative Verify deadline or a log-limit overflow instead
  aborts the entire unpublished Run.
- After a managed process tree is terminated, collectors receive up to one
  second to drain and close both output pipes. If a deliberately escaped
  descendant still holds a pipe, the runner closes its readers and aborts the
  unpublished Run rather than hanging or publishing a truncated successful
  log.
- POSIX uses a new session and process-group TERM, 200 ms grace, then KILL. It
  also terminates ordinary descendants left after a successful root exit. A
  descendant that deliberately creates a new session can escape portable
  process-group ownership, but the bounded pipe drain prevents it from holding
  Verify open.
- Windows creates the process suspended, assigns it to a kill-on-close Job,
  and resumes only after assignment. Job creation, assignment, or resume
  failure is recorded as launch failure; there is no shell or `taskkill`
  fallback. Ordinary descendants remain in the Job. As in the frozen runner, a
  child that deliberately requests `CREATE_BREAKAWAY_FROM_JOB` may escape; that
  documented nested-Job compatibility limit is not a security boundary.

Check logs are opened through the retained staging-directory handle with
exclusive creation. A mutable reconstructed staging pathname is not used.

## Source and Scope

The same snapshot collector is used for S0, S1, and later S2. Its digest binds
the saved baseline and every baseline-relative final product entry, including
content or symlink-target bytes, add/delete state, executable mode when exposed
by POSIX or tracked Git metadata, size, and SHA-256. Source identity is
independent of whether equivalent final bytes came from committed, staged, or
unstaged state.

Layered changes preserve committed, staged, unstaged, and nonignored untracked
records in that order. Rename Scope requires both old and new paths to be in
Scope. The binary patch preserves those product layers. Seal-owned metadata is
excluded from source and change decisions except `.seal/checks.json`, which is
product source.

Supported states include ordinary and linked non-bare worktrees, valid or
detached HEAD, 40- or 64-character object identities, regular and binary files,
owner-executable files, Git symlinks, gitignore, and ordinary renames. Windows
preserves executable mode for tracked files through the Git index; an untracked
Windows file has no native POSIX executable bit and is recorded as `100644`.

The collector fails closed for unmerged indexes, active replace refs, sparse or
hidden index state (`skip-worktree` or `assume-unchanged`), changed gitlinks,
invalid UTF-8 paths, special filesystem nodes, and Windows reparse points not
represented as supported Git symlinks. Git is always invoked with explicit
argv and replace-object processing disabled.

Source observation cooperates with the cumulative Verify deadline. Git
subprocesses receive the deadline context. POSIX Git runs in a private session;
Windows Git starts suspended and resumes only after joining a non-breakaway
kill-on-close Job. Git output-pipe waiting is bounded to one second so a
deliberately session-escaped POSIX descendant cannot hold Verify open, although
portable process-group APIs cannot terminate that escaped process itself. File
walking and hashing check for cancellation between operations. An individual
filesystem syscall blocked inside the operating system is not made preemptible
by that cooperative Go context; the 600-second deadline is therefore a bounded
orchestration contract, not a guarantee that every hostile or stalled
filesystem returns on time.

## Complete Run artifacts

Every published directory contains:

```text
task.json
checks/<two raw log files per check>
source-before-checks.json
source-after-checks.json
changed-files.json
diff.patch
checks.json
verification.json
run-manifest.json
```

`verification.json` schema v2 records Task/Run identity, baseline, product
changes and Scope violations, required-check aggregate, S0/S1 digests and
stability, mechanical result, evidence-file paths, timestamp, and duration.
Mechanical pass is exactly:

```text
scope_pass && required_checks_pass && source_stable_during_checks
```

Optional check failure or timeout does not make `required_checks_pass` false.

The schema-v1 manifest is the final completeness marker. Its sorted records
contain the raw byte length and SHA-256 of every mechanical artifact and log,
excluding the manifest itself. `evidence_sha256` hashes canonical compact JSON
of exactly schema version, Task id, Run id, and those records; `created_at` is
excluded from the digest. Extra physical consumer files remain outside the
mechanical manifest contract.

Every Go-produced Run must be accepted by both frozen Python `run show` and Go
`run show` with the same public identity, check, Scope, source-stability,
mechanical-result, and manifest-digest semantics.

## Publication and safety boundary

- Run ids are UUID-v4-shaped 32-character lowercase hexadecimal values.
  Allocation retries at most 100 true collisions. Exhausting those retries is
  a publication failure at exit `3` with no new Run or staging residue. The
  frozen implementation classified this synthetic exhaustion as exit `2`; the
  Go v1 exit table explicitly approves the narrower publication classification.
- Staging is a sibling `.tmp-<RUN_ID>` directory. POSIX systems enforce mode
  `0700`. Windows applies a protected DACL that grants inheritable full access
  only to the current process-token user and LocalSystem before any artifact is
  written.
- The writer records the created staging object's identity; on Windows that
  identity comes from the original `NtCreateFile` handle before it is closed.
  The subsequently retained `os.Root` must identify the same object before any
  artifact is written. A create-to-reopen name swap is exit `3`, and the
  replacement object is not removed or modified.
- Writer ancestors `.seal`, `.seal/evidence`, and the Task Evidence directory
  must be real directories. Symlinks, non-directories, and repository escape
  are rejected.
- Generated documents and check logs use retained `os.Root` handles. Writer
  files use exclusive creation and mode `0600`.
- Artifact files are synchronized before publication. POSIX also synchronizes
  private directories before the rename; Windows has no portable directory
  flush here and claims atomic visibility on NTFS, not crash durability.
- Linux uses `renameat2(RENAME_NOREPLACE)`, macOS uses
  `renameatx_np(RENAME_EXCL)`, and Windows uses handle-relative
  `NtSetInformationFile` without replacement. There is no pathname or shell
  fallback.
- A failure before the native rename removes the bound staging directory and
  never creates a final Run. Injected write, sync, manifest, validation, and
  publication failures, cumulative deadline expiry, and check-log overflow are
  regression-tested for this outcome.
- If cleanup itself also fails, the call returns repository exit `3`; a private
  `.tmp-*` directory may remain, but no final Run is reported. Cleanup never
  deletes a different object after a detected staging-name swap.
- After the native rename, the Run is committed. Parent-directory sync and
  handle-close failures are best effort so the CLI never reports pre-commit
  failure for an already visible Run.

The atomic-visibility support claim is limited to same-filesystem ordinary
POSIX filesystems that implement the named native operation and to NTFS.
Network filesystems and FUSE are not claimed. On POSIX, an adversarial process
running as the same operating-system account may still swap a staging name in
the final interval after identity validation; that hostile same-user race is
outside the v1 atomic object-binding claim. Static symlinks, broken links,
non-directory ancestors, collision races, and ordinary injected failures are
inside the tested boundary.

## Explicit exclusions

Verify does not collect S2, decide Completion, write completion state, read or
write Verdict, execute a reviewer, infer latest identities, retry or repair a
Run, truncate Evidence, install Git, or invoke another Toolkit module.
The execution bounds do not impose CPU or memory quotas, restrict network
access, filter or allowlist inherited environment variables, sandbox Source
writes, or add Reviewer or Verdict behavior.
