package cli

// generate_cache.go — the whole-run skip for `forge generate`.
//
// A no-op generate on control-plane measured 2.5 minutes uncontended and 14
// on a loaded box, and agents ran it hundreds of times a day: after every
// edit, "to be sure", and once more inside every verify-generated gate. Most
// of those runs regenerate a tree that is already exactly what generate
// produces. This file lets such a run prove that and stop.
//
// THE CLAIM A SKIP MAKES. "A full run, with this forge binary, these tools
// and this environment, started from this exact tree and changed nothing."
// That is a FIXED POINT: generate(T) == T. Recorded once, it answers every
// later run that starts from the same T — generate is a function of what it
// reads, so it would produce T again. Nothing weaker is recorded. In
// particular a run that WROTE files is not evidence, even though its output
// is "what generate produces": the next run from that output is the one
// that proves the tree has converged, and verify-generated exists precisely
// to ask that question. So the run after a change runs in full, and the
// one after that skips.
//
// WHAT "THE SAME T" COVERS — the fingerprint, every input class generate
// reads:
//
//   - the work tree as git sees it: every tracked and untracked-not-ignored
//     file, by content (the same listing the rollback verification uses), plus
//     forge's committed state under .forge/;
//   - gitignored inputs: go.work/go.work.sum (here or in any parent, as cmd/go
//     finds it), each package.json's installed node_modules (by npm's own
//     .package-lock.json), the dev-bridge root manifest, root dotenvs and
//     secrets/ stores, forge's top-level .forge/ state files (port ledgers,
//     the generating-build record) — and, as a catch-all for the next input
//     nobody named, every other gitignored FILE git lists outside a wholly
//     ignored directory;
//   - the outputs the recorded run wrote, by path — so a deleted or edited
//     generated file forces a run even when git ignores it — and every file
//     under the output directories external tools declare (gen/, a frontend's
//     src/gen);
//   - source outside the project the build compiles: local `replace` targets
//     and go.work `use` directories (their tracked files, and untracked Go
//     build sources — not a report a CI job tees into the checkout);
//   - the forge executable (content hash), each tool generate shells out to
//     (go, buf, the protoc plugins buf.gen.yaml names, goimports, node,
//     generate hooks' commands), the environment variables that steer
//     forge, cmd/go and the shadow database, and which postgres server the
//     shadow schema would be applied to. Values are stored hashed.
//
// Symlinks are followed to their targets, and a submodule's files are read
// through its own listing, so content outside the plain tree is covered too.
//
// Known gaps, each a false skip if it bites: a tool a generate hook runs
// INDIRECTLY (a Makefile target calling an unpinned binary on PATH — pin it
// by version in the repo, as `make sqlc` does); hand edits inside an
// installed node_modules that npm's lockfile does not record; a shadow
// postgres server upgraded in place at the same address; a buf `remote:`
// plugin with no pinned version, which the BSR can move; an UNTRACKED,
// non-Go file that a replace target's package embeds. Every one of them
// also makes generate's output depend on machine state, which
// verify-generated in a fresh CI clone (no record) would catch.
//
// Anything outside that list that changes generate's output is a false skip,
// the failure this design ranks above every other. So: the cache refuses
// outright during a merge/rebase, records only full-strength runs (no
// --skip-validate and friends), stores only fixed points, and --no-cache (or
// FORGE_GENERATE_NO_CACHE=1) always runs every step and refreshes the record.

import (
	"crypto/sha256"
	gobuildinfo "debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/hostslot"
)

const (
	// generateCacheRel is the manifest: per checkout (it lives in the
	// project's own .forge/, which is gitignored), rewritten whole by every
	// run that verifies a fixed point.
	generateCacheRel = ".forge/generate-cache.json"

	// generateCacheVersion is part of the key. Bump it whenever the
	// fingerprint's meaning changes, so a manifest written under the old
	// rules is never read under the new ones.
	generateCacheVersion = 1

	// generateNoCacheEnv is --no-cache for runs nobody types: a gate script,
	// or the `forge generate` verify-generated spawns.
	generateNoCacheEnv = "FORGE_GENERATE_NO_CACHE"

	// racyWindow guards the executable-identity cache against git's "racy
	// clean" problem: a file rewritten within one timestamp tick of being
	// hashed keeps its stat. A cached identity is trusted only for a file
	// whose timestamps were already this much older than the hash.
	racyWindow = 2 * time.Second
)

// generateCacheMode says what a run may do with the cache.
type generateCacheMode struct {
	consult bool // may skip when the tree is a recorded fixed point
	record  bool // may record the tree it ends on as one
}

// generateCacheModeFor derives the mode from the run's flags.
func generateCacheModeFor(flags pipelineFlags) generateCacheMode {
	if !flags.Cache {
		// Only `forge generate` (and --check) opt in. A scaffold command's
		// internal regenerate is never a skip candidate: it just wrote files.
		return generateCacheMode{}
	}
	// Flags that change WHAT a run writes: such a run is not the run the
	// manifest describes, and proves nothing about one.
	if flags.Steps != "" || flags.TemplatesOnly || flags.ExplainDrift || flags.Force || flags.Heal || flags.ForceCleanup {
		return generateCacheMode{}
	}
	m := generateCacheMode{
		consult: !flags.NoCache && !envTruthy(os.Getenv(generateNoCacheEnv)),
		record:  true,
	}
	// Flags that WEAKEN a run's checks. Such a run may consult a record a
	// full-strength run made — it asks less of the tree — but never makes
	// one: a tree that skipped its validate build has not been validated.
	if flags.SkipValidate || flags.SkipPreChecks || flags.SkipConfigCheck {
		m.record = false
	}
	return m
}

