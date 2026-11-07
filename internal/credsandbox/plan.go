// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What a later run executes or believes.
//
// Hiding the credential files keeps a credentials-none command from READING a
// secret. It does nothing about a command that WRITES somewhere a later,
// credentialed command looks: the daemon binary that git runs as its credential
// helper, the git config that names it, the shared project checkouts every
// workflow fetches into and runs scripts from, and the Go build and module
// caches every later build trusts. A command that may not hold a credential
// could plant code in any of them and have the next credentialed step run it
// (Reliant bug e433fa60). PlanFor closes those:
//
//   - read-only: the daemon's binary and /daemon-bin, every credential helper
//     the git and forge config name, the user's git, Go and shell config, the
//     daemon's own config and the files it trusts, and every shared project
//     checkout that is not the command's own (its .git included);
//   - private: the Go build and module caches, the golangci-lint cache, the
//     pinned-forge and forge tool caches, and every directory on PATH the
//     daemon's user can write. The command reads them as they are and writes
//     to an overlay that is its own and is discarded when it exits, so a warm
//     cache stays warm and nothing it writes reaches a later build;
//   - sealed: a trusted file that does not exist yet (~/.gitconfig on a fresh
//     machine) cannot be created for real, because its directory is rebuilt
//     and anything new in it is gone when the command exits.
//
// "The command's own" is the checkout that holds its working directory and,
// when it runs in a Reliant worktree, every checkout of that worktree. A step
// that must write a shared checkout runs in it (its working directory), names
// it in its run node's writable_checkouts (a checkout, or a directory inside
// one), or runs with credentials.

// managedProjectsDir is where a Reliant cloud machine keeps its project
// checkouts (internal/handlers/project). A variable for the tests.
var managedProjectsDir = "/home/workspace/projects"

// daemonBinDir is the directory a cloud machine's daemon binary is mounted at,
// and the binary is also the git credential helper. A variable for the tests.
var daemonBinDir = "/daemon-bin"

// readOnlyReliantRel are the daemon's own config and the files it trusts under
// ~/.reliant, beside the credentials that are hidden and the worktrees that
// are not touched: its config, MCP servers (commands it starts), presets,
// workflows, the usr-local tree mounted at /usr/local, and the packages it
// installs at boot.
var readOnlyReliantRel = []string{
	".reliant/config.yaml", ".reliant/mcp.json", ".reliant/presets", ".reliant/workflows",
	".reliant/project-meta.yaml", ".reliant/instance.json", ".reliant/usr-local",
	".reliant/system-packages", ".reliant/bin",
	".config/reliant",
}

// sealedHomeRel are trusted home files that a credentials-none command must not
// create when they are absent: git's and Go's config, and the files every
// shell sources.
var sealedHomeRel = []string{
	".gitconfig", ".config/git", ".config/go/env",
	".bashrc", ".bash_profile", ".bash_login", ".profile", ".zshrc", ".zshenv", ".zprofile",
}

// projectTrustRel are the files under a project root that is not itself a
// checkout which every chat and workflow in the project loads: its workflows,
// skills, presets and MCP servers, and its memory file. They are read-only
// even when the root is the command's working directory: they are config, not
// work.
var projectTrustRel = []string{".reliant", "reliant.md"}

// planEnv is what PlanFor reads about the process and the command.
type planEnv struct {
	home         string
	cwd          string
	env          []string // the command's environment
	executable   string
	projectsDir  string
	daemonBinDir string
	// optIn are the directories the command declared it writes
	// (Writable.Paths, absolute). What may be named is decided in one place,
	// writableFor; anything else is Plan.Rejected.
	optIn []string
	// handOffsOnly is Writable.HandOffsOnly.
	handOffsOnly bool
}

