#!/usr/bin/env bash
# changelog-release.sh — write a release's section of CHANGELOG.md.
#
# CHANGELOG.md is written ONLY at release time, by scripts/release-forge.sh,
# from the squash commits (whose subjects are the merged PR titles) between the
# previous tag and HEAD. A pull request never edits it: ci.yml's
# scripts/check-changelog-untouched.sh fails one that does.
#
#   scripts/changelog-release.sh --changelog FILE --version vX.Y.Z --date YYYY-MM-DD
#       [--prev-tag vA.B.C] [--breaking-prs "12 34"] [--repo-url URL]
#       [--generated-out FILE]
#
# Run from the repo root. It rewrites FILE in place: the new
# `## [vX.Y.Z] - DATE` section replaces the body of `## [Unreleased]`, which is
# left empty. It does NOT format FILE — release-forge.sh runs prettier after.
#
# THE SECTION. Headings, in order: Breaking changes, Added, Changed, Deprecated,
# Removed, Fixed, Security, Migration, then any other `###` heading the
# Unreleased body had. Under each, the Unreleased prose already written there
# comes first (folded in once — after this release there is none), then one
# bullet per commit since --prev-tag:
#
#   feat   -> Added        fix -> Fixed        anything else -> Changed
#
# A commit is a breaking change — listed under "Breaking changes" instead of
# its type's heading — when its type carries `!` (`feat(x)!:`), its body has a
# `BREAKING CHANGE:` line, or its PR number is in --breaking-prs (the PRs the
# caller found labelled `breaking` on GitHub). Release commits and merge
# commits are skipped. Each bullet is the PR title less its `(#N)`, which
# becomes a link to the pull request when the origin is on github.com.
#
# --generated-out also receives ONLY the commit bullets, grouped, with no
# heading: a GitHub release body is capped at 125000 characters, and the first
# section a long-lived Unreleased is folded into can be far larger.
set -euo pipefail

CHANGELOG="" VERSION="" DATE="" PREV_TAG="" BREAKING_PRS="" REPO_URL="" GENERATED_OUT=""

usage() {
  echo "usage: $0 --changelog FILE --version vX.Y.Z --date YYYY-MM-DD [--prev-tag TAG] [--breaking-prs \"N N\"] [--repo-url URL] [--generated-out FILE]" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --changelog)     [ $# -ge 2 ] || usage; CHANGELOG="$2"; shift 2 ;;
    --version)       [ $# -ge 2 ] || usage; VERSION="$2"; shift 2 ;;
    --date)          [ $# -ge 2 ] || usage; DATE="$2"; shift 2 ;;
    --prev-tag)      [ $# -ge 2 ] || usage; PREV_TAG="$2"; shift 2 ;;
    --breaking-prs)  [ $# -ge 2 ] || usage; BREAKING_PRS="$2"; shift 2 ;;
    --repo-url)      [ $# -ge 2 ] || usage; REPO_URL="$2"; shift 2 ;;
    --generated-out) [ $# -ge 2 ] || usage; GENERATED_OUT="$2"; shift 2 ;;
    *) usage ;;
  esac
done
{ [ -n "$CHANGELOG" ] && [ -n "$VERSION" ] && [ -n "$DATE" ]; } || usage
[ -f "$CHANGELOG" ] || { echo "error: $CHANGELOG not found" >&2; exit 1; }

if [ -z "$REPO_URL" ]; then
  origin="$(git config --get remote.origin.url 2>/dev/null || true)"
  case "$origin" in
    git@github.com:*)      REPO_URL="https://github.com/${origin#git@github.com:}" ;;
    https://github.com/*)  REPO_URL="$origin" ;;
  esac
  REPO_URL="${REPO_URL%.git}"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ── 1. The commits since the previous tag ───────────────────────────
# With no previous tag there is no "since": the whole history is not a release's
# worth of changes, so only the Unreleased prose is folded in.

G_BREAKING="" G_ADDED="" G_CHANGED="" G_FIXED=""
conv_re='^([A-Za-z]+)(\(([^)]+)\))?(!)?:[[:space:]]+(.+)$'

while IFS= read -r -d $'\x1e' rec; do
  rec="${rec#$'\n'}"
  [ -n "$rec" ] || continue
  rest="${rec#*$'\x1f'}"
  subject="${rest%%$'\x1f'*}"
  body="${rest#*$'\x1f'}"

  case "$subject" in
    "chore: release v"*|"Merge "*) continue ;;
  esac

  pr=""
  if [[ "$subject" =~ \ \(#([0-9]+)\)$ ]]; then
    pr="${BASH_REMATCH[1]}"
    subject="${subject% (#"$pr")}"
  fi

  type="" scope="" bang="" desc="$subject"
  if [[ "$subject" =~ $conv_re ]]; then
    type="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    scope="${BASH_REMATCH[3]}"
    bang="${BASH_REMATCH[4]}"
    desc="${BASH_REMATCH[5]}"
  fi

  breaking=0
  [ -n "$bang" ] && breaking=1
  if printf '%s\n' "$body" | grep -Eq '^BREAKING[ -]CHANGE:'; then breaking=1; fi
  if [ -n "$pr" ]; then
    for b in $BREAKING_PRS; do [ "$b" = "$pr" ] && breaking=1; done
  fi

  ref=""
  if [ -n "$pr" ]; then
    if [ -n "$REPO_URL" ]; then ref=" ([#$pr]($REPO_URL/pull/$pr))"; else ref=" (#$pr)"; fi
  fi

  if [ "$breaking" = 1 ]; then
    label="$type"
    [ -n "$scope" ] && label="$type($scope)"
    if [ -n "$label" ]; then
      G_BREAKING+="- **${label}:** ${desc}${ref}"$'\n'
    else
      G_BREAKING+="- ${desc}${ref}"$'\n'
    fi
    continue
  fi

  text="$desc"
  [ -n "$scope" ] && text="**${scope}:** ${desc}"
  case "$type" in
    feat) G_ADDED+="- ${text}${ref}"$'\n' ;;
    fix)  G_FIXED+="- ${text}${ref}"$'\n' ;;
    *)    G_CHANGED+="- ${text}${ref}"$'\n' ;;
  esac
