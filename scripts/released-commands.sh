#!/usr/bin/env bash
# scripts/released-commands.sh — record the forge command tree of a release.
#
#   scripts/released-commands.sh [vX.Y.Z]     (default: internal/buildinfo/VERSION)
#
# Writes internal/cli/testdata/released_commands.txt: every command path of
# that release's `forge` (hidden commands and aliases included), one per line.
#
# Why: a forge built from a checkout is on no module proxy, so `forge project
# new` pins the project's forge_version to the newest RELEASE its source
# descends from (buildinfo.PublishedFloor, the VERSION file), and the
# project's Taskfile and CI run THAT forge. A scaffold template that invokes a
# command newer than the release therefore breaks every project such a forge
# scaffolds: the Taskfile once ran `forge project frontends`, which the pinned
# release did not have (#1374). TestScaffoldTemplatesRunOnlyReleasedForgeCommands
# (internal/cli) checks every forge invocation in the scaffold templates
# against this file, offline.
#
# The release's tree is read from the release itself: the tag is checked out
# into a private temporary worktree (never this checkout) and a throwaway
# main there walks its cli.NewRootCmd(). Run it after a release when a
# template is to use a command that release added; until then the file
# names an older release, which only makes the check stricter.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
VERSION="${1:-$(tr -d '[:space:]' <"$ROOT/internal/buildinfo/VERSION")}"
OUT="$ROOT/internal/cli/testdata/released_commands.txt"

if ! git -C "$ROOT" rev-parse -q --verify "refs/tags/$VERSION^{commit}" >/dev/null; then
  echo "error: no tag $VERSION in this checkout (git fetch --tags first)" >&2
  exit 1
fi

TMP="$(mktemp -d)"
cleanup() {
  git -C "$ROOT" worktree remove --force "$TMP/src" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT
git -C "$ROOT" worktree add -q --detach "$TMP/src" "$VERSION^{commit}"

mkdir -p "$TMP/src/cmd/released-commands"
cat >"$TMP/src/cmd/released-commands/main.go" <<'GO'
// Command released-commands prints every command path of this forge's
// cobra tree, aliases included: scripts/released-commands.sh.
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli"
)

func main() {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, child := range c.Commands() {
			if child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			for _, name := range append([]string{child.Name()}, child.Aliases...) {
				p := path + " " + name
				fmt.Println(p)
				walk(child, p)
			}
		}
	}
	walk(cli.NewRootCmd(), "forge")
}
GO

(cd "$TMP/src" && go run ./cmd/released-commands) | LC_ALL=C sort -u >"$TMP/commands"
{
  echo "# forge $VERSION: every command path of the release, hidden commands and aliases included."
  echo "# Written by scripts/released-commands.sh; read by TestScaffoldTemplatesRunOnlyReleasedForgeCommands."
  cat "$TMP/commands"
} >"$OUT"
echo "wrote $OUT ($(wc -l <"$TMP/commands" | tr -d ' ') commands of forge $VERSION)"