// Writable is what a credentials-none command declared it writes beyond its
// own checkout and working directory: a run node's writable_checkouts, or the
// writable_checkouts a workflow bound on an agent's shell. Either way the
// workflow wrote it, never a model (the shell tool takes it only from a run
// step or a bound parameter), and the same rules decide what it may name.
type Writable struct {
	// Paths are shared project checkouts or directories inside one, and
	// hand-off directories inside a cache root (cacheRootOverlays). Relative
	// ones are relative to the command's directory.
	Paths []string
	// HandOffsOnly limits Paths to hand-off directories. Set for an agent's
	// shell: its commands are the model's, so a binding there may give it
	// its own scratch clone to commit in, never a shared checkout.
	HandOffsOnly bool
}

// PlanFor is the plan for a credentials-none command with working directory
// dir (empty: this process's) and environment env (nil: this process's), which
// declared writable.
func PlanFor(dir string, env []string, writable Writable) Plan {
	if env == nil {
		env = os.Environ() //nolint:forbidigo // a command with no environment of its own inherits this process's
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	var optIn []string
	for _, w := range writable.Paths {
		if w = strings.TrimSpace(w); w == "" {
			continue
		}
		if !filepath.IsAbs(w) {
			w = filepath.Join(dir, w)
		}
		optIn = append(optIn, w)
	}
	return buildPlan(DefaultPlan(), planEnv{
		home: home, cwd: dir, env: env, executable: self,
		projectsDir: managedProjectsDir, daemonBinDir: daemonBinDir, optIn: optIn,
		handOffsOnly: writable.HandOffsOnly,
	})
}

// optInKind is what a declared writable directory turned out to be.
type optInKind int

const (
	optInRejected       optInKind = iota
	optInOwn                      // inside what the command writes anyway: nothing to do
	optInHandOff                  // a hand-off inside a cache root: kept real (Passthrough)
	optInSharedCheckout           // a shared checkout itself: the command's own
	optInInsideShared             // a directory inside a shared checkout: a carve-out
)

// writableFor is the one rule for what a declared writable directory w may
// be, for a run node's and a bound agent shell's alike. Never the daemon's
// binary, git or daemon config, a credential helper or a tool cache: those are
// not checkouts or hand-offs, so they are rejected.
func writableFor(w string, handOffsOnly bool, own, shared []string, cwd string, cwdOwn bool, roots, tools []Overlay) optInKind {
	if under(w, own...) {
		return optInOwn
	}
	// Before the working directory: a shared checkout inside it (the
	// multi-repo root a command starts in) is read-only all the same.
	for _, s := range shared {
		switch {
		case handOffsOnly && under(w, s):
			return optInRejected
		case w == s:
			return optInSharedCheckout
		case under(w, s):
			return optInInsideShared
		}
	}
	switch {
	case handOffDir(w, roots, tools):
		return optInHandOff
	case cwdOwn && under(w, cwd):
		return optInOwn
	}
	return optInRejected
}

func buildPlan(base Plan, in planEnv) Plan {
	p := base
	cwd := canonicalDir(in.cwd)
	home := in.home

	var ro, seal []string
	if in.daemonBinDir != "" {
		ro = append(ro, in.daemonBinDir)
	}
	if in.executable != "" {
		ro = append(ro, in.executable)
		if real, err := filepath.EvalSymlinks(in.executable); err == nil {
			ro = append(ro, real)
		}
	}
	helpersPresent, helpersAbsent := credentialHelpers(home, in.env)
	ro = append(ro, helpersPresent...)
	if home != "" {
		for _, rel := range readOnlyReliantRel {
			ro = append(ro, filepath.Join(home, rel))
			seal = append(seal, filepath.Join(home, rel))
		}
		for _, rel := range sealedHomeRel {
			ro = append(ro, filepath.Join(home, rel))
			seal = append(seal, filepath.Join(home, rel))
		}
		if goenv := envValue(in.env, "GOENV"); goenv != "" && goenv != "off" {
			ro = append(ro, goenv)
			seal = append(seal, goenv)
		}
	}

	// The command's own checkouts.
	own := ownCheckouts(cwd, home)
	isOwn := func(path string) bool {
		for _, o := range own {
			if path == o {
				return true
			}
		}
		return false
	}

	// The shared ones: every checkout of the machine's projects, and the main
	// checkout behind each of the command's own worktrees.
	var shared []string
	if in.projectsDir != "" {
		shared = append(shared, discoverCheckouts(in.projectsDir)...)
		for _, root := range projectRoots(in.projectsDir) {
			for _, rel := range projectTrustRel {
				ro = append(ro, filepath.Join(root, rel))
			}
		}
	}
	commons := map[string]string{} // a linked own checkout's git directory -> its main checkout
	for _, o := range own {
		common, linked := commonDir(o)
		if !linked {
			continue
		}
		if main := mainCheckout(common); main != "" {
			shared = append(shared, main)
			commons[common] = main
		}
	}
	tools := cacheOverlays(home, in.env)
	tools = append(tools, pathOverlays(in.env, home)...)
	roots := cacheRootOverlays(home, in.env)
	// Every root and cache by its real path, as an opt-in is classified.
	for i := range tools {
		tools[i].Path = canonicalDir(tools[i].Path)
	}
	for i := range roots {
		roots[i].Path = canonicalDir(roots[i].Path)
	}
	// The working directory is the command's own unless it is the home or
	// above it: a command started in ~ does not own ~/.cache.
	cwdOwn := cwd != "" && (home == "" || !under(home, cwd))

	// An opt-in naming a shared checkout makes it the command's own; one
	// inside a shared checkout is a carve-out of it; one inside a cache root
	// stays real there (below). Anything else is refused.
	var writable, handOffs, rejected []string
	shared = dedupe(shared)
	ownBefore := append([]string(nil), own...)
	for _, w := range in.optIn {
		w = canonicalDir(w)
		switch writableFor(w, in.handOffsOnly, ownBefore, shared, cwd, cwdOwn, roots, tools) {
		case optInSharedCheckout:
			own = append(own, w)
		case optInInsideShared:
			writable = append(writable, w)
		case optInHandOff:
			handOffs = append(handOffs, w)
		case optInRejected:
			rejected = append(rejected, w)
		case optInOwn:
		}
	}
	readOnlyShared := map[string]bool{}
	for _, s := range shared {
		if !isOwn(s) {
			ro = append(ro, s)
			readOnlyShared[s] = true
		}
	}
	for common, main := range commons {
		if !readOnlyShared[main] {
			continue
		}
		// git in a linked worktree writes the shared repository's objects,
		// refs and logs and its own entry under worktrees/; it never needs to
		// write the config or the hooks, which decide what git runs in the
		// shared checkout and every other worktree of it.
		writable = append(writable, common)
		ro = append(ro, filepath.Join(common, "config"), filepath.Join(common, "hooks"))
	}
	// A carve-out matters only inside something read-only; the command's own
	// checkout nested in a shared one (a repository inside a multi-repo root
	// that is itself a checkout) is the other case.
	writable = append(writable, own...)

	roAll := cleanAbs(append(append([]string(nil), p.ReadOnly...), ro...))
	var keep []Overlay
	for _, o := range append(tools, roots...) {
		if under(o.Path, own...) || (cwdOwn && under(o.Path, cwd)) {
			continue // the command's own tools
		}
		if under(o.Path, roAll...) {
			continue // read-only already, which is stricter
		}
		keep = append(keep, o)
	}

	// What stays real inside a private cache root: the command's own
	// checkouts and working directory, the hand-off directories its run node
	// named (writable_checkouts), and the heavy-slot locks every command on
	// the machine queues on. A named directory is one strictly inside a cache
	// root and apart from the tool caches, so an opt-in cannot make the Go
	// build cache, the module cache or a pinned forge real again.
	var pass []string
	if cwdOwn {
		pass = append(pass, cwd)
	}
	pass = append(pass, own...)
	if slots := slotDir(in.env); slots != "" {
		pass = append(pass, slots)
	}
	pass = append(pass, handOffs...)
	var keptRoots []string
	for _, o := range keep {
		if o.Notice {
			keptRoots = append(keptRoots, o.Path)
		}
	}
	var passthrough []string
	for _, w := range cleanAbs(pass) {
		for _, r := range keptRoots {
			if w != r && under(w, r) {
				passthrough = append(passthrough, w)
				break
			}
		}
	}

	// A helper not on PATH yet must not be creatable where a credentialed git
	// would find it: in a directory kept private it cannot outlive the
	// command anyway; in one of the home's, or the command's own, that is not,
	// it is sealed. System directories are the system's, which the daemon's
	// user cannot write.
	var keptPaths []string
	for _, o := range keep {
		keptPaths = append(keptPaths, o.Path)
	}
	for _, c := range helpersAbsent {
		d := filepath.Dir(c)
		if under(d, keptPaths...) || under(d, roAll...) {
			continue
		}
		if (home != "" && under(d, home)) || under(d, own...) || (cwdOwn && under(d, cwd)) {
			seal = append(seal, c)
		}
	}

	p.ReadOnly = roAll
	p.Seal = cleanAbs(append(p.Seal, seal...))
	p.Writable = onlyUnderReadOnly(cleanAbs(append(p.Writable, writable...)), p.ReadOnly)
	p.Overlay = dedupeOverlays(append(p.Overlay, keep...))
	p.Passthrough = cleanAbs(append(p.Passthrough, passthrough...))
	p.Rejected = cleanAbs(append(p.Rejected, rejected...))
	return p
}

// cacheRootOverlays are the user's cache directories as a whole: ~/.cache and
// XDG_CACHE_HOME. Besides the tools' caches (cacheOverlays) they hold what a
// workflow's steps hand one another (a credentialed step fetches into
// $XDG_CACHE_HOME/<pipeline>/<run>, an uncredentialed one reads it), so a
// credentials-none command reads them as they are and writes them privately:
// it cannot change a hand-off of its own run's or of another run running beside
// it. A step that must write a hand-off a later step reads names it in its run
// node's writable_checkouts (Plan.Passthrough). Notice: the command is told,
// as it exits, what it wrote there and lost.
func cacheRootOverlays(home string, env []string) []Overlay {
	var out []Overlay
	if v := envValue(env, "XDG_CACHE_HOME"); filepath.IsAbs(v) {
		out = append(out, Overlay{Path: filepath.Clean(v), Cache: true, Notice: true})
	}
	if home != "" {
		out = append(out, Overlay{Path: filepath.Join(home, ".cache"), Cache: true, Notice: true})
	}
	return out
}

// handOffDir reports whether an opt-in w may make a directory inside a private
// cache root real: strictly inside one, and neither inside nor holding a tool
// cache.
func handOffDir(w string, roots, tools []Overlay) bool {
	inRoot := false
	for _, r := range roots {
		if w != r.Path && under(w, r.Path) {
			inRoot = true
		}
	}
	if !inRoot {
		return false
	}
	for _, t := range tools {
		if under(w, t.Path) || under(t.Path, w) {
			return false
		}
	}
	return true
}

// slotDir is where the machine's heavy slots are (forge/pkg/hostslot): lock
// files every command queues on, so they must be the real ones.
func slotDir(env []string) string {
	if d := envValue(env, "FORGE_SLOTS_DIR"); filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	cache := envValue(env, "XDG_CACHE_HOME")
	if !filepath.IsAbs(cache) {
		home := envValue(env, "HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		cache = filepath.Join(home, ".cache")
	}
	return filepath.Join(cache, "forge", "slots")
}

// ownCheckouts are the checkout holding cwd and, when cwd is in a Reliant
// worktree (~/.reliant/worktrees/<project>/<name>/), every checkout of it.
func ownCheckouts(cwd, home string) []string {
	var own []string
	if c := checkoutRoot(cwd); c != "" {
		own = append(own, c)
	}
	if home != "" {
		wts := canonicalDir(filepath.Join(home, ".reliant", "worktrees"))
		if rel, err := filepath.Rel(wts, cwd); err == nil && !strings.HasPrefix(rel, "..") {
			if parts := strings.Split(rel, string(filepath.Separator)); len(parts) >= 2 {
				ws := filepath.Join(wts, parts[0], parts[1])
				own = append(own, ws)
				own = append(own, discoverCheckouts(ws)...)
			}
		}
	}
	return dedupe(own)
}

// checkoutRoot is the nearest directory at or above dir that holds a .git
// entry, or "".
func checkoutRoot(dir string) string {
	for d := dir; d != "" && d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
	}
	return ""
}

// commonDir is the git directory a checkout shares with its main checkout,
// and whether the checkout is a linked worktree (its .git a file naming it).
func commonDir(checkout string) (string, bool) {
	dotgit := filepath.Join(checkout, ".git")
	fi, err := os.Lstat(dotgit)
	if err != nil || fi.IsDir() {
		return dotgit, false
	}
	raw, err := os.ReadFile(dotgit)
	if err != nil {
		return "", false
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(checkout, gitdir)
	}
	common := gitdir
	if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	}
	return canonicalDir(filepath.Clean(common)), true
}

