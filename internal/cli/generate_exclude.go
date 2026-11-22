package cli

// generate_exclude.go — scaffold.exclude at the level of whole commands.
//
// The write chokepoints (internal/checksums/exclude.go) decline an excluded
// path before writing it and record the skip. Two things are left for this
// file:
//
//  1. Outputs no chokepoint writes: buf's stubs and the frontend hooks. They
//     are directories, decided here per frontend, and a glob over a generated
//     tree other generated code imports is refused rather than skipped.
//  2. The guarantee. A forge run writes through hundreds of sites; most go
//     through a chokepoint, and some — a raw write, an external tool — do
//     not. So every run that holds the generate lock (generate, every
//     scaffold, rescaffold, project new) is bracketed: the files under an
//     excluded glob are captured when the lock is taken, and anything the run
//     changed there is put back before the lock is released and reported as
//     a skipped write. "forge never writes there" is then a property of the
//     run, not of each writer remembering to ask.
//
// The guard cannot tell forge's writes from anyone else's: a person, an
// editor or another agent may edit an excluded file while a run holds the
// lock, and the lock does not stop them. So it never deletes or overwrites
// anything. A created or changed file is MOVED (one rename) under
// .forge/exclude-guard/<run>/ (gitignored), and the run names each one; a
// changed or deleted file's pre-run bytes are then written back only if the
// path is still free (O_EXCL). A path someone writes again in between is
// left exactly as they wrote it, and reported as not put back.

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/pathglob"
)

// skipExcludedDir reports whether forge must not write the generated
// directory relDir, judged by probe, a file name forge writes there. It
// records the skip.
func skipExcludedDir(root, relDir string) bool {
	probe := filepath.ToSlash(filepath.Join(relDir, "x-hooks.ts"))
	glob, ok := checksums.ExcludedBy(root, probe)
	if ok {
		checksums.NoteExcluded(filepath.ToSlash(relDir)+"/", glob)
	}
	return ok
}