func envTruthy(v string) bool { return v != "" && v != "0" && !strings.EqualFold(v, "false") }

// generateCacheManifest is the record of one verified fixed point.
type generateCacheManifest struct {
	Version    int       `json:"version"`
	VerifiedAt time.Time `json:"verified_at"`
	// Forge is the generating build, for the skip line. The key uses the
	// executable's content hash (Env["forge"]), never this string: a dev
	// build carries the same version across every rebuild.
	Forge string `json:"forge"`
	// Strict records that the verifying run was --strict. A strict request
	// is satisfied only by a strict record.
	Strict bool `json:"strict,omitempty"`
	// Outputs and OutputDirs are what the verifying run wrote (forge's own
	// writes, and the directories external tools declare). They join every
	// later fingerprint by path, ignored by git or not.
	Outputs    []string `json:"outputs,omitempty"`
	OutputDirs []string `json:"output_dirs,omitempty"`
	// Env and Files are the fingerprint.
	Env   map[string]string `json:"env"`
	Files map[string]string `json:"files"`
	// Executables caches executable identities by stat, so a fingerprint
	// does not re-read a 170MB forge and a 125MB node on every run.
	Executables map[string]execStamp `json:"executables,omitempty"`
}

// execStamp is one cached executable identity.
type execStamp struct {
	Size       int64  `json:"size"`
	ModTime    int64  `json:"mtime_ns"`
	ChangeTime int64  `json:"ctime_ns,omitempty"`
	HashedAt   int64  `json:"hashed_at_ns"`
	ID         string `json:"id"`
}

func loadGenerateCacheManifest(root string) *generateCacheManifest {
	data, err := os.ReadFile(filepath.Join(root, generateCacheRel))
	if err != nil {
		return nil
	}
	var m generateCacheManifest
	if json.Unmarshal(data, &m) != nil || m.Version != generateCacheVersion || m.Files == nil || m.Env == nil {
		return nil
	}
	return &m
}

// saveGenerateCacheManifest writes the manifest atomically: a reader (the
// next run, under the same project lock) sees the old record or the new one,
// never half of either.
func saveGenerateCacheManifest(root string, m *generateCacheManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".forge-tmp-generate-cache-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	// CreateTemp makes the file 0600; the rest of .forge/ is 0644.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(root, generateCacheRel)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// generateCacheSys is the cache's view of the process environment, a seam so
// tests can vary the executable, PATH and env without touching the real ones.
type generateCacheSys struct {
	executable func() (string, error)
	lookPath   func(string) (string, error)
	environ    func() []string
	now        func() time.Time
	identity   func() string
	shadowHost func(root string) string
}

// generateCacheSystem is the environment the pipeline fingerprints. Tests
// that drive the real pipeline swap it, so they neither hash the test binary
// nor depend on what this machine has on PATH.
var generateCacheSystem = osGenerateCacheSys

func osGenerateCacheSys() generateCacheSys {
	return generateCacheSys{
		executable: os.Executable,
		lookPath:   exec.LookPath,
		environ:    os.Environ,
		now:        time.Now,
		identity:   buildinfo.Identity,
		shadowHost: shadowdb.Resolve,
	}
}

// generateFingerprint is everything one run could read, reduced to
// identities: files maps a key (a project-relative path, or an "ext:"/
// "gowork:"/"goenv:" key for a file outside the project) to its content
// identity; env maps an environment component to its identity.
type generateFingerprint struct {
	files map[string]string
	env   map[string]string
	execs map[string]execStamp
}

// fingerprintGenerate fingerprints the project at root. snap is the work-tree
// snapshot the pipeline takes anyway; outputs and outputDirs are the recorded
// run's outputs; execCache is the previous manifest's executable stat cache.
// An error means no trustworthy fingerprint exists — the run is then neither
// skipped nor recorded.
func fingerprintGenerate(root string, snap *workTreeSnapshot, outputs, outputDirs []string, execCache map[string]execStamp, sys generateCacheSys) (*generateFingerprint, error) {
	if snap == nil {
		return nil, errors.New("the work tree could not be listed")
	}
	if op := gitOperationInProgress(root); op != "" {
		return nil, fmt.Errorf("a git %s is in progress", op)
	}
	fp := &generateFingerprint{
		files: make(map[string]string, len(snap.files)+len(outputs)+64),
		env:   map[string]string{},
		execs: map[string]execStamp{},
	}
	var links []string
	for rel, st := range snap.files {
		id, err := fileIdentity(root, rel, st)
		if err != nil {
			return nil, err
		}
		fp.files[rel] = id
		if st.mode&fs.ModeSymlink != 0 {
			links = append(links, rel)
		}
	}
	// A link's identity is its target text; what it points AT is read
	// through it (buf follows a symlinked proto directory). Content outside
	// the project is fingerprinted where it lives.
	for _, rel := range links {
		if err := fp.addLinkTarget(root, rel); err != nil {
			return nil, err
		}
	}
	// A submodule is one gitlink entry to the listing, never its files —
	// and a shared-protos submodule is exactly where generate's inputs live.
	for _, rel := range gitSubmodules(root) {
		if err := fp.addExternalTree(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return nil, err
		}
	}
	for _, rel := range generateCacheExtraFiles(root, snap) {
		if err := fp.addFile(root, rel); err != nil {
			return nil, err
		}
	}
	for _, rel := range outputs {
		if err := fp.addFile(root, rel); err != nil {
			return nil, err
		}
	}
	for _, dir := range outputDirs {
		if err := fp.addDir(root, dir); err != nil {
			return nil, err
		}
	}
	environ := sys.environ()
	if work := effectiveGoWork(root, environ); work != "" {
		id, err := pathIdentity(work)
		if err != nil {
			return nil, err
		}
		// A go.work inside the project is keyed by its project-relative
		// path, like every other project file, so a copy of the project
		// elsewhere (verify-generated's scratch) fingerprints the same.
		key := work
		if rel, rerr := filepath.Rel(root, work); rerr == nil && within(root, work) {
			key = filepath.ToSlash(rel)
		}
		fp.files["gowork:"+key] = id
		if id, err = pathIdentity(work + ".sum"); err != nil {
			return nil, err
		}
		fp.files["gowork:"+key+".sum"] = id
	}
	extern, err := generateCacheExternalModules(root, snap, environ)
	if err != nil {
		return nil, err
	}
	for _, dir := range extern {
		if err := fp.addExternalModule(dir); err != nil {
			return nil, err
		}
	}
	if goenv := goEnvFile(environ); goenv != "" {
		id, err := pathIdentity(goenv)
		if err != nil {
			return nil, err
		}
		fp.files["goenv:"+goenv] = id
	}

	execs := &execIdentities{prev: execCache, next: fp.execs, now: sys.now()}
	exe, err := sys.executable()
	if err != nil {
		return nil, fmt.Errorf("locate the forge executable: %w", err)
	}
	// The forge binary is identified by CONTENT, never by its build info:
	// templates are embedded, and a dev build reports one version across
	// every rebuild (or any version an -ldflags stamp claims).
	if fp.env["forge"], err = execs.identify(exe, false); err != nil {
		return nil, fmt.Errorf("fingerprint the forge executable: %w", err)
	}
	for _, tool := range generateCacheTools(root, snap) {
		p, lookErr := sys.lookPath(tool)
		if lookErr != nil {
			fp.env["tool:"+tool] = "absent"
			continue
		}
		if fp.env["tool:"+tool], err = execs.identify(p, true); err != nil {
			return nil, fmt.Errorf("fingerprint %s: %w", tool, err)
		}
	}
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if ok && generateCacheEnvRelevant(name) {
			fp.env["env:"+name] = shortSum([]byte(val))
		}
	}
	// Which postgres hosts the shadow schema the ORM and fixtures are
	// projected from: the dev stack's server when it is up, embedded
	// postgres when nothing answers. Different servers can render the same
	// catalog differently, so the run that sees a different one runs.
	fp.env["shadow"] = shortSum([]byte(sys.shadowHost(root)))
	fp.env["cache"] = strconv.Itoa(generateCacheVersion)
	return fp, nil
}