// mainCheckout is the checkout whose .git is common, or "" for a bare one.
func mainCheckout(common string) string {
	if filepath.Base(common) != ".git" {
		return ""
	}
	return filepath.Dir(common)
}

// Discovery is bounded: project roots and their children, and the checkouts
// nested directly inside a checkout (a multi-repo root that is itself a
// repository). A checkout's own tree is never walked.
const (
	discoverDepth    = 3
	discoverMaxDirs  = 2000
	discoverCacheTTL = 15 * time.Second
)

var discoverCache = struct {
	sync.Mutex
	at   map[string]time.Time
	hits map[string][]string
}{at: map[string]time.Time{}, hits: map[string][]string{}}

// discoverCheckouts lists the checkouts under root, the shallowest first.
func discoverCheckouts(root string) []string {
	root = canonicalDir(root)
	discoverCache.Lock()
	if at, ok := discoverCache.at[root]; ok && time.Since(at) < discoverCacheTTL {
		out := discoverCache.hits[root]
		discoverCache.Unlock()
		return out
	}
	discoverCache.Unlock()

	var out []string
	visited := 0
	var walk func(dir string, depth int, inCheckout bool)
	walk = func(dir string, depth int, inCheckout bool) {
		if visited >= discoverMaxDirs {
			return
		}
		visited++
		isCheckout := false
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			out = append(out, dir)
			isCheckout = true
		}
		if inCheckout || depth >= discoverDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				continue
			}
			walk(filepath.Join(dir, name), depth+1, isCheckout)
		}
	}
	if fi, err := os.Stat(root); err == nil && fi.IsDir() {
		walk(root, 0, false)
	}
	sort.SliceStable(out, func(i, j int) bool { return depthOf(out[i]) < depthOf(out[j]) })

	discoverCache.Lock()
	discoverCache.at[root] = time.Now()
	discoverCache.hits[root] = out
	discoverCache.Unlock()
	return out
}

