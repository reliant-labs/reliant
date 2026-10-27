#!/usr/bin/env bash
# TypeScript solution-builder can skip content checks when restored build-info
# is newer than the sources. Never import another checkout's incremental state.
# --force also invalidates that timestamp shortcut for copied installations or
# backdated edits in this checkout; a passing gate must check this source tree.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)/frontends/web"

# Pool installations may start with cp -al. tsc writes its state in place, so
# detach it before invoking the compiler, including when another gate runs.
tmp=node_modules/.tmp
if [ -d "$tmp" ]; then
  while IFS= read -r -d '' f; do
    copy=$(mktemp "${f}.unlink.XXXXXX")
    cp -p "$f" "$copy"
    mv -f "$copy" "$f"
  done < <(find "$tmp" -type f -links +1 -print0)
fi
npm run --silent typecheck -- --force
