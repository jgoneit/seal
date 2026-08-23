# Repository instructions

These instructions apply to the entire repository.

## Product boundary

- Seal belongs only to the Acceptance plane.
- Native Agents own planning, implementation, and execution.
- Seal exposes deterministic state and decisions; it does not own workflow
  transitions or invoke other Toolkit modules.
- The Codex Plugin is a thin adapter over the documented CLI. It must not make
  Acceptance decisions itself or take workflow authority from Native Agents.
- The frozen Python implementation identified in `REFERENCE.md` is the
  behavioral reference until an explicit canonical transition is approved.

## Migration rules

- Reproduce external contracts and outcomes; do not translate Python package,
  class, helper, Legacy Plugin, Legacy Skill, or test-suite structure.
- Justify new behavior with a conformance scenario from the reference.
- Do not add new product features during compatibility migration.
- Do not infer a latest Task or Run, retry or repair work, execute reviewers,
  enforce security policy, or add Knowledge-plane behavior.
- Do not create a common Toolkit runtime, event bus, provider registry, central
  state machine, or future-facing empty abstraction.
- Do not create `.seal` or `.harness` runtime data during bootstrap work.

## Implementation rules

- Prefer the Go standard library.
- Do not export production APIs or add production indirection solely for tests.
- Never execute runtime shell command strings. A future Git integration must
  pass an explicit argv to `os/exec`.
- Keep public output and exit behavior deterministic and machine-consumable.
- Keep prose proportional: preserve unique API semantics and link to the
  canonical contract instead of restating it.
- Keep each change within one conformance slice and document what remains
  intentionally unsupported.

## Validation

Run these checks for Go changes:

```bash
gofmt -w cmd
go test ./...
go vet ./...
go build ./cmd/seal
```

Also smoke-test the built binary with `--help` and `--version`. Do not describe
an unavailable or skipped check as passing.
