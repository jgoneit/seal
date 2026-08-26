# Seal Trust Model

Seal makes a local, deterministic Acceptance decision for one exact Task and
Run. This document states the upper bound of that claim. It does not add a
Security plane or change the canonical contracts under `conformance/`.

## What Seal establishes

For an exact Task and Run, Seal can establish that:

- the required Evidence files are present and internally consistent with the
  Run Manifest;
- the saved Task, Run identity, check outcomes, Scope result, and source
  snapshots agree under the documented contracts;
- the source observed before and after checks was stable when the recorded
  mechanical result says it was; and
- Basic Completion revalidated that Run and observed matching current source
  at its S2 collection point.

These are local, point-in-time properties. `verify` exit `0` means a complete
Run was recorded, not that it passed. Only `complete` exit `0` records accepted
Basic Completion.

## What Seal does not establish

The Run Manifest is a local corruption and tamper detector. It is not a digital
signature, remote attestation, authorization decision, trusted timestamp, or
defense against a malicious maintainer or another process with the same
operating-system authority.

Seal does not establish:

- the quality or completeness of selected checks;
- authorship or ownership of changed files;
- that pre-existing dirty changes belong to the requested Task;
- the trustworthiness or current state of Git, compilers, test tools, or
  external services;
- that source stays unchanged after S2 or after `complete` returns; or
- that a local Evidence directory was produced on a trusted host.

The frozen read compatibility also follows a saved Task path when it is a
symlink. `verify` may consequently consume that saved Task and its argv. This
known Legacy compatibility behavior is not an untrusted-repository security
boundary.

## Check execution boundary

Checks execute with explicit saved argv and no shell added by Seal. They run at
the repository root, inherit the caller's environment, and may use the network
or modify source if their own program does so. Seal supplies noninteractive
stdin and enforces its documented time and output limits, but it provides no:

- filesystem or process sandbox;
- CPU or memory quota;
- network restriction;
- environment-variable allowlist; or
- prevention or rollback of source writes.

Source comparison detects changes after execution; it is not prevention.

## Stored data and secrets

`.seal/tasks/` stores normalized Task metadata. `.seal/evidence/` stores raw
stdout, raw stderr, `diff.patch`, source metadata, check results, and the Run
Manifest. Task snapshots use the documented deterministic Task permissions;
Evidence writers use private platform-specific permissions where supported.
Neither should be treated as encrypted storage.

Check argv, logs, and diffs can contain credentials, tokens, personal data, or
proprietary source. Do not place secrets in check argv or output. Repositories
should track `.seal/checks.json` while ignoring the runtime directories:

```gitignore
.seal/tasks/
.seal/evidence/
```

Do not upload or commit runtime state unless an explicit review has established
that its contents are safe and the disclosure is intended.

## Retention

Seal has no automatic redaction, encryption, upload, pruning, or retention
timer. The repository operator owns retention and deletion. Removing a Task or
Evidence Run is allowed operationally, but future exact lookup, integrity
validation, and Completion from that state will no longer be possible.
