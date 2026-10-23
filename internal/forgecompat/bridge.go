package forgecompat

import (
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// This file owns the skew Decide deliberately waves through: a project that
// resolves forge to a LOCAL CHECKOUT (a go.work `use`, or a directory
// `replace`).
//
// There, the version comparison has nothing to compare — the project
// compiles against source — but the pairing can still be wrong in a way
// nothing reported. Generated code comes from the BINARY; the library it
// calls comes from the CHECKOUT. Install the binary, let the checkout move on
// (a pull, another agent's merge, a branch switch, an uncommitted edit), and
// the two silently disagree. A dogfood run spent a day on exactly that: a
// binary built Oct 1 generating code against forge main as of Oct 5.
//
// So: identify the source the binary was compiled from, read the checkout as
// git sees it, and say — in one line, with the fix — when they are not the
// same bytes.

// Bridge is a project resolving forge to a directory on disk.
type Bridge struct {
	// Dir is the checkout, absolute and cleaned.
	Dir string
	// File is the go.work or go.mod that declares the bridge.
	File string
	// Replace is true for a directory `replace`, false for a go.work `use`.
	Replace bool
	// written is Dir as the file spells it, for `go work edit -dropuse`.
	written string
}

// String names the bridge for a one-line message.
func (b Bridge) String() string {
	switch {
	case !b.Replace:
		return "go.work bridges " + b.Dir
	case filepath.Base(b.File) == "go.work":
		return "go.work replaces forge with " + b.Dir
	default:
		return "go.mod replaces forge with " + b.Dir
	}
}

// Undo is the command that removes the bridge, run from the project.
func (b Bridge) Undo() string {
	switch {
	case !b.Replace:
		return "go work edit -dropuse=" + b.written
	case filepath.Base(b.File) == "go.work":
		return "go work edit -dropreplace=" + ModulePath
	default:
		return "go mod edit -dropreplace=" + ModulePath
	}
}

// LocalBridge reports the local forge checkout projectDir compiles against,
// if any, read from the files that declare it rather than from the toolchain.
//
// It follows the go command's own rules for the one module it cares about:
// GOWORK=off disables the workspace, GOWORK=<file> selects one, otherwise the
// nearest go.work at or above the project applies; a workspace `use` of the
// forge module wins, then a go.work directory `replace`, then the project
// go.mod's. Reading the files keeps this answerable when the module graph is
// not — a fresh scaffold before tidy, a go.work naming a missing gen/ — which
// is precisely when a `go list` would fail and a warning would go silent.
func LocalBridge(projectDir string) (Bridge, bool) {
	if work := activeGoWork(projectDir); work != "" {
		if b, ok := bridgeInGoWork(work); ok {
			return b, true
		}
	}
	return bridgeInGoMod(filepath.Join(projectDir, "go.mod"))
}

func activeGoWork(projectDir string) string {
	switch gw := strings.TrimSpace(os.Getenv("GOWORK")); {
	case gw == "off":
		return ""
	case gw != "":
		if !filepath.IsAbs(gw) {
			gw = filepath.Join(projectDir, gw)
		}
		return gw
	}
	dir, err := filepath.Abs(projectDir)
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, "go.work")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func bridgeInGoWork(path string) (Bridge, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Bridge{}, false
	}
	wf, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		return Bridge{}, false
	}
	base := filepath.Dir(path)
	for _, u := range wf.Use {
		dir := resolveDir(base, u.Path)
		if moduleAt(dir) == ModulePath {
			return Bridge{Dir: dir, File: path, written: u.Path}, true
		}
	}
	for _, r := range wf.Replace {
		if r.Old.Path == ModulePath && r.New.Version == "" {
			return Bridge{Dir: resolveDir(base, r.New.Path), File: path, Replace: true}, true
		}
	}
	return Bridge{}, false
}

func bridgeInGoMod(path string) (Bridge, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Bridge{}, false
	}
	// Parse, not ParseLax: ParseLax skips replace directives by design.
	mf, err := modfile.Parse(path, data, nil)
	if err != nil {
		return Bridge{}, false
	}
	for _, r := range mf.Replace {
		if r.Old.Path == ModulePath && r.New.Version == "" {
			return Bridge{Dir: resolveDir(filepath.Dir(path), r.New.Path), File: path, Replace: true}, true
		}
	}
	return Bridge{}, false
}

