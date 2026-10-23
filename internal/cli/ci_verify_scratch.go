package cli

// ci_verify_scratch.go — the throwaway copy `forge ci verify-generated` and
// `forge generate --check` regenerate in.
//
// Both used to run `forge generate` over the caller's own checkout and then
// read `git status`. That rewrote the very tree they were judging: every
// other reader of the checkout (a concurrent gate's lint, build and test
// rows) had to wait for it, and a crash or kill mid-run left the tree dirty
// with half a regenerate in it. They now regenerate a SCRATCH copy and diff
// the copy against itself, so the caller's tree is never written.
//
// WHAT IS JUDGED. The working tree as it stands — every tracked file and
// every untracked file git does not ignore, byte for byte, which is exactly
// what `git add -A` would commit. On a clean checkout (CI) that is HEAD, the
// same tree the in-place check judged. A dirty tree used to be refused,
// because the in-place diff could not tell an earlier step's edit from a
// regenerate's; the scratch diff compares the copy against itself, so every
// change it reports was made by `forge generate`, and a dirty tree gets a
// real verdict instead of a refusal.
//
// WHERE. A hidden sibling of the repository's top level,
// `<parent>/.<repo>.forge-verify-<pid>-<random>`, so a path that leaves the
// repository (`replace … => ../lib`, a go.work `use ../forge/pkg`, a go.work
// in a parent directory) resolves to the same place it does from the
// checkout, and so the generate no-op cache's fingerprint — keyed by
// repository-relative path, or by absolute path for content outside the
// project — matches the caller's. Only when the parent is not writable does
// it fall back to the temp directory, where such paths may not resolve.
//
// HOW. The scratch is its own git repository (never a linked worktree, which
// would write .git/worktrees/ in the caller's clone), whose object store
// borrows the caller's through objects/info/alternates. Its index is the
// caller's index entry for entry, so tracked/untracked is what the caller's
// git says; its files are copied from the caller's working tree, not checked
// out from a commit, so the bytes are the caller's even under eol filters,
// skip-worktree or assume-unchanged. The gitignored files generate reads
// (generateCacheExtraFiles — dotenvs, secrets, .forge/ state) and the cache
// manifest are copied too; each package's node_modules is symlinked, never
// copied, and kept out of the scratch's listing by its info/exclude.
//
// SPEED. The regenerate shares the caller's generate cache: its manifest is
// copied in, so a tree the caller already verified as a fixed point skips
// every step; and a fixed point the scratch run verifies is copied back to
// the caller's .forge/generate-cache.json (gitignored state, never the tree)
// when the caller's manifest has not moved meanwhile, so the next check and
// the next in-place `forge generate` skip too. GOCACHE is the shared one.
//
// CLEANUP. The scratch is removed when the check returns, including on
// Ctrl-C (the context is cancelled, the child generate dies, the deferred
// removal runs). A SIGKILL leaves it behind; the next check removes any
// scratch whose creating process is gone.

import (
	"bytes"
	"context"
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
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// verifyScratch is one scratch copy of a checkout.
type verifyScratch struct {
	callerTop  string // the caller's repository top level (symlinks resolved)
	callerRoot string // the caller's project root (symlinks resolved)
	top        string // the scratch's repository top level
	root       string // the project root inside the scratch
	// cacheIn is the caller's generate-cache manifest as it was copied in
	// (nil when it had none): copy-back happens only while it is unchanged.
	cacheIn []byte
	// tracked is the caller's index, by top-relative path.
	tracked map[string]bool
	// before is the scratch's listing, by top-relative path, before the
	// regenerate.
	before map[string]string
}

// verifyScratchMarker names a scratch directory: `.<repo>` + marker +
// `<pid>-<random>`.
const verifyScratchMarker = ".forge-verify-"

// verifyScratchWorkers bounds the copy and hashing concurrency.
const verifyScratchWorkers = 8

// newVerifyScratch copies the checkout holding the project at root into a
// fresh scratch repository. The caller must Remove it.
func newVerifyScratch(ctx context.Context, root string, stderr io.Writer) (*verifyScratch, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git is not on PATH: the scratch copy is built from the checkout's git index")
	}
	callerRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	topOut, err := callerGit(ctx, callerRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git work tree (%w): verify-generated judges a checkout", root, err)
	}
	callerTop, err := filepath.EvalSymlinks(strings.TrimSpace(string(topOut)))
	if err != nil {
		return nil, err
	}
	prefix, err := filepath.Rel(callerTop, callerRoot)
	if err != nil || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("project %s is not inside its repository %s", callerRoot, callerTop)
	}
	common, err := callerGit(ctx, callerTop, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("locate the repository's object store: %w", err)
	}
	objects := filepath.Join(strings.TrimSpace(string(common)), "objects")
	format := "sha1"
	if out, ferr := callerGit(ctx, callerTop, "rev-parse", "--show-object-format"); ferr == nil && strings.TrimSpace(string(out)) != "" {
		format = strings.TrimSpace(string(out))
	}
	index, err := callerGit(ctx, callerTop, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, fmt.Errorf("read the checkout's index: %w", err)
	}
	listing, err := callerGit(ctx, callerTop, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list the checkout's files: %w", err)
	}

	s := &verifyScratch{callerTop: callerTop, callerRoot: callerRoot, tracked: map[string]bool{}}
	for _, entry := range strings.Split(string(index), "\x00") {
		if _, rel, ok := strings.Cut(entry, "\t"); ok {
			s.tracked[rel] = true
		}
	}
	if s.top, err = makeVerifyScratchDir(callerTop, stderr); err != nil {
		return nil, err
	}
	s.root = filepath.Join(s.top, prefix)
	if err := s.populate(ctx, objects, format, index, splitNul(listing)); err != nil {
		s.Remove()
		return nil, err
	}
	return s, nil
}