// projectRoots are the directories directly under the projects directory that
// are not checkouts themselves: multi-repo roots.
func projectRoots(projectsDir string) []string {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		d := filepath.Join(canonicalDir(projectsDir), e.Name())
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			continue
		}
		out = append(out, d)
	}
	return out
}

// credentialHelpers are the programs a later credentialed git or forge runs to
// fetch a credential, as git and forge find them: FORGE_CREDENTIAL_HELPER's
// program, and every helper in the user's and the system's git config. A
// helper that is a path is that file. One that is not is found on PATH by the
// credentialed command: a `!` shell helper's first word as it is, any other
// helper as git-credential-<name> (git's own exec directory, root's, comes
// first and needs nothing). Those are every candidate on the command's PATH:
// existing ones (present) are made read-only; missing ones (absent) must not
// be creatable where they would be found (buildPlan seals them).
func credentialHelpers(home string, env []string) (present, absent []string) {
	type helper struct {
		name  string
		shell bool // a `!` helper, or forge's: the name as it is
	}
	var hs []helper
	if v := envValue(env, "FORGE_CREDENTIAL_HELPER"); v != "" {
		var argv []string
		if json.Unmarshal([]byte(v), &argv) == nil && len(argv) > 0 {
			hs = append(hs, helper{name: argv[0], shell: true})
		} else {
			hs = append(hs, helper{name: firstWord(v), shell: true})
		}
	}
	configs := []string{"/etc/gitconfig"}
	if home != "" {
		configs = append(configs, filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git", "config"))
	}
	for _, c := range configs {
		for _, h := range gitConfigHelpers(c) {
			hs = append(hs, helper{name: h.program, shell: h.shell})
		}
	}
	var dirs []string
	for _, d := range filepath.SplitList(envValue(env, "PATH")) {
		if filepath.IsAbs(d) {
			dirs = append(dirs, filepath.Clean(d))
		}
	}
	for _, h := range hs {
		switch {
		case h.name == "":
		case filepath.IsAbs(h.name):
			present = append(present, h.name)
		case strings.ContainsRune(h.name, '/'):
			// Relative to whatever directory the credentialed git runs in:
			// not a path this plan can name.
		default:
			name := h.name
			if !h.shell {
				name = "git-credential-" + name
			}
			for _, d := range dirs {
				c := filepath.Join(d, name)
				if _, err := os.Lstat(c); err == nil {
					present = append(present, c)
				} else {
					absent = append(absent, c)
				}
			}
		}
	}
	return present, absent
}

// gitHelper is one `helper = ...` value of a git config: the program it runs
// and whether it is a `!` shell helper.
type gitHelper struct {
	program string
	shell   bool
}

// gitConfigHelpers reads the `helper = ...` values of a git config file and
// returns the program each runs: the first word of a `!` shell helper, or the
// helper itself (a path, or a name git prefixes with git-credential-).
func gitConfigHelpers(path string) []gitHelper {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []gitHelper
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "helper") {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.TrimPrefix(value, `"`)
		value = strings.TrimSuffix(value, `"`)
		shell := strings.HasPrefix(value, "!")
		value = strings.TrimPrefix(value, "!")
		if w := firstWord(value); w != "" {
			out = append(out, gitHelper{program: w, shell: shell})
		}
	}
	return out
}

