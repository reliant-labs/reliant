#!/bin/sh
# install.sh — install the forge CLI from a GitHub release, no Go toolchain needed.
#
#   curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
#
# Non-interactive and sudo-free: it detects the OS and CPU, downloads the
# matching release archive, verifies its sha256 against the release's
# checksums.txt, verifies the release's signature, and installs one `forge`
# binary. It never prompts.
#
# THE SIGNATURE. checksums.txt comes from the same release as the archive, so
# on its own it detects corruption, not tampering: whoever can replace one
# release asset can replace both. Each release therefore also carries
# forge-release.sigstore.json, a Sigstore attestation over every archive and
# checksums.txt, signed keyless by forge's release-attest.yml workflow
# (docs/releasing.md, "Signed release assets"). This script checks the archive
# against it with `gh attestation verify` (gh 2.49+; no login needed) or, when
# gh cannot, `cosign verify-blob-attestation`, pinned to that workflow's
# identity, and the signed statement must name the release being installed
# ("latest" is resolved to its tag first), so a valid signature replayed from
# another release is refused. A signature that does not verify fails the
# install. When the release has no signature, or neither verifier is
# installed, it warns and installs on the sha256 alone, unless
# FORGE_REQUIRE_SIGNATURE=1.
#
# Environment:
#   FORGE_VERSION            release to install, e.g. v1.0.0 or 1.0.0 (default: latest)
#   FORGE_INSTALL_DIR        directory to install into (default: $HOME/.local/bin)
#   FORGE_REQUIRE_SIGNATURE  1: refuse to install unless the signature verifies
#                            (no signature, or no gh/cosign, is then an error)
#
# Testing seam (not for general use):
#   FORGE_RELEASE_BASE_URL  replaces https://github.com/reliant-labs/forge/releases;
#                           the script reads the redirect of /latest and
#                           appends /download/<tag>/<file> to it.
#
# Windows: use install.ps1 (PowerShell). Release assets are named by
# scripts/build-release-assets.sh; keep the installers in step with it.
set -eu

REPO="reliant-labs/forge"
BASE_URL="${FORGE_RELEASE_BASE_URL:-https://github.com/$REPO/releases}"
BASE_URL="${BASE_URL%/}"

# Who may sign a release: release-attest.yml, run for a release tag or
# dispatched from main (a backfill). Anything else, including the same file run
# from another branch, is refused. .github/workflows/release-attest.yml and
# release-npm.yml verify with the same three values; internal/installers holds
# them equal.
SIGNATURE_ASSET="forge-release.sigstore.json"
SIGNER_IDENTITY='^https://github\.com/reliant-labs/forge/\.github/workflows/release-attest\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$'
SIGNER_ISSUER="https://token.actions.githubusercontent.com"
PREDICATE_TYPE="https://github.com/reliant-labs/forge/attestations/release-assets/v1"

say() { printf '%s\n' "$*"; }
die() {
  printf 'forge install: error: %s\n' "$*" >&2
  exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
  # fetch URL DEST
  if have curl; then
    # A redirect may not downgrade the download to plain http.
    curl -fsSL --proto-redir =https --retry 3 -o "$2" "$1"
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

# verify_signature FILE BUNDLE checks FILE against the Sigstore bundle with the
# first verifier that can: 0 verified, 1 refused, 2 no verifier installed. The
# verifier's output goes to $tmp/verify.log and names it in $verifier.
verify_signature() {
  if have gh && gh attestation verify --help >/dev/null 2>&1; then
    verifier=gh
    # --bundle makes this offline from GitHub's API, so no `gh auth login`.
    GH_HOST=github.com GH_NO_UPDATE_NOTIFIER=1 GH_PROMPT_DISABLED=1 \
      gh attestation verify "$1" --bundle "$2" --repo "$REPO" \
      --cert-identity-regex "$SIGNER_IDENTITY" --cert-oidc-issuer "$SIGNER_ISSUER" \
      --predicate-type "$PREDICATE_TYPE" --deny-self-hosted-runners >"$tmp/verify.log" 2>&1 || return 1
    return 0
  fi
  if have cosign && cosign verify-blob-attestation --help >/dev/null 2>&1; then
    verifier=cosign
    file=$1
    bundle=$2
    # cosign 2.x reads a Sigstore bundle only with --new-bundle-format; 3.x
    # reads it by default and deprecates the flag.
    if cosign verify-blob-attestation --help 2>&1 | grep -q -- '--new-bundle-format'; then
      set -- --new-bundle-format
    else
      set --
    fi
    cosign verify-blob-attestation --bundle "$bundle" "$@" \
      --certificate-identity-regexp "$SIGNER_IDENTITY" --certificate-oidc-issuer "$SIGNER_ISSUER" \
      --type "$PREDICATE_TYPE" "$file" >"$tmp/verify.log" 2>&1 || return 1
    return 0
  fi
  return 2
}

# latest_tag prints the tag $BASE_URL/latest redirects to (GitHub answers
# /releases/latest with a redirect to /releases/tag/<tag>), or nothing. The
# redirect is read, not followed.
latest_tag() {
  if have curl; then
    loc="$(curl -sS --retry 3 -o /dev/null -w '%{redirect_url}' "$BASE_URL/latest" 2>/dev/null)" || loc=""
  elif have wget; then
    loc="$(wget -q -S --max-redirect=0 -O /dev/null "$BASE_URL/latest" 2>&1 | awk '$1 == "Location:" { print $2; exit }')" || loc=""
  else
    die "need curl or wget to download the release"
  fi
  loc="$(printf '%s' "$loc" | tr -d '\r')"
  case "$loc" in
    */tag/*) printf '%s\n' "${loc##*/tag/}" ;;
  esac
}

