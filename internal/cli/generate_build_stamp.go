package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Recording which forge build last generated a tree.
//
// Two forge builds can share one PATH: `forge` (standalone, `go install
// ./cmd/forge`) and `reliant forge` (compiled into the reliant binary, which
// only changes when RELIANT is rebuilt). Install one and not the other and
// they drift apart silently. They then disagree about the same project: one
// generates it happily, the other refuses at the forge/pkg compatibility
// handshake — and nothing in that refusal used to say "a different forge
// build generated this tree."
//
// The build id is recorded in .forge/, NOT forge.yaml, on purpose:
//
//   - forge.yaml is committed and shared. A machine-local build identity
//     ("embedded in reliant v1.5.1 @0359a80a") would churn the file on every
//     developer's first generate and produce meaningless diffs and conflicts.
//     forge.yaml's existing forge_version is a deliberate, semantic PIN;
//     this is an observation about the last local run.
//   - .forge/ is already the project's local-state dir (lockfile, debug
//     binaries, idp secrets) and is already excluded from checksum walks.
//
// The record is advisory. A missing or unreadable file simply means "we
// don't know what generated this," and the diagnosis degrades to naming the
// running build — still strictly better than the old message.

// generatingBuildFile is the .forge-relative path recording the forge build
// identity that last ran `generate` against this tree.
const generatingBuildFile = "generating-build"

// generatingBuildPath returns the path of the build-identity record for a
// project.
func generatingBuildPath(projectDir string) string {
	return filepath.Join(projectDir, ".forge", generatingBuildFile)
}

// recordGeneratingBuild writes this forge build's identity into the
// project's .forge/ dir. Best-effort by design: failing to record must never
// fail a generate that otherwise succeeded, since the record is a diagnostic
// aid and not a correctness input.
func recordGeneratingBuild(projectDir string) {
	path := generatingBuildPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = atomicfile.WriteFile(path, []byte(buildinfo.Identity()+"\n"), 0o644)
}

// readGeneratingBuild returns the recorded identity of the forge build that
// last generated this tree, or "" when unknown (never generated, a tree from
// before this record existed, or an unreadable file).
func readGeneratingBuild(projectDir string) string {
	data, err := os.ReadFile(generatingBuildPath(projectDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