func resolveDir(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

func moduleAt(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}

// Binary is the running forge binary, as the skew check sees it.
type Binary struct {
	// Name is what the user calls it ("forge", "reliant").
	Name string
	// Revision / Modified / Tag / Embedded: see buildinfo.Source.
	Revision string
	Modified bool
	Tag      string
	Embedded bool
	// Root is the checkout the binary was compiled from ("" unknown).
	Root string
	// BuiltAt is the executable's modification time — when `go install`
	// wrote it. Zero when unreadable.
	BuiltAt time.Time
}

// CurrentBinary describes the running binary.
func CurrentBinary() Binary {
	src := buildinfo.ForgeSource()
	b := Binary{
		Name:     "forge",
		Revision: src.Revision,
		Modified: src.Modified,
		Tag:      src.Tag,
		Embedded: src.Embedded,
		Root:     buildinfo.SourceRoot(),
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		b.Name = strings.TrimSuffix(filepath.Base(exe), ".exe")
		if st, serr := os.Stat(exe); serr == nil {
			b.BuiltAt = st.ModTime()
		}
	}
	return b
}

// Checkout is a bridged directory as git sees it now.
type Checkout struct {
	Dir  string
	Head string // full commit sha
	// Dirty: tracked changes or untracked files (what `go build` stamps as
	// vcs.modified).
	Dirty bool
	// SourceEdits: some of those changes are to a file a build of forge can
	// read (sourceEdit) — the ones that make a binary built from HEAD a
	// different forge. A test report a CI job tees into the root, an
	// editor's scratch notes or a CHANGELOG edit is Dirty but not this.
	SourceEdits bool
	// HeadMovedAt is when HEAD last moved in this checkout — a commit, pull,
	// rebase or checkout — read from the reflog; HEAD's commit time when
	// there is no reflog.
	HeadMovedAt time.Time
	// LastSourceEditAt is the newest modification among the source edits.
	// A deleted file has no mtime and counts as edited now.
	LastSourceEditAt time.Time
}

// ReadCheckout reads dir with git. ok is false when dir is not a git
// checkout (or git is missing): there is no HEAD to compare against.
func ReadCheckout(dir string) (Checkout, bool) {
	head, err := gitIn(dir, "rev-parse", "HEAD")
	if err != nil || head == "" {
		return Checkout{}, false
	}
	c := Checkout{Dir: dir, Head: head}
	if status, err := gitIn(dir, "status", "--porcelain", "-z", "--untracked-files=all"); err == nil {
		c.Dirty, c.SourceEdits, c.LastSourceEditAt = dirtyEdits(dir, status, packageDirs(dir))
	}
	if logPath, err := gitIn(dir, "rev-parse", "--git-path", "logs/HEAD"); err == nil && logPath != "" {
		if !filepath.IsAbs(logPath) {
			logPath = filepath.Join(dir, logPath)
		}
		if st, err := os.Stat(logPath); err == nil {
			c.HeadMovedAt = st.ModTime()
		}
	}
	if c.HeadMovedAt.IsZero() {
		if ct, err := gitIn(dir, "log", "-1", "--format=%ct"); err == nil {
			if sec, perr := strconv.ParseInt(ct, 10, 64); perr == nil {
				c.HeadMovedAt = time.Unix(sec, 0)
			}
		}
	}
	return c, true
}

// ResolveTag returns the commit tag names in dir, or "".
func ResolveTag(dir, tag string) string {
	rev, err := gitIn(dir, "rev-parse", "--verify", "-q", tag+"^{commit}")
	if err != nil {
		return ""
	}
	return rev
}

// gitIn runs a read-only git command in dir, which is a checkout forge does
// not own (the forge checkout a project is bridged to, often another
// worktree's). --no-optional-locks: without it `git status` refreshes the
// index's stat cache and rewrites .git/index whenever a file's mtime moved,
// a write into someone else's checkout from a command that only reads.
func gitIn(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"--no-optional-locks", "-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// dirtyEdits parses `git status --porcelain -z` and returns whether anything
// is dirty, whether any entry is a source edit (sourceEdit, over the
// checkout's package directories pkgDirs), and the newest edit time among
// the source edits.
func dirtyEdits(dir, status string, pkgDirs map[string]bool) (dirty, source bool, newest time.Time) {
	entries := strings.Split(status, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		dirty = true
		code, path := e[:2], e[3:]
		from := ""
		if code[0] == 'R' || code[0] == 'C' {
			i++ // a rename/copy is followed by its source path
			if i < len(entries) {
				from = entries[i]
			}
		}
		if !sourceEdit(path, pkgDirs) && (from == "" || !sourceEdit(from, pkgDirs)) {
			continue
		}
		source = true
		mtime := time.Now()
		if st, err := os.Lstat(filepath.Join(dir, path)); err == nil {
			mtime = st.ModTime()
		}
		if mtime.After(newest) {
			newest = mtime
		}
	}
	return dirty, source, newest
}

// packageDirs is every directory of the checkout holding a non-test Go file,
// tracked or untracked: the directories whose packages a build of forge can
// compile, and so the only ones a //go:embed can read beneath (a pattern
// names files in its package's directory or below it, never above). nil when
// git cannot list them, which makes every dirty file a source edit.
func packageDirs(dir string) map[string]bool {
	out, err := gitIn(dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go")
	if err != nil {
		return nil
	}
	dirs := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f == "" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		dirs[pathpkg.Dir(f)] = true
	}
	return dirs
}

// sourceEdit reports whether a changed file, rel to the checkout, is one a
// build of forge can read: a Go build source (Go, assembly, cgo, syso, the
// module and workspace files), or any file beneath a package directory,
// where a //go:embed pattern may name it — templates, KCL schemas, skills.
// Anything else (a test report a CI job tees into the root, docs, editor
// scratch) cannot change the binary, and counting it as skew would rebuild
// forge from the checkout for every write to it. With pkgDirs nil (git could
// not list them) every file counts: an unneeded rebuild beats a missed one.
func sourceEdit(rel string, pkgDirs map[string]bool) bool {
	rel = pathpkg.Clean(filepath.ToSlash(rel))
	switch pathpkg.Base(rel) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	switch pathpkg.Ext(rel) {
	case ".go", ".s", ".S", ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".m", ".f", ".syso":
		return true
	}
	if pkgDirs == nil {
		return true
	}
	for d := pathpkg.Dir(rel); ; d = pathpkg.Dir(d) {
		if pkgDirs[d] {
			return true
		}
		if d == "." || d == "/" {
			return false
		}
	}
}

// SkewKind names how a binary and its bridged checkout disagree.
type SkewKind int

const (
	// SkewRevision means the binary was built from a different commit.
	SkewRevision SkewKind = iota + 1
	// SkewCheckoutEdits means the checkout has uncommitted changes the
	// binary was not built from (or edits newer than the binary).
	SkewCheckoutEdits
	// SkewBinaryEdits means the binary was built from uncommitted changes
	// the clean checkout does not have.
	SkewBinaryEdits
	// SkewUnverifiable means the binary records no forge commit and cannot
	// be shown to have been built from the checkout as it is now.
	SkewUnverifiable
)

// AssessSkew is the pure decision: do b and c hold the same forge source?
//
// With a recorded revision the comparison is exact, refined by time for a
// dirty tree: a dirty binary built from THIS checkout after its newest edit
// carries those edits. Without one — forge embedded in a host through a
// workspace records nothing — only a proof by time can clear it: the binary
// was compiled from this checkout, and the checkout has not moved or been
// edited since. Anything that proof cannot cover is reported, because a
// silent mismatch is the failure this exists to end.
func AssessSkew(b Binary, c Checkout) (SkewKind, bool) {
	sameTree := b.Root != "" && samePath(b.Root, c.Dir)
	notAfterBuild := func(t time.Time) bool {
		return !b.BuiltAt.IsZero() && !t.IsZero() && !t.After(b.BuiltAt)
	}
	if b.Revision != "" {
		if !sameRevision(b.Revision, c.Head) {
			return SkewRevision, true
		}
		if c.SourceEdits {
			if b.Modified && sameTree && notAfterBuild(c.LastSourceEditAt) {
				return 0, false
			}
			return SkewCheckoutEdits, true
		}
		// The binary carries uncommitted edits and the checkout has no
		// source edit: unless this tree is dirty with edits that cannot
		// reach a build (what stamped the binary modified), they are edits
		// the checkout lacks.
		if b.Modified && !(sameTree && c.Dirty) {
			return SkewBinaryEdits, true
		}
		return 0, false
	}
	if sameTree && notAfterBuild(c.HeadMovedAt) && (!c.SourceEdits || notAfterBuild(c.LastSourceEditAt)) {
		return 0, false
	}
	return SkewUnverifiable, true
}

func sameRevision(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

func samePath(a, b string) bool {
	norm := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			p = strings.ToLower(p)
		}
		return p
	}
	return norm(a) == norm(b)
}

// SkewLine renders the one-line warning: what disagrees, and the fix.
func SkewLine(kind SkewKind, b Binary, br Bridge, c Checkout) string {
	at := short(c.Head)
	if c.SourceEdits {
		at += " + uncommitted changes"
	}
	built := ""
	if !b.BuiltAt.IsZero() {
		built = " (built " + b.BuiltAt.Format("2006-01-02 15:04") + ")"
	}
	var what string
	switch kind {
	case SkewRevision:
		rev := short(b.Revision)
		if b.Modified {
			rev += " + uncommitted changes"
		}
		what = fmt.Sprintf("%s was built from forge %s, but %s at %s", b.Name, rev, br, at)
	case SkewCheckoutEdits:
		what = fmt.Sprintf("%s at %s, but %s%s was not built from those changes", br, at, b.Name, built)
	case SkewBinaryEdits:
		what = fmt.Sprintf("%s was built from uncommitted forge changes that %s at %s lacks", b.Name, br, at)
	default:
		if b.Root != "" && !samePath(b.Root, c.Dir) {
			what = fmt.Sprintf("%s%s records no forge commit and was compiled from %s, but %s at %s", b.Name, built, b.Root, br, at)
		} else {
			what = fmt.Sprintf("%s%s records no forge commit, and %s, which changed after it was built (now %s)", b.Name, built, br, at)
		}
	}
	return fmt.Sprintf("⚠️  forge skew: %s — generated code and library differ. Fix: rebuild %s from that checkout, or unbridge: %s",
		what, b.Name, br.Undo())
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// BridgeReport is everything the skew check learned about one project.
type BridgeReport struct {
	Bridged    bool
	Bridge     Bridge
	CheckoutOK bool // the bridged directory is a readable git checkout
	Checkout   Checkout
	Binary     Binary
	Skewed     bool
	Kind       SkewKind
}

// Line is the one-line warning, or "" when there is nothing to warn about.
func (r BridgeReport) Line() string {
	if !r.Skewed {
		return ""
	}
	return SkewLine(r.Kind, r.Binary, r.Bridge, r.Checkout)
}

// InspectBridge runs the whole check for projectDir. It never fails: an
// unbridged project, or a bridge that is not a git checkout, simply reports
// nothing to warn about.
func InspectBridge(projectDir string) BridgeReport {
	var r BridgeReport
	r.Bridge, r.Bridged = LocalBridge(projectDir)
	if !r.Bridged {
		return r
	}
	r.Checkout, r.CheckoutOK = ReadCheckout(r.Bridge.Dir)
	if !r.CheckoutOK {
		return r
	}
	r.Binary = CurrentBinary()
	if r.Binary.Revision == "" && r.Binary.Tag != "" {
		r.Binary.Revision = ResolveTag(r.Bridge.Dir, r.Binary.Tag)
	}
	r.Kind, r.Skewed = AssessSkew(r.Binary, r.Checkout)
	return r
}
