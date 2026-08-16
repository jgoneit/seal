# Frozen contract origins

These files are byte-for-byte copies from the frozen Python Reference at
commit `94bb931a7934efe31549d4c21dc7153e43f27a08` (`0.3.0.dev0`). They are
immutable compatibility inputs; they are not independently authored Go
contracts.

| Local file | Reference path | SHA-256 | Use in this slice |
| --- | --- | --- | --- |
| `task.schema.json` | `schemas/task.schema.json` | `28be5f1b39d695ed5649157291087ab9aeee480a0e42637d4f6df3711a37f269` | Documents the **Task create input** only. It is not applied by `task show`, and a normalized stored Task has the additional `baseline` field. |
| `verification.schema.json` | `schemas/verification.schema.json` | `c721b1285c639a28cb3472434c999ecd64af17698f55c605aed1f383178edea3` | Documents the persisted source-bound verification v2 object consumed as one part of canonical Run validation. |

The Reference has no canonical standalone schema files for
`changed-files.json`, `checks.json`, Source Snapshots, or
`run-manifest.json`. This slice does not invent replacements. Bundle, Verdict,
Completion, verifier-prompt, Plugin, and UI contracts are outside the
implemented Task-create and read-only query slices and are intentionally not
copied.