// addFile records one project-relative path, present or not. An absent path
// is recorded as such: its appearing later is a change too.
func (fp *generateFingerprint) addFile(root, rel string) error {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if _, done := fp.files[rel]; done {
		return nil
	}
	id, err := pathIdentity(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	fp.files[rel] = id
	return nil
}

// addDir records every file under a project-relative directory, so a file
// that appears in an output directory is a change even when git ignores it.
func (fp *generateFingerprint) addDir(root, dir string) error {
	dir = filepath.ToSlash(filepath.Clean(dir))
	abs := filepath.Join(root, filepath.FromSlash(dir))
	if _, err := os.Lstat(abs); err != nil {
		fp.files[dir+"/"] = "absent"
		return nil
	}
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return fp.addFile(root, rel)
	})
}

// addLinkTarget records what a symlink in the tree resolves to when that is
// outside the project: a file by content, a directory as an external tree.
// A dangling link is recorded as such (its target appearing is a change).
func (fp *generateFingerprint) addLinkTarget(root, rel string) error {
	target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		fp.files["link:"+rel] = "dangling"
		return nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	if within(realRoot, target) {
		return nil // inside the project: fingerprinted where it lives
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fp.addExternalTree(target)
	}
	id, err := pathIdentity(target)
	if err != nil {
		return err
	}
	fp.files["ext:"+filepath.ToSlash(target)] = id
	return nil
}

// addExternalTree records a source tree outside the project: the files git
// lists there (tracked plus untracked-not-ignored), or every file when it is
// not a git work tree.
func (fp *generateFingerprint) addExternalTree(dir string) error {
	return fp.addExternalFiles(dir, false)
}

// addExternalModule records a Go module outside the project that its build
// compiles (a local replace target, a go.work `use`): every tracked file,
// and the untracked files a Go build of it can read (goBuildSource). An
// untracked file of any other kind in a module checkout — a test report a CI
// job tees into the root, a coverage profile — is neither compiled nor
// imported, and keying on it would turn every such write into a miss.
func (fp *generateFingerprint) addExternalModule(dir string) error {
	return fp.addExternalFiles(dir, true)
}

// goBuildSource reports whether an untracked file in a module checkout is one
// the Go toolchain reads when building it.
func goBuildSource(rel string) bool {
	switch path.Base(rel) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	switch path.Ext(rel) {
	case ".go", ".s", ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".m", ".f", ".syso":
		return true
	}
	return false
}

