// Ownership inspector — single source of truth for "what does forge own
// at relPath, and how should we treat it?".
//
// Background (2026-06-04 collapse pass): four parallel call sites in the
// generate pipeline each re-derived their own view of disowned / Tier-1
// / Tier-2 ownership. The Inspector collapses those walks behind a
// small interface. In the self-certifying era ownership is read from
// the files themselves (the forge:hash marker scan) plus the two small
// state files (.forge/disowned.json, .forge/hashes.json) — there is no
// manifest to walk.
//
// Scope: the Inspector answers questions about a *single project's*
// on-disk state. Call NewInspector once per pipeline run with the
// loaded *FileChecksums and project root; per-directory and per-file
// results are memoized so repeated queries inside one run don't
// re-read the disk.
package checksums

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Inspector is the read-only ownership query API.
type Inspector struct {
	root string
	cs   *FileChecksums

	// markers is the lazily-populated forge:hash scan of the project.
	markers map[string]MarkerInfo

	// disownedGoByDir lazily groups disowned Go paths by parent dir.
	disownedGoByDir map[string][]string

	// declaredTypesCache memoizes DeclaredTypesIn(relPath) results.
	declaredTypesCache map[string]map[string]bool

	// goSiblingsCache memoizes per-directory Go file listings.
	goSiblingsCache map[string][]string
}

// NewInspector constructs an Inspector. A nil cs is tolerated (every
// query returns a safe zero value).
func NewInspector(root string, cs *FileChecksums) *Inspector {
	return &Inspector{
		root:               root,
		cs:                 cs,
		declaredTypesCache: map[string]map[string]bool{},
		goSiblingsCache:    map[string][]string{},
	}
}

// markerScan returns the memoized forge:hash scan of the project.
func (i *Inspector) markerScan() map[string]MarkerInfo {
	if i.markers == nil {
		i.markers = ScanMarkers(i.root)
	}
	return i.markers
}

// IsTracked reports whether forge claims any ownership record for
// relPath: an embedded marker on disk, a scoped-fallback hash entry, or
// a disown record. Untracked paths are user-owned by convention.
func (i *Inspector) IsTracked(relPath string) bool {
	if i == nil {
		return false
	}
	relPath = slashKey(relPath)
	if i.IsDisowned(relPath) {
		return true
	}
	if i.cs != nil {
		if _, ok := i.cs.Unstampable[relPath]; ok {
			return true
		}
	}
	_, ok := i.markerScan()[relPath]
	return ok
}

// IsDisowned reports whether relPath was `forge project disown`-ed: a one-way
// transfer to user ownership recorded in .forge/disowned.json.
func (i *Inspector) IsDisowned(relPath string) bool {
	if i == nil || i.cs == nil {
		return false
	}
	return i.cs.IsDisowned(relPath)
}

// IsTier1 reports whether relPath is a Tier-1 (regenerated-every-run)
// file: it carries forge's certification marker (or scoped fallback
// entry) and has not been disowned.
func (i *Inspector) IsTier1(relPath string) bool {
	if i == nil || i.IsDisowned(relPath) {
		return false
	}
	relPath = slashKey(relPath)
	if i.cs != nil {
		if _, ok := i.cs.Unstampable[relPath]; ok {
			return true
		}
	}
	_, ok := i.markerScan()[relPath]
	return ok
}

// IsGo is the small filename-suffix probe shared by every Go-aware
// ownership query.
func (i *Inspector) IsGo(relPath string) bool {
	return IsGoPath(relPath)
}

// DisownedGoFilesByDir groups every disowned Go file by its parent
// directory. The returned map's values are project-relative paths,
// sorted for deterministic iteration. Empty when no Go file is
// disowned. Memoized.
func (i *Inspector) DisownedGoFilesByDir() map[string][]string {
	if i == nil || i.cs == nil {
		return map[string][]string{}
	}
	if i.disownedGoByDir != nil {
		return i.disownedGoByDir
	}
	out := map[string][]string{}
	for relPath := range i.cs.Disowned {
		if !IsGoPath(relPath) {
			continue
		}
		// path, not filepath: the keys are slash-separated identities (see
		// slashKey), and filepath.Dir would hand Windows callers a
		// backslashed directory that matches nothing else.
		dir := path.Dir(relPath)
		out[dir] = append(out[dir], relPath)
	}
	for dir := range out {
		sort.Strings(out[dir])
	}
	i.disownedGoByDir = out
	return out
}

// Tier1GoFiles returns the sorted list of project-relative paths for
// every Tier-1 Go file — files on disk carrying a forge:hash marker
// (any verification status) that have not been disowned. Used by the
// pre-codegen exports snapshot: rename detection only watches files
// forge still owns.
func (i *Inspector) Tier1GoFiles() []string {
	if i == nil {
		return nil
	}
	var out []string
	for relPath := range i.markerScan() {
		if !IsGoPath(relPath) {
			continue
		}
		if i.IsDisowned(relPath) {
			continue
		}
		out = append(out, relPath)
	}
	sort.Strings(out)
	return out
}

// GoSiblingsIn returns the project-relative paths of every *.go file
// (excluding *_test.go) physically present under projectRoot/relDir.
// Reads the directory once and memoizes the result.
// The paths are slash-separated (see slashKey), so they compare equal to
// DisownedGoFilesByDir's values on every OS.
func (i *Inspector) GoSiblingsIn(relDir string) ([]string, error) {
	relDir = slashKey(relDir)
	if cached, ok := i.goSiblingsCache[relDir]; ok {
		return cached, nil
	}
	absDir := filepath.Join(i.root, relDir)
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, path.Join(relDir, name))
	}
	sort.Strings(out)
	i.goSiblingsCache[relDir] = out
	return out, nil
}

// DeclaredTypesIn returns the set of top-level type identifiers declared
// in projectRoot/relPath. Returns an empty (non-nil) set on parse error
// or a missing file. Memoized per path.
func (i *Inspector) DeclaredTypesIn(relPath string) map[string]bool {
	if cached, ok := i.declaredTypesCache[relPath]; ok {
		return cached
	}
	content, err := os.ReadFile(filepath.Join(i.root, relPath))
	if err != nil {
		out := map[string]bool{}
		i.declaredTypesCache[relPath] = out
		return out
	}
	out := topLevelTypeNames(content)
	i.declaredTypesCache[relPath] = out
	return out
}

// topLevelTypeNames returns the set of top-level type identifiers
// declared in Go source `content`. Lower-case names are included
// because Go treats them as package-local declarations that can
// satisfy an unqualified type reference within the same package.
//
// Returns an empty (non-nil) set when the file fails to parse — a
// single broken file shouldn't blind a package-wide scan.
func topLevelTypeNames(content []byte) map[string]bool {
	out := map[string]bool{}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution)
	if err != nil {
		return out
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			out[ts.Name.Name] = true
		}
	}
	return out
}
