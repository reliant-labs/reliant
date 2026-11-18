package lint

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/frontenddeps"
)

// ruleFrontendLockDrift is a frontend whose package.json and lockfile disagree
// about a dependency range: the state `npm ci` (and pnpm's --frozen-lockfile)
// refuses, which forge's own range bumps used to leave behind.
const ruleFrontendLockDrift = "forge-frontend-lock-drift"

// collectFrontendLockJSON compares each frontend's package.json with the
// lockfile that governs it, offline: no install, no registry. Every frontend is
// checked, adopted ones included — a frozen install fails on any drift,
// whoever caused it. A frontend with no lockfile has nothing to disagree with.
func collectFrontendLockJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	root := rc.cwd
	var dirs []string
	for _, t := range frontendTypecheckTargets(rc.cfg) {
		dirs = append(dirs, t.dir)
	}
	if hooks := filepath.Join("packages", "hooks"); dirExists(hooks) {
		dirs = append(dirs, hooks)
	}
	var out []lintJSONFinding
	for _, dir := range dirs {
		lock, ok := frontenddeps.FindLock(dir, root)
		if !ok {
			continue
		}
		drift, err := frontenddeps.LockDrift(lock, dir)
		if err != nil {
			out = append(out, lintJSONFinding{
				File:     filepath.ToSlash(filepath.Join(dir, "package.json")),
				Severity: lintSevError,
				Rule:     ruleFrontendLockDrift,
				Message:  fmt.Sprintf("could not compare package.json with %s: %v", lock.File, err),
			})
			continue
		}
		if drift == nil {
			continue
		}
		out = append(out, lintJSONFinding{
			File:     filepath.ToSlash(filepath.Join(dir, "package.json")),
			Severity: lintSevError,
			Rule:     ruleFrontendLockDrift,
			Message: fmt.Sprintf("package.json and %s are out of sync, so a frozen install (`%s ci`) fails: %s",
				lock.File, lock.Runner, strings.Join(drift.Details, "; ")),
			FixHint: "refresh the lockfile and commit it: " + frontenddeps.LockRefreshCommand(lock, dir),
		})
	}
	return out, len(out) > 0, nil
}
