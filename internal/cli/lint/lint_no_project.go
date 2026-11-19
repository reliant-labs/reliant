// File: internal/cli/lint/lint_no_project.go
//
// `forge lint` in a repo that is not a forge project.
//
// forge lint runs anywhere: an existing Node, Python or Go app with no
// forge.yaml still gets migration safety, the marker lanes and the rest that
// read only files. A lane that needs something the repo does not have — a Go
// module, the forge_ro role `forge generate` writes — does not APPLY there,
// which is a skip with a reason, never a failure: failing it would make
// `forge lint` red in every repo that has not adopted forge, for checks that
// have nothing to check.

package lint

import (
	"os"
	"path/filepath"
)

// inGoModule reports whether the directory lint runs in sits in a Go module:
// a go.mod at it or in any parent. Used only when there is no forge.yaml; a
// forge project answers from its config (config.ProjectConfig.HasGoModule).
func (rc *lintRunCtx) inGoModule() bool {
	dir, err := filepath.Abs(rc.moduleDir())
	if err != nil {
		return true // cannot tell: run the lane and let it report
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// noGoModuleSkip is the skip reason for a Go lane in a repo with no go.mod.
func noGoModuleSkip(lane string) string {
	return "no go.mod and no forge.yaml — skipping " + lane + ": there is no Go module to lint"
}
