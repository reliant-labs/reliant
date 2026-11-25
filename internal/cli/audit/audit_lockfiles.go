// File: internal/cli/audit/audit_lockfiles.go
//
// The `lockfiles` audit category: one project, one JavaScript package
// manager.
//
// A repository whose JS packages are locked by two package managers —
// a pnpm-lock.yaml in one frontend, a package-lock.json in a tool
// directory, a yarn.lock left behind by a migration — installs each of
// them with different resolution rules, a different store and different
// CI caching, and nothing tells the reader which lock is authoritative.
// Two lockfiles in ONE directory are worse: whichever manager runs there
// rewrites its own lock and leaves the other one describing a tree that
// no longer exists. Neither breaks a build today, which is exactly why it
// drifts unnoticed, so the category is advisory: `warn`, never `error`,
// and audit's exit code (audit_gate.go) is unchanged by it.

package audit

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// jsLockfiles maps each lockfile name to the package manager that writes
// it. npm-shrinkwrap.json is npm's publishable lock; bun has written both
// a binary (bun.lockb) and a text (bun.lock) lock.
var jsLockfiles = map[string]string{
	"package-lock.json":   "npm",
	"npm-shrinkwrap.json": "npm",
	"pnpm-lock.yaml":      "pnpm",
	"yarn.lock":           "yarn",
	"bun.lockb":           "bun",
	"bun.lock":            "bun",
}

// lockfileWalkSkip are directories never descended into: dependency trees
// (a lockfile inside node_modules belongs to a dependency, not to this
// project), build output, and forge/VCS state.
var lockfileWalkSkip = map[string]struct{}{
	"node_modules": {}, "vendor": {}, ".git": {}, ".forge": {}, "testdata": {},
	"dist": {}, "build": {}, "out": {}, ".next": {}, ".expo": {}, "bin": {}, "gen": {},
}

// lockfileDir is one directory that holds lockfiles of more than one
// package manager.
type lockfileDir struct {
	Dir       string   `json:"dir"`
	Lockfiles []string `json:"lockfiles"`
}

// auditLockfiles reports every JS lockfile in the project grouped by the
// package manager that writes it. It warns when more than one manager is
// present anywhere in the project, naming the directories that hold two
// different managers' locks and the lockfiles that are not the primary
// manager's.
func auditLockfiles(projectDir string) audittype.Category {
	byManager := map[string][]string{}
	byDir := map[string]map[string][]string{} // dir -> manager -> lockfiles

	if err := filepath.WalkDir(projectDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := lockfileWalkSkip[d.Name()]; skip && p != projectDir {
				return filepath.SkipDir
			}
			return nil
		}
		manager, ok := jsLockfiles[d.Name()]
		if !ok {
			return nil
		}
		rel, rerr := filepath.Rel(projectDir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		byManager[manager] = append(byManager[manager], rel)
		dir := path.Dir(rel)
		if byDir[dir] == nil {
			byDir[dir] = map[string][]string{}
		}
		byDir[dir][manager] = append(byDir[dir][manager], rel)
		return nil
	}); err != nil {
		// A partial walk cannot establish which manager is primary or
		// recommend deleting any lockfile. Report the cause instead.
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: "JavaScript lockfile scan incomplete",
			Details: map[string]any{"scan_error": err.Error()},
		}
	}

	managers := make([]string, 0, len(byManager))
	for m, files := range byManager {
		sort.Strings(files)
		managers = append(managers, m)
	}
	sort.Strings(managers)

	details := map[string]any{
		"managers":  managers,
		"lockfiles": byManager,
	}
	if len(managers) == 0 {
		return audittype.Category{Status: audittype.StatusOK, Summary: "no JavaScript lockfiles", Details: details}
	}

	primary := primaryLockfileManager(byManager, byDir)
	details["primary_manager"] = primary

	var mixedDirs []lockfileDir
	for dir, locks := range byDir {
		if len(locks) < 2 {
			continue
		}
		var files []string
		for _, lf := range locks {
			files = append(files, lf...)
		}
		sort.Strings(files)
		mixedDirs = append(mixedDirs, lockfileDir{Dir: dir, Lockfiles: files})
	}
	sort.Slice(mixedDirs, func(i, j int) bool { return mixedDirs[i].Dir < mixedDirs[j].Dir })

	if len(managers) == 1 {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: fmt.Sprintf("one package manager (%s), %d lockfile(s)", primary, len(byManager[primary])),
			Details: details,
		}
	}

	var stragglers []string
	for _, m := range managers {
		if m != primary {
			stragglers = append(stragglers, byManager[m]...)
		}
	}
	sort.Strings(stragglers)
	details["stragglers"] = stragglers
	if len(mixedDirs) > 0 {
		details["mixed_dirs"] = mixedDirs
	}
	details["hint"] = fmt.Sprintf(
		"lock every JS package with one manager: in each straggler's directory delete the lockfile and run `%s install`, "+
			"or make the packages members of one workspace with one lockfile at its root (forge skill `frontend-workspaces`)",
		primary)

	summary := fmt.Sprintf("%d package managers lock this project's JS packages (%s); primary %s, %d straggler lockfile(s)",
		len(managers), strings.Join(managers, ", "), primary, len(stragglers))
	if len(mixedDirs) > 0 {
		summary += fmt.Sprintf(", %d directory(ies) holding two managers' lockfiles", len(mixedDirs))
	}
	return audittype.Category{Status: audittype.StatusWarn, Summary: summary, Details: details}
}

// primaryLockfileManager picks the manager the stragglers should convert
// to: the one locking the project root when exactly one does (a root lock
// is the workspace's), else the manager with the most lockfiles, ties
// broken by name so the answer is stable.
func primaryLockfileManager(byManager map[string][]string, byDir map[string]map[string][]string) string {
	if root := byDir["."]; len(root) == 1 {
		for m := range root {
			return m
		}
	}
	best := ""
	for m, files := range byManager {
		if best == "" || len(files) > len(byManager[best]) || (len(files) == len(byManager[best]) && m < best) {
			best = m
		}
	}
	return best
}
