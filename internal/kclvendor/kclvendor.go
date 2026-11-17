// Package kclvendor supplies the forge KCL module to every KCL evaluation
// forge performs, straight from the binary that is performing it.
//
// A project's env `main.k` files `import forge` — the typed schemas and
// render layer shipped as github.com/reliant-labs/forge/kcl and EMBEDDED in
// the forge binary. Forge resolves that import ONE way, on every build of
// forge: it materializes the embedded module into a content-addressed
// directory in the USER cache (<UserCacheDir>/forge/kcl/<hash>/) and hands
// it to KCL as an external package (`forge=<dir>`) at render and at option
// discovery. The project declares no `forge` dependency in kcl.mod and
// holds no copy of the module anywhere in its tree.
//
// # Why the module comes from the binary, and nowhere else
//
// The version that must render a project is the version of the forge doing
// the render. For a project pinned to a released forge (go.mod
// `github.com/reliant-labs/forge vX.Y.Z`), the binary CI installs at that pin
// IS that release's module — version-matched by construction, with no
// network, no git, no registry and nothing to publish. A dev build uses the
// identical mechanism with its own embedded module. There is no dev/release
// split to maintain, and no committed file differs by which build generated
// it: kcl.mod is byte-identical under every forge.
//
// # What this replaced (docs/adr/0003-kcl-module-from-the-binary.md)
//
// Before this, every project carried the module as `.forge-kcl/` and kcl.mod
// pointed at it (`forge = { path = "../../.forge-kcl" }`). That copy was
// committed, and every failure it produced came from two forge builds
// sharing one copy through git: a developer's older binary rewrote
// control-plane's committed schema.k backwards and broke prod's `env
// render`; the downgrade refusal added in response then failed a scaffolded
// project's CI outright, because the forge its workflow pinned lagged go.mod
// by one commit ("refusing to overwrite .forge-kcl/ with an OLDER forge's
// KCL module"). Keeping the copy out of git and syncing it per render would
// have fixed the sharing, but a project-relative path in kcl.mod still
// required every machine — CI, containers, the deploy path — to materialize
// a project-local directory before anything rendered. Supplying the module
// as an external package needs neither.
//
// Before THAT, release builds pointed kcl.mod at a `kcl-vX.Y.Z` git tag that
// was never published (docs/adr/0001-always-vendor-forge-kcl.md). The
// external-package mechanism keeps ADR 0001's decision — one mechanism, from
// the binary, offline — and drops only its vendored project copy.
//
// # Migration
//
// [MigrateKclMod] removes the legacy `forge = …` dependency (and the
// marker block forge maintained around it) from a managed kcl.mod, and drops
// a kcl.mod.lock that still records it: kpm resolves a locked `forge` package
// ahead of the external one, which would shadow the binary's module.
// [RemoveLegacyVendorDir] deletes the project-local `.forge-kcl/`. Both are
// run by `forge generate`; a render that finds an unmigrated kcl.mod fails
// with that instruction ([CheckKclMods]) rather than evaluating a stale copy.
package kclvendor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/checksums"
	forgekcl "github.com/reliant-labs/forge/kcl"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ModuleName is the KCL package name projects import (`import forge`).
const ModuleName = "forge"

// LegacyVendorDirName is the project-root directory older forge versions
// materialized the module into. Nothing reads it any more; it survives as a
// name so migration can remove it and the scaffold .gitignore can keep a
// stray one out of git.
const LegacyVendorDirName = ".forge-kcl"

// MarkerHeader is the first line of the dependency block older forge
// versions maintained in kcl.mod. Migration recognizes it so it removes the
// whole forge-owned block, not just the dependency line under it.
const MarkerHeader = "# ── Vendored forge KCL module (maintained by forge generate) ──"

// legacyMarkerHeaders are marker headers even earlier forge versions wrote.
var legacyMarkerHeaders = []string{
	"# ── Dev-mode local forge KCL module vendor ──",
}

// CacheDirEnv names the directory forge materializes its KCL module under,
// replacing <UserCacheDir>/forge/kcl. For environments whose home directory
// is read-only or ephemeral (a locked-down CI container) and for test
// isolation. The module lands in <CacheDirEnv>/<content-hash>/.
const CacheDirEnv = "FORGE_KCL_MODULE_CACHE"

// cacheDirOverride, when non-empty, replaces the cache root ahead of
// CacheDirEnv. Tests set it (via SetCacheDirForTest) so they never write
// into a developer's real cache.
var cacheDirOverride string

