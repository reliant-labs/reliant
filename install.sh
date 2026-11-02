#!/bin/sh
# install.sh — install the forge CLI from a GitHub release, no Go toolchain needed.
#
#   curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
#
# Non-interactive and sudo-free: it detects the OS and CPU, downloads the
# matching release archive, verifies its sha256 against the release's
# checksums.txt, and installs one `forge` binary. It never prompts.
#
# Environment:
#   FORGE_VERSION      release to install, e.g. v1.0.0 or 1.0.0 (default: latest)
#   FORGE_INSTALL_DIR  directory to install into (default: $HOME/.local/bin)
#
# Testing seam (not for general use):
#   FORGE_RELEASE_BASE_URL  replaces https://github.com/reliant-labs/forge/releases;
#                           the script appends /latest/download/<file> or
#                           /download/<tag>/<file> to it.
#
# Windows: use install.ps1 (PowerShell). Release assets are named by
# scripts/build-release-assets.sh; keep the installers in step with it.
set -eu

REPO="reliant-labs/forge"
BASE_URL="${FORGE_RELEASE_BASE_URL:-https://github.com/$REPO/releases}"
BASE_URL="${BASE_URL%/}"

say() { printf '%s\n' "$*"; }
die() {
  printf 'forge install: error: %s\n' "$*" >&2
  exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
  # fetch URL DEST
  if have curl; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif have wget; then
    wget -q -O "$2" "$1"
  else
    die "need curl or wget to download the release"
  fi
}

sha256_of() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif have openssl; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    die "need sha256sum, shasum or openssl to verify the download"
  fi
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) die "on Windows use install.ps1: irm https://raw.githubusercontent.com/$REPO/main/install.ps1 | iex" ;;
  *) die "unsupported OS: $(uname -s) (releases cover linux, darwin and windows)" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU: $(uname -m) (releases cover amd64 and arm64)" ;;
esac

if [ -n "${FORGE_VERSION:-}" ]; then
  tag="v${FORGE_VERSION#v}"
  case "$tag" in
    *[!0-9A-Za-z.+-]* | v) die "FORGE_VERSION must look like v1.0.0 (got: $FORGE_VERSION)" ;;
  esac
  release_url="$BASE_URL/download/$tag"
  wanted="$tag"
else
  release_url="$BASE_URL/latest/download"
  wanted="the latest release"
fi

if [ -n "${FORGE_INSTALL_DIR:-}" ]; then
  dest_dir="$FORGE_INSTALL_DIR"
else
  [ -n "${HOME:-}" ] || die "HOME is not set; set FORGE_INSTALL_DIR to choose where to install"
  dest_dir="$HOME/.local/bin"
fi

have tar || die "need tar to unpack the release"
have gzip || die "need gzip to unpack the release"

asset="forge_${os}_${arch}.tar.gz"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/forge-install.XXXXXX")" || die "could not create a temporary directory"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

say "Installing forge ($wanted, $os/$arch) into $dest_dir"

fetch "$release_url/checksums.txt" "$tmp/checksums.txt" ||
  die "could not download $release_url/checksums.txt — $wanted may not exist or may not have binaries attached yet.
  Pin a published version with FORGE_VERSION=vX.Y.Z, or build from source: go install github.com/$REPO/cmd/forge@latest"
fetch "$release_url/$asset" "$tmp/$asset" ||
  die "could not download $release_url/$asset — this release has no build for $os/$arch"

expected="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt" | tr 'A-F' 'a-f')"
[ -n "$expected" ] || die "checksums.txt has no entry for $asset"
actual="$(sha256_of "$tmp/$asset" | tr 'A-F' 'a-f')"
[ "$expected" = "$actual" ] || die "sha256 mismatch for $asset (expected $expected, got $actual); nothing was installed"

mkdir "$tmp/unpacked"
tar -xzf "$tmp/$asset" -C "$tmp/unpacked" || die "could not unpack $asset"
[ -f "$tmp/unpacked/forge" ] || die "$asset does not contain a forge binary"

mkdir -p "$dest_dir" || die "cannot create $dest_dir; set FORGE_INSTALL_DIR to a writable directory"
staged="$dest_dir/.forge.install.$$"
cp "$tmp/unpacked/forge" "$staged" || die "cannot write to $dest_dir; set FORGE_INSTALL_DIR to a writable directory"
chmod 755 "$staged"
# Rename over the old binary rather than overwrite it: replacing a running
# forge in place fails with "text file busy" on Linux.
mv -f "$staged" "$dest_dir/forge" || {
  rm -f "$staged"
  die "cannot install into $dest_dir"
}

say "Installed $dest_dir/forge"
if installed_version="$("$dest_dir/forge" version 2>/dev/null)"; then
  say "$installed_version"
else
  say "warning: $dest_dir/forge did not run (is $dest_dir mounted noexec?)" >&2
fi

case ":${PATH:-}:" in
  *":$dest_dir:"*) ;;
  *)
    say ""
    say "$dest_dir is not on your PATH. Add it for this shell and future ones:"
    say "  export PATH=\"$dest_dir:\$PATH\""
    ;;
esac
