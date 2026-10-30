#!/usr/bin/env bash
# TypeScript solution-builder can skip content checks when restored build-info
# is newer than the sources. Never import another checkout's incremental state.
# Incremental state THIS checkout wrote is trusted (an edit or a checkout here
# makes the sources newer than it), so only a first run, or state that came
# from elsewhere (a copied installation), pays the cold --force check (~9 min
# median on a loaded host); the owner stamp says whose state it is.
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
owner="$tmp/.typecheck-owner"
here="$(pwd -P)"
if [ "$(cat "$owner" 2>/dev/null)" = "$here" ]; then
  npm run --silent typecheck
else
  rm -f "$owner"
  npm run --silent typecheck -- --force
  mkdir -p "$tmp" && printf '%s\n' "$here" >"$owner"
fi
