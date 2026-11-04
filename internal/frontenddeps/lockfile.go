package frontenddeps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Lock is the lockfile that records a manifest's dependency ranges: where it
// lives and which package manager wrote it.
type Lock struct {
	// Dir is the directory the lockfile sits in (the install root it belongs
	// to). It is the manifest's own directory unless a workspace root above it
	// holds the lockfile.
	Dir string
	// File is the lockfile's base name (package-lock.json, pnpm-lock.yaml,
	// yarn.lock).
	File string
	// Runner is the package manager that owns it: npm, pnpm or yarn.
	Runner string
}

// Path is the lockfile's full path.
func (l Lock) Path() string { return filepath.Join(l.Dir, l.File) }

// lockfiles lists the lockfiles forge understands, in the order they are
// preferred when one directory holds several.
var lockfiles = []struct{ file, runner string }{
	{"pnpm-lock.yaml", "pnpm"},
	{"yarn.lock", "yarn"},
	{"package-lock.json", "npm"},
}

// FindLock returns the lockfile that governs manifestDir's package.json: the
// nearest one in manifestDir or an ancestor, never above stopAt. A workspace
// member's lockfile lives at the workspace root, so the walk goes upward; the
// nearest wins because that is the one the package manager installs against.
func FindLock(manifestDir, stopAt string) (Lock, bool) {
	dir, err := filepath.Abs(manifestDir)
	if err != nil {
		return Lock{}, false
	}
	stop, err := filepath.Abs(stopAt)
	if err != nil {
		return Lock{}, false
	}
	for {
		for _, lf := range lockfiles {
			if fileExists(filepath.Join(dir, lf.file)) {
				return Lock{Dir: dir, File: lf.file, Runner: lf.runner}, true
			}
		}
		if dir == stop {
			return Lock{}, false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Lock{}, false
		}
		dir = parent
	}
}

// lockRefreshArgs returns the package-manager invocation that rewrites the
// lockfile to match the manifests without installing a tree or running a
// dependency's scripts, and the directory to run it in.
//
// npm is pinned to the manifest's own directory with --prefix when the
// lockfile sits beside it: without it npm walks up to a workspace root that
// adopts the frontend (forge's dev web-runtime bridge is one) and rewrites the
// root's lockfile instead of the tracked one.
func lockRefreshArgs(lock Lock, manifestDir string) (runner string, args []string, dir string) {
	switch lock.Runner {
	case "pnpm":
		args := []string{"install", "--lockfile-only", "--ignore-scripts", "--no-frozen-lockfile"}
		if PnpmInstallRoot(manifestDir) != manifestDir && sameDir(lock.Dir, manifestDir) {
			args = append(args, "--ignore-workspace")
		}
		return "pnpm", args, lock.Dir
	case "yarn":
		// Berry can rewrite the lockfile alone; classic has no such mode, so
		// it installs (which rewrites it as a side effect).
		if fileExists(filepath.Join(lock.Dir, ".yarnrc.yml")) {
			return "yarn", []string{"install", "--mode", "update-lockfile"}, lock.Dir
		}
		return "yarn", []string{"install", "--ignore-scripts"}, lock.Dir
	}
	args = []string{"install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund"}
	if sameDir(lock.Dir, manifestDir) {
		args = append(args, "--prefix", lock.Dir)
	}
	return "npm", args, lock.Dir
}

// LockRefreshCommand is the exact command that brings lock back in step with
// manifestDir's package.json, as a shell line, for messages.
func LockRefreshCommand(lock Lock, manifestDir string) string {
	runner, args, dir := lockRefreshArgs(lock, manifestDir)
	return fmt.Sprintf("(cd %s && %s %s)", dir, runner, strings.Join(args, " "))
}