// refuseExcludedGoStubs fails the run when scaffold.exclude covers gen/: the
// protobuf and Connect stubs every handler and the generated composition
// import, regenerated from proto on every run. There is no half of that
// pairing a project can own while forge keeps the other.
func refuseExcludedGoStubs(root string) error {
	probes := []string{"gen/forge_descriptor.json", "gen/services/x/v1/x.pb.go", "gen/services/x/v1/xv1connect/x.connect.go"}
	_ = filepath.WalkDir(filepath.Join(root, "gen"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && len(probes) < 64 {
			if rel, rerr := filepath.Rel(root, p); rerr == nil {
				probes = append(probes, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	for _, p := range probes {
		if glob, ok := checksums.ExcludedBy(root, p); ok {
			return &checksums.ExcludeConflictError{Path: "gen/", Glob: glob,
				Dependent: "every handler under internal/handlers/, internal/app's generated mounts and each frontend's hooks",
				Instead:   "To stop forge generating code at all, pin `features: {codegen: false}`"}
		}
	}
	return nil
}

// excludedTSStubs decides a frontend's TypeScript stubs (<feDir>/src/gen)
// under scaffold.exclude: skip when they are excluded together with the hooks
// that import them, refuse when only the stubs are.
func excludedTSStubs(root, feDir string) (skip bool, err error) {
	genProbe := filepath.ToSlash(filepath.Join(feDir, "src", "gen", "x_pb.ts"))
	glob, ok := checksums.ExcludedBy(root, genProbe)
	if !ok {
		return false, nil
	}
	hooksProbe := filepath.ToSlash(filepath.Join(feDir, "src", "hooks", "x-hooks.ts"))
	if _, hooksExcluded := checksums.ExcludedBy(root, hooksProbe); !hooksExcluded {
		return true, &checksums.ExcludeConflictError{Path: filepath.ToSlash(filepath.Join(feDir, "src", "gen")) + "/", Glob: glob,
			Dependent: "the generated hooks in " + filepath.ToSlash(filepath.Join(feDir, "src", "hooks")) + "/",
			Instead:   "Exclude the hooks with it (`" + filepath.ToSlash(feDir) + "/src/hooks/`), or the whole frontend (`" + filepath.ToSlash(feDir) + "/`)"}
	}
	checksums.NoteExcluded(filepath.ToSlash(filepath.Join(feDir, "src", "gen"))+"/", glob)
	return true, nil
}

// excludeCaptureLimit bounds the bytes one excluded file may hold in memory
// for the run. A larger file is fingerprinted only; a run that changes one is
// reported, since it cannot be put back.
const excludeCaptureLimit = 16 << 20

// The guard never descends a directory config.ExcludeUnguardedDir names: no
// forge writer targets one, and walking node_modules under a
// `frontends/web/**` glob would dominate the run. Arm and settle share this
// one walk, so a pruned directory is neither captured nor undone; an exclude
// entry that names a path inside one is a load error (validateScaffoldExclude).

type excludedFile struct {
	data []byte // nil when over excludeCaptureLimit
	hash string
	mode fs.FileMode
	glob string
}

// excludeGuard is one locked run's capture of the files under
// scaffold.exclude (see the file comment).
type excludeGuard struct {
	root   string
	globs  []string
	exempt []string // generate.hooks writes: the project's own commands
	pre    map[string]excludedFile
	mu     sync.Mutex
	// run names this run's backup directory under .forge/exclude-guard.
	run string
	// kept maps each excluded path the run found changed or created to where
	// the bytes it found there were kept.
	kept map[string]string
}

var (
	activeExcludeGuardMu sync.Mutex
	activeExcludeGuard   *excludeGuard
)

// armExcludeGuard captures the excluded files of the project at root. It
// returns nil — a no-op guard — for a project with no scaffold.exclude.
func armExcludeGuard(root string) *excludeGuard {
	globs := checksums.ExcludeGlobs(root)
	if len(globs) == 0 {
		return nil
	}
	g := &excludeGuard{root: root, globs: globs, exempt: config.ReadGenerateHookWrites(root),
		pre: map[string]excludedFile{}, kept: map[string]string{},
		run: fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102T150405Z"), os.Getpid())}
	g.walk(func(rel, glob string, full string, fi fs.FileInfo) {
		f := excludedFile{mode: fi.Mode().Perm(), glob: glob}
		data, err := os.ReadFile(full)
		if err != nil {
			return
		}
		f.hash = checksums.Hash(data)
		if len(data) <= excludeCaptureLimit {
			f.data = data
		}
		g.pre[rel] = f
	})
	activeExcludeGuardMu.Lock()
	activeExcludeGuard = g
	activeExcludeGuardMu.Unlock()
	return g
}

// walk visits every regular file under root an exclude glob matches and no
// generate hook declares as its output.
func (g *excludeGuard) walk(visit func(rel, glob, full string, fi fs.FileInfo)) {
	seen := map[string]bool{}
	for _, glob := range g.globs {
		start := filepath.Join(g.root, filepath.FromSlash(pathglob.StaticPrefix(glob)))
		_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != start && config.ExcludeUnguardedDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			rel, rerr := filepath.Rel(g.root, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] || !d.Type().IsRegular() || g.isExempt(rel) {
				return nil
			}
			if matched, ok := g.matches(rel); ok {
				seen[rel] = true
				if fi, ierr := d.Info(); ierr == nil {
					visit(rel, matched, p, fi)
				}
			}
			return nil
		})
	}
}

func (g *excludeGuard) matches(rel string) (string, bool) {
	for _, glob := range g.globs {
		if pathglob.Match(glob, rel) {
			return glob, true
		}
	}
	return "", false
}

func (g *excludeGuard) isExempt(rel string) bool {
	for _, w := range g.exempt {
		w = strings.TrimSuffix(filepath.ToSlash(w), "/")
		if rel == w || strings.HasPrefix(rel, w+"/") {
			return true
		}
	}
	return false
}

// settle puts back every excluded file the run changed, created or deleted,
// records each as a skipped write, and returns the paths it could not restore.
// Safe to call more than once: a settled tree matches the capture.
func (g *excludeGuard) settle() (unrestored []string) {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := map[string]bool{}
	g.walk(func(rel, glob, full string, _ fs.FileInfo) {
		now[rel] = true
		before, had := g.pre[rel]
		if !had {
			// Created under an excluded glob while the run held the lock:
			// forge's write, or someone else's. Moved aside, never deleted.
			if !g.keep(rel, full) {
				unrestored = append(unrestored, rel)
				return
			}
			pruneEmptyDirsUpTo(filepath.Dir(full), g.root)
			checksums.NoteExcluded(rel, glob)
			return
		}
		data, err := os.ReadFile(full)
		if err != nil || checksums.Hash(data) == before.hash {
			return
		}
		if before.data == nil || !g.keep(rel, full) || !g.restore(rel, full, before) {
			unrestored = append(unrestored, rel)
			return
		}
		checksums.NoteExcluded(rel, before.glob)
	})
	for rel, before := range g.pre {
		if now[rel] {
			continue
		}
		// Deleted by the run (a stale-output sweep): put it back.
		full := filepath.Join(g.root, filepath.FromSlash(rel))
		if !g.restore(rel, full, before) {
			unrestored = append(unrestored, rel)
			continue
		}
		checksums.NoteExcluded(rel, before.glob)
	}
	sort.Strings(unrestored)
	return unrestored
}

// keep moves the file at full, as the run left it, under the run's backup
// directory with one rename, and records where. A rename is atomic: the
// bytes are never in neither place, and a writer still holding the file open
// writes on into the backup. What the guard moves may be a person's or
// another agent's edit, not forge's write, so it is moved, never deleted.
func (g *excludeGuard) keep(rel, full string) bool {
	if _, done := g.kept[rel]; done {
		return false // a second change in one run: left where it is, reported
	}
	backupRel := filepath.ToSlash(filepath.Join(".forge", "exclude-guard", g.run, filepath.FromSlash(rel)))
	backup := filepath.Join(g.root, filepath.FromSlash(backupRel))
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return false
	}
	if err := os.Rename(full, backup); err != nil {
		return false
	}
	g.kept[rel] = backupRel
	return true
}

// restore writes the captured pre-run bytes back at full only if nothing is
// there: a file someone wrote at the path since is theirs, and is left alone.
func (g *excludeGuard) restore(rel, full string, f excludedFile) bool {
	if f.data == nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false
	}
	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, f.mode)
	if err != nil {
		return false
	}
	if _, err := out.Write(f.data); err != nil {
		_ = out.Close()
		return false
	}
	return out.Close() == nil
}

// pruneEmptyDirsUpTo removes dir and its parents while they are empty,
// stopping at root.
func pruneEmptyDirsUpTo(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// reportExcludedWrites settles the active guard and prints the run's skipped
// writes under scaffold.exclude: one line per path, at most
// excludeReportPerGlob per glob unless the run is verbose.
func reportExcludedWrites(w io.Writer) {
	activeExcludeGuardMu.Lock()
	g := activeExcludeGuard
	activeExcludeGuardMu.Unlock()
	unrestored := g.settle()
	printExcludedSkips(w, checksums.TakeExcludedSkips(), generateVerbose)
	printUnrestoredExcluded(w, unrestored)
	printKeptExcluded(w, g.takeKept())
}

// takeKept returns the guard's backups not yet reported, as "path ->
// backup" sorted by path, and forgets them, so a run that settles twice
// names each once.
func (g *excludeGuard) takeKept() [][2]string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([][2]string, 0, len(g.kept))
	for rel, backup := range g.kept {
		out = append(out, [2]string{rel, backup})
	}
	clear(g.kept)
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// takeActiveExcludeKept is takeKept of the locked run in progress, if any.
func takeActiveExcludeKept() [][2]string {
	activeExcludeGuardMu.Lock()
	g := activeExcludeGuard
	activeExcludeGuardMu.Unlock()
	return g.takeKept()
}

// printKeptExcluded names the backups once, with what to do when the change
// was not forge's.
func printKeptExcluded(w io.Writer, kept [][2]string) {
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(w, "  ↷ what this run found at %d excluded path(s) was moved aside, never deleted; "+
		"if a change there was yours, or another agent's, move it back:\n", len(kept))
	for _, k := range kept {
		fmt.Fprintf(w, "      %s ← %s\n", k[0], k[1])
	}
}

// settleActiveExcludeGuard settles the guard of the locked run in progress,
// if any (see excludeGuard.settle).
func settleActiveExcludeGuard() []string {
	activeExcludeGuardMu.Lock()
	g := activeExcludeGuard
	activeExcludeGuardMu.Unlock()
	return g.settle()
}

func printUnrestoredExcluded(w io.Writer, unrestored []string) {
	for _, rel := range unrestored {
		fmt.Fprintf(w, "⚠️  %s is excluded by scaffold.exclude and changed during this run; forge left it as it is now rather than overwrite it "+
			"(what it found earlier, if anything, is kept under .forge/exclude-guard/; git diff it before restoring)\n", rel)
	}
}

// releaseExcludeGuard is reportExcludedWrites for the end of a locked run.
func releaseExcludeGuard(g *excludeGuard, w io.Writer) {
	if g == nil {
		return
	}
	reportExcludedWrites(w)
	activeExcludeGuardMu.Lock()
	if activeExcludeGuard == g {
		activeExcludeGuard = nil
	}
	activeExcludeGuardMu.Unlock()
}

// excludeReportPerGlob caps the default report per glob: an excluded tree can
// hold every page of a frontend, and the point is that forge said so, once.
const excludeReportPerGlob = 5

func printExcludedSkips(w io.Writer, skips []checksums.ExcludedSkip, verbose bool) {
	if len(skips) == 0 {
		return
	}
	shown := map[string]int{}
	hidden := map[string]int{}
	for _, s := range skips {
		if !verbose && shown[s.Glob] >= excludeReportPerGlob {
			hidden[s.Glob]++
			continue
		}
		shown[s.Glob]++
		fmt.Fprintf(w, "  ↷ %s skipped (scaffold.exclude: %s)\n", s.Path, s.Glob)
	}
	globs := make([]string, 0, len(hidden))
	for g := range hidden {
		globs = append(globs, g)
	}
	sort.Strings(globs)
	for _, g := range globs {
		fmt.Fprintf(w, "  ↷ … and %d more skipped (scaffold.exclude: %s); -v lists them\n", hidden[g], g)
	}
}