// populate makes the scratch directory a repository holding the caller's
// index and working tree.
func (s *verifyScratch) populate(ctx context.Context, objects, format string, index []byte, files []string) error {
	if _, err := scratchGit(ctx, s.top, nil, "init", "-q", "--template=", "--object-format="+format); err != nil {
		return fmt.Errorf("create the scratch repository: %w", err)
	}
	alternates := filepath.Join(s.top, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(alternates, []byte(objects+"\n"), 0o644); err != nil {
		return err
	}
	// A shallow clone (CI's fetch-depth 1) lacks its HEAD's parents; without
	// the same boundary every `git log` here — cmd/go's VCS stamp in the
	// validate build among them — fails reading a parent that is not there.
	if p, err := callerGit(ctx, s.callerTop, "rev-parse", "--path-format=absolute", "--git-path", "shallow"); err == nil {
		if body, rerr := os.ReadFile(strings.TrimSpace(string(p))); rerr == nil {
			if err := atomicfile.WriteFile(filepath.Join(s.top, ".git", "shallow"), body, 0o644); err != nil {
				return err
			}
		}
	}
	if head, err := callerGit(ctx, s.callerTop, "rev-parse", "-q", "--verify", "HEAD^{commit}"); err == nil {
		if _, err := scratchGit(ctx, s.top, nil, "update-ref", "HEAD", strings.TrimSpace(string(head))); err != nil {
			return fmt.Errorf("point the scratch HEAD at the checkout's: %w", err)
		}
	}
	if len(index) > 0 {
		if _, err := scratchGit(ctx, s.top, bytes.NewReader(index), "update-index", "-z", "--index-info"); err != nil {
			return fmt.Errorf("copy the checkout's index: %w", err)
		}
	}

	if err := copyTreeFiles(s.callerTop, s.top, files); err != nil {
		return err
	}
	// Links first: an ignored input inside an installed tree (npm's
	// node_modules/.package-lock.json) is then already there, through the
	// link, and is not copied into a real directory that blocks it.
	excludes, err := s.linkNodeModules(files)
	if err != nil {
		return err
	}
	if err := s.copyIgnoredInputs(files); err != nil {
		return err
	}
	if err := s.writeExcludes(ctx, excludes); err != nil {
		return err
	}
	s.before, err = s.listing(ctx)
	return err
}

// copyIgnoredInputs copies the gitignored files generate reads, its cache
// manifest, and the outputs that manifest names, from the caller's project.
// Each is the caller's file at the same project-relative path, so the
// regenerate reads what an in-place one would and fingerprints the same.
func (s *verifyScratch) copyIgnoredInputs(topFiles []string) error {
	prefix := s.prefix()
	snap := &workTreeSnapshot{files: map[string]fileStamp{}}
	for _, rel := range topFiles {
		if prefix == "" {
			snap.files[rel] = fileStamp{}
		} else if r, ok := strings.CutPrefix(rel, prefix+"/"); ok {
			snap.files[r] = fileStamp{}
		}
	}
	want := generateCacheExtraFiles(s.callerRoot, snap)
	want = append(want, generateCacheRel)
	if m := loadGenerateCacheManifest(s.callerRoot); m != nil {
		want = append(want, m.Outputs...)
		for _, dir := range m.OutputDirs {
			_ = filepath.WalkDir(filepath.Join(s.callerRoot, filepath.FromSlash(dir)), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if d.Name() == "node_modules" || d.Name() == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if rel, rerr := filepath.Rel(s.callerRoot, p); rerr == nil {
					want = append(want, filepath.ToSlash(rel))
				}
				return nil
			})
		}
	}
	// The sources overlap — an output the manifest names is often also under
	// one of its output dirs, or a gitignored input generate reads (a
	// frontend's public/config.js is both) — and the copies run in parallel
	// with O_EXCL, so a path listed twice fails the copy with "file exists".
	var copyList []string
	listed := make(map[string]bool, len(want))
	for _, rel := range want {
		if listed[rel] {
			continue
		}
		listed[rel] = true
		if path.Base(rel) == "forge.lock" {
			continue // holds a pid; the scratch has its own
		}
		if _, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(rel))); err == nil {
			continue // already copied from the listing
		}
		if info, err := os.Lstat(filepath.Join(s.callerRoot, filepath.FromSlash(rel))); err == nil && !info.IsDir() {
			copyList = append(copyList, rel)
		}
	}
	if err := copyTreeFiles(s.callerRoot, s.root, copyList); err != nil {
		return err
	}
	// A module the build compiles from inside the repository but git does
	// not list (a gitignored vendored copy a `replace` names): copy it
	// whole, or the regenerate's validate build cannot resolve it.
	for _, dir := range s.localModuleDirs(topFiles) {
		rel, err := filepath.Rel(s.callerTop, dir)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(s.top, rel)); err == nil {
			continue
		}
		if err := copyDirTree(dir, filepath.Join(s.top, rel)); err != nil {
			return fmt.Errorf("copy the module at %s into the scratch copy: %w", rel, err)
		}
	}
	s.cacheIn, _ = os.ReadFile(filepath.Join(s.callerRoot, generateCacheRel))
	return nil
}