// SetCacheDirForTest points the module cache at dir for the duration of a
// test and returns a restore func. Not for production use.
func SetCacheDirForTest(dir string) (restore func()) {
	moduleMu.Lock()
	prev, prevDir := cacheDirOverride, moduleDir
	cacheDirOverride, moduleDir = dir, ""
	moduleMu.Unlock()
	return func() {
		moduleMu.Lock()
		cacheDirOverride, moduleDir = prev, prevDir
		moduleMu.Unlock()
	}
}

var (
	moduleMu  sync.Mutex
	moduleDir string // memoized per process once materialized
)

// ModuleDir returns a directory holding exactly the KCL module embedded in
// this binary, materializing it on first use.
//
// The directory is <UserCacheDir>/forge/kcl/<hash>, keyed by a hash of the
// embedded CONTENT rather than by version string: two builds that embed an
// identical module share one directory, and a `+dirty` rebuild with a
// schema change can never be served a stale directory under the same
// version name. Content-addressing also makes the directory immutable once
// complete, so concurrent forge processes need no coordination beyond an
// atomic rename into place.
func ModuleDir() (string, error) {
	moduleMu.Lock()
	defer moduleMu.Unlock()
	if moduleDir != "" {
		return moduleDir, nil
	}
	root := cacheDirOverride
	if root == "" {
		root = os.Getenv(CacheDirEnv)
	}
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("locate the user cache directory for the forge KCL module: %w", err)
		}
		root = filepath.Join(base, "forge", "kcl")
	}
	hash, err := moduleHash()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, hash)
	if complete(dir) {
		moduleDir = dir
		return dir, nil
	}
	if err := materializeInto(root, dir); err != nil {
		return "", err
	}
	moduleDir = dir
	return dir, nil
}

// ExternalPkgArg is the `name=path` external-package binding a KCL
// evaluation needs to resolve `import forge` from this binary's module.
func ExternalPkgArg() (string, error) {
	dir, err := ModuleDir()
	if err != nil {
		return "", err
	}
	return ModuleName + "=" + dir, nil
}

// completeMarker is written last into a materialized module directory. A
// directory without it is a partial write from a process that died, and is
// rebuilt rather than trusted.
const completeMarker = ".complete"

func complete(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, completeMarker))
	return err == nil
}

