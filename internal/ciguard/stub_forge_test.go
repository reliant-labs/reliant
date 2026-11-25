package ciguard

import (
	"os"
	"path/filepath"
	"testing"
)

// stubForge stands in for this checkout's forge in the script fixtures: the
// scripts reach forge through scripts/ci-forge.sh, which runs $CI_FORGE when
// it is set. It logs each call's arguments, one line per call, to
// $FORGE_LOG; answers `test begin` with $STUB_RUN (default
// ./.forge/tests/runs/stub); runs a `test capture … -- <command>` command
// as capture does (unchanged, its exit status its own); and answers anything
// else with $STUB_STDOUT on stdout, $STUB_STDERR on stderr and exit status
// $STUB_EXIT (default 0).
const stubForge = `#!/usr/bin/env bash
[ -n "$FORGE_LOG" ] && echo "$*" >>"$FORGE_LOG"
if [ "$1 $2" = "test begin" ]; then
  echo "${STUB_RUN:-$PWD/.forge/tests/runs/stub}"
  exit 0
fi
if [ "$1 $2" = "test capture" ]; then
  while [ $# -gt 0 ] && [ "$1" != -- ]; do shift; done
  shift
  exec "$@"
fi
[ -n "$STUB_STDOUT" ] && echo "$STUB_STDOUT"
[ -n "$STUB_STDERR" ] && echo "$STUB_STDERR" >&2
exit "${STUB_EXIT:-0}"
`

// installStubForge copies scripts/ci-forge.sh into root/scripts and writes
// stubForge beside it, returning the environment entry that selects it.
func installStubForge(t *testing.T, root string) string {
	t.Helper()
	wrapper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-forge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "ci-forge.sh"), wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(root, "stub-forge")
	if err := os.WriteFile(stub, []byte(stubForge), 0o755); err != nil {
		t.Fatal(err)
	}
	return "CI_FORGE=" + stub
}
