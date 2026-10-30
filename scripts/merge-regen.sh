#!/usr/bin/env bash
# merge-regen.sh — merge main into a pull request's head, resolve every
# conflict that is only in GENERATED files by regenerating them, and push the
# result to the PR branch. Hand-written conflicts are refused, never resolved.
#
# WHY. Generated files carry whole-project fingerprints (a `forge:hash=` header,
# SEED_FINGERPRINT, a descriptor stamp), so any two PRs that change any forge
# input rewrite the same line. forge's merge driver settles that in a LOCAL
# merge, but GitHub's server-side merge (the merge queue, "update branch") runs
# no driver: the PR conflicts and is dequeued. A Go-only PR can also leave a
# generated file stale on main, and then every later PR's gate fails "generate
# is not a no-op". This script is the one place both are repaired: it merges
# with forge's driver installed, regenerates with the PINNED forge on the merged
# tree, and commits the merge plus the regenerate as one commit.
#
# USAGE
#   scripts/merge-regen.sh (--pr <n> | --branch <head-branch>) [--base main] [--push | --out <ref>]
#
# Without --push or --out it reports what it would do and changes nothing.
# --out <ref> builds the commit and points <ref> (in the caller's repository)
# at it instead of pushing, for a caller that pushes as someone else (the PR
# pipeline's git_publish node, as the GitHub App). Either way the base and head
# the run used are left in refs/merge-regen/base and refs/merge-regen/head.
#
# CONTRACT (stable; callers branch on the exit code, and the last stdout line)
#   exit 0  `merge-regen: no-drift <head-sha>`   head merges with base cleanly and
#                                                 generate is a no-op on the merge:
#                                                 nothing to push.
#   exit 0  `merge-regen: pushed <new-sha>`      (--push) a "chore: regenerate on
#                                                 merge" merge commit was pushed to
#                                                 the branch; the PR's head moved.
#   exit 0  `merge-regen: ready <new-sha>`       (--out) the same commit, not pushed:
#                                                 <ref> points at it; push it with
#                                                 --force-with-lease against
#                                                 refs/merge-regen/head.
#   exit 0  `merge-regen: would-push <tree-sha>` (neither) the same, not kept.
#   exit 2  `merge-regen: refused-conflict <paths>`  a hand-written file conflicts;
#                                                 the author must merge main.
#   exit 3  `merge-regen: refused-drift <paths>`     generate changed hand-written
#                                                 files; the branch must commit them.
#   exit 4  `merge-regen: lease-lost <sha>`      the branch moved while we worked;
#                                                 nothing pushed, run again.
#   exit 1  `merge-regen: error <why>`           generate failed, bad arguments, …
#
# Idempotent: a branch that already carries the merge of the current base and a
# current regenerate reports no-drift. Safe on a conflicting PR: nothing is
# pushed unless every conflict was in a generated path and generate succeeded.
#
# The work happens in a throwaway worktree; the caller's checkout is untouched.
#
# OVERRIDES (tests, other repos)
#   MERGE_REGEN_GENERATE        the regenerate command, run at the merged tree's root
#   MERGE_REGEN_DRIVER_INSTALL  the command that installs forge's merge driver
#   MERGE_REGEN_GEN_RE          ERE of generated paths (in addition to
#                               `merge=forge-generated` in .gitattributes)
#   MERGE_REGEN_REMOTE          remote name (default origin)
set -euo pipefail

say() { echo "merge-regen: $*"; }
die() { say "error $*"; exit 1; }

pr="" branch="" base="main" push=0 out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --pr) pr=${2:?}; shift 2 ;;
    --branch) branch=${2:?}; shift 2 ;;
    --base) base=${2:?}; shift 2 ;;
    --push) push=1; shift ;;
    --out) out=${2:?}; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//;$d'; exit 0 ;;
    *) die "unknown argument $1" ;;
  esac
done
if [ -n "$pr" ] && [ -z "$branch" ]; then
  branch=$(gh pr view "$pr" --json headRefName --jq .headRefName) || die "cannot resolve PR #$pr"
fi
[ -n "$branch" ] || die "--pr or --branch is required"
[ "$push" = 1 ] && [ -n "$out" ] && die "--push and --out are mutually exclusive"
case "$out" in ""|refs/*) ;; *) die "--out takes a full ref name (refs/...), got $out" ;; esac

repo=$(git rev-parse --show-toplevel) || die "not in a git repository"
remote=${MERGE_REGEN_REMOTE:-origin}

# ── per-repo defaults: the pinned forge, as the repo's own gate runs it ──────
pin=$({ sed -n 's/^forge_version:[[:space:]]*//p' "$repo/forge.yaml" 2>/dev/null || true; } | head -1 | tr -d "\"'")
if grep -q '^  generate:forge:' "$repo/Taskfile.yml" 2>/dev/null; then
  # reliant: `task generate:forge` runs `go run …/cmd/forge@<pin> generate`.
  forge_cmd="env GOWORK=off go run github.com/reliant-labs/forge/cmd/forge@${pin}"
  default_generate="task generate:forge && task generate:docs"
else
  # control-plane: `task tools` installs the pinned forge into .tools/bin.
  forge_cmd="task tools >/dev/null && env PATH=\$PWD/.tools/bin:\$PATH forge"
  default_generate="task tools >/dev/null && PATH=\$PWD/.tools/bin:\$PATH forge generate"
fi
generate=${MERGE_REGEN_GENERATE:-$default_generate}
driver_install=${MERGE_REGEN_DRIVER_INSTALL:-"$forge_cmd merge-driver install"}
gen_re=${MERGE_REGEN_GEN_RE:-'^gen/|^generated/|/gen/|^\.forge/|^deploy/observability/|^docs/reference/[^/]*\.mdx$|^docs/changelog\.mdx$|_gen(_test)?\.[a-z]+$|_pb\.ts$|_connect\.ts$|\.generated\.ts$|^internal/workflow/runtime/schema/field_descriptions\.go$|^internal/skills/catalog/builtin/workflow-builder/SKILL\.md$'}