// firstWord is s's first shell word, its single or double quotes removed.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if q := s[0]; q == '\'' || q == '"' {
		if end := strings.IndexByte(s[1:], q); end >= 0 {
			return s[1 : 1+end]
		}
		return ""
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// cacheOverlays are the shared caches a later build trusts, where the command
// would otherwise write: Go's build and module caches, golangci-lint's, the
// pinned-forge cache and forge's tool cache.
func cacheOverlays(home string, env []string) []Overlay {
	goenv := goEnvFile(home, env)
	lookup := func(name string) string {
		if v := envValue(env, name); v != "" {
			return v
		}
		return goenv[name]
	}
	userCache := envValue(env, "XDG_CACHE_HOME")
	if userCache == "" && home != "" {
		userCache = filepath.Join(home, ".cache")
	}

	var out []Overlay
	gocache := lookup("GOCACHE")
	if gocache == "" && userCache != "" {
		gocache = filepath.Join(userCache, "go-build")
	}
	if gocache != "off" {
		out = append(out, Overlay{Path: gocache, Env: "GOCACHE", Cache: true})
	}
	modcache := lookup("GOMODCACHE")
	if modcache == "" {
		gopath := lookup("GOPATH")
		if gopath == "" && home != "" {
			gopath = filepath.Join(home, "go")
		}
		if first := filepath.SplitList(gopath); len(first) > 0 && first[0] != "" {
			modcache = filepath.Join(first[0], "pkg", "mod")
		}
	}
	out = append(out, Overlay{Path: modcache, Env: "GOMODCACHE", Cache: true})
	lint := envValue(env, "GOLANGCI_LINT_CACHE")
	if lint == "" && userCache != "" {
		lint = filepath.Join(userCache, "golangci-lint")
	}
	out = append(out, Overlay{Path: lint, Env: "GOLANGCI_LINT_CACHE", Cache: true})
	if userCache != "" {
		out = append(out,
			Overlay{Path: filepath.Join(userCache, "reliant", "forge-pinned"), Cache: true},
			Overlay{Path: filepath.Join(userCache, "forge", "tools"), Cache: true},
			Overlay{Path: filepath.Join(userCache, "forge", "pinned"), Cache: true},
		)
	}
	if v := envValue(env, "FORGE_TOOLS_CACHE"); v != "" {
		out = append(out, Overlay{Path: v, Cache: true})
	}
	var abs []Overlay
	for _, o := range out {
		if filepath.IsAbs(o.Path) {
			o.Path = filepath.Clean(o.Path)
			abs = append(abs, o)
		}
	}
	return abs
}

