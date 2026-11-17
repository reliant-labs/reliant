#!/usr/bin/env bash
# render-homebrew-formula.sh — print Formula/forge.rb for a forge release.
#
#   scripts/render-homebrew-formula.sh <version> <checksums.txt> > Formula/forge.rb
#
# <version> is X.Y.Z or vX.Y.Z; <checksums.txt> is the release asset written by
# scripts/build-release-assets.sh. The formula installs the prebuilt darwin and
# linux archives (no Go toolchain, no build from source) and pins each to its
# sha256, so a bump needs only the new version and that one file.
#
# The URLs spell the version out and there is no `version` stanza: Homebrew
# scans the version from the release URL, and `brew audit --strict` rejects a
# stanza that repeats it ("version ... is redundant with version scanned from
# URL"). The tap's workflow audits every formula it renders.
#
# The tap (reliant-labs/homebrew-reliant) runs this from its
# update-forge-formula.yml, triggered by release-forge.sh's dispatch. It lives
# here, beside build-release-assets.sh, because the formula names those
# archives; changing one means changing the other, in one pull request.
set -euo pipefail

[ $# -eq 2 ] || {
  echo "usage: $0 <version> <checksums.txt>" >&2
  exit 2
}
VERSION="${1#v}"
SUMS="$2"
echo "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || {
  echo "error: version must look like X.Y.Z (got: $1); prereleases are not published to Homebrew" >&2
  exit 1
}

sum_for() {
  local asset="forge_$1.tar.gz" sum
  sum="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$SUMS")"
  if ! echo "$sum" | grep -Eq '^[0-9a-f]{64}$'; then
    echo "error: $SUMS has no sha256 for $asset" >&2
    exit 1
  fi
  echo "$sum"
}

DARWIN_ARM64="$(sum_for darwin_arm64)"
DARWIN_AMD64="$(sum_for darwin_amd64)"
LINUX_ARM64="$(sum_for linux_arm64)"
LINUX_AMD64="$(sum_for linux_amd64)"

cat <<EOF
class Forge < Formula
  desc "Production-ready Go + Connect RPC + Postgres + Next.js project framework"
  homepage "https://github.com/reliant-labs/forge"
  license "Apache-2.0"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/reliant-labs/forge/releases/download/v$VERSION/forge_darwin_arm64.tar.gz"
      sha256 "$DARWIN_ARM64"
    end
    on_intel do
      url "https://github.com/reliant-labs/forge/releases/download/v$VERSION/forge_darwin_amd64.tar.gz"
      sha256 "$DARWIN_AMD64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/reliant-labs/forge/releases/download/v$VERSION/forge_linux_arm64.tar.gz"
      sha256 "$LINUX_ARM64"
    end
    on_intel do
      url "https://github.com/reliant-labs/forge/releases/download/v$VERSION/forge_linux_amd64.tar.gz"
      sha256 "$LINUX_AMD64"
    end
  end

  def install
    bin.install "forge"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/forge version")
  end
end
EOF
