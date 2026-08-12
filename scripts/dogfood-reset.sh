#!/usr/bin/env bash
# dogfood-reset.sh — return a dogfood SLOT to a fresh birth, in place.
#
# A dogfood run needs a project that has never been built in. It does NOT need
# a project that has never been NAMED. This script makes one reusable directory
# behave like a brand-new one, so runs stop accumulating a full Go + Next.js
# scaffold (~1GB with node_modules) per run.
#
# WHY THIS IS NOT JUST `rm -rf`
#
# `reliant workflow run --project-path` is find-or-create, and the "find" half
# short-circuits. CreateProject returns AlreadyExists as soon as it matches the
# path (internal/grpc/services/project.go:364) — BEFORE the auto git-init block
# at :453. So on a reused path:
#
#   first run   → row created → repo.discover finds nothing → git init -b main
#   later runs  → row found   → returns immediately, git init NEVER runs
#
# Wipe the tree and do nothing else and the next run starts in a directory with
# no .git at all, while the first run started in a git repo. That is not the
# same experiment, and nothing in the run would announce the difference:
# is_git_repo reconciles quietly to false on the next GetProject, forge
# scaffolds into a non-repo, and every "did it commit / what changed" question
# in §3 answers differently for a reason that has nothing to do with forge.
#
# So this script reproduces what CreateProject would have done on a first run:
# an empty directory plus `git init -b <branch>` with NO initial commit —
# matching handleInitGitRepo's InitialCommit:false. Commitless is deliberate;
# the worktree path calls gitutil.EnsureInitialCommit itself when it needs a
# real branch ref, exactly as it would for a genuinely new project.
#
# WHAT IS DELIBERATELY NOT RESET
#
# The projects row, and the chats/workflows under it, survive. Per-run
# forensics is keyed by execution id, so old chats do not corrupt a new run's
# numbers — but `reliant-dev workflow ps` and the project's chat list DO show
# the history, and that is the honest cost of reuse.
#
# ~/.reliant/worktrees/<id>/ is NOT touched. Those are keyed by workspace/repo
# id and other projects' worktrees live in the same parent; deleting by the
# wrong key destroys another session's work. This script reports them instead.
set -euo pipefail

LABS_ROOT="${LABS_ROOT:-$HOME/src/reliant-labs}"
FORGE_DIR="${FORGE_DIR:-$LABS_ROOT/forge}"
RELIANT_DIR="${RELIANT_DIR:-$LABS_ROOT/reliant}"
CONTROL_PLANE_DIR="${CONTROL_PLANE_DIR:-$LABS_ROOT/control-plane}"
DOGFOOD_SLOT_BRANCH="${DOGFOOD_SLOT_BRANCH:-main}"

MARKER=".dogfood-slot"

usage() {
  cat >&2 <<EOF
usage: dogfood-reset.sh <slot-path>

Resets a dogfood slot to a fresh birth: empties it and re-inits git the way a
brand-new project would have been initialized.

Refuses any directory that is not already a slot. To bless a new one, create it
and drop the marker in yourself:

  mkdir -p <slot-path> && touch <slot-path>/$MARKER

That refusal is the whole safety model — this script deletes recursively, so it
must never be pointable at a real checkout by a typo.
EOF
  exit 2
}

[ $# -eq 1 ] || usage
SLOT="$1"

case "$SLOT" in
  -h | --help) usage ;;
esac

# ── Guards ───────────────────────────────────────────────────────────────────
#
# Everything below runs BEFORE anything is deleted. A guard that fires after
# the rm is decoration.

