package cli

// generate_go_memo.go — the run that has work to do skips the go tools whose
// inputs it did not change.
//
// The whole-run skip (generate_cache.go) answers one question: is this tree a
// fixed point a full run already verified? The runs agents actually wait on
// are the other kind: the run after an edit, and the run after THAT, which
// converges and records. On growth (22 entities, 6 services, a loaded
// machine) such a run was 8m40s, and about a third of it was two go tools
// answering questions whose inputs had not changed since they last answered:
//
//   - `go mod tidy`, six times a run (gen/ and the root, before composition,
//     before wiring, and once more at the end): 65s. Its result depends on
//     go.mod, go.sum and the IMPORTS of every package, and an edit to a proto
//     or a handler almost never changes an import.
//   - the validate build (`go list -export -deps -test ./...`): 90s, though
//     every package it compiles comes from the build cache — what it pays for
//     is cmd/go proving that, package by package.
//
// THE CLAIM A RECORD MAKES. "This tool ran, succeeded, and left exactly these
// inputs behind." It is taken AFTER the tool ran, from the state the tool
// itself produced, and a later call whose inputs are identical skips:
//
//   - tidy is idempotent. Its output is a function of its inputs (go.mod,
//     go.sum, the build constraints, package name and import paths of every
//     .go file in the module and in each local replace target, go.work, the
//     go toolchain and the go environment)
//     and tidy(tidy(x)) == tidy(x). Inputs equal to what a tidy left behind
//     are a tidy module, and running it again writes nothing.
//   - the validate build writes nothing in the tree. Its verdict is a function
//     of every file a Go build of the module can read, the toolchain and the
//     environment; the same inputs compile the same way.
//
// WHAT "THE SAME INPUTS" COVERS for the validate build: every file under a
// directory that holds a .go file, or under one of its descendants (a Go
// package can embed anything in its own subtree, and db/embed.go embeds
// db/migrations), every go.mod, go.sum, go.work and vendor/modules.txt in the
// tree, the same for each local replace target and go.work `use` outside it,
// gitignored files included — the walk reads the disk, not git's listing. A
// tree with cgo (`import "C"` can #include from anywhere) or a symlinked
// directory is never memoized: what it reads cannot be enumerated here.
// Which of those files a build SELECTS is keyed by the toolchain (toolchain):
// every GO*/CGO_* variable (GOFLAGS' -tags, GOOS, GOARCH, CGO_ENABLED …), the
// `go env -w` file, GOROOT/go.env, and the C compiler an unset CGO_ENABLED
// would find.
//
// The memo lives in .forge/generate-memo.json (per checkout, gitignored, and
// outside the whole-run fingerprint: it is bookkeeping, never an input — a
// skip it allows is by construction a run that would have changed nothing).
// It is live only inside a generate pipeline whose run may use the cache
// (generateCacheModeFor); --no-cache and FORGE_GENERATE_NO_CACHE=1 run every
// tool and refresh the records.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/goexec"
)

const (
	// generateMemoRel is the per-checkout record of what each go tool last
	// ran on. Like generate-cache.json it is not part of the fingerprint.
	generateMemoRel = ".forge/generate-memo.json"

	// generateMemoVersion is part of every key's meaning: bump it when a key
	// covers something different, so no record is read under new rules.
	generateMemoVersion = 3
)

// errGoMemoUnsupported says a tree's inputs cannot be enumerated (cgo, a
// symlinked directory), so the tool just runs.
var errGoMemoUnsupported = errors.New("inputs cannot be enumerated")

// generateMemoKeep is how many states each tool's record keeps. One run
// tidies a module at three points (before composition, before wiring, at the
// end), and the imports differ between them: codegen writes a file's imports
// one way and goimports, later in the run, prunes or adds some. Keeping the
// last few states lets the next run match each point to its counterpart, and
// a checkout that alternates between two branches match both.
const generateMemoKeep = 8

type generateMemoFile struct {
	Version int `json:"version"`
	// Entries maps a tool ("tidy:<module dir>", "validate") to the keys of
	// the inputs its recent successful runs left behind, oldest first.
	Entries map[string][]string `json:"entries"`
	// Executables caches the go toolchain's identity by stat, so a key does
	// not re-hash the go binary on every call.
	Executables map[string]execStamp `json:"executables,omitempty"`
}

// generateMemo is one pipeline run's view of the record.
type generateMemo struct {
	mu      sync.Mutex
	root    string
	consult bool // may skip a tool whose inputs match its record
	sys     generateCacheSys
	file    generateMemoFile
	// skipped names the tools this run skipped, for the timings report.
	skipped []string
}

