package cigate

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/templates"
)

// LockfileCheckName is the affected-mode row proving frontend fe's lockfile still
// installs: lockfile-<name>.
func LockfileCheckName(fe string) string { return "lockfile-" + fe }

// WithLockfileChecks adds a lockfile-<name> row, just before deps-<name>, for
// each frontend whose package.json, lockfile or pnpm-workspace.yaml changed
// on a branch in affected mode. The tests run against whatever node_modules
// is on disk, so without it nothing notices a lockfile the package manager's
// frozen install rejects: reliant #1343 bumped a dependency in package.json,
// hand-edited the lockfile, and every fresh install of main then failed with
// "Missing: zone.js from lock file" while every local test stayed green.
//
// The row asks the frontend's own package manager (frontenddeps detection).
// npm validates offline with a dry run; pnpm uses lockfile-only mode. Yarn
// installs in a disposable copy, with lifecycle scripts disabled, because it
// has no non-mutating validation mode. In full mode (no changed files), and
// for a frontend whose deps row is not selected (another lane), nothing is added.
func WithLockfileChecks(checks []Check, a Affected, root string, frontends []templates.FrontendCIConfig) []Check {
	if len(a.Changed) == 0 || len(frontends) == 0 {
		return checks
	}
	taken := map[string]bool{}
	for _, c := range checks {
		taken[c.Name] = true
	}
	rows := map[string]Check{} // by the deps row it precedes
	for _, fe := range frontends {
		if taken[LockfileCheckName(fe.Name)] || !taken[depsName(fe)] {
			continue
		}
		if c, ok := lockfileCheck(root, fe, a.Changed); ok {
			rows[depsName(fe)] = c
		}
	}
	if len(rows) == 0 {
		return checks
	}

	out := make([]Check, 0, len(checks)+len(rows))
	for _, c := range checks {
		if r, ok := rows[c.Name]; ok {
			r.Lane = c.Lane
			out = append(out, r)
		}
		out = append(out, c)
	}
	return out
}

// lockfileCheck is fe's lockfile row, or false when none of its manifest,
// lockfile or workspace file changed.
func lockfileCheck(root string, fe templates.FrontendCIConfig, changed []string) (Check, bool) {
	feDir := filepath.Join(root, fe.Path)
	lock, ok := frontenddeps.FindLock(feDir, root)
	if !ok {
		return Check{}, false // no lockfile to be out of sync with
	}
	lockRel, err := filepath.Rel(root, lock.Dir)
	if err != nil {
		return Check{}, false
	}
	watch := map[string]bool{
		relJoin(fe.Path, "package.json"): true,
		relJoin(lockRel, lock.File):      true,
	}
	if lock.Runner == "pnpm" {
		watch[relJoin(lockRel, "pnpm-workspace.yaml")] = true
	}
	var hits []string
	for _, f := range changed {
		if watch[path.Clean(filepath.ToSlash(f))] {
			hits = append(hits, path.Clean(filepath.ToSlash(f)))
		}
	}
	if len(hits) == 0 {
		return Check{}, false
	}
	argv := frontenddeps.ExecArgv(lock.Dir, lock.Runner, lockfileArgs(lock)...)
	if lock.Runner == "yarn" {
		argv = append([]string{"node", "-e", isolatedYarnLockfileCheck}, argv...)
	}
	dir := filepath.ToSlash(lockRel)
	if dir == "." {
		dir = ""
	}
	return Check{
		Name:     LockfileCheckName(fe.Name),
		Group:    GroupSetup,
		Requires: []string{argv[0]},
		Argv:     argv,
		Dir:      dir,
		Note:     "affected mode: " + strings.Join(hits, ", ") + " changed — the lockfile must still install frozen",
	}, true
}

// lockfileArgs asks lock's package manager whether a frozen install would
// accept the lockfile, writing nothing it can avoid.
func lockfileArgs(lock frontenddeps.Lock) []string {
	switch lock.Runner {
	case "pnpm":
		return []string{"install", "--frozen-lockfile", "--lockfile-only", "--ignore-scripts"}
	case "yarn":
		if _, err := os.Stat(filepath.Join(lock.Dir, ".yarnrc.yml")); err == nil {
			return []string{"install", "--immutable", "--mode=skip-build"}
		}
		return []string{"install", "--frozen-lockfile", "--ignore-scripts", "--non-interactive"}
	default:
		return []string{"ci", "--dry-run", "--offline", "--ignore-scripts", "--no-audit", "--no-fund"}
	}
}

// Yarn has no dry-run/lockfile-only validation. Copy the workspace, not just
// package.json: local workspace manifests and committed Yarn configuration are
// inputs too. Never link the checkout's node_modules into the disposable tree.
// node is already a prerequisite of Yarn and avoids a platform-specific shell.
const isolatedYarnLockfileCheck = `
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const cp = require('node:child_process');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'forge-lockfile-'));
try {
  const dest = path.join(tmp, 'workspace');
  fs.cpSync(process.cwd(), dest, {
    recursive: true, dereference: true,
    filter: p => !['node_modules', '.git', '.forge'].includes(path.basename(p)),
  });
  const result = cp.spawnSync(process.argv[1], process.argv.slice(2), {
    cwd: dest, stdio: 'inherit',
    env: {...process.env, YARN_ENABLE_SCRIPTS: 'false'},
  });
  if (result.error) throw result.error;
  process.exitCode = result.status === null ? 1 : result.status;
} finally {
  fs.rmSync(tmp, {recursive: true, force: true});
}
`

func relJoin(dir, file string) string {
	return path.Clean(path.Join(filepath.ToSlash(dir), file))
}

// WithLockfileReport records in a the lockfile rows checks carries: the
// --json document's affected.lockfile_checks.
func WithLockfileReport(a Affected, checks []Check) Affected {
	a.LockfileChecks = nil
	for _, c := range checks {
		if strings.HasPrefix(c.Name, LockfileCheckName("")) {
			a.LockfileChecks = append(a.LockfileChecks, c.Name)
		}
	}
	sort.Strings(a.LockfileChecks)
	return a
}
