# Read-only Run export

Run `seal run export --format json` from an explicitly selected repository.
`--format=json` is equivalent; `seal run export --help` describes the command.
Export inventories validated `.seal/tasks` snapshots and validates each exact
stored Run through the same boundary as `run show`. It never selects a latest
identity, runs checks, observes current source, repairs data, or writes files.
This is the narrow interface extension approved in
[the migration charter](../MIGRATION_CHARTER.md), not a change to existing query,
stored-schema, or Acceptance contracts.

The published `v0.3.0-rc.4` binary does not support this command. The reviewed
commit containing this change and the `seal-run-export/v1` contract identify
support; the unchanged `0.3.0-rc.4` version string alone does not. This change
does not publish a new RC or establish acceptance evidence for one.

## JSON contract

The envelope contains exactly these six fields. All arrays are present, using
`[]` when empty; nullable fields are present as JSON `null`, never omitted.

| Field | Type and meaning |
| --- | --- |
| `schema` | String, always `seal-run-export/v1`. |
| `exporter_version` | String from the reader executable's version. This does not identify the historical producer or uniquely identify a modified build. |
| `scan_complete` | Boolean; false whenever at least one issue is reported. |
| `tasks` | Array of `{task_id: string}` for validated saved Tasks, including Tasks without Runs. |
| `runs` | Array of the validated Run projections below. |
| `issues` | Array of `{code: string, task_id: string or null, run_id: string or null}`. |

Tasks sort by `task_id`; Runs sort by `(task_id, run_id)`, in ascending ASCII
order. Checks retain saved check order with a zero-based `index`. Issues retain
deterministic scan order: initial inventory, Task inventory, Run traversal,
then final inventory and concurrent-change detection. Issues are not
deduplicated: multiple unusable metrics can report the same code for one Run.
JSON object key order is not semantic.

Each `runs` element contains:

| Field | Type and meaning |
| --- | --- |
| `task_id`, `run_id` | Validated exact identity strings. |
| `evidence_sha256` | Validated Evidence digest string for local correlation. |
| `run_version` | String or null; currently always null because saved Evidence has no producer version. Never substitute `exporter_version`. |
| `timestamp` | Stored verification timestamp string, or null if unusable as RFC3339 with optional fractional seconds. This is not export time or Host completion time. |
| `mechanical_result` | String, `pass` or `fail`, from validated saved results. |
| `required_checks_pass`, `scope_pass`, `source_stable_during_checks` | Booleans from validated saved results. |
| `scope_violation_count` | Nonnegative integer; Scope paths are omitted. |
| `checks` | Array of check summaries described below. |
| `completion_record` | Object with `state` and nullable `completed_at`, described below. |

Every check has integer `index`; boolean `required`, `passed`, and `timed_out`;
nullable integer `exit_code`; and nullable number `duration_seconds`. Valid
historical integer exit codes are emitted as JSON numbers without truncation
to a machine integer or float. Consumers must decode them without precision
loss or explicitly report unsupported values; they must not substitute zero.
Durations are finite, nonnegative saved check runtime, not Seal overhead, user
waiting time, or time saved against another workflow.

The compatibility validator accepts some historical non-finite numbers and
loosely typed timestamp text. Export preserves those Runs, emits unusable
durations or timestamps as null, and reports `invalid_metric`. It does not
change Acceptance validation or silently turn unknown values into zero.

No repository path, check name, command, task objective, source content, Scope
path, raw log, or error prose is exported. Task and Run IDs can still carry
caller-chosen meaning, and digests allow correlation. This is not an
anonymization boundary; consumers publishing reports should pseudonymize IDs
and keep original identity mappings private.

## Historical Completion meaning

