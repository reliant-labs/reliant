package buildtarget

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// fingerprintScheme names the layout of the bytes FingerprintInputs hashes.
// Bump it whenever that layout changes, so a fingerprint recorded by an older
// forge can never equal one computed by a newer forge over the same inputs
// and authorize a skip under different rules.
const fingerprintScheme = "forge-shellbuild-inputs/v1"

// prunedDirs are directory names a walk never descends into unless a
// pattern names them literally. `.git` is git's object store and index,
// never a build input; node_modules is an install tree whose lockfile is
// the input. Both are large enough that hashing them would dominate.
var prunedDirs = map[string]bool{".git": true, "node_modules": true}

// Fingerprint is the content address of a ShellBuild's declared inputs.
type Fingerprint struct {
	// Digest is "sha256:<hex>" over the scheme, the caller's recipe bytes
	// and, for every selected file in path order, its path, size,
	// executable bit and content hash.
	Digest string
	// Files and Bytes describe what was hashed, for the build log.
	Files int
	Bytes int64
	// files maps each selected path (slash-separated, relative to cwd) to
	// its content entry, so Changed can name what moved.
	files map[string]string
}

// Changed lists the paths whose entry differs between f and other (added,
// removed or modified), sorted. It names what a build touched in its own
// declared inputs.
func (f Fingerprint) Changed(other Fingerprint) []string {
	var out []string
	for p, e := range f.files {
		if other.files[p] != e {
			out = append(out, p)
		}
	}
	for p := range other.files {
		if _, ok := f.files[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// inputPattern is one parsed `inputs` entry.
type inputPattern struct {
	raw    string
	negate bool
	// clean is the slash-separated pattern relative to cwd, cleaned, so a
	// leading run of ".." segments is the only place ".." can appear.
	clean string
	segs  []string
	// root is the longest leading run of segments without glob metacharacters:
	// the directory (or file) a walk for this pattern starts from.
	root string
	// literal is true when the whole pattern has no metacharacter. A literal
	// naming a directory selects every file beneath it.
	literal bool
}

func hasMeta(s string) bool { return strings.ContainsAny(s, `*?[\`) }

func parseInputPattern(raw string) (inputPattern, error) {
	p := inputPattern{raw: raw}
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "!") {
		p.negate = true
		s = s[1:]
	}
	if s == "" {
		return p, fmt.Errorf("inputs entry %q is empty", raw)
	}
	s = filepath.ToSlash(s)
	if path.IsAbs(s) || filepath.IsAbs(s) {
		return p, fmt.Errorf("inputs entry %q is absolute; inputs are relative to the ShellBuild's cwd", raw)
	}
	p.clean = path.Clean(s)
	if p.clean == "." {
		p.clean = "**"
	}
	p.segs = strings.Split(p.clean, "/")
	for _, seg := range p.segs {
		if strings.Contains(seg, "**") && seg != "**" {
			return p, fmt.Errorf("inputs entry %q: `**` must be a whole path segment (\"src/**\", not \"src**\")", raw)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return p, fmt.Errorf("inputs entry %q: %w", raw, err)
		}
	}
	var root []string
	for _, seg := range p.segs {
		if hasMeta(seg) {
			break
		}
		root = append(root, seg)
	}
	p.literal = len(root) == len(p.segs)
	p.root = strings.Join(root, "/")
	if p.root == "" {
		p.root = "."
	}
	return p, nil
}

// matches reports whether rel (slash-separated, relative to cwd, cleaned)
// is selected by the pattern.
func (p inputPattern) matches(rel string) bool {
	if p.literal {
		return rel == p.clean || strings.HasPrefix(rel, p.clean+"/")
	}
	return matchSegments(p.segs, strings.Split(rel, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Collapse consecutive `**`, then try every split point.
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// namesSegment reports whether the pattern names seg literally, which is
// what lets a pattern reach into a pruned directory on purpose.
func (p inputPattern) namesSegment(seg string) bool {
	for _, s := range p.segs {
		if s == seg {
			return true
		}
	}
	return false
}

// InputScope is where a ShellBuild's inputs may come from.
type InputScope struct {
	// Cwd is the directory patterns are relative to: the build's resolved
	// working directory.
	Cwd string
	// Roots are the directories a pattern may reach outside Cwd into — for
	// a project build, the directory holding the project and its sibling
	// checkouts, the same reach a ShellBuild's cwd already has. Cwd itself
	// is always allowed. A pattern whose walk starts outside every root, or
	// a selected file whose resolved path (symlinks followed) lies outside
	// every root, is refused.
	Roots []string
}

// ExpandInputs resolves patterns against scope.Cwd and returns the selected
// files, each as a slash-separated path relative to it, sorted. Every
// symlink the selection passes through (a directory link walked into, or one
// on the way to a pattern's start) is listed too, so it is keyed on its own
// text and resolved target.
//
// Patterns are slash-separated and relative to cwd; a leading `../` reaches a
// sibling checkout inside one of scope.Roots. `*`, `?` and `[...]` match
// within one path segment and a whole `**` segment matches any number of
// segments. A pattern without metacharacters naming a directory selects every
// file beneath it. A pattern starting with `!` excludes what it matches from
// every other pattern's selection, whatever their order. Walks never descend
// into `.git` or `node_modules` unless a pattern names that directory
// literally, and never follow a symlink out of the allowed roots.
//
// An including pattern that selects no file is an error: a typo would
// otherwise silently drop an input, and the build would be skipped when the
// file it meant changed.
func ExpandInputs(scope InputScope, patterns []string) ([]string, error) {
	b, err := newBoundary(scope)
	if err != nil {
		return nil, err
	}
	return expandInputs(b, patterns)
}

func expandInputs(b boundary, patterns []string) ([]string, error) {
	var includes, excludes []inputPattern
	for _, raw := range patterns {
		p, err := parseInputPattern(raw)
		if err != nil {
			return nil, err
		}
		if p.negate {
			excludes = append(excludes, p)
		} else {
			includes = append(includes, p)
		}
	}
	if len(includes) == 0 {
		return nil, errors.New("inputs declares only exclusions (`!…`); name at least one pattern that selects files")
	}
	selected := map[string]bool{}
	for _, p := range includes {
		n, err := walkPattern(b, p, excludes, selected)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("inputs entry %q matches no file under %s", p.raw, b.cwd)
		}
	}
	out := make([]string, 0, len(selected))
	for rel := range selected {
		excluded := false
		for _, x := range excludes {
			if x.matches(rel) {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

// boundary is a resolved InputScope: every directory both as written
// (absolute, cleaned) and as resolved (symlinks followed), so a lexical path
// and a resolved one can each be checked against the form it compares to.
type boundary struct {
	cwd      string
	lexical  []string
	resolved []string
	// anchors are the resolved roots with a stable label ("cwd", "root1",
	// …, by position in InputScope), for keying a resolved path relative
	// to the root it lies in.
	anchors []anchor
}

type anchor struct{ label, path string }

func newBoundary(scope InputScope) (boundary, error) {
	cwd, err := filepath.Abs(scope.Cwd)
	if err != nil {
		return boundary{}, err
	}
	b := boundary{cwd: cwd}
	for i, r := range append([]string{cwd}, scope.Roots...) {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return boundary{}, err
		}
		b.lexical = append(b.lexical, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			b.resolved = append(b.resolved, real)
			label := "cwd"
			if i > 0 {
				label = fmt.Sprintf("root%d", i)
			}
			b.anchors = append(b.anchors, anchor{label, real})
		}
	}
	return b, nil
}

// within reports whether p is root or beneath it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func withinAny(roots []string, p string) bool {
	for _, r := range roots {
		if within(r, p) {
			return true
		}
	}
	return false
}

// keyResolved expresses a resolved path relative to the most specific
// allowed root it lies in ("cwd:sub/a.go", "root1:reliant/go.mod"), so a
// fingerprint keyed on it does not change when the whole workspace is checked
// out somewhere else. A path outside every root (a dangling chain's last hop,
// or a link to a non-regular file, neither of which is ever read) has no root
// to be relative to and is keyed absolutely: that ties only this entry to the
// checkout path, which can cost a rebuild but never a stale skip.
func (b boundary) keyResolved(real string) string {
	best := anchor{}
	for _, a := range b.anchors {
		if within(a.path, real) && len(a.path) > len(best.path) {
			best = a
		}
	}
	if best.path == "" {
		return "abs:" + filepath.ToSlash(real)
	}
	rel, err := filepath.Rel(best.path, real)
	if err != nil {
		return "abs:" + filepath.ToSlash(real)
	}
	return best.label + ":" + filepath.ToSlash(rel)
}

// describe names the allowed roots for a refusal.
func (b boundary) describe() string { return strings.Join(b.lexical, ", ") }

// checkResolved refuses a path whose resolved location (every symlink
// followed) lies outside the allowed roots: a link inside the tree must not
// make forge read, and key a skip on, a file outside it.
func (b boundary) checkResolved(raw, abs string) error {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if !withinAny(b.resolved, real) {
		return fmt.Errorf("inputs entry %q selects %s, which resolves to %s, outside the allowed roots (%s); exclude it with `!` or keep inputs inside them", raw, abs, real, b.describe())
	}
	return nil
}

// walkPattern adds every file p selects to selected and returns how many it
// matched (including ones another pattern already selected).
func walkPattern(b boundary, p inputPattern, excludes []inputPattern, selected map[string]bool) (int, error) {
	start := filepath.Join(b.cwd, filepath.FromSlash(p.root))
	// A root may be named through a symlink (macOS's /var is /private/var)
	// while cwd arrives resolved, or the reverse: either spelling counts.
	if !withinAny(b.lexical, start) && !withinAny(b.resolved, start) {
		return 0, fmt.Errorf("inputs entry %q reaches %s, outside the allowed roots (%s)", p.raw, start, b.describe())
	}
	info, err := os.Stat(start)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := b.checkResolved(p.raw, start); err != nil {
		return 0, err
	}
	// A symlink on the way to the walk's start (`vendored/**` where vendored
	// is a link) is an input like one met inside the walk: retargeting it,
	// or a link further down its chain, must change the fingerprint.
	if err := selectRootLinks(b.cwd, p.root, selected); err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if p.matches(p.root) {
			selected[p.root] = true
			return 1, nil
		}
		return 0, nil
	}
	// Walk the resolved directory so a root that is itself a symlink (a
	// sibling checkout linked into place) is descended, while paths are still
	// reported under the name the pattern used.
	real, err := filepath.EvalSymlinks(start)
	if err != nil {
		return 0, err
	}
	w := &inputWalker{b: b, p: p, excludes: excludes, selected: selected, visited: map[string]bool{}}
	err = w.dir(real, p.root)
	return w.n, err
}

// selectRootLinks selects every symlink among the path components of root
// (relative to cwd), so hashFile keys each on its text and resolved target.
func selectRootLinks(cwd, root string, selected map[string]bool) error {
	if root == "." {
		return nil
	}
	segs := strings.Split(root, "/")
	for i := range segs {
		if segs[i] == ".." {
			continue
		}
		rel := strings.Join(segs[:i+1], "/")
		li, err := os.Lstat(filepath.Join(cwd, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if li.Mode()&fs.ModeSymlink != 0 {
			selected[rel] = true
		}
	}
	return nil
}

// inputWalker walks one including pattern's tree.
//
// Unlike filepath.WalkDir it DESCENDS a symlinked directory, under the link's
// lexical path: a link inside the tree is how a monorepo or a vendored
// checkout often shares sources, and treating it as an opaque link text left
// every edit beneath it invisible to the fingerprint — a stale skip. A
// directory symlink is followed only when it resolves inside the allowed
// roots; one that leads outside them, where a file the pattern could select
// lies, refuses the fingerprint (the build runs and says to exclude it), the
// same fail-safe rule a symlinked file follows.
//
// Every resolved directory is walked at most once per pattern, so a symlink
// cycle terminates and fan-in through several links costs one walk. A tree
// reached by a second lexical path is therefore keyed under the first (in
// sorted walk order, so deterministically); its content is hashed either way.
type inputWalker struct {
	b        boundary
	p        inputPattern
	excludes []inputPattern
	selected map[string]bool
	visited  map[string]bool
	n        int
}

func (w *inputWalker) dir(real, rel string) error {
	if w.visited[real] {
		return nil
	}
	w.visited[real] = true
	entries, err := os.ReadDir(real)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		childReal := filepath.Join(real, name)
		childRel := path.Clean(path.Join(rel, name))
		if e.Type()&fs.ModeSymlink != 0 {
			walked, err := w.symlinkedDir(name, childReal, childRel)
			if err != nil {
				return err
			}
			if walked {
				continue
			}
			// A link to a file (or a dangling link) is a file entry below.
		} else if e.IsDir() {
			if w.descends(name, childRel) {
				if err := w.dir(childReal, childRel); err != nil {
					return err
				}
			}
			continue
		}
		if !w.p.matches(childRel) {
			continue
		}
		// A symlinked FILE is read through its link: it must resolve inside
		// the roots. A dangling link is hashed as its link text and never
		// read, so it needs no check.
		if e.Type()&fs.ModeSymlink != 0 {
			if st, serr := os.Stat(childReal); serr == nil && st.Mode().IsRegular() {
				if err := w.b.checkResolved(w.p.raw, childReal); err != nil {
					return err
				}
			}
		}
		w.selected[childRel] = true
		w.n++
	}
	return nil
}

// symlinkedDir handles a symlink entry that resolves to a directory: it is
// descended under its lexical path when the pattern reaches into it, after
// its target is checked to lie inside the allowed roots. done is false for a
// link to a file, or a dangling one, which dir treats as a file entry.
func (w *inputWalker) symlinkedDir(name, childReal, childRel string) (done bool, err error) {
	st, err := os.Stat(childReal)
	if err != nil || !st.IsDir() {
		return false, nil
	}
	if !w.descends(name, childRel) {
		return true, nil
	}
	target, err := filepath.EvalSymlinks(childReal)
	if err != nil {
		return true, err
	}
	if !withinAny(w.b.resolved, target) {
		return true, fmt.Errorf("inputs entry %q reaches %s through the directory symlink %s, outside the allowed roots (%s); exclude it with `!%s/**` or keep inputs inside them",
			w.p.raw, target, childRel, w.b.describe(), childRel)
	}
	// The link's own text is an input too: retargeting it between two equal
	// trees is still a change.
	w.selected[childRel] = true
	return true, w.dir(target, childRel)
}

// descends reports whether the walk enters directory rel: not a pruned
// directory the pattern does not name, not a tree an exclusion removes
// whole, and a tree in which the pattern could still select a file.
func (w *inputWalker) descends(name, rel string) bool {
	if prunedDirs[name] && !w.p.namesSegment(name) {
		return false
	}
	for _, x := range w.excludes {
		if x.coversDir(rel) {
			return false
		}
	}
	return w.p.mayMatchBelow(rel)
}

// mayMatchBelow reports whether some file beneath directory rel could match
// the pattern. A literal pattern selects everything beneath it, so it reads
// as if it ended in `**`.
func (p inputPattern) mayMatchBelow(rel string) bool {
	segs := p.segs
	if p.literal {
		segs = append(append([]string(nil), segs...), "**")
	}
	return matchPrefix(segs, strings.Split(rel, "/"))
}

// matchPrefix reports whether pat could match some path that extends dir by
// at least one segment.
func matchPrefix(pat, dir []string) bool {
	for len(dir) > 0 {
		if len(pat) == 0 {
			return false
		}
		if pat[0] == "**" {
			return true
		}
		if ok, _ := path.Match(pat[0], dir[0]); !ok {
			return false
		}
		pat, dir = pat[1:], dir[1:]
	}
	return len(pat) > 0
}

// coversDir reports whether the exclusion removes directory rel and
// everything beneath it, so the walk need not enter it. Conservative: an
// exclusion it cannot prove covers the whole tree returns false, and the
// walk enters (matching files are still dropped afterwards).
func (p inputPattern) coversDir(rel string) bool {
	if p.literal {
		return p.matches(rel)
	}
	n := len(p.segs)
	return n > 0 && p.segs[n-1] == "**" && matchSegments(p.segs[:n-1], strings.Split(rel, "/"))
}

// FingerprintInputs hashes recipe together with every file patterns select
// in scope (see ExpandInputs). recipe carries everything else the command's
// output depends on — the command itself, its environment, platform and
// destination — so two builds with equal fingerprints ran the same command
// over the same bytes.
//
// Contents are hashed, not mtimes: a fresh checkout or a `git switch` there
// and back rewrites mtimes without changing a byte, and a same-second edit
// can leave an mtime unchanged.
func FingerprintInputs(scope InputScope, patterns []string, recipe []byte) (Fingerprint, error) {
	b, err := newBoundary(scope)
	if err != nil {
		return Fingerprint{}, err
	}
	files, err := expandInputs(b, patterns)
	if err != nil {
		return Fingerprint{}, err
	}
	entries, total, err := hashFiles(b, files)
	if err != nil {
		return Fingerprint{}, err
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%d\n", fingerprintScheme, len(recipe))
	_, _ = h.Write(recipe)
	sorted := append([]string(nil), patterns...)
	sort.Strings(sorted)
	for _, p := range sorted {
		_, _ = fmt.Fprintf(h, "\npattern\x00%s", p)
	}
	fp := Fingerprint{Files: len(files), Bytes: total, files: make(map[string]string, len(files))}
	for i, rel := range files {
		_, _ = fmt.Fprintf(h, "\nfile\x00%s\x00%s", rel, entries[i])
		fp.files[rel] = entries[i]
	}
	fp.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return fp, nil
}

// hashFiles returns one entry per file — "[link <sha256 of link text> ]<size>
// <x|-> <sha256>" — in the order given, hashing in parallel.
func hashFiles(b boundary, files []string) ([]string, int64, error) {
	entries := make([]string, len(files))
	sizes := make([]int64, len(files))
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if workers > len(files) {
		workers = len(files)
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				entry, size, err := hashFile(b, filepath.Join(b.cwd, filepath.FromSlash(files[i])))
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("hash input %s: %w", files[i], err)
					}
					mu.Unlock()
					continue
				}
				entries[i], sizes[i] = entry, size
			}
		}()
	}
	for i := range files {
		next <- i
	}
	close(next)
	wg.Wait()
	if firstErr != nil {
		return nil, 0, firstErr
	}
	var total int64
	for _, s := range sizes {
		total += s
	}
	return entries, total, nil
}

func hashFile(b boundary, abs string) (string, int64, error) {
	li, err := os.Lstat(abs)
	if err != nil {
		return "", 0, err
	}
	// A symlink is keyed on its own link text AND its fully resolved target
	// (relative to the root it resolves inside), so retargeting it — or any
	// link further down its chain (`current -> mid -> a`, with mid moved to
	// an identical `b`) — is a change even between targets with equal
	// content: a command can read the link, or resolve paths relative to
	// where it lands. A link to a regular file is that plus the target's
	// content; anything else (a link to a directory, whose files the walk
	// keys under the link's path, or a dangling link) is the link alone.
	link := ""
	if li.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return "", 0, err
		}
		resolved := "dangling"
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			resolved = b.keyResolved(real)
		}
		sum := sha256.Sum256([]byte(target + "\x00" + resolved))
		link = "link " + hex.EncodeToString(sum[:])
		if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
			return link, 0, nil
		}
		link += " "
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	exec := "-"
	if st.Mode()&0o111 != 0 {
		exec = "x"
	}
	return fmt.Sprintf("%s%d %s %s", link, n, exec, hex.EncodeToString(h.Sum(nil))), n, nil
}
