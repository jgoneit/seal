# Seal

Seal is an evidence-backed completion tool for coding agents.

Its one-sentence responsibility in the Acceptance plane is: determine whether a
specific Task and Run have intact Evidence, still match the relevant Source, and
satisfy the declared completion conditions.

## Place in the Jgoneit Agent Toolkit

Seal is an independent Acceptance module. It can be installed, used, versioned,
and removed without a Toolkit manager or another module.

The architecture keeps execution outside Seal:

- The Native Agent owns planning, implementation, tool choice, and execution.
- The user, Native Agent, or CI owns composition between independent tools.
- Seal exposes state, not transitions.
- Seal never invokes another Toolkit module or chooses what runs next.

## Migration status

This repository is an **experimental Go successor candidate**. The canonical
behavioral reference for Task, Evidence, manifest, source, Scope, and check
meanings remains [Seal Legacy](https://github.com/jgoneit/seal-legacy), the
preserved Python implementation. The Basic-only verifier policy and immutable
v2 Completion record are the explicit Go v1 canonical transition documented in
`MIGRATION_CHARTER.md`.

The canonical implementation has not switched to Go. This repository does not
translate the Python package structure. Its current compatibility surface
creates normalized Task snapshots, records manifest-valid Evidence for one
explicit Task, and provides exact-identity queries for stored Tasks and Runs
under `.seal`. It also evaluates one explicit Run against current source under
the approved Go v1 Basic Completion policy. `REFERENCE.md` identifies which
meanings remain frozen-Reference compatibility targets and which Completion
decisions moved to the Go v1 contract.

## Current CLI

The following operations are available:

```text
seal --help
seal --version
seal task create --file <TASK_JSON> [--force]
seal task show <TASK_ID>
seal verify <TASK_ID>
seal run show <TASK_ID> --run-id <RUN_ID>
seal complete <TASK_ID> --run-id <RUN_ID>
```

The checked-in version is `0.3.0-rc.3`.

`task create` validates and normalizes a Task Spec, resolves catalog check
references, records the repository's current full HEAD as its baseline, and
writes the snapshot to `.seal/tasks/<TASK_ID>.json`. Existing snapshots require
an explicit `--force` to be replaced.

`task show` reproduces the frozen reference's lookup behavior: it requires an
exact path-safe Task ID and returns a syntactically valid stored JSON object
without assigning Schema validity or Acceptance meaning to its fields.

`verify` reads one exact saved Task, observes source before and after its saved
checks, records raw check logs and layered Git evidence, writes the Run Manifest
last, self-validates the staged Run, and publishes the complete directory with
a native no-replace rename. Exit 0 means a manifest-valid Run was recorded; it
does not mean the mechanical result passed.

`run show` requires exact Task and Run IDs, validates the stored Evidence v2
documents and raw-byte Run Manifest through one canonical read boundary, and
then returns the transient `validated-run-summary/v1` view. A structurally
valid failed Run still returns exit 0; missing, unsafe, unsupported, tampered,
or contradictory Evidence returns exit 8.

`complete` validates one exact Run, observes current source as S2, applies the
fixed Basic Acceptance gates, and atomically records or reuses an immutable v2
`completion.json`. Only `verifier.required=false` is supported for Completion;
`verifier.required=true` returns exit 7, and Verdict files are not inputs.

Seal does not implement Bundle, Verdict, or Reviewer behavior. It does not
infer a latest identity, retry, repair, rerun checks during Completion, or
invoke another Toolkit module, and it has no `.harness` fallback.

## Codex Plugin

This checkout is also a skills-only local Codex Plugin. The Plugin makes the
documented CLI available to Native Agents; it does not bundle the `seal` binary
or add another Acceptance authority. After the Plugin and CLI are installed,
`@Seal` selects it explicitly. Its single Skill may also be selected for a
concrete implementation request when the target repository already opts in
with `.seal/checks.json`. Planning, explanation, read-only review, and
unconfigured repositories do not activate Seal implicitly.

The Native Agent still performs the work normally. The Skill creates the Task
before implementation, carries exact Task and Run identities, and reports
`verify`, `run show`, and Basic `complete` as distinct results. It does not add
approval stages, retry or repair loops, Reviewer behavior, or workflow state.
Malformed or unresolved selected checks and Core-unsupported timeouts block
every new Agent Task. The stricter duration and command-intent heuristics apply
only to implicit activation; for explicit use they are advisory, and the Native
Agent keeps control of the exact selected argv and timeout under its ordinary
permission and Scope boundaries.
Start a new Codex Task after installing or updating the Plugin so its Skill is
loaded.

To exercise the candidate from a Go checkout:

```bash
go run ./cmd/seal --help
go run ./cmd/seal --version
go run ./cmd/seal task create --file <TASK_JSON> [--force]
go run ./cmd/seal task show <TASK_ID>
go run ./cmd/seal verify <TASK_ID>
go run ./cmd/seal run show <TASK_ID> --run-id <RUN_ID>
go run ./cmd/seal complete <TASK_ID> --run-id <RUN_ID>
```

## Distribution

Tagged releases contain exactly five native archives plus a sorted
`checksums.txt`:

```text
seal_<VERSION>_linux_amd64.tar.gz
seal_<VERSION>_linux_arm64.tar.gz
seal_<VERSION>_darwin_amd64.tar.gz
seal_<VERSION>_darwin_arm64.tar.gz
seal_<VERSION>_windows_amd64.zip
checksums.txt
```

Each archive contains one standalone `seal` binary. Python and the Go toolchain
are not runtime dependencies. Git must already be installed when Seal evaluates
a repository.

For a published Linux or macOS release, download the installer from the same
tag and pass that exact tag explicitly:

```bash
tag=v0.3.0-rc.3
installer="$(mktemp "${TMPDIR:-/tmp}/seal-install.XXXXXX")"
trap 'rm -f -- "${installer}"' EXIT
curl -fsSL "https://raw.githubusercontent.com/jgoneit/seal/${tag}/install.sh" \
  -o "${installer}"
sh "${installer}" --version "${tag}"
```

The fixed target is `$HOME/.local/bin/seal`. The installer downloads only the
requested tag's native archive and checksum file, requires exactly one matching
SHA-256 entry, validates the archive shape and embedded binary version, then
smoke-tests the absolute installed path. It reports when the target directory
is not on `PATH`.

For a published Windows amd64 release, use Windows PowerShell:

```powershell
$Tag = "v0.3.0-rc.3"
$Installer = Join-Path ([IO.Path]::GetTempPath()) ("seal-install-" + [Guid]::NewGuid().ToString("N") + ".ps1")
try {
    Invoke-WebRequest -UseBasicParsing "https://raw.githubusercontent.com/jgoneit/seal/$Tag/install.ps1" -OutFile $Installer
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Installer -Version $Tag
} finally {
    Remove-Item -LiteralPath $Installer -Force -ErrorAction SilentlyContinue
}
```

The fixed Windows target is
`$LOCALAPPDATA\Programs\Seal\bin\seal.exe`. Neither installer uses `sudo`,
edits a shell profile, enables auto-update, installs Git, or makes a Seal
Acceptance decision. A download, checksum, archive, or version failure leaves
an existing target unchanged. If the installed-path smoke fails, the installer
restores the prior binary or removes a new target before returning. Windows
retries transient executable locks for a bounded interval; if restoration
still cannot finish, it preserves the prior bytes in a reported same-directory
backup. Releases provide checksums but no signatures or attestations.

See [RELEASING.md](RELEASING.md) for the native build and publication contract.

## Conformance-first development

Future functionality is added only as a narrow vertical slice justified by a
behavioral contract from the Python reference. The goal is reproduction of
established Acceptance meaning and outcomes, not new product design.

The first compatibility slice covers exact read-only `task show` and `run show`
identities using fixtures derived from the frozen reference. The second covers
the normalized `task create` snapshot writer. The third records one explicit
manifest-valid Evidence Run with `verify`. The fourth evaluates one explicit
Run under the approved Go v1 Basic Completion transition. See
[the read-only contract](conformance/read-only-contract.md),
[the Task-create contract](conformance/task-create-contract.md),
[the Verify contract](conformance/verify-contract.md), and
[the Completion contract](conformance/complete-contract.md), together with
[fixture provenance](conformance/README.md). These slices do not add retries,
repair, latest-Run inference, reviewer execution, or workflow orchestration.

See [MIGRATION_CHARTER.md](MIGRATION_CHARTER.md) for the migration invariants and
[REFERENCE.md](REFERENCE.md) for the frozen reference identity.

## Not part of Seal

Seal does not own agent execution, planning, reviewer selection, knowledge
authoring, security enforcement, deployment, CI orchestration, or a common
Toolkit runtime. It does not impose a model reasoning format, SubAgent count,
or Agent Team topology.
