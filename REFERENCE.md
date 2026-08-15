# Frozen Python Reference

The Go successor is anchored to the following immutable Python reference.

| Field | Value |
| --- | --- |
| Legacy repository | <https://github.com/jgoneit/seal-legacy> |
| Reference branch | `seal-legacy` |
| Annotated reference tag | `python-reference-v0.3.0-dev.0-94bb931` |
| Exact reference commit | `94bb931a7934efe31549d4c21dc7153e43f27a08` |
| Python Core version | `0.3.0.dev0` |
| Offline bundle filename | `seal-python-reference-94bb931.bundle` |
| Bundle SHA-256 | `84cb52f42d4ceb73fcc01866f645f9e16fdd0ce4e777a145bfda0456bf3acb49` |

The bundle checksum records the offline migration backup. The public reference
branch and annotated tag both resolve to the exact commit above.

## Preserved release history

Seal Legacy retains the historical releases and tags:

- `v0.1.0`
- `v0.1.1`
- `v0.2.0`
- `v0.2.1`

They are historical artifacts. This Go bootstrap creates no tag or release and
does not move or reinterpret an existing one.

## Contract meaning to reproduce

Conformance work may carry forward these externally observable meanings:

- Task identity, baseline, and Scope
- catalog-derived normalized checks and required versus optional checks
- S0, S1, and optional S2 source binding
- the distinction between a valid failed Run and missing or corrupt Evidence
- one authority for stored Run integrity
- exact, read-only Run summary semantics
- deterministic completion decision order
- machine-readable standard output and stable exit codes
- confined paths, atomic writes, manifests, and content digests
- explicit trust and security limitations
- behavioral regressions captured as future conformance scenarios

## Intentionally not reproduced

The Go codebase does not copy or mirror:

- Python package, class, private-helper, or packaging structure
- the Python test suite as implementation scaffolding
- Codex Plugin, Skill, marketplace, cache, or UI workflow behavior
- historical planning documents or compatibility branches
- provider SDKs or automatic reviewer execution
- Knowledge or Security functionality
- retry, repair, latest-identity inference, or process control

## First conformance slice candidate

The next recommended slice is exact-identity, read-only compatibility for:

```text
seal task show <TASK_ID>
seal run show <TASK_ID> --run-id <RUN_ID>
```

That slice should use behavioral fixtures derived from the frozen reference and
compare JSON envelopes, integrity failures, and exit codes. It must not write
Evidence, infer a latest identity, execute a reviewer, or implement completion.
Fixture creation and the slice itself are outside this bootstrap.