// pathOverlays are the directories on the command's PATH outside the system's
// own (which the daemon's user cannot write anyway): a binary planted in one
// runs in every later command that names it. Whether the user can write each
// is the Linux side's question (writable).
func pathOverlays(env []string, home string) []Overlay {
	var out []Overlay
	for _, d := range filepath.SplitList(envValue(env, "PATH")) {
		if !filepath.IsAbs(d) {
			continue
		}
		d = filepath.Clean(d)
		switch d {
		case "/bin", "/sbin", "/usr/bin", "/usr/sbin":
			continue
		}
		out = append(out, Overlay{Path: d, Sealed: home != "" && under(d, home)})
	}
	return out
}

// goEnvFile is the go env -w file's settings.
func goEnvFile(home string, env []string) map[string]string {
	path := envValue(env, "GOENV")
	if path == "off" {
		return nil
	}
	if path == "" {
		cfg := envValue(env, "XDG_CONFIG_HOME")
		if cfg == "" && home != "" {
			cfg = filepath.Join(home, ".config")
		}
		if cfg == "" {
			return nil
		}
		path = filepath.Join(cfg, "go", "env")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[k] = v
		}
	}
	return out
}

func envValue(env []string, name string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val // the last one wins, as in os/exec
		}
	}
	return strings.TrimSpace(v)
}

