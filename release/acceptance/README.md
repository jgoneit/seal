# RC acceptance reports

This directory contains anonymous release-process reports for Seal release
candidates. These files are not Task, Evidence, Run, or Completion schemas and
do not change the public `seal` CLI.

`report-v1.schema.json` documents the closed report shape. The Go validator in
`internal/releasegate` is the release authority because it also enforces
cross-field, Git-tag, ancestry, highest-same-base-RC, and normalized-source
invariants.

## Recording rules

- Name a report exactly `<RC_TAG>.json`, for example
  `v0.3.0-rc.4.json`.
- Begin the observation window strictly after the exact annotated RC tag time.
  Record every consecutive eligible real-user Task in that window, including
  failures and unavailable or indeterminate attempts. Do not select only
  successful Tasks and do not reuse Tasks from an earlier RC.
- A stable release requires at least 20 entries. More entries may be retained
  when the consecutive window continues, up to the 10,000-entry format bound
  and the stricter 1 MiB report-file limit.
- Keep ordinals exact and contiguous. The top-level candidate identity and
  `exact_candidate_used` attestation bind every observation to the same RC's
  Core binary or Plugin surface, as applicable to the observed interface.
- Record initial worktree state as `clean` or `dirty`. Implicit Plugin use from
  a dirty worktree remains a truthful pilot observation but blocks stable;
  attribution correctness is recorded separately.
- Record zero false acceptances, Evidence-corruption bypasses,
  source-binding bypasses, wrong change attributions, and wrong Plugin routing
  decisions for a stable release. At most one false source mismatch is allowed
  across the complete stable report. An in-progress pilot must record incidents
  truthfully; syntax-only CI accepts typed incident booleans and does not apply
  these stable thresholds.
- Do not include repository names, user names, Task or Run ids, paths, source
  text, diffs, raw logs, Evidence digests, or free-form notes.

For every entry, record the selected-check count, optional-check count, Seal
tool-call count, check duration, Seal duration, whether a Task was created
before implementation, whether exact Task/Run binding was preserved, Evidence
result, mechanical result, Completion result, and whether the user understood
the result without follow-up. Routing or preflight stops may record zero checks
and zero tool calls. A Task recorded as created before implementation requires
at least one selected check and one Seal tool call; recorded Evidence requires
the same positive counts even when the Task was created after implementation.
The creation, binding, and usability observations are not stable-release
pass/fail gates. Durations are anonymous integer milliseconds; they are not raw
timing logs.

Use Evidence result `recorded`, `not-recorded`, or `indeterminate`; mechanical
result `pass`, `fail`, `unavailable`, or `indeterminate`; and Completion result
`accepted`, `rejected`, `failed`, `not-attempted`, or `indeterminate`. The
validator enforces only definitionally sound combinations: acceptance requires
recorded Evidence and a mechanical pass, a mechanical pass or failure requires
recorded Evidence, and unavailable or indeterminate Evidence cannot be reported
as accepted or rejected. Operational and publication failures therefore remain
truthfully representable without storing a raw exit code.

An in-progress report with fewer than 20 entries passes the syntax-only CI
check. Any critical fix or acceptance-surface change after the RC changes the
normalized digest and requires a new RC and a fresh report. Root operational
documents and checked-in report instances are excluded from that digest;
report schema, validator, workflows, Skill, CLI, and Core behavior remain in
scope.

Validate checked-in report syntax with:

```text
go run ./internal/releasegate/cmd/validate-report \
  --reports-dir release/acceptance \
  --syntax-only
```

The release workflow reads the complete report from the stable tag tree before
publishing. Working-tree or untracked report content is never stable authority.
RC releases do not require a report, but their tagged Core and Plugin surface
still must match the annotated RC version.