var activeGoMemo struct {
	mu sync.Mutex
	m  *generateMemo
}

// beginGenerateMemo makes the memo live for one pipeline run at root and
// returns the function that ends it. A run that may not use the cache gets no
// memo at all; one that may only record (--no-cache) runs every tool and
// records what each left. A run that weakens its checks records too: what a
// tool left behind does not depend on the checks the run skipped.
func beginGenerateMemo(root string, flags pipelineFlags, sys generateCacheSys) (end func()) {
	mode := generateCacheModeFor(flags)
	if !mode.consult && !mode.record {
		return func() {}
	}
	m := &generateMemo{root: root, consult: mode.consult, sys: sys}
	m.file = loadGenerateMemoFile(root)
	activeGoMemo.mu.Lock()
	prev := activeGoMemo.m
	activeGoMemo.m = m
	activeGoMemo.mu.Unlock()
	return func() {
		activeGoMemo.mu.Lock()
		activeGoMemo.m = prev
		activeGoMemo.mu.Unlock()
	}
}

// goMemoFor returns the live memo when it belongs to the project at dir (or
// to a module inside it), else nil.
func goMemoFor(dir string) *generateMemo {
	activeGoMemo.mu.Lock()
	m := activeGoMemo.m
	activeGoMemo.mu.Unlock()
	if m == nil {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil || !within(m.root, abs) {
		return nil
	}
	return m
}

// goMemoSkipped is what the live memo skipped so far, for --timings.
func goMemoSkipped() []string {
	activeGoMemo.mu.Lock()
	m := activeGoMemo.m
	activeGoMemo.mu.Unlock()
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.skipped...)
}

func loadGenerateMemoFile(root string) generateMemoFile {
	empty := generateMemoFile{Version: generateMemoVersion, Entries: map[string][]string{}, Executables: map[string]execStamp{}}
	data, err := os.ReadFile(filepath.Join(root, generateMemoRel))
	if err != nil {
		return empty
	}
	var f generateMemoFile
	if json.Unmarshal(data, &f) != nil || f.Version != generateMemoVersion || f.Entries == nil {
		return empty
	}
	if f.Executables == nil {
		f.Executables = map[string]execStamp{}
	}
	return f
}

// key computes a tool's input key with this memo's toolchain identity cache.
// ok is false when the inputs cannot be keyed; the tool then runs and nothing
// is recorded.
func (m *generateMemo) key(compute func(*memoHasher) error) (string, bool) {
	h := &memoHasher{h: sha256.New()}
	h.add("memo", strconv.Itoa(generateMemoVersion))
	if err := compute(h); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.h.Sum(nil)), true
}

// hit reports whether one of name's records matches key, noting a skip.
func (m *generateMemo) hit(name, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.consult || !slices.Contains(m.file.Entries[name], key) {
		return false
	}
	m.skipped = append(m.skipped, name)
	return true
}

// store records key as what one of name's successful runs left behind,
// keeping the generateMemoKeep most recent. The file is rewritten at once, so
// a later failure in the run keeps the record: it describes the inputs the
// tool produced, which stays true whatever the run does next (a rollback
// restores other bytes, which then do not match).
func (m *generateMemo) store(name, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := slices.DeleteFunc(m.file.Entries[name], func(k string) bool { return k == key })
	keys = append(keys, key)
	if len(keys) > generateMemoKeep {
		keys = keys[len(keys)-generateMemoKeep:]
	}
	m.file.Entries[name] = keys
	data, err := json.Marshal(m.file)
	if err != nil {
		return
	}
	// Best-effort: an unwritable record only costs the next run a tool call.
	_ = writeForgeStateFile(m.root, generateMemoRel, data)
}