// localModuleDirs lists the directories inside the repository that a go.mod
// in the listing, or the project's go.work, names as a local module (a
// directory `replace`, a go.work `use`).
func (s *verifyScratch) localModuleDirs(topFiles []string) []string {
	var dirs []string
	add := func(dir string) {
		if within(s.callerTop, dir) {
			dirs = append(dirs, dir)
		}
	}
	modReplaces := func(gomod string) error {
		data, err := os.ReadFile(gomod)
		if err != nil {
			return nil
		}
		f, err := modfile.Parse(gomod, data, nil)
		if err != nil {
			return nil
		}
		for _, r := range f.Replace {
			if modfile.IsDirectoryPath(r.New.Path) {
				add(resolveFrom(filepath.Dir(gomod), r.New.Path))
			}
		}
		return nil
	}
	for _, rel := range topFiles {
		if path.Base(rel) == "go.mod" {
			_ = modReplaces(filepath.Join(s.callerTop, filepath.FromSlash(rel)))
		}
	}
	_ = goWorkModules(filepath.Join(s.callerRoot, "go.work"), add, modReplaces)
	return dirs
}

// copyDirTree copies every file under src to dst, skipping VCS metadata and
// installed node_modules.
func copyDirTree(src, dst string) error {
	var rels []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != src && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	return copyTreeFiles(src, dst, rels)
}

// linkNodeModules symlinks every package's installed node_modules into the
// scratch (protoc-gen-es runs from there) and returns the info/exclude
// patterns that keep the links out of git's view of the scratch.
func (s *verifyScratch) linkNodeModules(topFiles []string) ([]string, error) {
	var excludes []string
	seen := map[string]bool{}
	for _, rel := range topFiles {
		if path.Base(rel) != "package.json" || strings.Contains(rel, "node_modules/") {
			continue
		}
		dir := path.Join(path.Dir(rel), "node_modules")
		if seen[dir] {
			continue
		}
		seen[dir] = true
		src := filepath.Join(s.callerTop, filepath.FromSlash(dir))
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			continue
		}
		dst := filepath.Join(s.top, filepath.FromSlash(dir))
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.Symlink(src, dst); err != nil {
			// No symlinks here (Windows without the privilege): copy the
			// installed tree instead. Slower, and the same to git and to
			// the fingerprint, which both see an ignored directory.
			if cerr := copyDirTree(src, dst); cerr != nil {
				return nil, fmt.Errorf("link %s into the scratch copy (%v), or copy it: %w", dir, err, cerr)
			}
		}
		excludes = append(excludes, "/"+dir)
	}
	return excludes, nil
}