func (fp *generateFingerprint) addExternalFiles(dir string, goModule bool) error {
	files, ok := gitListWorkTree(dir)
	if ok && goModule {
		tracked, tok := gitListTracked(dir)
		if !tok {
			return fmt.Errorf("could not list the tracked files of %s", dir)
		}
		kept := files[:0]
		for _, rel := range files {
			if tracked[rel] || goBuildSource(rel) {
				kept = append(kept, rel)
			}
		}
		files = kept
	}
	if !ok {
		files = nil
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != dir && (d.Name() == ".git" || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			rel, relErr := filepath.Rel(dir, p)
			if relErr != nil {
				return relErr
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			fp.files["ext:"+dir+"/"] = "absent"
			return nil
		}
	}
	for _, rel := range files {
		id, err := pathIdentity(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if id != "absent" { // listed by git but deleted on disk
			fp.files["ext:"+dir+"/"+rel] = id
		}
	}
	return nil
}

// fileIdentity is a snapshot entry's identity: mode and content for a regular
// file, the target for a symlink.
func fileIdentity(root, rel string, st fileStamp) (string, error) {
	switch {
	case st.mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		return "link:" + target, nil
	case st.mode.IsRegular():
		sum := st.sum
		if sum == ([sha256.Size]byte{}) {
			var ok bool
			if sum, ok = fileSum(root, rel); !ok {
				return "", fmt.Errorf("could not read %s", rel)
			}
		}
		return fmt.Sprintf("%o:%x", st.mode.Perm(), sum), nil
	default:
		return "mode:" + st.mode.String(), nil
	}
}

// pathIdentity is fileIdentity for an arbitrary path, "absent" when nothing
// is there and "dir" for a directory.
func pathIdentity(abs string) (string, error) {
	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "dir", nil
	}
	st := fileStamp{size: info.Size(), modTime: info.ModTime(), mode: info.Mode()}
	return fileIdentity(filepath.Dir(abs), filepath.Base(abs), st)
}

// generateCacheIgnoredState are .forge/ files that are not inputs: the lock
// (it holds a pid), this cache's own manifest, and the friction log, which
// nothing in generate reads.
var generateCacheIgnoredState = map[string]bool{
	"forge.lock":          true,
	"generate-cache.json": true,
	"friction.jsonl":      true,
}

// generateCacheExtraFiles lists the gitignored files generate reads, as
// project-relative paths. Each is recorded present or absent.
func generateCacheExtraFiles(root string, snap *workTreeSnapshot) []string {
	set := map[string]bool{
		// The dev web-runtime bridge: a gitignored workspace root manifest,
		// its lockfile and npmrc.
		"package.json":                    true,
		"package-lock.json":               true,
		".npmrc":                          true,
		"node_modules/.package-lock.json": true,
	}
	// Every package.json's installed tree, by npm's own record of it: the
	// TypeScript stubs come from the protoc-gen-es installed there, and a
	// missing node_modules makes generate install one.
	for rel := range snap.files {
		if path.Base(rel) == "package.json" && !strings.Contains(rel, "node_modules/") {
			set[path.Join(path.Dir(rel), "node_modules", ".package-lock.json")] = true
		}
	}
	// forge's own top-level state: port ledgers a KCL render reads, the
	// generating-build record the compatibility check reads.
	if entries, err := os.ReadDir(filepath.Join(root, ".forge")); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.Type().IsRegular() && !generateCacheIgnoredState[name] && !strings.HasPrefix(name, ".forge-tmp-") {
				set[".forge/"+name] = true
			}
		}
	}
	// Root dotenvs and the per-env secret stores: the shadow-database
	// resolution reads DATABASE_URL from them. Named here because a secrets/
	// directory holding only ignored files is collapsed out of the listing
	// below.
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".env") && !e.IsDir() {
				set[e.Name()] = true
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, "secrets")); err == nil {
		for _, e := range entries {
			if e.Type().IsRegular() {
				set["secrets/"+e.Name()] = true
			}
		}
	}
	// Every other gitignored FILE, whatever reads it: the list above names
	// the inputs known today, this catches the next one. Ignored directories
	// arrive collapsed (node_modules/, build output, logs) and stay out — a
	// tree under one is covered only where named above.
	for _, rel := range gitIgnoredFiles(root) {
		name := path.Base(rel)
		if strings.HasPrefix(name, ".forge-tmp-") || (path.Dir(rel) == ".forge" && generateCacheIgnoredState[name]) {
			continue
		}
		// An installed tree is covered by npm's record of it, above. A
		// node_modules that is a symlink (verify-generated's scratch copy
		// links the checkout's) arrives here as a file; its directory twin
		// is collapsed out, and both must fingerprint alike.
		if name == "node_modules" {
			continue
		}
		set[rel] = true
	}
	out := make([]string, 0, len(set))
	for rel := range set {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// gitIgnoredFiles lists the gitignored files under root, with every wholly
// ignored directory collapsed to one "dir/" entry and dropped. nil outside a
// git work tree, where snapshotWorkTree already walked every file.
func gitIgnoredFiles(root string) []string {
	cmd := exec.Command("git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue
		}
		files = append(files, rel)
	}
	return files
}

// gitListTracked returns the set of files git tracks under root.
func gitListTracked(root string) (map[string]bool, bool) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	set := map[string]bool{}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			set[rel] = true
		}
	}
	return set, true
}

// gitSubmodules lists the project-relative paths of root's gitlinks
// (submodules, and nested repositories added as one).
func gitSubmodules(root string) []string {
	cmd := exec.Command("git", "ls-files", "-z", "--stage")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var subs []string
	for _, entry := range strings.Split(string(out), "\x00") {
		meta, rel, ok := strings.Cut(entry, "\t")
		if ok && strings.HasPrefix(meta, "160000 ") {
			subs = append(subs, rel)
		}
	}
	return subs
}

// effectiveGoWork returns the go.work cmd/go would use for root — $GOWORK
// when set, else the nearest one in root or above — or "".
func effectiveGoWork(root string, environ []string) string {
	if v, ok := environLookup(environ, "GOWORK"); ok && v != "" {
		if v == "off" {
			return ""
		}
		return v
	}
	return findGoWork(root)
}

// goEnvFile returns the file `go env -w` writes (GOENV, or the user config
// default), or "" when it is switched off or cannot be located.
func goEnvFile(environ []string) string {
	if v, ok := environLookup(environ, "GOENV"); ok {
		if v == "off" || v == "" {
			return ""
		}
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "go", "env")
}