// toolchain keys the go command env would run: the binary, every variable
// that steers it, `go env -w` settings and the go.work in effect — and the
// forge running it, which decides what a tool's output means (which test-file
// diagnostics fail the validate build, how a tidy is invoked).
func (m *generateMemo) toolchain(h *memoHasher, env []string) error {
	goBin, err := m.sys.lookPath("go")
	if err != nil {
		return err
	}
	m.mu.Lock()
	execs := &execIdentities{prev: m.file.Executables, next: m.file.Executables, now: m.sys.now()}
	id, err := execs.identify(goBin, true)
	forgeID := m.sys.runningForge
	if err == nil && forgeID == "" {
		var exe string
		if exe, err = m.sys.executable(); err == nil {
			forgeID, err = execs.identify(exe, false)
			m.sys.runningForge = forgeID
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	h.add("go", id)
	h.add("forge", forgeID)
	var vars []string
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && generateCacheEnvRelevant(name) {
			vars = append(vars, kv)
		}
	}
	sort.Strings(vars)
	for _, kv := range vars {
		h.add("env", kv)
	}
	if f := goEnvFile(env); f != "" {
		if err := h.file("goenv", f); err != nil {
			return err
		}
	}
	// The toolchain's own defaults layer (GOROOT/go.env: GOFLAGS, GOTOOLCHAIN,
	// GOPROXY …) sits beside the binary, not in it.
	if err := h.file("goroot.env", filepath.Join(goRootFor(goBin, env), "go.env")); err != nil {
		return err
	}
	// With CGO_ENABLED unset, cmd/go enables cgo only when it finds a C
	// compiler, and that decides which `//go:build cgo` files (here and in
	// every dependency) the build selects. Key what it would find.
	for _, cc := range cCompilerCandidates(env) {
		p, err := m.sys.lookPath(cc)
		if err != nil {
			p = "absent"
		}
		h.add("cc:"+cc, p)
	}
	work, err := goWorkSelection(m.root, env)
	if err != nil {
		return err
	}
	// Which workspace is in effect, not only what it says: two go.work files
	// with the same bytes in different places resolve their relative use and
	// replace paths to different modules.
	h.add("gowork.selected", work)
	if work != "" {
		if err := h.file("gowork", work); err != nil {
			return err
		}
		if err := h.file("gowork.sum", work+".sum"); err != nil {
			return err
		}
	}
	return nil
}

// goWorkSelection is the absolute path of the go.work a go command run in
// root would use ("" for none): $GOWORK when set (off selects none), else the
// first go.work found walking up from root, as cmd/go discovers it. A $GOWORK
// that is not an absolute path is one cmd/go refuses, and its meaning would
// depend on the working directory, so it makes the inputs unsupported.
func goWorkSelection(root string, env []string) (string, error) {
	if v, ok := environLookup(env, "GOWORK"); ok && v != "" && v != "off" && !filepath.IsAbs(v) {
		return "", errGoMemoUnsupported
	}
	work := effectiveGoWork(root, env)
	if work == "" {
		return "", nil
	}
	return filepath.Clean(work), nil
}

// goRootFor is the GOROOT the go binary at goBin runs with: $GOROOT when set,
// else the directory above the (symlink-resolved) binary's bin/.
func goRootFor(goBin string, env []string) string {
	if v, ok := environLookup(env, "GOROOT"); ok && v != "" {
		return v
	}
	if resolved, err := filepath.EvalSymlinks(goBin); err == nil {
		goBin = resolved
	}
	return filepath.Dir(filepath.Dir(goBin))
}

// cCompilerCandidates names the C compilers cmd/go would look for: $CC's
// first word when set, else its defaults.
func cCompilerCandidates(env []string) []string {
	if v, ok := environLookup(env, "CC"); ok {
		if fields := strings.Fields(v); len(fields) > 0 {
			return []string{fields[0]}
		}
	}
	return []string{"gcc", "clang"}
}

// goTidyKey is the key of one `go mod tidy`: full covers every input, sources
// every input but the go.mod and go.sum files tidy rewrites.
type goTidyKey struct{ full, sources string }

// goTidyMemoKey keys `go mod tidy` in moduleDir: the toolchain, then go.mod,
// go.sum and what tidy reads of every .go file (goImportHeader) of the module
// and of every local replace target it reaches.
func (m *generateMemo) goTidyMemoKey(moduleDir string, env []string) (goTidyKey, bool) {
	var sources string
	full, ok := m.key(func(h *memoHasher) error {
		src := &memoHasher{h: sha256.New()}
		both := func(label, value string) {
			h.add(label, value)
			src.add(label, value)
		}
		both("tool", "tidy")
		if err := m.toolchain(h, env); err != nil {
			return err
		}
		seen := map[string]bool{}
		var addModule func(dir string) error
		addModule = func(dir string) error {
			dir = filepath.Clean(dir)
			if seen[dir] {
				return nil
			}
			seen[dir] = true
			both("module", dir)
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := h.file(name, filepath.Join(dir, name)); err != nil {
					return err
				}
			}
			files, err := walkGoInputs(dir, true)
			if err != nil {
				return err
			}
			for _, f := range files {
				if !f.goFile {
					continue
				}
				header, err := goImportHeader(filepath.Join(dir, f.rel))
				if err != nil {
					return err
				}
				both("go:"+f.rel, string(header))
			}
			targets, err := localReplaceTargets(filepath.Join(dir, "go.mod"))
			if err != nil {
				return err
			}
			for _, t := range targets {
				if err := addModule(t); err != nil {
					return err
				}
			}
			return nil
		}
		if err := addModule(moduleDir); err != nil {
			return err
		}
		sources = hex.EncodeToString(src.h.Sum(nil))
		return nil
	})
	return goTidyKey{full: full, sources: sources}, ok
}

