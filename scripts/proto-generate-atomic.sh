#!/usr/bin/env bash
# proto-generate-atomic.sh — run `buf generate` so a failure can never leave the
# tree without generated code.
#
# Usage: scripts/proto-generate-atomic.sh <buf-template> <out-dir> [<out-dir>...]
#
# The out-dirs are the template's plugin `out:` directories, relative to the
# repo root. They are regenerated WHOLESALE: anything not produced by this run
# is gone afterwards, which is how stale files from removed protos get pruned.
#
# Why not `rm -rf <out-dirs> && buf generate`: that order deletes first and
# generates second, so any generate failure (buf.build rate limiting, a
# network blip, a bad proto) left gen/controlplane and web/src/gen/controlplane
# deleted — a tree that no longer builds, with nothing to roll back to.
#
# Instead buf writes into a scratch directory (`buf generate -o`, which
# prefixes every plugin's out: path), and only after it succeeds — and every
# expected out-dir actually came out non-empty — is each live directory
# replaced by its freshly generated twin. A failure anywhere before the swap
# exits non-zero with the existing directories untouched.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <buf-template> <out-dir> [<out-dir>...]" >&2
  exit 2
fi

template=$1
shift

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

for dir in "$@"; do
  case $dir in
    /* | *..* | "" | .)
      echo "proto-generate-atomic: out-dir must be a relative path inside the repo, got '$dir'" >&2
      exit 2
      ;;
  esac
done

# The scratch dir lives inside the repo (gitignored tmp/) so the final mv is a
# same-filesystem rename, not a cross-device copy.
mkdir -p tmp
staging=$(mktemp -d "$repo_root/tmp/proto-generate.XXXXXX")
trap 'rm -rf "$staging"' EXIT

PATH="$repo_root/web/node_modules/.bin:$PATH" buf generate --template "$template" -o "$staging"

for dir in "$@"; do
  if [[ ! -d "$staging/$dir" ]] || [[ -z "$(ls -A "$staging/$dir")" ]]; then
    echo "proto-generate-atomic: buf generate succeeded but produced nothing in '$dir'; leaving the existing tree untouched" >&2
    exit 1
  fi
done

# Every out-dir generated: swap them in. Each live dir is moved aside before
# its replacement moves in, so no instant has it missing-and-unrecoverable.
for dir in "$@"; do
  mkdir -p "$(dirname "$dir")"
  if [[ -e "$dir" ]]; then
    mv "$dir" "$staging/.previous.$(echo "$dir" | tr '/' '_')"
  fi
  mv "$staging/$dir" "$dir"
done