// RefreshLock rewrites lock so it agrees with manifestDir's package.json,
// touching only what the manifest changed: the package manager keeps every
// version the lockfile already pins and still satisfies its range.
//
// It is the step that has to follow any edit forge makes to a dependency
// range. Without it `npm ci` fails ("package.json and package-lock.json are
// not in sync") until someone runs an install by hand, and nothing says so.
// A failure is returned with the command, so the caller can name it; a missing
// package manager or an unreachable registry must not be a silent success.
func RefreshLock(ctx context.Context, lock Lock, manifestDir string) error {
	runner, args, dir := lockRefreshArgs(lock, manifestDir)
	restore := keepHiddenLockfile(runner, lock.Dir)
	err := runQuiet(ctx, runner, dir, args)
	if err != nil && transientFailure(err.Error()) {
		err = runQuiet(ctx, runner, dir, args)
	}
	if rerr := restore(); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// keepHiddenLockfile snapshots npm's hidden lockfile,
// node_modules/.package-lock.json, and returns the func that puts it back.
//
// npm records the tree it INSTALLED in that file, and trusts it: a later
// `npm install` reads it instead of walking node_modules, and a gate that
// skips `npm ci` while it is newer than package-lock.json does the same. But
// `npm install --package-lock-only` rewrites it to the new lockfile's tree
// while installing nothing (npm 10, verified), so after a refresh that raised
// a range node_modules claims a version it does not hold, and nothing
// reinstalls it: typecheck then fails against the old package. Putting the
// file back as it was (content and mtime), or removing it when there was
// none, leaves node_modules describing itself truthfully — stale, so the next
// install runs.
func keepHiddenLockfile(runner, lockDir string) func() error {
	if runner != "npm" {
		return func() error { return nil }
	}
	path := filepath.Join(lockDir, "node_modules", ".package-lock.json")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return func() error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s, which the lockfile refresh wrote without installing: %w", path, err)
			}
			return nil
		}
	}
	data, rerr := os.ReadFile(path)
	if err != nil || rerr != nil {
		// Unreadable: it cannot be put back, so do not let it vouch for a
		// tree either.
		return func() error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s after the lockfile refresh: %w", path, err)
			}
			return nil
		}
	}
	return func() error {
		if err := atomicfile.WriteFile(path, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("restoring %s after the lockfile refresh: %w", path, err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			return fmt.Errorf("restoring %s's mtime after the lockfile refresh: %w", path, err)
		}
		return nil
	}
}