// memoizedGoTidy runs tidy in moduleDir (named name in the record) unless
// the module is what a tidy already left behind. done is printed on a skip.
//
// The record is the state AFTER tidy, taken only when no source changed while
// it ran: an import someone added mid-tidy is in the post-run sources but was
// never tidied, and recording it would skip the tidy it needs.
func memoizedGoTidy(moduleDir, name, done string, tidy func() error) error {
	m := goMemoFor(moduleDir)
	if m == nil {
		return tidy()
	}
	env := goexec.Env()
	pre, ok := m.goTidyMemoKey(moduleDir, env)
	if ok && m.hit(name, pre.full) {
		fmt.Println(done)
		return nil
	}
	if err := tidy(); err != nil {
		return err
	}
	if post, ok2 := m.goTidyMemoKey(moduleDir, env); ok && ok2 && post.sources == pre.sources {
		m.store(name, post.full)
	}
	return nil
}

// memoizedGoValidate runs the validate build unless the tree's build inputs
// are ones it already passed on. A pass is recorded only when the inputs did
// not move while it ran.
func memoizedGoValidate(projectDir string, validate func() error) error {
	m := goMemoFor(projectDir)
	if m == nil || !sameDirectory(projectDir, m.root) {
		// The key discovers the workspace from the memo's root; a build run
		// elsewhere could select another go.work.
		return validate()
	}
	env := goexec.CheckEnv()
	pre, ok := m.goValidateMemoKey(env)
	if ok && m.hit("validate", pre) {
		fmt.Println("\n🔨 Validating generated code...")
		fmt.Println("  ✅ unchanged since it last compiled: every Go build input, the toolchain and the environment are byte-identical")
		return nil
	}
	if err := validate(); err != nil {
		return err
	}
	if post, ok2 := m.goValidateMemoKey(env); ok && ok2 && post == pre {
		m.store("validate", pre)
	}
	return nil
}

// goValidateMemoKey keys the validate build of the project at root: the
// toolchain, every file a Go build of it can read (see the file comment), and
// the same for the modules outside it the build compiles.
func (m *generateMemo) goValidateMemoKey(env []string) (string, bool) {
	return m.key(func(h *memoHasher) error {
		h.add("tool", "validate")
		if err := m.toolchain(h, env); err != nil {
			return err
		}
		external, err := m.addBuildTree(h, m.root)
		if err != nil {
			return err
		}
		work, err := goWorkSelection(m.root, env)
		if err != nil {
			return err
		}
		if work != "" {
			uses, err := goWorkUses(work)
			if err != nil {
				return err
			}
			external = append(external, uses...)
		}
		seen := map[string]bool{filepath.Clean(m.root): true}
		for len(external) > 0 {
			dir := filepath.Clean(external[0])
			external = external[1:]
			if seen[dir] || within(m.root, dir) {
				continue
			}
			seen[dir] = true
			more, err := m.addBuildTree(h, dir)
			if err != nil {
				return err
			}
			external = append(external, more...)
		}
		return nil
	})
}

// addBuildTree adds every build input under dir and returns the local replace
// targets its go.mod files name.
func (m *generateMemo) addBuildTree(h *memoHasher, dir string) ([]string, error) {
	files, err := walkGoInputs(dir, false)
	if err != nil {
		return nil, err
	}
	pkgDirs := map[string]bool{}
	for _, f := range files {
		if f.goFile {
			pkgDirs[path.Dir(f.rel)] = true
		}
	}
	h.add("tree", dir)
	var targets []string
	for _, f := range files {
		base := filepath.Base(f.rel)
		moduleFile := base == "go.mod" || base == "go.sum" || base == "go.work" || base == "go.work.sum" || base == "modules.txt"
		if !moduleFile && !underPackageDir(f.rel, pkgDirs) {
			continue
		}
		abs := filepath.Join(dir, f.rel)
		if f.goFile {
			cgo, err := importsC(abs)
			if err != nil {
				return nil, err
			}
			if cgo {
				return nil, errGoMemoUnsupported
			}
		}
		if err := h.file(f.rel, abs); err != nil {
			return nil, err
		}
		if base == "go.mod" {
			t, err := localReplaceTargets(abs)
			if err != nil {
				return nil, err
			}
			targets = append(targets, t...)
		}
	}
	return targets, nil
}

