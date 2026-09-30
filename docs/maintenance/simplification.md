# Logic simplification backlog

This backlog records the 2026-09-30 review of duplicated, repeated, or
over-built logic and the work that follows from it. It is a maintenance record,
not a contract; the contracts under `conformance/` and `docs/run-export.md`
remain authoritative.

`deadcode` reported no unreachable functions. The findings below are about
duplicate implementations, repeated work, and bounded over-design.

## Decisions

- Each task lands as its own stacked pull request, in the order below.
- Task 6 (export inventory) was approved for simplification on 2026-09-30,
  accepting weaker concurrent-change detection.
- Task 7 (removing Legacy byte parity) produces a proposal only. Removing the
  parity code requires an approved canonical transition.

## Tasks

| # | Task | Status | Pull request |
|---|---|---|---|
| 1 | CLI cleanup: list `run export` in the usage synopsis, drop the redundant informational-command branch, drop the unused Completion observation hook | done | simplify/01-cleanup |
| 2 | `verify` repository-root discovery matches the frozen Reference (`git rev-parse`, like `task create`) | pending | |
| 3 | One Python-compatible JSON implementation (`internal/pyjson`) for `runstate`, `taskstate`, and `sourceobs` | pending | |
| 4 | Shared no-replace publication and Windows private-descriptor helpers inside `runstate` | pending | |
| 5 | Fewer repeated Git subprocesses during source observation | pending | |
| 6 | Simpler `run export` concurrent-change detection | pending | |
| 7 | Go canonical transition proposal for Legacy byte-parity code | pending | |

## Task notes

### 1. CLI cleanup

- `commandUsage` omitted `run export` even though the fallback error names it.
- `isInformationalCommand` only skipped `os.Getwd`; the `Getwd` failure path
  already passed an empty cwd, so help and version output never depended on it.
- `completeHooks.observeSnapshot` was never set by a test.

### 2. Repository-root discovery

The frozen Reference resolves `task create`, `task show`, and `verify` with
`git rev-parse --show-toplevel`, and `run show` and `complete` by walking up to
the nearest `.git`. Go `verify` walked up, so `GIT_DIR`, `core.worktree`, or an
empty inner `.git` directory could make `task create` and `verify` use
different roots.

### 3. JSON compatibility

`runstate` and `taskstate` each implemented Python-compatible decoding and
pretty printing (about 700 duplicated lines) with different surrogate and
constant strategies. `runstate` float rendering used Go's shortest `%g`, which
switches to exponent form at 1e6 where Python `repr` does not.

### 4. Platform publication helpers

Evidence and Completion publication repeat the same native no-replace rename,
Windows open-for-rename, rename-information, and private security-descriptor
code. Error wrapping, access masks, and identity checks stay at each call site.

### 5. Source observation

Each observation re-resolves the repository with 14 Git subprocesses, runs the
repository guards twice, and change collection reads a baseline tree it never
uses. The immutable baseline tree and blob identities can be shared within one
`verify`; HEAD, index, guards, and untracked state are still re-checked.

### 6. Export inventory

The before/after inventory hashes metadata documents under separate 100,000
entry, 64 MiB, and depth-32 bounds. The simplified form compares directory
listings and object identity only.

### 7. Canonical transition

See the proposal added by task 7.

## Follow-ups not scheduled

- Snapshot collection starts one `git cat-file blob` process per baseline blob;
  the Reference uses one `git cat-file --batch` process.
- `task create` checks the integer digit limit only after a successful decode,
  so a long integer followed by a syntax error exits 2 instead of 1, and a
  nesting-depth failure exits 2 without an approved divergence. Aligning either
  changes public exit categories.
- `task create` rejects an unpaired surrogate anywhere in the raw Task Spec or
  catalog, including unused catalog entries, which is broader than the
  Reference encoding failure.
- `checkrun` and `sourceobs` process-tree control is not shared: their retry
  bounds, `ESRCH` mapping, and build constraints differ, and only about 30
  lines overlap.