// canonicalDir resolves dir's symlinks, or returns it cleaned.
//
// For a path that does not exist (yet), the links are resolved in its longest
// existing ancestor and the rest is appended: a hand-off to be made under a
// cache root reached through a link is classified, and later made and
// mounted, as the path it really is, and one whose existing ancestry leaves
// the roots is seen to.
func canonicalDir(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	var rest []string
	for d := dir; ; d = filepath.Dir(d) {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		rest = append(rest, filepath.Base(d))
	}
}

// under reports whether path is one of roots or inside one.
func under(path string, roots ...string) bool {
	for _, r := range roots {
		if r == "" {
			continue
		}
		if path == r || strings.HasPrefix(path, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}

// onlyUnderReadOnly keeps the carve-outs that lie strictly inside a read-only
// path: one that does not is writable already.
func onlyUnderReadOnly(writable, readOnly []string) []string {
	var out []string
	for _, w := range writable {
		for _, r := range readOnly {
			if w != r && under(w, r) {
				out = append(out, w)
				break
			}
		}
	}
	return out
}

func dedupeOverlays(in []Overlay) []Overlay {
	seen := map[string]bool{}
	var out []Overlay
	for _, o := range in {
		if o.Path == "" || !filepath.IsAbs(o.Path) || seen[o.Path] {
			continue
		}
		seen[o.Path] = true
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func depthOf(p string) int { return strings.Count(filepath.Clean(p), string(filepath.Separator)) }

// dedupe is in without its repeats, in order.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
