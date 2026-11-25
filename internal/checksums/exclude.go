package checksums

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/pathglob"
)

// exclude.go — forge.yaml's `scaffold.exclude`: the areas of a repository
// forge never writes.
//
// An excluded path is the third way a path becomes the project's, beside
// `forge project disown` (one file, after forge wrote it) and `forge project
// disown --absent` (one scaffold, declined before birth). It is consulted at
// the same chokepoints those two are — the Tier-1 preamble, the scaffold-once
// birth decision, the refresh-until-touched scaffolds, `forge project upgrade`
// and `forge project rescaffold` — and behaves like them: a file already
// there is left exactly as it is, and an absent one stays absent. Unlike a
// disowned Tier-1 file, deleting an excluded one does not hand it back: the
// glob, not the file, carries the decision.
//
// The globs are read from forge.yaml here rather than threaded through every
// writer's signature, the way the disowned set and the birth ledger are read
// from .forge/: a writer that forgot to pass them would be the one that
// writes into an excluded tree.

// excludeEntry caches one project's globs against forge.yaml's identity, so a
// run that edits forge.yaml (a test, `forge project new --in-place`) is seen.
type excludeEntry struct {
	size  int64
	mtime time.Time
	globs []string
}

var (
	excludeMu    sync.Mutex
	excludeCache = map[string]excludeEntry{}
	// excludeSkips is every excluded path a writer declined since the last
	// TakeExcludedSkips, path → the glob that excluded it. It is NOT part of
	// ResetPerRunState: a scaffold command declines writes of its own before
	// the generate pipeline it runs resets the per-run state, and those
	// skips belong in the same report.
	excludeSkips = map[string]string{}
)

// ExcludeGlobs returns the project's scaffold.exclude globs (normalized), or
// nil when it declares none.
func ExcludeGlobs(root string) []string {
	if root == "" {
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	fi, err := os.Stat(filepath.Join(abs, "forge.yaml"))
	if err != nil {
		return nil
	}
	excludeMu.Lock()
	defer excludeMu.Unlock()
	if e, ok := excludeCache[abs]; ok && e.size == fi.Size() && e.mtime.Equal(fi.ModTime()) {
		return e.globs
	}
	globs := config.ReadScaffoldExclude(abs)
	excludeCache[abs] = excludeEntry{size: fi.Size(), mtime: fi.ModTime(), globs: globs}
	return globs
}

// ExcludedBy reports the scaffold.exclude glob that covers relPath, if any.
// It records nothing; a writer that declines the path calls SkipExcluded.
func ExcludedBy(root, relPath string) (glob string, ok bool) {
	rel := slashKey(relPath)
	for _, g := range ExcludeGlobs(root) {
		if pathglob.Match(g, rel) {
			return g, true
		}
	}
	return "", false
}

// SkipExcluded reports whether relPath is excluded and, when it is, records
// the declined write for the run's report. Every writer that would otherwise
// write relPath asks this first.
func SkipExcluded(root, relPath string) bool {
	glob, ok := ExcludedBy(root, relPath)
	if ok {
		NoteExcluded(relPath, glob)
	}
	return ok
}

// NoteExcluded records that forge declined to write relPath because glob
// excludes it. The first glob recorded for a path wins.
func NoteExcluded(relPath, glob string) {
	rel := slashKey(relPath)
	excludeMu.Lock()
	defer excludeMu.Unlock()
	if _, seen := excludeSkips[rel]; !seen {
		excludeSkips[rel] = glob
	}
}

// ExcludedSkip is one write forge declined because scaffold.exclude covers it.
type ExcludedSkip struct {
	Path string
	Glob string
}

// TakeExcludedSkips returns the declined writes recorded since the last call,
// sorted by path, and forgets them, so each is reported once.
func TakeExcludedSkips() []ExcludedSkip {
	excludeMu.Lock()
	defer excludeMu.Unlock()
	out := make([]ExcludedSkip, 0, len(excludeSkips))
	for p, g := range excludeSkips {
		out = append(out, ExcludedSkip{Path: p, Glob: g})
	}
	excludeSkips = map[string]string{}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ExcludeConflictError is a scaffold.exclude glob that covers a generated file
// other code depends on. Forge refuses the run rather than leave the
// dependent compiling against a file it no longer regenerates.
type ExcludeConflictError struct {
	Path      string // the excluded generated path
	Glob      string // the glob that excluded it
	Dependent string // what depends on it
	Instead   string // the switch that turns the pair off together
}

func (e *ExcludeConflictError) Error() string {
	msg := fmt.Sprintf("scaffold.exclude: %q excludes %s, which forge generates on every run and %s depends on; "+
		"excluding it alone would leave that out of step with the rest of the generated code",
		e.Glob, e.Path, e.Dependent)
	if e.Instead != "" {
		msg += ". " + e.Instead
	}
	return msg + ". Narrow the glob in forge.yaml so it no longer covers " + e.Path + "."
}

// excludeTier1Decision is the Tier-1 preamble's question: may forge write
// relPath, given scaffold.exclude? It returns skip=true for an excluded path
// (recording it), and an *ExcludeConflictError when the path is a generated
// Go file whose package depends on it:
//
//   - a sibling *_gen.go that is NOT excluded: generated code in one package
//     is generated together, and one half regenerated against a stale other
//     half does not compile;
//   - the file is absent and a hand-written sibling is not excluded: nothing
//     would ever create what that code calls.
//
// An excluded generated file that exists, in a package nothing else in which
// forge regenerates, is the project's to maintain, exactly like a disowned one.
func excludeTier1Decision(root, relPath string) (skip bool, err error) {
	glob, ok := ExcludedBy(root, relPath)
	if !ok {
		return false, nil
	}
	if strings.HasSuffix(relPath, "_gen.go") {
		if dep := dependentSibling(root, relPath); dep != "" {
			return true, &ExcludeConflictError{Path: relPath, Glob: glob, Dependent: dep,
				Instead: excludeAlternative(relPath)}
		}
	}
	NoteExcluded(relPath, glob)
	return true, nil
}

// dependentSibling names a file in relPath's package that depends on it and
// is not excluded, or "".
func dependentSibling(root, relPath string) string {
	dir := filepath.Dir(filepath.FromSlash(relPath))
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return ""
	}
	_, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(relPath)))
	absent := os.IsNotExist(statErr)
	base := filepath.Base(relPath)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == base || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		sib := filepath.ToSlash(filepath.Join(dir, n))
		if _, excluded := ExcludedBy(root, sib); excluded {
			continue
		}
		if strings.HasSuffix(n, "_gen.go") || absent {
			return sib
		}
	}
	return ""
}

// excludeAlternative names the switch that stops forge generating relPath's
// kind of file at all, for the conflict message.
func excludeAlternative(relPath string) string {
	switch {
	case strings.Contains(filepath.Base(relPath), "orm_") || strings.HasSuffix(relPath, "store_gen.go"):
		return "To stop forge generating the data layer, mark its package `//forge:no-orm: <why>`"
	case strings.HasPrefix(relPath, "gen/"):
		return "To stop forge generating code at all, pin `features: {codegen: false}`"
	case strings.HasSuffix(relPath, "mock_gen.go"):
		return "To stop forge generating a package's mocks, list the package in `contracts.exclude`"
	}
	return "To own the whole package, exclude its directory (`" + filepath.ToSlash(filepath.Dir(relPath)) + "/`)"
}
