#!/bin/sh

set -eu

usage() {
  printf '%s\n' 'Usage: install.sh --version <TAG>'
}

fail() {
  printf 'seal installer: %s\n' "$1" >&2
  exit 1
}

if [ "$#" -ne 2 ] || [ "$1" != "--version" ]; then
  usage >&2
  exit 2
fi

tag=$2
case "$tag" in
  v[0-9]*) ;;
  *) fail 'TAG must start with v followed by a digit.' ;;
esac
case "$tag" in
  *[!A-Za-z0-9._-]*) fail 'TAG contains unsupported characters.' ;;
esac

version=${tag#v}
release_base=${SEAL_RELEASE_BASE_URL:-https://github.com/jgoneit/seal/releases/download}
release_base=${release_base%/}

case "$(uname -s)" in
  Linux) seal_os=linux ;;
  Darwin) seal_os=darwin ;;
  *) fail 'this installer supports only Linux and macOS.' ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) seal_arch=amd64 ;;
  arm64 | aarch64) seal_arch=arm64 ;;
  *) fail 'this installer supports only amd64 and arm64.' ;;
esac

asset="seal_${version}_${seal_os}_${seal_arch}.tar.gz"
temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/seal-install.XXXXXX") || fail 'could not create a temporary directory.'
destination_stage=
destination_backup=
replacement_applied=0
had_existing_target=0
rollback_failure_message=

rollback_replacement() {
  if [ "$replacement_applied" -ne 1 ]; then
    return 0
  fi
  if [ "$had_existing_target" -eq 1 ]; then
    if [ -n "$destination_backup" ] && mv -f "$destination_backup" "$target"; then
      destination_backup=
      replacement_applied=0
      return 0
    fi
    preserved_backup=$destination_backup
    destination_backup=
    replacement_applied=0
    rollback_failure_message="installation failed and rollback also failed; the prior binary remains at $preserved_backup."
    return 1
  fi
  if rm -f -- "$target"; then
    replacement_applied=0
    return 0
  fi
  replacement_applied=0
  rollback_failure_message="installation failed and the incomplete target could not be removed: $target."
  return 1
}

cleanup() {
  if [ "$replacement_applied" -eq 1 ]; then
    if ! rollback_replacement; then
      printf 'seal installer: %s\n' "$rollback_failure_message" >&2
    fi
  fi
  if [ -n "$destination_stage" ]; then
    rm -f -- "$destination_stage"
  fi
  if [ -n "$destination_backup" ]; then
    rm -f -- "$destination_backup"
  fi
  rm -rf -- "$temporary_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

download() {
  source_url=$1
  output_path=$2
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --silent --show-error --output "$output_path" "$source_url"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget --quiet --output-document="$output_path" "$source_url"
    return
  fi
  fail 'curl or wget is required to download a release.'
}

archive_path="$temporary_dir/$asset"
checksums_path="$temporary_dir/checksums.txt"
download "$release_base/$tag/$asset" "$archive_path"
download "$release_base/$tag/checksums.txt" "$checksums_path"

checksum_matches=$(awk -v name="$asset" '
  $2 == name {
    if (NF != 2 || length($0) != 66 + length(name) || substr($0, 65, 2) != "  ") {
      print "INVALID"
    } else {
      print $1
    }
  }
' "$checksums_path")
checksum_count=$(printf '%s\n' "$checksum_matches" | awk 'NF { count++ } END { print count + 0 }')
if [ "$checksum_count" -ne 1 ]; then
  fail "checksums.txt must contain exactly one entry for $asset."
fi
expected_checksum=$checksum_matches
if [ "${#expected_checksum}" -ne 64 ]; then
  fail "checksums.txt has an invalid SHA-256 for $asset."
fi
case "$expected_checksum" in
  *[!0-9a-f]*) fail "checksums.txt has an invalid SHA-256 for $asset." ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum=$(sha256sum "$archive_path" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum=$(shasum -a 256 "$archive_path" | awk '{ print $1 }')
else
  fail 'sha256sum or shasum is required to verify a release.'
fi
if [ "$actual_checksum" != "$expected_checksum" ]; then
  fail "SHA-256 mismatch for $asset."
fi

archive_entries=$(tar -tzf "$archive_path") || fail "could not inspect $asset."
if [ "$archive_entries" != "seal" ]; then
  fail "$asset must contain exactly one seal binary."
fi
tar -xzf "$archive_path" -C "$temporary_dir"
candidate="$temporary_dir/seal"
if [ ! -f "$candidate" ] || [ -L "$candidate" ]; then
  fail "$asset does not contain a regular seal binary."
fi
chmod 755 "$candidate"

expected_version_output="$temporary_dir/expected-version.txt"
actual_version_output="$temporary_dir/actual-version.txt"
actual_version_error="$temporary_dir/actual-version.err"
printf '%s\n' "$version" >"$expected_version_output"

matches_requested_version() {
  candidate_path=$1
  : >"$actual_version_output"
  : >"$actual_version_error"
  "$candidate_path" --version >"$actual_version_output" 2>"$actual_version_error" || return 1
  [ ! -s "$actual_version_error" ] || return 1
  cmp -s "$expected_version_output" "$actual_version_output"
}

if ! matches_requested_version "$candidate"; then
  fail "$asset does not report requested version $version."
fi

if [ -z "${HOME:-}" ]; then
  fail 'HOME is required to select the install directory.'
fi
install_dir=$HOME/.local/bin
case "$install_dir" in
  /*) ;;
  *) fail 'the install directory must be an absolute path.' ;;
esac

mkdir -p "$install_dir"
target="$install_dir/seal"
if [ -e "$target" ] || [ -L "$target" ]; then
  if [ ! -f "$target" ] || [ -L "$target" ]; then
    fail "$target is not a regular file."
  fi
  had_existing_target=1
else
  had_existing_target=0
fi

destination_stage=$(mktemp "$install_dir/.seal-install.XXXXXX") || fail 'could not stage the installed binary.'
cp "$candidate" "$destination_stage"
chmod 755 "$destination_stage"

if [ "$had_existing_target" -eq 1 ]; then
  destination_backup=$(mktemp "$install_dir/.seal-backup.XXXXXX") || fail 'could not preserve the existing binary.'
  cp -p "$target" "$destination_backup"
fi

replacement_applied=1
mv -f "$destination_stage" "$target"
destination_stage=

if ! matches_requested_version "$target"; then
  if ! rollback_replacement; then
    fail "$rollback_failure_message"
  fi
  fail 'the installed binary failed its absolute-path version smoke test.'
fi

replacement_applied=0

if [ -n "$destination_backup" ]; then
  rm -f -- "$destination_backup"
  destination_backup=
fi

printf 'Installed Seal %s at %s\n' "$version" "$target"
case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *) printf 'Add %s to PATH to invoke seal by name.\n' "$install_dir" ;;
esac
printf '%s\n' 'Git is required when Seal evaluates a repository; this installer does not install Git.'