func environLookup(environ []string, name string) (string, bool) {
	val, found := "", false
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			val, found = v, true // last one wins, as in the process environment
		}
	}
	return val, found
}

// generateCacheExternalModules lists the source directories OUTSIDE the
// project that a build of it compiles: local-path replace targets of every
// go.mod in the tree, and the go.work's `use` and replace directories (plus
// the replaces of the modules it uses — those are main modules too).
//
// A go.mod or go.work that does not parse is an error, not an omission: the
// replaces it may declare are exactly what the fingerprint would then miss.
// testdata/ trees are skipped, as cmd/go skips them.
func generateCacheExternalModules(root string, snap *workTreeSnapshot, environ []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		abs, err := filepath.Abs(dir)
		if err != nil || within(root, abs) || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	modReplaces := func(gomod string) error {
		data, err := os.ReadFile(gomod)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		// Parse, not ParseLax: ParseLax drops replace directives on
		// purpose (they bind only the main module), and they are the
		// whole point here.
		f, err := modfile.Parse(gomod, data, nil)
		if err != nil {
			return err
		}
		for _, r := range f.Replace {
			if modfile.IsDirectoryPath(r.New.Path) {
				add(resolveFrom(filepath.Dir(gomod), r.New.Path))
			}
		}
		return nil
	}
	for rel := range snap.files {
		if path.Base(rel) != "go.mod" || strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/") {
			continue
		}
		if err := modReplaces(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return nil, err
		}
	}
	if work := effectiveGoWork(root, environ); work != "" {
		if err := goWorkModules(work, add, modReplaces); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// goWorkModules feeds add every directory a go.work names — its `use`
// modules and local replace targets — and modReplaces each used module's
// go.mod. A missing go.work names nothing.
func goWorkModules(work string, add func(string), modReplaces func(string) error) error {
	data, err := os.ReadFile(work)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	f, err := modfile.ParseWork(work, data, nil)
	if err != nil {
		return err
	}
	for _, u := range f.Use {
		dir := resolveFrom(filepath.Dir(work), u.Path)
		add(dir)
		if err := modReplaces(filepath.Join(dir, "go.mod")); err != nil {
			return err
		}
	}
	for _, r := range f.Replace {
		if modfile.IsDirectoryPath(r.New.Path) {
			add(resolveFrom(filepath.Dir(work), r.New.Path))
		}
	}
	return nil
}

func resolveFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, filepath.FromSlash(p))
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// generateCacheBaseTools are the executables generate runs on every project
// that has them: go (tidy, build, packages loads), buf and its Go plugins,
// goimports, and node for protoc-gen-es. forge itself is fingerprinted
// separately (it is also buf's protoc-gen-forge plugin).
var generateCacheBaseTools = []string{
	"go", "buf", "protoc-gen-go", "protoc-gen-connect-go", "protoc-gen-connect-openapi", "goimports", "node",
}