# ── fetch, and a throwaway worktree on the PR head ───────────────────────────
git -C "$repo" fetch -q "$remote" "+refs/heads/$base:refs/merge-regen/base" "+refs/heads/$branch:refs/merge-regen/head" ||
  die "cannot fetch $base and $branch from $remote"
base_sha=$(git -C "$repo" rev-parse refs/merge-regen/base)
head_sha=$(git -C "$repo" rev-parse refs/merge-regen/head)
wt=$(mktemp -d "${TMPDIR:-/tmp}/merge-regen.XXXXXX")
cleanup() { git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1 || rm -rf "$wt"; }
trap cleanup EXIT
git -C "$repo" worktree add -q --detach "$wt" "$head_sha"
cd "$wt"
git config user.name >/dev/null || git config user.name "merge-regen"
git config user.email >/dev/null || git config user.email "merge-regen@users.noreply.github.com"

is_generated() { # path
  [ "$(git check-attr merge -- "$1" | sed 's/.*: merge: //')" = forge-generated ] && return 0
  printf '%s\n' "$1" | grep -Eq "$gen_re"
}

# ── merge, with forge's driver ───────────────────────────────────────────────
# Best effort: without it the merge is git's plain one, and every generated
# conflict below is still settled by regenerating. The driver only makes the
# fallback rarer (and merges .forge's state files key by key).
(eval "$driver_install") >/dev/null 2>&1 || say "note: forge's merge driver could not be installed; generated conflicts are regenerated without it" >&2

merged_clean=1
if ! git merge -q --no-ff --no-commit "$base_sha" >/dev/null 2>&1; then
  merged_clean=0
fi
conflicts=$(git diff --name-only --diff-filter=U)
if [ "$merged_clean" = 0 ] && [ -z "$conflicts" ] && ! git rev-parse -q --verify MERGE_HEAD >/dev/null; then
  die "git merge of $base into $branch failed: $(git merge --no-commit "$base_sha" 2>&1 | tail -1)"
fi
hand=()
for f in $conflicts; do is_generated "$f" || hand+=("$f"); done
if [ ${#hand[@]} -gt 0 ]; then
  say "refused-conflict ${hand[*]}"
  exit 2
fi
# Every conflict is generated: take this side's copy (a verifiable render of
# some vintage, or the state file as this side has it) and let generate render
# the merged inputs over it. A .forge state map takes both sides' keys.
for f in $conflicts; do
  case "$f" in
    .forge/scaffolded.json|.forge/disowned.json|.forge/hashes.json)
      if git show ":2:$f" >"$wt/.mr-ours" 2>/dev/null && git show ":3:$f" >"$wt/.mr-theirs" 2>/dev/null &&
        jq -S --indent 2 -s '.[1] * .[0]' "$wt/.mr-ours" "$wt/.mr-theirs" >"$f" 2>/dev/null; then :
      else git checkout -q --ours -- "$f"; fi
      rm -f "$wt/.mr-ours" "$wt/.mr-theirs" ;;
    *) git checkout -q --ours -- "$f" 2>/dev/null || git rm -q -- "$f" ;;
  esac
  git add -- "$f" 2>/dev/null || true
done

# ── regenerate on the merged tree ────────────────────────────────────────────
before=$(git write-tree)
if ! (eval "$generate") >"$wt/.mr-generate.log" 2>&1; then
  tail -30 "$wt/.mr-generate.log" >&2
  die "generate failed on the merge of $base into $branch"
fi
rm -f "$wt/.mr-generate.log"
git add -A
after=$(git write-tree)
changed=$(git diff --name-only "$before" "$after")
bad=()
for f in $changed; do is_generated "$f" || bad+=("$f"); done
if [ ${#bad[@]} -gt 0 ]; then
  say "refused-drift ${bad[*]}"
  exit 3
fi

# Nothing to fix: the merge was clean and generate a no-op on it.
if [ "$merged_clean" = 1 ] && [ "$before" = "$after" ]; then
  say "no-drift $head_sha"
  exit 0
fi
if git rev-parse -q --verify MERGE_HEAD >/dev/null; then
  git commit -q --no-verify -m "chore: regenerate on merge with $base ($(git rev-parse --short "$base_sha"))" \
    -m "merge-regen: merged $base into $branch and regenerated with the pinned forge;${conflicts:+ generated conflicts resolved: $(echo $conflicts)}"
else
  # base was already merged: only the regenerate is new.
  git commit -q --no-verify -m "chore: regenerate on merge with $base ($(git rev-parse --short "$base_sha"))"
fi
new=$(git rev-parse HEAD)
if [ -n "$out" ]; then
  # The worktree shares the repository's object store, so the commit outlives
  # the worktree once a ref in the caller's repository names it.
  git -C "$repo" update-ref "$out" "$new" || die "cannot update $out"
  say "ready $new"
  exit 0
fi
if [ "$push" = 0 ]; then
  say "would-push $(git rev-parse HEAD^{tree})"
  exit 0
fi
if ! git push -q "$remote" "HEAD:refs/heads/$branch" --force-with-lease="refs/heads/$branch:$head_sha" 2>"$wt/.mr-push.err"; then
  if grep -qiE 'stale info|rejected|fetch first' "$wt/.mr-push.err"; then
    say "lease-lost $head_sha"
    exit 4
  fi
  cat "$wt/.mr-push.err" >&2
  die "push to $branch failed"
fi
say "pushed $new"
