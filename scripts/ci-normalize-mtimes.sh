#!/usr/bin/env bash
# Give every tracked file and directory an mtime derived from its CONTENT, so
# Go's test cache can replay a result across CI runs.
#
#   scripts/ci-normalize-mtimes.sh        (from the repository root, after checkout)
#
# WHY. `go test` caches a package's result keyed on the test binary, its flags,
# the environment variables it read, and every file inside the module it
# opened or stat'ed. For files that key is size + mode + MTIME, not content
# (cmd/go/internal/test: hashOpen / hashStat), and for a directory it read,
# the mtime of the directory and of every entry in it. A fresh checkout stamps
# every file with the moment of the checkout, so on a CI runner no package
# that touches a testdata file, a template or its own directory ever replays:
# measured on 2026-10-21, the pull-request shards replayed 0 of 163 packages
# from main's build cache, every run.
#
# HOW. A file's mtime becomes a fixed epoch plus the first 28 bits of its git
# blob id; a directory's, the same of its tree id. Unchanged content gets the
# same mtime on every runner and every commit, and changed content a
# different one (a stale replay needs a 1-in-2^28 prefix collision AND an
# equal size). Every stamp is years old, so Go's "too new to trust" cutoff
# (two seconds) never applies. Symlinks and submodules are left alone.
#
# Only what git tracks is stamped. A file a step creates inside the tree
# afterwards (node_modules, a build output) keeps a real mtime, and so does
# the directory that gained it: a package reading those simply misses, which
# is the safe direction.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# The stamp comes from HEAD's object ids, so it is only true of a clean tree:
# an edited file would get its committed content's mtime and replay a result
# its new content never earned.
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "::error::$0 needs a clean checkout: it stamps files with mtimes derived from HEAD's content" >&2
  git status --short --untracked-files=no >&2
  exit 1
fi

# 2000-01-01T00:00:00Z. 2^28 seconds on top reaches 2008.
epoch=946684800

{
  # The root directory, which ls-tree does not list.
  printf '040000 tree %s\t.\0' "$(git rev-parse 'HEAD^{tree}')"
  git ls-tree -r -t -z --full-tree HEAD
} | perl -0 -ne '
  chomp;
  my ($meta, $path) = split /\t/, $_, 2;
  my ($mode, $type, $oid) = split / /, $meta;
  next if $mode eq "120000" || $type eq "commit";
  next unless -e $path;
  my $t = '"$epoch"' + hex(substr($oid, 0, 7));
  utime($t, $t, $path) or die "utime $path: $!\n";
  $n++;
  END { print "stamped $n tracked paths with content-derived mtimes\n" }
'
