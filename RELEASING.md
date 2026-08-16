# Releasing Seal

Seal releases are standalone native binaries. Release preparation changes the
checked-in CLI version; the release workflow never injects or rewrites a
version at build time.

## Release contract

A release tag must:

- be an annotated Git tag whose name starts with `v`;
- point at the exact release-preparation commit;
- equal `v` followed by the output of `seal --version`; and
- never be moved, deleted for reuse, or republished with different assets.

The tag workflow builds and smoke-tests these five native targets on matching
GitHub-hosted runners:

| Runner | Asset |
|---|---|
| `ubuntu-24.04` | `seal_<VERSION>_linux_amd64.tar.gz` |
| `ubuntu-24.04-arm` | `seal_<VERSION>_linux_arm64.tar.gz` |
| `macos-15-intel` | `seal_<VERSION>_darwin_amd64.tar.gz` |
| `macos-15` | `seal_<VERSION>_darwin_arm64.tar.gz` |
| `windows-2025` | `seal_<VERSION>_windows_amd64.zip` |

Each native job runs `go test ./...`, builds without cross-compiling, and runs
the resulting binary's `--help` and `--version`. Publication starts only after
all five jobs succeed. The publish job rejects missing or additional archives,
creates a bytewise filename-sorted `checksums.txt`, and creates the GitHub
Release with those six files. Tags containing `-rc.` create prereleases.

The workflow grants `contents: write` only to the publish job. All third-party
workflow actions are official GitHub actions pinned to full commit SHAs. Seal
does not publish signatures or attestations in v1.

## Release procedure

1. Confirm the intended release version and update the CLI default version and
   release-facing documentation in one release-preparation commit. Do not make
   that version change in an installer-only change.
2. Run the repository validation gates and record actual results. At minimum:

   ```text
   gofmt
   go test ./...
   go test -race ./...
   go vet ./...
   go build ./cmd/seal
   seal --help
   seal --version
   git diff --check
   ```

3. For a release candidate, create a new annotated tag such as
   `v0.3.0-rc.1`. If it needs a correction, use `rc.2` or later; never reuse the
   old tag.
4. Push the exact release commit and annotated tag. The Release workflow checks
   the tag object and version before publishing.
5. Install the published archives through both installers on clean supported
   hosts and confirm the displayed absolute path and version.
6. Accumulate 20 real user Tasks in the RC acceptance report before preparing
   the final version. The report must record zero false acceptances, zero
   Evidence-corruption bypasses, zero source-binding bypasses, and zero
   repeated false source mismatches. Synthetic fixtures do not replace that
   product gate.
7. For the final release, update the checked-in version to the final value in a
   new release-preparation commit and annotate exactly that commit. Do not turn
   an RC tag or its assets into the final release.

An existing GitHub Release causes publication to fail. Investigate a partial
publication rather than overwriting assets or moving the tag.

## Installer test boundary

`install.sh --version <TAG>` and `install.ps1 -Version <TAG>` are the only
public installer forms. The installers do not select a latest release.

Repository integration tests set two environment variables:

- `SEAL_RELEASE_BASE_URL` points to an isolated local release server.
- `SEAL_INSTALL_DIR` points to a disposable absolute destination.

These variables exist only for deterministic installer tests; they are not a
supported alternate distribution channel or system-wide installation mode.
The production defaults remain `$HOME/.local/bin` and
`$LOCALAPPDATA\Programs\Seal\bin`.