`completion_record.state` is `absent`, `recorded_pass`, or `invalid`.
`completed_at` is the stored timestamp string only for `recorded_pass`, otherwise
null. `recorded_pass` means a stored exact-v2 Completion matches the validated
Run's identity, Evidence digest, and saved source, and saved Basic gates are
consistent with pass. It does not prove current source Acceptance, actual Host
delivery, tool execution, independent task quality, or that local files were
never forged. Later source changes do not invalidate this historical report.

Rejected Completion attempts have no persistent record and cannot be recovered
from export. `absent` does not prove Completion was never attempted. An invalid
Completion retains its otherwise valid Run, reports `invalid_completion`, and
does not revise that Run's mechanical result. Seal does not rewrite, upgrade,
or call `complete` while reading a record.

## Partial scans and limits

| Exit | Meaning |
| --- | --- |
| `0` | Complete bounded scan, valid JSON, no issues. This includes an empty repository with no `.seal` state. |
| `8` | Valid JSON with `scan_complete:false`, static issues, and any successfully projected Tasks and Runs. |
| `2` | Invalid invocation; no export envelope. |
| `3` | Repository discovery/open failure; no export envelope. |
| `1` | Other operational, JSON rendering, or output failure; output may be absent or truncated and must not be consumed as a complete report. |

The static issue codes are:

| Code | Meaning |
| --- | --- |
| `invalid_identity` | Malformed Task/Run identity or unexpected Task snapshot filename. Malformed identity text is never echoed. |
| `unsafe_entry` | A directory, Task snapshot, or object binding fails export's safe-entry check. |
| `unreadable` | A needed entry cannot be inspected/opened, or disappears before it is read. |
| `invalid_task` | A saved Task fails parsing or canonical snapshot validation. |
| `invalid_evidence` | Exact Run validation fails; that Run is excluded. |
| `invalid_completion` | Historical Completion cannot be safely read or validated; the Run remains. |
| `invalid_metric` | An otherwise valid Run has an unusable timestamp or duration; that field becomes null. |
| `concurrent_change` | A bounded inventory detects change during the scan. |
| `scan_limit` | A scan/inventory bound is exceeded; the report is incomplete. |

Issue identities are null when unavailable or unsafe to disclose as an identity;
valid IDs remain available for local correlation. `scan_complete:false` means
some result or metric is unreliable or missing, even if every Run appears.
No issue prose, filesystem path, or raw validator error is exposed.

Each directory permits at most 10,000 entries; detection reads one extra entry.
An oversized directory contributes no arbitrary directory-order subset. At
most 10,000 candidate Runs are examined across Task directories, including
invalid candidates. Private `.tmp-` entries are ignored until final publication,
but still count toward directory entry limits. Each before/after inventory is
bounded by 100,000 entries, 64 MiB of metadata-document hashing, and recursive
depth 32 starting at `.seal/tasks` or `.seal/evidence`. A bound reports
`scan_limit`; it is never evidence of a complete inventory. Completion reads
are capped at 64 KiB; an oversized Completion reports `invalid_completion`.

Export rejects directory and saved Task symlinks even where older compatibility
queries permit them; it retains canonical Evidence validation. The scan is not
a transactional global snapshot. Before/after inventories compare object
identity, size, and modification time, plus hashes of metadata documents, to
flag detected publication, removal, replacement, or writes. Already validated
results remain available. An undetected change remains possible.

## Periodic consumers

Use an external timeout suitable for the repository: canonical Run validation
hashes saved Evidence, including potentially large logs. The inventory limits
do not cap that total validation work. Interrupted or incomplete output must
not advance a successful collection checkpoint.

Full scans revisit immutable Runs; deduplicate exact identities and Evidence
digests. A Completion can be added after a Run, so compare its exported state
on subsequent scans too. A changed Evidence digest under an existing identity
is a conflict, not another independent observation. Rescan after partial or
concurrent-change results; export supplies no atomic snapshot or durable cursor.
Missing Runs, partial scans, unsupported exporters, and skipped collection must
never be interpreted as successful task outcomes or zero tool activity.