// moduleHash is the hex SHA-256 over every embedded file's path and bytes,
// in sorted order.
func moduleHash() (string, error) {
	files, err := embeddedModuleFiles()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, p := range files {
		data, err := fs.ReadFile(forgekcl.Module, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:24], nil
}

// embeddedModuleFiles lists every file in the embedded module, sorted, in
// forward-slash form.
func embeddedModuleFiles() ([]string, error) {
	var out []string
	err := fs.WalkDir(forgekcl.Module, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read embedded forge KCL module: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// materializeInto writes the embedded module into a fresh temp directory
// under root and renames it to dir. A concurrent process that wins the
// rename first leaves an identical directory behind (same content hash), so
// losing the race is success.
func materializeInto(root, dir string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create forge KCL module cache %s: %w", root, err)
	}
	tmp, err := os.MkdirTemp(root, ".tmp-")
	if err != nil {
		return fmt.Errorf("create forge KCL module cache entry: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	files, err := embeddedModuleFiles()
	if err != nil {
		return err
	}
	for _, p := range files {
		data, err := fs.ReadFile(forgekcl.Module, p)
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	if err := atomicfile.WriteFile(filepath.Join(tmp, completeMarker), nil, 0o644); err != nil {
		return err
	}
	// A leftover partial directory at dir (no completeMarker) blocks the
	// rename; clear it first.
	if _, err := os.Stat(dir); err == nil && !complete(dir) {
		_ = os.RemoveAll(dir)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if complete(dir) {
			return nil // another process materialized the same content
		}
		return fmt.Errorf("install forge KCL module into %s: %w", dir, err)
	}
	return nil
}

// ── kcl.mod migration ────────────────────────────────────────────────────

// Result reports what a migration call did.
type Result struct {
	// Changed is true when the file was rewritten.
	Changed bool
	// Warning, when non-empty, is a caller-surfaceable reason the file
	// was left alone.
	Warning string
}

// forgeDepLineRE matches a single-line `forge = …` dependency in kcl.mod.
var forgeDepLineRE = regexp.MustCompile(`^[\t ]*forge[\t ]*=[\t ]*(.+?)[\t ]*$`)

// forgeTableRE matches a `[dependencies.forge]` table header — a spelling
// forge never wrote, which migration therefore refuses to edit.
var forgeTableRE = regexp.MustCompile(`^[\t ]*\[[\t ]*dependencies[\t ]*\.[\t ]*forge[\t ]*\][\t ]*$`)

// ManagedKclMods returns the kcl.mod locations forge manages:
// deploy/kcl/kcl.mod (the canonical package root) and the legacy project-root
// kcl.mod older scaffolds emitted.
func ManagedKclMods(projectDir string) []string {
	return []string{
		filepath.Join(projectDir, "deploy", "kcl", "kcl.mod"),
		filepath.Join(projectDir, "kcl.mod"),
	}
}

// forgeDep locates the forge dependency in a kcl.mod. idx is the line of a
// single-line dependency (-1 when there is none); table reports a
// `[dependencies.forge]` table, which is never edited.
func forgeDep(lines []string) (idx int, count int, table bool) {
	idx = -1
	for i, line := range lines {
		if forgeTableRE.MatchString(line) {
			table = true
			continue
		}
		if forgeDepLineRE.MatchString(line) {
			if idx == -1 {
				idx = i
			}
			count++
		}
	}
	return idx, count, table
}

// ownedBlockStart walks up from the dependency line over the contiguous
// comment run directly above it. If that run begins with MarkerHeader — or a
// header an earlier forge wrote — the block [start..depIdx] is forge-owned and
// returns start; otherwise returns depIdx (only the line itself is ours to
// remove, so user comments above it are preserved).
func ownedBlockStart(lines []string, depIdx int) int {
	start := depIdx
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") {
		start--
	}
	if start == depIdx {
		return depIdx
	}
	head := strings.TrimSpace(lines[start])
	if head == MarkerHeader {
		return start
	}
	for _, legacy := range legacyMarkerHeaders {
		if head == legacy {
			return start
		}
	}
	return depIdx
}

// HasForgeDep reports whether the kcl.mod at path declares a `forge`
// dependency in any spelling (false when the file does not exist).
func HasForgeDep(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	idx, _, table := forgeDep(strings.Split(string(data), "\n"))
	return idx != -1 || table, nil
}

// MigrateKclMod removes the legacy `forge = …` dependency from the kcl.mod at
// path, together with the marker-delimited comment block forge maintained
// around it, and deletes a sibling kcl.mod.lock that still records the forge
// package. Every shape older forges wrote (a vendored relative path, an
// absolute host path, the unpublished git tag) is removed the same way: the
// module now comes from the binary, so no spelling of the dependency is
// right any more.
//
// Idempotent: a kcl.mod with no forge dependency is untouched. A missing file
// is a silent no-op. A shape forge never wrote (a `[dependencies.forge]`
// table, several forge lines) is left alone with a Warning.
func MigrateKclMod(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, nil
		}
		return Result{}, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	idx, count, table := forgeDep(lines)
	switch {
	case table || count > 1:
		return Result{Warning: fmt.Sprintf(
			"%s declares the forge KCL module in a shape `forge generate` does not manage — leaving it untouched. "+
				"forge now supplies `import forge` from its own binary; delete the forge dependency from [dependencies] by hand, then remove kcl.mod.lock",
			path)}, nil
	case idx == -1:
		return Result{}, dropForgeLock(path)
	}

	start := ownedBlockStart(lines, idx)
	updated := make([]string, 0, len(lines))
	updated = append(updated, lines[:start]...)
	updated = append(updated, lines[idx+1:]...)
	out := strings.Join(updated, "\n")
	// Journaled, so a `forge generate` that fails later rolls the kcl.mod
	// edit back instead of reporting an unchanged tree (#271).
	checksums.RecordPreWriteAbs(path)
	if err := atomicfile.WriteFile(path, []byte(out), 0o644); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := dropForgeLock(path); err != nil {
		return Result{}, err
	}
	return Result{Changed: true}, nil
}

// dropForgeLock removes the forge package from the kcl.mod.lock beside
// kclModPath. kpm resolves a locked dependency before it consults the
// external packages it was handed, so a stale entry silently shadows the
// binary's module (observed: `attribute 'Bundle' not found in module
// 'forge'`).
//
// The lock is REWRITTEN, not deleted. kpm writes a kcl.mod.lock on every
// run — an empty one for a package with no dependencies — so deleting it
// would leave a tracked file that reappears (empty) at the next render: a
// diff produced by rendering, which `forge ci verify-generated` rightly
// fails. Stripping the forge entry leaves exactly the file kpm itself
// would write, so the state is stable. Entries for other packages are kept.
func dropForgeLock(kclModPath string) error {
	lockPath := filepath.Join(filepath.Dir(kclModPath), "kcl.mod.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil //nolint:nilerr // no lock: nothing can shadow the module
	}
	if !lockNamesForge(data) {
		return nil
	}
	checksums.RecordPreWriteAbs(lockPath)
	if err := atomicfile.WriteFile(lockPath, stripForgeFromLock(data), 0o644); err != nil {
		return fmt.Errorf("rewrite %s: %w", lockPath, err)
	}
	return nil
}

// lockForgeTableRE matches the header of the forge package's entry in a kpm
// lock file.
var lockForgeTableRE = regexp.MustCompile(`^\s*\[dependencies\.forge\]\s*$`)

// lockTableRE matches any TOML table header.
var lockTableRE = regexp.MustCompile(`^\s*\[[^\]]+\]\s*$`)

func lockNamesForge(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if lockForgeTableRE.MatchString(line) {
			return true
		}
	}
	return false
}

// stripForgeFromLock removes the `[dependencies.forge]` table (its header
// through the line before the next table header). A lock left with nothing
// but the bare `[dependencies]` header becomes empty — the exact bytes kpm
// writes for a package with no dependencies.
func stripForgeFromLock(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		switch {
		case lockForgeTableRE.MatchString(line):
			skipping = true
			continue
		case skipping && lockTableRE.MatchString(line):
			skipping = false
		}
		if !skipping {
			out = append(out, line)
		}
	}
	meaningful := 0
	for _, line := range out {
		if t := strings.TrimSpace(line); t != "" && t != "[dependencies]" {
			meaningful++
		}
	}
	if meaningful == 0 {
		return nil
	}
	return []byte(strings.Join(out, "\n"))
}

// RemoveLegacyVendorDir deletes <projectDir>/.forge-kcl, the project-local
// copy older forge versions maintained. Returns true when it removed one.
//
// Every file goes through the generate rollback journal (a no-op outside a
// generate run), so a generate that fails after this step hands the copy
// back exactly as it found it rather than leaving it half-deleted (#271).
func RemoveLegacyVendorDir(projectDir string) (bool, error) {
	dir := filepath.Join(projectDir, LegacyVendorDirName)
	if _, err := os.Stat(dir); err != nil {
		return false, nil //nolint:nilerr // absent is the goal state
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return checksums.RemoveJournaled(p)
	})
	if err == nil {
		err = os.RemoveAll(dir) // only empty directories remain
	}
	if err != nil {
		return false, fmt.Errorf("remove legacy %s/: %w", LegacyVendorDirName, err)
	}
	return true, nil
}

