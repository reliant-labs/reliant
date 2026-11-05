#!/usr/bin/env bash
# check-compat.sh: the merge gate's backwards-compatibility checks.
#
# Owner decision, 2026-11-05: real semver with the major locked at 1, so a breaking
# change to a public surface never merges: deprecate it, keep the old path working,
# and remove it only in a future major, which only the owner decides. The same file
# runs in forge's, reliant's and control-plane's gates.
#
# Usage:
#   scripts/check-compat.sh proto        buf breaking: this tree's proto vs the base's
#   scripts/check-compat.sh migrations   a migration declaring forge:breaks-rollback
#                                        needs the owner's label
#
# Env:
#   CI_BASE        the base to judge against (default: merge-base of HEAD and origin/main)
#   CI_PR          the pull request number, to read its labels (no PR: a finding fails)
#   COMPAT_OWNERS  logins whose label counts (default: reliant-01, the owner)
#   COMPAT_GH      the gh binary (default: gh)
#   COMPAT_REPO    owner/repo (default: from origin's URL)
#
# A pull request passes a finding only when the OWNER applied its label (the label's
# `labeled` event names the actor; a label a bot or the author added does not count):
#   breaking:approved                     proto (or API / CLI) break
#   migration:breaks-rollback-approved    a migration the previous release cannot run on
set -euo pipefail

GH="${COMPAT_GH:-gh}"
OWNERS="${COMPAT_OWNERS:-reliant-01}"
die() { echo "check-compat: $*" >&2; exit 2; }

base="${CI_BASE:-}"
[ -n "$base" ] || base="$(git merge-base HEAD origin/main 2>/dev/null || true)"
[ -n "$base" ] || die "no base: set CI_BASE, or fetch origin/main"
base="$(git rev-parse --verify --quiet "$base^{commit}")" || die "cannot resolve the base $CI_BASE"

repo="${COMPAT_REPO:-}"
if [ -z "$repo" ]; then
  repo="$(git config --get remote.origin.url 2>/dev/null || true)"
  repo="${repo#git@github.com:}"; repo="${repo#https://github.com/}"; repo="${repo#ssh://git@github.com/}"; repo="${repo%.git}"
fi

# owner_approved <label>: the PR carries <label> and an owner was the last to apply it.
owner_approved() {
  [ -n "${CI_PR:-}" ] && [ -n "$repo" ] || return 1
  local has actor
  has="$("$GH" api "repos/$repo/issues/$CI_PR/labels" --jq "[.[] | select(.name == \"$1\")] | length" 2>/dev/null || echo 0)"
  [ "$has" -gt 0 ] 2>/dev/null || return 1
  # Fail closed on a partial read: refuse calls this in an `if`, where errexit is
  # off, so a later page failing after an owner's event must not leave that actor.
  actor="$("$GH" api --paginate "repos/$repo/issues/$CI_PR/events" \
    --jq ".[] | select(.event == \"labeled\" and .label.name == \"$1\") | .actor.login" 2>/dev/null | tail -1)" || return 1
  [ -n "$actor" ] && tr ', ' '\n\n' <<<"$OWNERS" | grep -qxF "$actor"
}

# refuse <label> <what> <how to deprecate>: fail unless the owner approved it.
refuse() {
  if owner_approved "$1"; then
    echo "check-compat: $2 — approved by the owner's '$1' label on #$CI_PR"
    return 0
  fi
  {
    echo "check-compat: FAIL: $2"
    echo "  Majors are locked at 1 (owner, 2026-11-05): a breaking change to a public surface"
    echo "  does not merge. $3"
    echo "  Only the owner can accept it, by applying the '$1' label to the pull request${CI_PR:+ (#$CI_PR)}."
  } >&2
  return 1
}

changed() { git diff --name-only --diff-filter="${2:-ACMRD}" "$base" HEAD -- "$1"; }

case "${1:-}" in
  proto)
    if [ ! -f buf.yaml ]; then
      # Fail closed: deleting the configuration must not switch the check off for
      # a base that publishes proto under it.
      if git cat-file -e "$base:buf.yaml" 2>/dev/null; then
        refuse breaking:approved "buf.yaml, which ${base:0:12} has, was removed, so its proto cannot be judged" \
          "Keep buf.yaml (and the proto under it); deprecate what should go instead of removing it (the proto-breaking skill)."
        exit
      fi
      echo "check-compat: no buf.yaml here or at ${base:0:12}; nothing to judge"; exit 0
    fi
    if [ -z "$(changed proto)$(changed buf.yaml)" ]; then echo "check-compat: proto unchanged since ${base:0:12}"; exit 0; fi
    command -v buf >/dev/null || die "buf is not on PATH (task tools installs the pinned one)"
    old="$(mktemp -d)"; trap 'rm -rf "$old"' EXIT
    # The base's tree, not `.git#ref=`: buf cannot clone a linked worktree's .git file.
    git archive "$base" -- buf.yaml $(git ls-tree --name-only "$base" -- buf.lock proto) | tar -x -C "$old"
    if out="$(buf breaking --against "$old" 2>&1)"; then
      echo "check-compat: proto is wire-compatible with ${base:0:12}"; exit 0
    fi
    printf '%s\n' "$out" >&2
    refuse breaking:approved "buf breaking: the proto change is incompatible with ${base:0:12}" \
      "Deprecate instead: keep the field, message, rpc or enum value and mark it [deprecated = true]; when a field truly goes, keep its number and name in 'reserved' and add the replacement beside it (the proto-breaking skill)."
    ;;
  migrations)
    found=""
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      if git show "HEAD:$f" 2>/dev/null | grep -qE '^[[:space:]]*--[[:space:]]*forge:breaks-rollback'; then found="$found $f"; fi
    done < <(changed 'db/migrations/*.sql' AM)
    [ -n "$found" ] || { echo "check-compat: no migration declares forge:breaks-rollback"; exit 0; }
    refuse migration:breaks-rollback-approved "migrations declaring forge:breaks-rollback:$found" \
      "Split it expand/contract across releases (add the new shape, backfill, move readers, drop the old shape a release later) and declare each step forge:backward-compatible."
    ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 2 ;;
esac