// generateCacheTools adds to the base set every plugin a buf template in the
// tree resolves from PATH, and the command of every generate hook.
func generateCacheTools(root string, snap *workTreeSnapshot) []string {
	set := map[string]bool{}
	for _, t := range generateCacheBaseTools {
		set[t] = true
	}
	for rel := range snap.files {
		base := path.Base(rel)
		if strings.HasPrefix(base, "buf.gen") && (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) {
			for _, name := range bufLocalPluginNames(filepath.Join(root, filepath.FromSlash(rel))) {
				set[name] = true
			}
		}
	}
	for _, name := range generateHookCommands(filepath.Join(root, "forge.yaml")) {
		set[name] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		// A path (./node_modules/.bin/protoc-gen-es, ./scripts/gen.sh) is
		// inside the tree or an installed node_modules, both fingerprinted.
		if t != "" && !strings.ContainsAny(t, `/\`) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// bufLocalPluginNames returns the `local:` plugin commands of a buf.gen.yaml:
// the string itself, or the first element of an argv list.
func bufLocalPluginNames(file string) []string {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var tmpl struct {
		Plugins []struct {
			Local any `yaml:"local"`
		} `yaml:"plugins"`
	}
	if yaml.Unmarshal(data, &tmpl) != nil {
		return nil
	}
	var out []string
	for _, p := range tmpl.Plugins {
		switch v := p.Local.(type) {
		case string:
			out = append(out, v)
		case []any:
			if len(v) > 0 {
				if s, ok := v[0].(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// generateHookCommands returns argv[0] of every forge.yaml generate hook.
func generateHookCommands(forgeYAML string) []string {
	data, err := os.ReadFile(forgeYAML)
	if err != nil {
		return nil
	}
	var cfg struct {
		Generate struct {
			Hooks []struct {
				Command []string `yaml:"command"`
			} `yaml:"hooks"`
		} `yaml:"generate"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return nil
	}
	var out []string
	for _, h := range cfg.Generate.Hooks {
		if len(h.Command) > 0 {
			out = append(out, h.Command[0])
		}
	}
	return out
}

// generateCacheEnvIgnored are variables that cannot change what generate
// writes: where caches live, how modules are fetched (go.sum pins what they
// contain), runtime tuning, and this cache's own switches. Every other FORGE_,
// GO, CGO_ and KCL_ variable is part of the key, so an unknown one costs a
// run rather than risking a skip.
var generateCacheEnvIgnored = map[string]bool{
	generateTimingsEnv:           true,
	generateNoCacheEnv:           true,
	"FORGE_SILENCE_EXPERIMENTAL": true,
	"FORGE_CREDENTIAL_HELPER":    true,
	"GOPROXY":                    true,
	"GOPRIVATE":                  true,
	"GONOPROXY":                  true,
	"GONOSUMDB":                  true,
	"GONOSUMCHECK":               true,
	"GOSUMDB":                    true,
	"GOINSECURE":                 true,
	"GOAUTH":                     true,
	"GOVCS":                      true,
	"GOPATH":                     true,
	"GOMODCACHE":                 true,
	"GOCACHE":                    true,
	"GOCACHEPROG":                true,
	"GOTMPDIR":                   true,
	"GOLANGCI_LINT_CACHE":        true,
	"GOMAXPROCS":                 true,
	"GOGC":                       true,
	"GOMEMLIMIT":                 true,
	"GOTRACEBACK":                true,
	"GOCOVERDIR":                 true,

	// Host-slot bookkeeping: which slot a run holds, and where slots
	// live. FORGE_SLOT_HELD is a fresh token per holder, so keying on it
	// made every run under a gate (or `forge slot run`) a miss, and every
	// record made there one no later run could match.
	hostslot.EnvHeld: true,
	hostslot.EnvDir:  true,
}

// generateCacheEnvRelevant reports whether an environment variable is part of
// the key. PATH is not: what it resolves is, tool by tool.
func generateCacheEnvRelevant(name string) bool {
	if generateCacheEnvIgnored[name] {
		return false
	}
	// FORGE_<CLASS>_SLOTS sizes a host-slot class: how many runs share the
	// machine, never what one of them writes.
	if strings.HasPrefix(name, "FORGE_") && strings.HasSuffix(name, "_SLOTS") {
		return false
	}
	switch name {
	case "CI", "GITHUB_ACTIONS", // buildinfo.IsCI changes what a dev build writes
		"HOME", "XDG_CONFIG_HOME", "RELIANT_USER_CONFIG_DIR", // where user config is read from
		"DATABASE_URL",              // which postgres hosts the shadow schema
		"NODE_OPTIONS", "NODE_PATH": // how node runs protoc-gen-es
		return true
	}
	for _, prefix := range []string{"FORGE_", "GO", "CGO_", "KCL_", "POSTGRES_", "BUF_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// execIdentities identifies executables, reusing a cached identity when the
// file's stat is unchanged and was already settled when it was hashed.
type execIdentities struct {
	prev map[string]execStamp
	next map[string]execStamp
	now  time.Time
}

// identify returns an executable's identity. byBuildInfo lets a Go-built
// tool be identified by its module version (two builds of buf v1.50.0 write
// the same stubs), falling back to content for a devel or dirty build. forge
// itself is always identified by content.
func (e *execIdentities) identify(p string, byBuildInfo bool) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	stamp := execStamp{Size: info.Size(), ModTime: info.ModTime().UnixNano(), ChangeTime: statChangeTime(info)}
	key := p
	if byBuildInfo {
		key = "tool:" + p
	}
	if old, ok := e.prev[key]; ok && old.Size == stamp.Size && old.ModTime == stamp.ModTime && old.ChangeTime == stamp.ChangeTime {
		settled := old.HashedAt - racyWindow.Nanoseconds()
		if old.ModTime < settled && old.ChangeTime < settled {
			e.next[key] = old
			return old.ID, nil
		}
	}
	id, err := executableID(p, byBuildInfo)
	if err != nil {
		return "", err
	}
	stamp.HashedAt = e.now.UnixNano()
	stamp.ID = id
	e.next[key] = stamp
	return id, nil
}

func executableID(p string, byBuildInfo bool) (string, error) {
	if byBuildInfo {
		if bi, err := gobuildinfo.ReadFile(p); err == nil && bi.Main.Path != "" && releasedModuleVersion(bi.Main.Version) {
			return "go:" + bi.Main.Path + "@" + bi.Main.Version, nil
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// releasedModuleVersion reports whether a binary's main-module version names
// immutable source: a valid semver that is not a dirty VCS build.
func releasedModuleVersion(v string) bool {
	return semver.IsValid(v) && !strings.Contains(v, "dirty")
}

func shortSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// gitOperationInProgress names a merge, rebase, cherry-pick or revert in
// progress in the repository holding root ("" when none). The drift guard
// behaves differently mid-merge, so no skip is offered and nothing recorded.
func gitOperationInProgress(root string) string {
	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if op := detectGitMergeState(dir); op != "" {
				return op
			}
			if gitDir := resolveGitDir(dir); gitDir != "" {
				if _, err := os.Stat(filepath.Join(gitDir, "REVERT_HEAD")); err == nil {
					return "revert"
				}
			}
			return ""
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// diffGenerateFingerprint explains how fp differs from the manifest, or
// returns "" when it does not.
func diffGenerateFingerprint(m *generateCacheManifest, fp *generateFingerprint) string {
	var envChanged []string
	for k, v := range fp.env {
		if m.Env[k] != v {
			envChanged = append(envChanged, k)
		}
	}
	for k := range m.Env {
		if _, ok := fp.env[k]; !ok {
			envChanged = append(envChanged, k)
		}
	}
	var filesChanged []string
	for k, v := range fp.files {
		if m.Files[k] != v {
			filesChanged = append(filesChanged, k)
		}
	}
	for k := range m.Files {
		if _, ok := fp.files[k]; !ok {
			filesChanged = append(filesChanged, k)
		}
	}
	sort.Strings(envChanged)
	sort.Strings(filesChanged)
	var parts []string
	if len(envChanged) > 0 {
		parts = append(parts, describeEnvChange(envChanged))
	}
	if len(filesChanged) > 0 {
		parts = append(parts, describeFileChange(filesChanged))
	}
	return strings.Join(parts, "; ")
}

func describeEnvChange(keys []string) string {
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		switch {
		case k == "forge":
			names = append(names, "the forge binary")
		case k == "cache":
			names = append(names, "the cache format")
		case strings.HasPrefix(k, "tool:"):
			names = append(names, strings.TrimPrefix(k, "tool:"))
		case strings.HasPrefix(k, "env:"):
			names = append(names, "$"+strings.TrimPrefix(k, "env:"))
		default:
			names = append(names, k)
		}
	}
	return listSome(names) + " changed"
}

func describeFileChange(keys []string) string {
	// Name what the user edited. forge's own .forge/ state moves with every
	// run that wrote, so it is one entry at the end, not three names that
	// push the proto that changed off the line.
	var names []string
	state := 0
	for _, k := range keys {
		if strings.HasPrefix(k, ".forge/") {
			state++
			continue
		}
		for _, prefix := range []string{"ext:", "gowork:", "goenv:"} {
			k = strings.TrimPrefix(k, prefix)
		}
		names = append(names, strings.TrimSuffix(k, "/"))
	}
	if state > 0 {
		names = append(names, fmt.Sprintf("%d .forge/ state file(s)", state))
	}
	return fmt.Sprintf("%d file(s) changed (%s)", len(keys), listSome(names))
}

func listSome(names []string) string {
	const shown = 3
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, … +%d more", strings.Join(names[:shown], ", "), len(names)-shown)
}

// generateCacheRun is one pipeline run's use of the cache: the pre-run
// fingerprint, taken once, serves both the skip decision and the fixed-point
// check at the end.
type generateCacheRun struct {
	root   string
	mode   generateCacheMode
	strict bool
	sys    generateCacheSys
	prev   *generateCacheManifest
	pre    *generateFingerprint
	preErr error
}

// openGenerateCache fingerprints the tree when the run may use the cache.
func openGenerateCache(root string, flags pipelineFlags, snap *workTreeSnapshot, sys generateCacheSys) *generateCacheRun {
	c := &generateCacheRun{root: root, mode: generateCacheModeFor(flags), strict: flags.Strict, sys: sys}
	if !c.mode.consult && !c.mode.record {
		return c
	}
	// The environment the run STARTED with is its input. forge sets
	// variables in its own process as it goes (the KCL runtime's
	// KCL_FILE_SCOPE, on the first render), and nothing else can change a
	// running process's environment, so the post-run check and the record
	// both use this copy.
	environ := sys.environ()
	c.sys.environ = func() []string { return environ }
	c.prev = loadGenerateCacheManifest(root)
	var outputs, dirs []string
	var execCache map[string]execStamp
	if c.prev != nil {
		outputs, dirs, execCache = c.prev.Outputs, c.prev.OutputDirs, c.prev.Executables
	}
	c.pre, c.preErr = fingerprintGenerate(root, snap, outputs, dirs, execCache, c.sys)
	return c
}

// verdict reports whether the run can stop here, and otherwise why not ("" for
// a first run, which has no record to compare against).
func (c *generateCacheRun) verdict() (skip bool, reason string) {
	if !c.mode.consult || c.prev == nil {
		return false, ""
	}
	switch {
	case c.preErr != nil:
		return false, c.preErr.Error()
	case c.strict && !c.prev.Strict:
		return false, "this run is --strict and the verified one was not"
	}
	if d := diffGenerateFingerprint(c.prev, c.pre); d != "" {
		return false, d + " since the last verified run"
	}
	return true, ""
}

// skip reports whether the run can stop here, printing one line saying why
// either way when a verified run is on record.
func (c *generateCacheRun) skip(w io.Writer) bool {
	ok, reason := c.verdict()
	if ok {
		c.printSkip(w)
		return true
	}
	if reason != "" {
		fmt.Fprintf(w, "ℹ️  generate: running every step — %s.\n", reason)
	}
	return false
}

func (c *generateCacheRun) printSkip(w io.Writer) {
	fmt.Fprintf(w, "⏭️  forge generate: nothing to do — the tree is byte-identical to one a full run verified as its own output %s (%d files; forge binary, tools and environment unchanged). --no-cache runs every step.\n",
		agoString(c.sys.now().Sub(c.prev.VerifiedAt)), len(c.pre.files))
}

// generateCachedNoop is the cache check `forge generate` makes BEFORE it
// queues for a host heavy slot: a run with nothing to do should not wait
// behind other agents' builds to find that out. It holds the project lock,
// so it never reads a tree another generate is half-way through writing —
// and a run that waited for that lock may find the other run has just
// recorded the tree it now skips. On a skip it prints the skip line and
// reports true. On a miss it prints nothing: the pipeline checks again
// inside its slot (the tree may have converged meanwhile) and says why.
func generateCachedNoop(projectDir string, flags pipelineFlags) (bool, error) {
	if !generateCacheModeFor(flags).consult {
		return false, nil
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return false, err
	}
	if loadGenerateCacheManifest(abs) == nil {
		return false, nil // nothing on record: no fingerprint worth taking
	}
	skipped := false
	err = withGenerateLock(projectDir, func() error {
		c := openGenerateCache(abs, flags, snapshotWorkTree(abs), generateCacheSystem())
		if ok, _ := c.verdict(); ok {
			c.printSkip(os.Stdout)
			skipped = true
		}
		return nil
	})
	return skipped, err
}

func agoString(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// record stores the tree the run ended on as a verified fixed point — only
// when the run changed nothing, by forge's writers, by external tools, or by
// anything else the fingerprint sees. ran is the steps that ran: their
// declared external outputs join the record. Returns whether it recorded.
func (c *generateCacheRun) record(w io.Writer, pctx *pipelineContext, ran []GenStep, sum checksums.WriteSummary) bool {
	if !c.mode.record || c.pre == nil {
		return false
	}
	if sum.Changed() > 0 {
		return false // not a fixed point: the next run is the one that proves convergence
	}
	if len(pctx.SoftFailures) > 0 {
		// A step that failed as a warning wrote nothing, so "0 changed"
		// says nothing about its outputs: they may be stale.
		fmt.Fprintf(w, "ℹ️  not recorded as a verified fixed point: %s failed; the next run runs every step.\n", strings.Join(pctx.SoftFailures, ", "))
		return false
	}
	outputs, dirs := generateRunOutputs(pctx, ran)
	post, err := fingerprintGenerate(c.root, snapshotWorkTree(c.root), outputs, dirs, c.pre.execs, c.sys)
	if err != nil {
		fmt.Fprintf(w, "ℹ️  not recorded as a verified fixed point (%v); the next run runs every step.\n", err)
		return false
	}
	if moved := movedDuringRun(c.root, c.pre, post); moved != "" {
		// Something the journal does not count moved while the run ran —
		// an install, another process editing the tree, or a write forge
		// makes outside its writers. Not a fixed point.
		fmt.Fprintf(w, "ℹ️  not recorded as a verified fixed point: %s while generate ran; the next run runs every step.\n", moved)
		return false
	}
	m := newGenerateCacheManifest(post, outputs, dirs, c.strict, c.sys)
	if err := saveGenerateCacheManifest(c.root, m); err != nil {
		fmt.Fprintf(w, "⚠️  could not record the generate cache (%v); the next run will run every step\n", err)
		return false
	}
	fmt.Fprintln(w, "⚡ Recorded as a verified fixed point: re-running with nothing changed skips straight to done (--no-cache forces a full run).")
	return true
}

// fingerprintKeyPath maps a fingerprint file key back to the path it
// identifies. isFile is false for a directory marker ("dir/").
func fingerprintKeyPath(root, key string) (p string, isFile bool) {
	if strings.HasSuffix(key, "/") {
		return "", false
	}
	for _, prefix := range []string{"ext:", "gowork:", "goenv:"} {
		if rest, ok := strings.CutPrefix(key, prefix); ok {
			if p := filepath.FromSlash(rest); !filepath.IsAbs(p) {
				return filepath.Join(root, p), true // a go.work inside the project
			}
			return filepath.FromSlash(rest), true
		}
	}
	return filepath.Join(root, filepath.FromSlash(key)), true
}

// newGenerateCacheManifest is the record of fp as a verified fixed point.
func newGenerateCacheManifest(fp *generateFingerprint, outputs, dirs []string, strict bool, sys generateCacheSys) *generateCacheManifest {
	return &generateCacheManifest{
		Version:     generateCacheVersion,
		VerifiedAt:  sys.now().UTC(),
		Forge:       sys.identity(),
		Strict:      strict,
		Outputs:     outputs,
		OutputDirs:  dirs,
		Env:         fp.env,
		Files:       fp.files,
		Executables: fp.execs,
	}
}

// movedDuringRun describes every input the pre-run fingerprint saw whose
// identity differs afterwards ("" when none): the run must have changed
// nothing it read. Files only the post-run fingerprint has are this run's
// outputs, whose content the write journal has already vouched for. A file
// the pre-run fingerprint saw that the post-run one does not list (a previous
// run's output this run no longer names) is re-read from disk, so a run that
// deleted it is caught.
func movedDuringRun(root string, pre, post *generateFingerprint) string {
	var envChanged, filesChanged []string
	for k, v := range pre.env {
		if post.env[k] != v {
			envChanged = append(envChanged, k)
		}
	}
	for k := range post.env {
		if _, ok := pre.env[k]; !ok {
			envChanged = append(envChanged, k)
		}
	}
	for k, v := range pre.files {
		pv, ok := post.files[k]
		if !ok {
			p, isFile := fingerprintKeyPath(root, k)
			if !isFile {
				continue
			}
			var err error
			if pv, err = pathIdentity(p); err != nil {
				pv = "unreadable"
			}
		}
		if pv != v {
			filesChanged = append(filesChanged, k)
		}
	}
	sort.Strings(envChanged)
	sort.Strings(filesChanged)
	var parts []string
	if len(envChanged) > 0 {
		parts = append(parts, describeEnvChange(envChanged))
	}
	if len(filesChanged) > 0 {
		parts = append(parts, describeFileChange(filesChanged))
	}
	return strings.Join(parts, "; ")
}

// generateRunOutputs lists what the run wrote: forge's own writes, and what
// each external-tool step that ran declares (directories walked whole).
func generateRunOutputs(pctx *pipelineContext, ran []GenStep) (outputs, dirs []string) {
	files := map[string]bool{}
	for rel := range checksums.WrittenPaths() {
		if filepath.IsAbs(rel) {
			r, err := filepath.Rel(pctx.AbsPath, rel)
			if err != nil || !within(pctx.AbsPath, rel) {
				continue
			}
			rel = r
		}
		files[filepath.ToSlash(filepath.Clean(rel))] = true
	}
	dirSet := map[string]bool{}
	for _, step := range ran {
		if step.Writes == nil {
			continue
		}
		for _, p := range step.Writes(pctx) {
			p = filepath.ToSlash(filepath.Clean(p))
			if info, err := os.Stat(filepath.Join(pctx.AbsPath, filepath.FromSlash(p))); err == nil && info.IsDir() {
				dirSet[p] = true
			} else {
				files[p] = true
			}
		}
	}
	for p := range files {
		outputs = append(outputs, p)
	}
	for p := range dirSet {
		dirs = append(dirs, p)
	}
	sort.Strings(outputs)
	sort.Strings(dirs)
	return outputs, dirs
}