// underPackageDir reports whether rel lies in a directory that holds a .go
// file, or below one.
func underPackageDir(rel string, pkgDirs map[string]bool) bool {
	for d := path.Dir(rel); ; d = path.Dir(d) {
		if pkgDirs[d] {
			return true
		}
		if d == "." {
			return false
		}
	}
}

// goInputFile is one regular file found under a walked directory.
type goInputFile struct {
	rel    string // slash-separated, relative to the walk root
	goFile bool
}

// walkGoInputs lists every regular file under dir, sorted, skipping .git and
// node_modules. With pruneModules it does not descend into a directory that
// holds its own go.mod (another module). A symlinked directory makes the tree
// unsupported; a symlinked file is read through.
func walkGoInputs(dir string, pruneModules bool) ([]goInputFile, error) {
	var out []goInputFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			if p == dir {
				return nil
			}
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if pruneModules {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			info, err := os.Stat(p)
			if err != nil {
				return nil // dangling: nothing a build can read
			}
			if info.IsDir() {
				return errGoMemoUnsupported
			}
		} else if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		out = append(out, goInputFile{rel: rel, goFile: strings.HasSuffix(rel, ".go")})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// goImportHeader returns what `go mod tidy` reads of a Go file: its build
// constraint lines, its package name and the sorted set of paths it imports.
// Not the header's bytes: codegen writes imports in one grouping and
// goimports regroups them, so the same imports arrive formatted two ways in
// one run, and tidy reads neither the grouping nor the aliases. A file that
// does not parse is returned whole: whatever tidy makes of it, the key then
// changes with every byte.
func goImportHeader(file string) ([]byte, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil || f == nil || f.Name == nil {
		return src, nil
	}
	var b strings.Builder
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				b.WriteString(strings.TrimSpace(c.Text))
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("package " + f.Name.Name + "\n")
	paths := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		if imp.Path != nil {
			paths = append(paths, imp.Path.Value)
		}
	}
	sort.Strings(paths)
	for i, p := range paths {
		if i == 0 || p != paths[i-1] {
			b.WriteString("import " + p + "\n")
		}
	}
	return []byte(b.String()), nil
}

// importsC reports whether a Go file uses cgo. An unparsable file does not
// (it fails the build either way).
func importsC(file string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return false, nil
	}
	for _, imp := range f.Imports {
		if imp.Path != nil && imp.Path.Value == `"C"` {
			return true, nil
		}
	}
	return false, nil
}

// localReplaceTargets returns the directories a go.mod's local-path replace
// directives name ("" go.mod: none).
func localReplaceTargets(gomod string) ([]string, error) {
	data, err := os.ReadFile(gomod)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Parse, not ParseLax: ParseLax drops replace directives.
	f, err := modfile.Parse(gomod, data, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range f.Replace {
		if modfile.IsDirectoryPath(r.New.Path) {
			out = append(out, resolveFrom(filepath.Dir(gomod), r.New.Path))
		}
	}
	return out, nil
}

// goWorkUses returns the module and replace directories a go.work names.
func goWorkUses(work string) ([]string, error) {
	var out []string
	err := goWorkModules(work, func(dir string) { out = append(out, dir) }, func(gomod string) error {
		t, err := localReplaceTargets(gomod)
		out = append(out, t...)
		return err
	})
	return out, err
}

// memoHasher writes length-prefixed fields, so no two input lists collide.
type memoHasher struct{ h hash.Hash }

func (h *memoHasher) add(label, value string) {
	fmt.Fprintf(h.h, "%d:%s%d:", len(label), label, len(value))
	// hash.Hash's Write "never returns an error" (its documented contract),
	// so there is no failure here to handle or report.
	_, _ = io.WriteString(h.h, value)
}

// file adds a file's mode and content under label, or that it is absent.
func (h *memoHasher) file(label, abs string) error {
	f, err := os.Open(abs)
	if errors.Is(err, fs.ErrNotExist) {
		h.add(label, "absent")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		h.add(label, "dir")
		return nil
	}
	fmt.Fprintf(h.h, "%d:%s%o:%d:", len(label), label, info.Mode().Perm(), info.Size())
	_, err = io.Copy(h.h, f)
	return err
}