done < <(if [ -n "$PREV_TAG" ]; then git log --no-merges --reverse --format='%H%x1f%s%x1f%b%x1e' "$PREV_TAG..HEAD"; fi)

# ── 2. Split the Unreleased body by `###` heading ───────────────────
# Fences are tracked so a `## ` or `### ` line inside a code block is prose.
UDIR="$WORK/u"
mkdir -p "$UDIR"
awk -v dir="$UDIR" '
  /^```/ { fence = !fence }
  !fence && /^## \[Unreleased\]/ && !seen { inu = 1; seen = 1; next }
  inu && !fence && /^## / { exit }
  !inu { next }
  !fence && /^### / {
    h = substr($0, 5); sub(/[[:space:]]+$/, "", h)
    if (!(h in idx)) { idx[h] = ++n; print idx[h] "\t" h >> (dir "/order") }
    cur = idx[h]; next
  }
  { f = (cur == "") ? dir "/pre" : dir "/" cur; print >> f }
' "$CHANGELOG"
touch "$UDIR/order" "$UDIR/pre"

trim() { # strip leading/trailing blank lines from stdin
  awk 'NF { started = 1 } started { buf[++n] = $0 } END { while (n && buf[n] !~ /[^[:space:]]/) n--; for (i = 1; i <= n; i++) print buf[i] }'
}

unreleased_for() { # heading -> its Unreleased prose, or nothing
  local h="$1" n
  n="$(awk -F'\t' -v h="$h" '$2 == h { print $1; exit }' "$UDIR/order")"
  if [ -n "$n" ] && [ -f "$UDIR/$n" ]; then trim < "$UDIR/$n"; fi
}

generated_for() {
  case "$1" in
    "Breaking changes") printf '%s' "$G_BREAKING" ;;
    Added)   printf '%s' "$G_ADDED" ;;
    Changed) printf '%s' "$G_CHANGED" ;;
    Fixed)   printf '%s' "$G_FIXED" ;;
  esac
}

# ── 3. Compose the section ──────────────────────────────────────────
SECTION="$WORK/section.md"
GEN="$WORK/generated.md"
: > "$SECTION"
: > "$GEN"
printf '## [%s] - %s\n\n' "$VERSION" "$DATE" >> "$SECTION"

CANON=("Breaking changes" Added Changed Deprecated Removed Fixed Security Migration)
HEADINGS=("${CANON[@]}")
while IFS=$'\t' read -r _ h; do
  [ -n "$h" ] || continue
  known=0
  for c in "${CANON[@]}"; do [ "$c" = "$h" ] && known=1; done
  [ "$known" = 1 ] || HEADINGS+=("$h")
done < "$UDIR/order"

emitted=0
for h in "${HEADINGS[@]}"; do
  prose="$(unreleased_for "$h")"
  gen="$(generated_for "$h")"
  [ -n "$prose" ] || [ -n "$gen" ] || continue
  emitted=1
  { printf '### %s\n\n' "$h"
    [ -z "$prose" ] || printf '%s\n\n' "$prose"
    [ -z "$gen" ] || printf '%s\n\n' "$gen"
  } >> "$SECTION"
  if [ -n "$gen" ]; then
    printf '### %s\n\n%s\n\n' "$h" "$gen" >> "$GEN"
  fi
done

# Prose that sat under Unreleased before any `###` heading goes first.
pre="$(trim < "$UDIR/pre")"
if [ -n "$pre" ]; then
  emitted=1
  { head -n 2 "$SECTION"; printf '%s\n\n' "$pre"; tail -n +3 "$SECTION"; } > "$SECTION.tmp"
  mv "$SECTION.tmp" "$SECTION"
fi
if [ "$emitted" = 0 ]; then
  printf 'No notable changes.\n' >> "$SECTION"
fi
printf '\n' >> "$SECTION"

# ── 4. Splice it into CHANGELOG.md ──────────────────────────────────
OUT="$WORK/changelog.new"
awk -v sec="$SECTION" -v ver="$VERSION" -v prev="$PREV_TAG" '
  function emit_section(   l) { while ((getline l < sec) > 0) print l; close(sec) }
  /^```/ { fence = !fence }
  !fence && /^## \[Unreleased\]/ && !done {
    print; print ""; emit_section(); done = 1; skipping = 1; next
  }
  skipping && !fence && /^## / { skipping = 0 }
  skipping { next }
  !fence && /^## / && !done {
    print "## [Unreleased]"; print ""; emit_section(); done = 1
  }
  /^\[Unreleased\]: / && !linked {
    i = index($0, "/compare/")
    if (i > 0) {
      base = substr($0, 15, i - 15)
      print "[Unreleased]: " base "/compare/" ver "...HEAD"
      if (prev != "") print "[" ver "]: " base "/compare/" prev "..." ver
      linked = 1; next
    }
  }
  { print }
  END {
    if (!done) { print ""; print "## [Unreleased]"; print ""; emit_section() }
  }
' "$CHANGELOG" > "$OUT"
cp "$OUT" "$CHANGELOG"

if [ -n "$GENERATED_OUT" ]; then cp "$GEN" "$GENERATED_OUT"; fi
