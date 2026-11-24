#!/usr/bin/env bash
# visual-recapture-apply.sh — the trusted half of visual-recapture.yml: commit
# re-captured visual baselines onto a pull request's head and push them.
#
#   STAGED=<dir> TARGET=<checkout of the PR head> BRANCH=<head branch> HEAD_SHA=<sha> \
#     .github/scripts/visual-recapture-apply.sh
#
# The capture job (contents: read) built and ran the PR's code; this step runs
# none of it. It takes only what the capture job uploaded under $STAGED, and
# accepts a file only when ALL of these hold, refusing the whole set otherwise:
#   - its path is frontends/web/e2e/visual/__screenshots__/<name>.png, with no
#     `..`, no leading dot segment and only [A-Za-z0-9._/-] in it;
#   - it is a regular file (no symlink), at most 1 MiB, starting with the PNG
#     signature.
# Then, in $TARGET (whose HEAD must be $HEAD_SHA), it copies them in, commits
# them as one commit and pushes HEAD to refs/heads/$BRANCH without force: a
# branch that moved since the capture refuses the push, and nothing is lost.
#
# Prints one RECAPTURE=<outcome> line last:
#   RECAPTURE=pushed n=<files> sha=<commit>   exit 0
#   RECAPTURE=none                            exit 0  (every capture already matched)
#   RECAPTURE=refused <why>                   exit 1  (a file failed a check; nothing pushed)
#   RECAPTURE=moved                           exit 1  (the head moved; nothing pushed)
#   RECAPTURE=failed <why>                    exit 1
# VISUAL_RECAPTURE_NO_PUSH=1 commits without pushing (the tests).
set -euo pipefail

out() { echo "RECAPTURE=$*"; }
die() { out "$1"; exit 1; }

: "${STAGED:?STAGED is the artifact directory of the capture job}"
: "${TARGET:?TARGET is a checkout of the PR head}"
: "${BRANCH:?BRANCH is the PR head branch}"
: "${HEAD_SHA:?HEAD_SHA is the head the capture ran on}"
[[ "$HEAD_SHA" =~ ^[0-9a-f]{40}$ ]] || die "failed HEAD_SHA is not a full commit id"
[[ "$BRANCH" =~ ^[A-Za-z0-9._/-]+$ && "$BRANCH" != *..* && "$BRANCH" != -* ]] || die "failed the branch name '$BRANCH' is not one this step pushes to"

[ "$(git -C "$TARGET" rev-parse HEAD)" = "$HEAD_SHA" ] || die "moved"

# Every entry of the artifact but its directories and the top-level manifest: each must
# pass the checks below, so anything else in it (another path, a device, a fifo) refuses all.
files=()
while IFS= read -r -d '' f; do
  r="${f#"$STAGED"/}"; [ "$r" = manifest.txt ] || files+=("$r")
done < <(find "$STAGED" -mindepth 1 ! -type d -print0 | sort -z)
[ "${#files[@]}" -gt 0 ] || { out none; exit 0; }

sig="$(printf '\x89PNG\r\n\x1a\n' | od -An -tx1 | tr -d ' \n')"
for f in "${files[@]}"; do
  [[ "$f" =~ ^frontends/web/e2e/visual/__screenshots__/[A-Za-z0-9._/-]+\.png$ ]] || die "refused $f is not a baseline path"
  case "/$f/" in */../*|*/./*|*/.*) die "refused $f has a dot segment" ;; esac
  p="$STAGED/$f"
  [ -f "$p" ] && [ ! -L "$p" ] || die "refused $f is not a regular file"
  [ "$(stat -c %s "$p")" -le 1048576 ] || die "refused $f is over 1 MiB"
  [ "$(head -c 8 "$p" | od -An -tx1 | tr -d ' \n')" = "$sig" ] || die "refused $f is not a PNG"
done

for f in "${files[@]}"; do
  mkdir -p "$TARGET/$(dirname "$f")"
  cp -- "$STAGED/$f" "$TARGET/$f"
done
git -C "$TARGET" add -- "${files[@]}"
if git -C "$TARGET" diff --cached --quiet; then out none; exit 0; fi
main=""; [ ! -f "$STAGED/manifest.txt" ] || main="$(sed -n 's/^main=\([0-9a-f]\{40\}\)$/\1/p' "$STAGED/manifest.txt")"
git -C "$TARGET" -c user.name='github-actions[bot]' -c user.email='41898283+github-actions[bot]@users.noreply.github.com' \
  commit -q -m "test(web): re-capture visual baselines" \
  -m "Re-captured by visual-recapture.yml on ${HEAD_SHA}${main:+ merged with main ${main}}: ${#files[@]} surface(s). Compare them in this commit's diff." \
  || die "failed git commit"
new="$(git -C "$TARGET" rev-parse HEAD)"
if [ "${VISUAL_RECAPTURE_NO_PUSH:-}" != 1 ]; then
  if ! git -C "$TARGET" push -q origin "HEAD:refs/heads/$BRANCH" 2>"$TARGET/../push.err"; then
    grep -qiE 'rejected|non-fast-forward|fetch first' "$TARGET/../push.err" && die "moved"
    die "failed git push: $(tail -n 1 "$TARGET/../push.err")"
  fi
fi
out "pushed n=${#files[@]} sha=$new"