case "$SLOT" in
  /*) ;;
  *)
    echo "✗ slot path must be absolute, got: $SLOT" >&2
    exit 1
    ;;
esac

# Normalize without requiring existence, so the comparisons below are done on
# one spelling of the path.
SLOT="${SLOT%/}"
if [ -d "$SLOT" ]; then
  SLOT="$(cd "$SLOT" && pwd -P)"
fi

# Refuse paths shallow enough that a typo is catastrophic. "$HOME/x" is 3
# components on macOS (/Users/<user>/x); anything shorter is not a slot.
depth="$(printf '%s' "${SLOT#/}" | awk -F/ '{print NF}')"
if [ "$depth" -lt 3 ]; then
  echo "✗ refusing to reset a path this shallow: $SLOT" >&2
  exit 1
fi
if [ "$SLOT" = "$HOME" ] || [ "$SLOT" = "$LABS_ROOT" ]; then
  echo "✗ refusing to reset $SLOT" >&2
  exit 1
fi

# Never inside — or containing — a real checkout.
for repo in "$FORGE_DIR" "$RELIANT_DIR" "$CONTROL_PLANE_DIR"; do
  [ -n "$repo" ] || continue
  case "$SLOT/" in
    "$repo"/*)
      echo "✗ slot is inside the $repo checkout: $SLOT" >&2
      exit 1
      ;;
  esac
  case "$repo/" in
    "$SLOT"/*)
      echo "✗ slot CONTAINS the $repo checkout: $SLOT" >&2
      exit 1
      ;;
  esac
done

# A slot must never be, or contain, a registered git worktree. DOGFOOD.md bans
# worktrees inside the scratch tree because searches then hit a second forge;
# the deletion hazard is worse than the search one, since removing a worktree's
# directory out from under git leaves the owning repo with a broken entry.
for repo in "$FORGE_DIR" "$RELIANT_DIR" "$CONTROL_PLANE_DIR"; do
  [ -d "$repo/.git" ] || [ -f "$repo/.git" ] || continue
  while IFS= read -r wt; do
    [ -n "$wt" ] || continue
    case "$wt/" in
      "$SLOT"/*)
        echo "✗ $repo has a git worktree inside the slot: $wt" >&2
        echo "  Remove it first: git -C $repo worktree remove $wt" >&2
        exit 1
        ;;
    esac
  done < <(git -C "$repo" worktree list --porcelain 2>/dev/null |
    awk '/^worktree /{print substr($0, 10)}')
done

# The marker is the load-bearing guard: it proves this directory was created to
# be destroyed. Anything else — a real project, a home directory, a typo that
# happens to name an existing tree — has no marker and is refused.
if [ -e "$SLOT" ]; then
  if [ ! -d "$SLOT" ]; then
    echo "✗ slot exists and is not a directory: $SLOT" >&2
    exit 1
  fi
  if [ ! -f "$SLOT/$MARKER" ]; then
    echo "✗ $SLOT is not a dogfood slot (no $MARKER) — refusing to delete it." >&2
    echo "  If this really is a throwaway slot: touch $SLOT/$MARKER" >&2
    exit 1
  fi
  # A slot that became a git worktree checkout of something else.
  if [ -f "$SLOT/.git" ]; then
    echo "✗ $SLOT/.git is a file (linked worktree), not a repository — refusing." >&2
    exit 1
  fi
fi

# ── Report what is about to be lost ──────────────────────────────────────────
#
# The project IS the evidence for §3. Print enough that a reset which destroys
# a run still under forensics is visible in the transcript, and recoverable as
# a decision ("I reset slot-a while it held the run I was reviewing") rather
# than as a mystery.
echo "── dogfood reset ────────────────────────────────────────"
echo "  slot:   $SLOT"
if [ -d "$SLOT" ]; then
  prev_size="$(du -sh "$SLOT" 2>/dev/null | awk '{print $1}')"
  prev_files="$(find "$SLOT" -maxdepth 1 -mindepth 1 2>/dev/null | wc -l | tr -d ' ')"
  echo "  before: ${prev_size:-unknown} across ${prev_files} top-level entries"
  if [ -d "$SLOT/.git" ]; then
    prev_head="$(git -C "$SLOT" log -1 --format='%h %s' 2>/dev/null || echo 'no commits')"
    echo "  git:    $prev_head"
  fi
else
  echo "  before: (does not exist yet)"
fi

# ── Reset ────────────────────────────────────────────────────────────────────
#
# Delete the directory rather than its contents: globbing contents misses
# dotfiles under some shells, and a half-emptied slot is worse than either
# state because it looks reset. The path is what the projects row is keyed on,
# and it is identical afterwards, so the row still resolves.
rm -rf "$SLOT"
mkdir -p "$SLOT"
touch "$SLOT/$MARKER"

# Reproduce CreateProject's auto-init: bare repo on the default branch, no
# initial commit. See the header for why this cannot be left to the server.
git -C "$SLOT" init -q -b "$DOGFOOD_SLOT_BRANCH"

echo "  after:  empty, git init -b $DOGFOOD_SLOT_BRANCH (no commits), marker restored"

# Worktrees are not ours to delete, but they accumulate for the same reason the
# scratch trees did, so surface the count rather than letting it grow unseen.
if [ -d "$HOME/.reliant/worktrees" ]; then
  wt_count="$(find "$HOME/.reliant/worktrees" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$wt_count" -gt 0 ]; then
    echo
    echo "  note: $wt_count directories in ~/.reliant/worktrees (not touched — they are"
    echo "        keyed by workspace/repo id and other projects share that parent)."
  fi
fi

echo
echo "✅ slot ready: $SLOT"