// UnmigratedError reports a managed kcl.mod that still declares the forge
// dependency (or a lock that still records it). Rendering it would resolve
// `import forge` from that declaration instead of from this binary — a stale
// project copy, or a git tag that does not exist — so render refuses and
// names the one command that fixes it.
type UnmigratedError struct {
	Paths []string // project-relative
}

func (e *UnmigratedError) Error() string {
	return fmt.Sprintf(
		"%s still declares the forge KCL module as a dependency.\n"+
			"    expected: no `forge = …` line — forge supplies `import forge` from the binary\n"+
			"              that is rendering, so the module always matches the forge you run\n"+
			"    found:    a declaration older forge versions wrote (a .forge-kcl/ path, or a git tag)\n"+
			"  Fix: run `forge generate` once. It removes the dependency and any kcl.mod.lock\n"+
			"  that records it, and deletes the old project-local .forge-kcl/. Commit the\n"+
			"  kcl.mod change; the result is identical under every forge build.",
		strings.Join(e.Paths, " and "))
}

// CheckKclMods returns an [UnmigratedError] when a managed kcl.mod under
// projectDir still declares the forge dependency, or its lock still records
// it. Render calls this before evaluating; it never edits anything.
func CheckKclMods(projectDir string) error {
	var bad []string
	for _, p := range ManagedKclMods(projectDir) {
		has, err := HasForgeDep(p)
		if err != nil {
			return err
		}
		lockData, lerr := os.ReadFile(filepath.Join(filepath.Dir(p), "kcl.mod.lock"))
		stale := lerr == nil && lockNamesForge(lockData)
		if has || stale {
			rel, rerr := filepath.Rel(projectDir, p)
			if rerr != nil {
				rel = p
			}
			bad = append(bad, filepath.ToSlash(rel))
		}
	}
	if len(bad) > 0 {
		return &UnmigratedError{Paths: bad}
	}
	return nil
}