// runQuiet runs a package manager, keeping its output for the error: a
// successful refresh has nothing to say.
func runQuiet(ctx context.Context, runner, dir string, args []string) error {
	var buf bytes.Buffer
	cmd := command(ctx, dir, runner, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(buf.String())
		if out == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// Drift is how a manifest and its lockfile disagree.
type Drift struct {
	Lock Lock
	// Details names each disagreement, one line each, sorted.
	Details []string
}

// depFields are the manifest sections a lockfile records for its own package.
var depFields = []string{"dependencies", "devDependencies", "optionalDependencies"}

// LockDrift reports whether lock records the same dependency ranges as
// manifestDir's package.json. It returns nil when they agree, when there is
// nothing to compare (no manifest, a lockfile format that does not record
// ranges, a package the lockfile does not list), and for yarn, whose
// lockfile keys are not a per-package record.
//
// This is the question `npm ci` / `pnpm install --frozen-lockfile` asks first,
// answered without a registry or an install, so a lint can ask it. only, when
// given, narrows the comparison to those package names.
func LockDrift(lock Lock, manifestDir string, only ...string) (*Drift, error) {
	want, err := manifestRanges(manifestDir)
	if err != nil || want == nil {
		return nil, err
	}
	var got map[string]string
	var listed bool
	switch lock.Runner {
	case "npm":
		got, listed, err = npmLockRanges(lock, manifestDir)
	case "pnpm":
		got, listed, err = pnpmLockRanges(lock, manifestDir)
	default:
		return nil, nil
	}
	if err != nil || !listed {
		return nil, err
	}
	if len(only) > 0 {
		keep := map[string]bool{}
		for _, n := range only {
			keep[n] = true
		}
		for _, m := range []map[string]string{want, got} {
			for name := range m {
				if !keep[name] {
					delete(m, name)
				}
			}
		}
	}
	var details []string
	for name, spec := range want {
		switch locked, ok := got[name]; {
		case !ok:
			details = append(details, fmt.Sprintf("%s: package.json declares %q, the lockfile does not list it", name, spec))
		case locked != spec:
			details = append(details, fmt.Sprintf("%s: package.json declares %q, the lockfile records %q", name, spec, locked))
		}
	}
	for name, locked := range got {
		if _, ok := want[name]; !ok {
			details = append(details, fmt.Sprintf("%s: the lockfile records %q, package.json no longer declares it", name, locked))
		}
	}
	if len(details) == 0 {
		return nil, nil
	}
	sort.Strings(details)
	return &Drift{Lock: lock, Details: details}, nil
}

// manifestRanges is name → range across every dependency section of
// manifestDir's package.json, or nil when there is no manifest.
func manifestRanges(manifestDir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(manifestDir, "package.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(manifestDir, "package.json"), err)
	}
	out := map[string]string{}
	for _, field := range depFields {
		var deps map[string]string
		if raw, ok := m[field]; ok && json.Unmarshal(raw, &deps) == nil {
			for name, spec := range deps {
				out[name] = spec
			}
		}
	}
	return out, nil
}

// lockMemberKey is the key a lockfile files a workspace member under: the
// member's path relative to the lockfile's directory, "" or "." for the root.
func lockMemberKey(lock Lock, manifestDir string) (string, bool) {
	abs, err := filepath.Abs(manifestDir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(lock.Dir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}

// npmLockRanges reads the ranges package-lock.json recorded for the package at
// manifestDir. listed is false for a lockfile that does not record them
// (lockfileVersion 1) or does not file this package.
func npmLockRanges(lock Lock, manifestDir string) (map[string]string, bool, error) {
	key, ok := lockMemberKey(lock, manifestDir)
	if !ok {
		return nil, false, nil
	}
	data, err := os.ReadFile(lock.Path())
	if err != nil {
		return nil, false, err
	}
	var doc struct {
		Packages map[string]map[string]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", lock.Path(), err)
	}
	pkg, ok := doc.Packages[key]
	if !ok {
		return nil, false, nil
	}
	out := map[string]string{}
	for _, field := range depFields {
		var deps map[string]string
		if raw, ok := pkg[field]; ok && json.Unmarshal(raw, &deps) == nil {
			for name, spec := range deps {
				out[name] = spec
			}
		}
	}
	return out, true, nil
}

// pnpmLockRanges reads the specifiers pnpm-lock.yaml recorded for the package
// at manifestDir: the importers map (lockfile v6+), or the top-level
// specifiers map of a single-project v5 lockfile.
func pnpmLockRanges(lock Lock, manifestDir string) (map[string]string, bool, error) {
	key, ok := lockMemberKey(lock, manifestDir)
	if !ok {
		return nil, false, nil
	}
	if key == "" {
		key = "."
	}
	data, err := os.ReadFile(lock.Path())
	if err != nil {
		return nil, false, err
	}
	type entry struct {
		Specifier string `yaml:"specifier"`
	}
	type importer struct {
		Dependencies         map[string]entry `yaml:"dependencies"`
		DevDependencies      map[string]entry `yaml:"devDependencies"`
		OptionalDependencies map[string]entry `yaml:"optionalDependencies"`
	}
	type document struct {
		Importers  map[string]importer `yaml:"importers"`
		Specifiers map[string]string   `yaml:"specifiers"`
	}
	// pnpm 12 writes TWO YAML documents when package.json pins pnpm itself
	// (packageManager / devEngines): the first records only the pinned pnpm
	// (packageManagerDependencies), the second is the project's lockfile.
	// yaml.Unmarshal decodes the first alone, which made every dependency read
	// as "the lockfile does not list it". The project's document is the last
	// one that records importers or specifiers.
	var doc document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var d document
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("parse %s: %w", lock.Path(), err)
		}
		if len(d.Importers) > 0 || len(d.Specifiers) > 0 {
			doc = d
		}
	}
	out := map[string]string{}
	if len(doc.Importers) > 0 {
		imp, ok := doc.Importers[key]
		if !ok {
			return nil, false, nil
		}
		for _, section := range []map[string]entry{imp.Dependencies, imp.DevDependencies, imp.OptionalDependencies} {
			for name, e := range section {
				out[name] = e.Specifier
			}
		}
		return out, true, nil
	}
	if key == "." && len(doc.Specifiers) > 0 {
		return doc.Specifiers, true, nil
	}
	return nil, false, nil
}

func sameDir(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == bb
}