base64_decode() {
  if base64 -d 2>/dev/null; then
    return 0
  fi
  have openssl || return 1
  openssl base64 -d -A
}

# signed_statement BUNDLE prints the in-toto statement the bundle's DSSE envelope
# carries, whitespace removed, or fails. Call it only after the bundle
# verified: the verifier checked the signature over exactly this payload.
signed_statement() {
  payloads="$(tr -d ' \t\r\n' <"$1" | grep -o '"payload":"[A-Za-z0-9+/=]*"')" || return 1
  # One envelope only: a bundle with two payloads could show the verifier one
  # and this check the other.
  [ "$(printf '%s\n' "$payloads" | wc -l | tr -d ' ')" = 1 ] || return 1
  printf '%s' "$payloads" | sed 's/^"payload":"//; s/"$//' | base64_decode | tr -d ' \t\r\n'
}

# bound_to_release BUNDLE TAG succeeds when the verified statement was signed
# for TAG of this repository. Without it, a valid signature from an older
# release, replayed with that release's archive and checksums.txt under a newer
# tag, would install the older binary.
bound_to_release() {
  statement="$(signed_statement "$1")" || return 1
  case "$statement" in
    *"\"repository\":\"$REPO\""*) ;;
    *) return 1 ;;
  esac
  case "$statement" in
    *"\"release\":\"$2\""*) return 0 ;;
  esac
  return 1
}

# unverified REASON: the install goes ahead on the sha256 alone, with a
# warning, unless FORGE_REQUIRE_SIGNATURE=1 refuses it.
unverified() {
  if [ "$require_signature" = 1 ]; then
    die "$1. FORGE_REQUIRE_SIGNATURE=1 refuses an install whose signature was not verified; nothing was installed"
  fi
  say "warning: $1." >&2
  say "  Installing on the sha256 check alone, which cannot detect a tampered release." >&2
  say "  Set FORGE_REQUIRE_SIGNATURE=1 to refuse an unverified install instead." >&2
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
fi

case "${FORGE_REQUIRE_SIGNATURE:-}" in
  1 | true | yes) require_signature=1 ;;
  "" | 0 | false | no) require_signature=0 ;;
  *) die "FORGE_REQUIRE_SIGNATURE must be 1 or 0 (got: $FORGE_REQUIRE_SIGNATURE)" ;;
esac

if [ -n "${FORGE_INSTALL_DIR:-}" ]; then
  dest_dir="$FORGE_INSTALL_DIR"
else
  [ -n "${HOME:-}" ] || die "HOME is not set; set FORGE_INSTALL_DIR to choose where to install"
  dest_dir="$HOME/.local/bin"
fi

have tar || die "need tar to unpack the release"
have gzip || die "need gzip to unpack the release"

if [ -n "${tag:-}" ]; then
  wanted="$tag"
else
  # The signature names the release it was made for, so it can only be bound
  # to a concrete tag: resolve "latest" to one before downloading anything.
  tag="$(latest_tag)"
  case "$tag" in
    v[0-9]*) ;;
    *) die "could not resolve the latest release from $BASE_URL/latest; pin one with FORGE_VERSION=vX.Y.Z" ;;
  esac
  case "$tag" in
    *[!0-9A-Za-z.+-]*) die "the latest release resolved to an unexpected tag ($tag); pin one with FORGE_VERSION=vX.Y.Z" ;;
  esac
  wanted="$tag (the latest release)"
fi
release_url="$BASE_URL/download/$tag"

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

if fetch "$release_url/$SIGNATURE_ASSET" "$tmp/$SIGNATURE_ASSET" 2>/dev/null; then
  verifier=""
  verify_signature "$tmp/$asset" "$tmp/$SIGNATURE_ASSET" && sig=0 || sig=$?
  case "$sig" in
    0)
      bound_to_release "$tmp/$SIGNATURE_ASSET" "$tag" ||
        die "the release signature verifies for $asset, but it was not made for $REPO $tag; nothing was installed.
  A signed set from another release may have been replayed under $tag. Report it: https://github.com/$REPO/security"
      say "Verified $asset against the release signature ($verifier; signer: $REPO release-attest.yml; release $tag)"
      ;;
    1)
      sed 's/^/  /' "$tmp/verify.log" >&2
      die "the release signature does not verify for $asset ($verifier refused it); nothing was installed.
  The release may have been tampered with. Report it: https://github.com/$REPO/security"
      ;;
    *) unverified "neither gh (2.49 or later) nor cosign is installed, so the release signature was not checked" ;;
  esac
else
  unverified "$wanted carries no signature ($SIGNATURE_ASSET); releases cut before signing began have none"
fi

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