// writeExcludes gives the scratch the caller's info/exclude (so it ignores
// what the caller ignores) plus the node_modules links and forge's per-run
// state, which no verdict is about.
func (s *verifyScratch) writeExcludes(ctx context.Context, extra []string) error {
	var b strings.Builder
	if p, err := callerGit(ctx, s.callerTop, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"); err == nil {
		if body, rerr := os.ReadFile(strings.TrimSpace(string(p))); rerr == nil {
			b.Write(body)
			if len(body) > 0 && body[len(body)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("# forge ci verify-generated scratch copy\n")
	for _, pattern := range extra {
		b.WriteString(pattern + "\n")
	}
	for _, rel := range []string{generateCacheRel, ".forge/forge.lock"} {
		b.WriteString("/" + path.Join(s.prefix(), rel) + "\n")
	}
	file := filepath.Join(s.top, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(file, []byte(b.String()), 0o644)
}

// prefix is the project's slash-separated path within the repository, "" at
// its top level.
func (s *verifyScratch) prefix() string {
	rel, _ := filepath.Rel(s.top, s.root)
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// listing identifies every file git shows in the scratch (tracked, plus
// untracked-not-ignored), by top-relative path.
func (s *verifyScratch) listing(ctx context.Context) (map[string]string, error) {
	out, err := scratchGit(ctx, s.top, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list the scratch copy: %w", err)
	}
	files := splitNul(out)
	ids := make([]string, len(files))
	errs := make([]error, len(files))
	parallelEach(len(files), func(i int) {
		ids[i], errs[i] = pathIdentity(filepath.Join(s.top, filepath.FromSlash(files[i])))
	})
	got := make(map[string]string, len(files))
	for i, rel := range files {
		if errs[i] != nil {
			return nil, errs[i]
		}
		got[rel] = ids[i]
	}
	return got, nil
}

// changes lists what the regenerate changed in the scratch, one
// `git status --porcelain` line per path (" M", "??", " D"), sorted, with
// paths relative to the repository's top level as porcelain prints them.
func (s *verifyScratch) changes(ctx context.Context) ([]string, error) {
	after, err := s.listing(ctx)
	if err != nil {
		return nil, err
	}
	var lines []string
	for rel, id := range after {
		before, existed := s.before[rel]
		switch {
		case !existed || before == "absent":
			if id == "absent" {
				continue
			}
			if s.tracked[rel] {
				lines = append(lines, " M "+rel)
			} else {
				lines = append(lines, "?? "+rel)
			}
		case id != before:
			if id == "absent" {
				lines = append(lines, " D "+rel)
			} else {
				lines = append(lines, " M "+rel)
			}
		}
	}
	for rel, id := range s.before {
		if _, still := after[rel]; !still && id != "absent" {
			lines = append(lines, " D "+rel)
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][3:] < lines[j][3:] })
	return lines, nil
}

// copyCacheBack hands a fixed point the scratch run recorded to the caller's
// generate cache. The manifest is keyed by content, so it can never make a
// later run skip over a tree it does not describe; it is written only while
// the caller's own manifest is still the one copied in, so a record a
// concurrent in-place run made is never replaced.
//
// The record names forge's top-level .forge/ state as it stood when the run
// ended — the generating-build record a run writes among it. That state is
// copied back first, where it differs, or the next scratch copy would carry
// the caller's older state and miss the record it just made. Only .forge/,
// forge's own machine-local state: no verdict found any other file different,
// and the tree is not this check's to write.
func (s *verifyScratch) copyCacheBack() {
	recorded, err := os.ReadFile(filepath.Join(s.root, generateCacheRel))
	if err != nil || bytes.Equal(recorded, s.cacheIn) {
		return
	}
	dst := filepath.Join(s.callerRoot, generateCacheRel)
	if _, err := os.Stat(filepath.Dir(dst)); err != nil {
		return // no .forge/ in the checkout: not ours to create
	}
	var m generateCacheManifest
	if json.Unmarshal(recorded, &m) != nil {
		return
	}
	for key, want := range m.Files {
		name, ok := strings.CutPrefix(key, ".forge/")
		if !ok || strings.Contains(name, "/") || want == "absent" || want == "dir" {
			continue
		}
		src := filepath.Join(s.root, ".forge", name)
		if id, err := pathIdentity(src); err != nil || id != want {
			continue
		}
		target := filepath.Join(s.callerRoot, ".forge", name)
		if id, err := pathIdentity(target); err == nil && id == want {
			continue
		}
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		body, err := os.ReadFile(src)
		if err != nil {
			return
		}
		if !bytes.Equal(readOrNil(dst), s.cacheIn) {
			return // a concurrent run moved the cache: its state is newer
		}
		if atomicfile.WriteFile(target, body, info.Mode().Perm()) != nil {
			return
		}
	}
	if !bytes.Equal(readOrNil(dst), s.cacheIn) {
		return
	}
	_ = atomicfile.WriteFile(dst, recorded, 0o644)
}

// readOrNil is a file's content, nil when it cannot be read.
func readOrNil(path string) []byte {
	body, _ := os.ReadFile(path)
	return body
}

// Remove deletes the scratch copy. The node_modules links are removed, not
// followed.
func (s *verifyScratch) Remove() {
	if s == nil || s.top == "" || !strings.Contains(filepath.Base(s.top), verifyScratchMarker) {
		return
	}
	_ = os.RemoveAll(s.top)
}

// makeVerifyScratchDir creates the scratch directory beside the repository
// top level, after removing scratch copies whose creating process is gone.
func makeVerifyScratchDir(callerTop string, stderr io.Writer) (string, error) {
	parent := filepath.Dir(callerTop)
	stem := "." + filepath.Base(callerTop) + verifyScratchMarker
	pruneVerifyScratches(parent, stem)
	dir, err := os.MkdirTemp(parent, stem+strconv.Itoa(os.Getpid())+"-")
	if err == nil {
		return dir, nil
	}
	dir, terr := os.MkdirTemp("", "forge"+verifyScratchMarker+strconv.Itoa(os.Getpid())+"-")
	if terr != nil {
		return "", fmt.Errorf("create a scratch copy beside %s (%v) or in the temp directory: %w", callerTop, err, terr)
	}
	fmt.Fprintf(stderr, "note: %s is not writable, so the scratch copy is in %s; a path that leaves the repository (`../…`) may not resolve there\n", parent, dir)
	return filepath.EvalSymlinks(dir)
}

// pruneVerifyScratches removes the scratch copies in dir whose creating
// process no longer runs: a check that was SIGKILLed never removed its own.
func pruneVerifyScratches(dir, stem string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), stem)
		if !ok || !e.IsDir() {
			continue
		}
		pidText, _, _ := strings.Cut(rest, "-")
		if pid, err := strconv.Atoi(pidText); err == nil && pid != os.Getpid() && !processAlive(pid) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// copyTreeFiles copies each slash-separated path from srcRoot to dstRoot:
// a regular file with its mode, a symlink as a symlink, and a submodule
// (a directory in a git listing) as the files its own listing names.
func copyTreeFiles(srcRoot, dstRoot string, rels []string) error {
	errs := make([]error, len(rels))
	parallelEach(len(rels), func(i int) {
		errs[i] = copyTreeEntry(srcRoot, dstRoot, rels[i])
	})
	return errors.Join(errs...)
}

func copyTreeEntry(srcRoot, dstRoot, rel string) error {
	src := filepath.Join(srcRoot, filepath.FromSlash(rel))
	dst := filepath.Join(dstRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // tracked but deleted in the working tree
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		// A submodule: its own working tree, minus its .git.
		out, err := callerGit(context.Background(), src, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if err != nil {
			return nil // not checked out: git shows nothing there either
		}
		return copyTreeFiles(src, dst, splitNul(out))
	case info.Mode().IsRegular():
		return copyRegularFile(src, dst, info.Mode().Perm())
	}
	return nil
}

func copyRegularFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// O_CREATE's mode is filtered by the umask; git records the exec bit.
	return os.Chmod(dst, perm)
}

// parallelEach runs fn(0..n-1) over verifyScratchWorkers goroutines.
func parallelEach(n int, fn func(int)) {
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < verifyScratchWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}

func splitNul(out []byte) []string {
	var parts []string
	seen := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && !seen[p] {
			seen[p] = true
			parts = append(parts, p)
		}
	}
	return parts
}

// callerGit runs a read-only git command in the caller's checkout. Optional
// locks are off, so not even git's opportunistic index refresh writes there.
func callerGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// scratchGit runs git in the scratch repository, with every variable that
// could point git at another repository removed — a check run from a git
// hook inherits GIT_DIR and GIT_INDEX_FILE naming the caller's.
func scratchGit(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = scratchEnv(os.Environ())
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// repoLocatingGitEnv are the variables that redirect git away from the
// repository its working directory is in.
var repoLocatingGitEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_NAMESPACE": true, "GIT_PREFIX": true,
}

// scratchEnv is environ without repoLocatingGitEnv: the environment for git
// and for the regenerate in the scratch copy.
func scratchEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !repoLocatingGitEnv[name] {
			out = append(out, kv)
		}
	}
	return out
}
