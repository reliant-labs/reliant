// Copyright (c) 2025 Reliant Labs
package main

// A forge pin bump moves go.mod's forge require, its go.sum lines and
// forge.yaml's forge_version, and nothing else of the three. The go-toolchain
// and forge-config rules would run the whole Go lane for it (41 minutes of
// merge gate for #3645; #3711 ran out the queue's 45). When the diff carries
// exactly that, ForgePin says which forge packages changed between the two
// versions, and the Go lane selects what reaches them, as it does for a
// change to one of this module's packages. Anything else in those files, or
// any failure to tell, keeps the lane full: the release gate runs everything
// regardless.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
)

// forgeModules are the module paths of forge: one commit pins both
// (dev-docs/pinning.md). A package belongs to the longest of them that go.mod
// requires and that prefixes its import path.
var forgeModules = []string{"github.com/reliant-labs/forge", "github.com/reliant-labs/forge/pkg"}

// pinFiles are the files a forge pin bump moves.
var pinFiles = []string{"go.mod", "go.sum", "forge.yaml"}

func isPinFile(p string) bool { return slices.Contains(pinFiles, p) }

// isForgePkg reports whether an import path is a package of forge.
func isForgePkg(p string) bool {
	return p == forgeModules[0] || strings.HasPrefix(p, forgeModules[0]+"/")
}

// ForgePin is the diff's forge pin move, set when go.mod or go.sum changed.
type ForgePin struct {
	// From and To name the old and new forge, as reasons print them.
	From, To string
	// Changed maps each forge package of this module's graph whose build
	// differs between the two versions to why: its own files changed, or it
	// imports (within forge) a package that did.
	Changed map[string]string
	// NotNarrowed is why the Go lane stays full: the pin files carry more
	// than a forge pin move, or the changed packages could not be computed.
	// Empty when the lane narrows to Changed.
	NotNarrowed string
}

// Narrows reports whether the Go lane selects by Changed instead of running
// in full.
func (fp *ForgePin) Narrows() bool { return fp != nil && fp.NotNarrowed == "" }

// modMove is one forge module's require moving from Old to New.
type modMove struct{ Old, New string }

// pinMove is what detectForgePin reads from the pin files.
type pinMove struct {
	// Moves are the forge modules whose require moved.
	Moves map[string]modMove
	// Required are the forge modules the new go.mod requires, moved or not.
	Required []string
}

// detectForgePin reports whether the pin files' changes are exactly a forge
// pin move. old and new hold the contents of each pin file the diff changes
// (a file the diff does not change is absent from both). The reason says what
// else changed.
func detectForgePin(old, cur map[string][]byte) (pinMove, string) {
	var pm pinMove
	oldMod, okOld := old["go.mod"]
	newMod, okNew := cur["go.mod"]
	if !okOld || !okNew {
		return pm, "go.mod's forge require did not move"
	}
	removed, added := multisetDiff(goModLines(oldMod), goModLines(newMod))
	pm.Moves = map[string]modMove{}
	// The new lines first: a reason names what the change brings.
	for _, l := range added {
		m, v, ok := forgeRequire(l)
		if !ok || pm.Moves[m].New != "" {
			return pm, "go.mod changes more than the forge require lines (" + modLineText(l) + ")"
		}
		mv := pm.Moves[m]
		mv.New = v
		pm.Moves[m] = mv
	}
	for _, l := range removed {
		m, v, ok := forgeRequire(l)
		if !ok || pm.Moves[m].Old != "" {
			return pm, "go.mod changes more than the forge require lines (" + modLineText(l) + ")"
		}
		mv := pm.Moves[m]
		mv.Old = v
		pm.Moves[m] = mv
	}
	if len(pm.Moves) == 0 {
		return pm, "go.mod's forge require did not move"
	}
	for m, mv := range pm.Moves {
		if mv.Old == "" || mv.New == "" {
			return pm, "go.mod adds or drops the require of " + m
		}
	}
	// Downloads compare the required versions, not replacement sources. Even
	// an unchanged replace can stop applying when a version-specific require
	// moves. Keep the full lane for forge replacements in either revision.
	for _, b := range [][]byte{oldMod, newMod} {
		mod, err := modfile.Parse("go.mod", b, nil)
		if err != nil {
			return pm, "cannot parse go.mod to rule out forge replacements"
		}
		for _, r := range mod.Replace {
			if slices.Contains(forgeModules, r.Old.Path) {
				return pm, "go.mod replaces forge module " + r.Old.Path
			}
		}
	}
	for _, l := range goModLines(newMod) {
		if m, _, ok := forgeRequire(l); ok {
			pm.Required = append(pm.Required, m)
		}
	}
	if sum, ok := cur["go.sum"]; ok {
		removed, added := multisetDiff(lines(old["go.sum"]), lines(sum))
		for _, l := range append(removed, added...) {
			f := strings.Fields(l)
			if len(f) == 0 {
				continue
			}
			if _, moved := pm.Moves[f[0]]; !moved {
				return pm, "go.sum changes another module's lines (" + f[0] + ")"
			}
		}
	}
	if y, ok := cur["forge.yaml"]; ok {
		removed, added := multisetDiff(lines(old["forge.yaml"]), lines(y))
		if len(removed) != 1 || len(added) != 1 || !strings.HasPrefix(removed[0], "forge_version:") || !strings.HasPrefix(added[0], "forge_version:") {
			return pm, "forge.yaml changes more than forge_version"
		}
	}
	return pm, ""
}

// goModLines are go.mod's non-blank lines, trimmed, each prefixed with the
// block it is in ("require", "replace", ... or "" outside one) and a NUL: a
// line of an exclude block is not a require, though it reads the same.
func goModLines(b []byte) []string {
	var out []string
	block := ""
	for _, l := range lines(b) {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
			continue
		case l == ")":
			block = ""
			continue
		case strings.HasSuffix(l, "(") && len(strings.Fields(l)) <= 2:
			block = strings.TrimSpace(strings.TrimSuffix(l, "("))
			continue
		}
		out = append(out, block+"\x00"+l)
	}
	return out
}

// modLineText is a goModLines line without its block.
func modLineText(l string) string { return l[strings.IndexByte(l, 0)+1:] }

// forgeRequire parses a goModLines line as the require of a forge module.
func forgeRequire(l string) (module, version string, ok bool) {
	block, line, _ := strings.Cut(l, "\x00")
	f := strings.Fields(line)
	switch {
	case block == "" && len(f) > 0 && f[0] == "require":
		f = f[1:]
	case block != "require":
		return "", "", false
	}
	if len(f) < 2 || !slices.Contains(forgeModules, f[0]) {
		return "", "", false
	}
	if len(f) > 2 && !strings.HasPrefix(f[2], "//") {
		return "", "", false
	}
	return f[0], f[1], true
}

func lines(b []byte) []string {
	return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
}

// multisetDiff returns the lines only in a and only in b, counting repeats.
func multisetDiff(a, b []string) (removed, added []string) {
	n := map[string]int{}
	for _, l := range a {
		n[l]++
	}
	for _, l := range b {
		n[l]--
	}
	for _, l := range slices.Sorted(maps.Keys(n)) {
		if strings.TrimSpace(modLineText(l)) == "" {
			continue
		}
		for i := n[l]; i > 0; i-- {
			removed = append(removed, l)
		}
		for i := n[l]; i < 0; i++ {
			added = append(added, l)
		}
	}
	return removed, added
}

// forgeSource returns the source tree of module@version.
type forgeSource func(ctx context.Context, module, version string) (fs.FS, error)

// goModDownload is the forgeSource of the module cache: `go mod download`,
// outside any module so nothing of the checkout is read or written.
func goModDownload(ctx context.Context, module, version string) (fs.FS, error) {
	dir, err := os.MkdirTemp("", "test-affected-forge-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", module+"@"+version)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var info struct{ Dir, Error string }
	if err := json.Unmarshal(out, &info); err != nil || info.Error != "" || info.Dir == "" {
		why := info.Error
		if why == "" && runErr != nil {
			why = runErr.Error() + ": " + firstLine(stderr.String())
		}
		if why == "" {
			why = "no module directory"
		}
		return nil, fmt.Errorf("go mod download %s@%s: %s", module, version, why)
	}
	return os.DirFS(info.Dir), nil
}

// forgePackages are the forge packages any package (or test binary) of the
// module depends on.
func forgePackages(g *Graph) []string {
	set := map[string]bool{}
	for _, deps := range []map[string]map[string]bool{g.BuildDeps, g.TestDeps} {
		for _, ds := range deps {
			for d := range ds {
				if isForgePkg(d) {
					set[d] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// loadForgePin judges the pin files' change (old and new contents, as
// detectForgePin takes them) and, when it is a forge pin move, which of pkgs
// (forgePackages) changed between the two versions.
func loadForgePin(ctx context.Context, old, cur map[string][]byte, src forgeSource, pkgs []string) *ForgePin {
	pm, why := detectForgePin(old, cur)
	if why != "" {
		return &ForgePin{NotNarrowed: why}
	}
	fp := &ForgePin{}
	var from, to []string
	for _, m := range slices.Sorted(maps.Keys(pm.Moves)) {
		from, to = append(from, shortVersion(pm.Moves[m].Old)), append(to, shortVersion(pm.Moves[m].New))
	}
	fp.From, fp.To = strings.Join(slices.Compact(from), ","), strings.Join(slices.Compact(to), ",")
	changed, err := changedForgePackages(ctx, pm, src, pkgs, fmt.Sprintf("forge %s..%s", fp.From, fp.To))
	if err != nil {
		return &ForgePin{From: fp.From, To: fp.To, NotNarrowed: "cannot tell which forge packages changed (" + firstLine(err.Error()) + ")"}
	}
	fp.Changed = changed
	return fp
}

var pseudoRev = regexp.MustCompile(`[-.]\d{14}-([0-9a-f]{12})$`)

// shortVersion is a pseudo-version's commit, or the version.
func shortVersion(v string) string {
	if m := pseudoRev.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return v
}

// pkgState is what of a forge package's directory its build reads.
type pkgState struct {
	exists  bool
	files   map[string][32]byte // module-relative path -> content hash
	imports []string            // forge packages its non-test files import
}

// changedForgePackages returns each of pkgs whose build differs between the
// moved versions, and why. A package changed when a file its build reads
// changed (a non-test source of its directory, or a file a //go:embed
// pattern may match), it appeared or vanished, or it imports, within forge,
// a package that changed. span names the versions in the reasons.
func changedForgePackages(ctx context.Context, pm pinMove, src forgeSource, pkgs []string, span string) (map[string]string, error) {
	trees := map[string]fs.FS{}
	tree := func(module, version string) (fs.FS, error) {
		key := module + "@" + version
		if t, ok := trees[key]; ok {
			return t, nil
		}
		t, err := src(ctx, module, version)
		if err != nil {
			return nil, err
		}
		trees[key] = t
		return t, nil
	}
	for _, m := range slices.Sorted(maps.Keys(pm.Moves)) {
		mv := pm.Moves[m]
		oldT, err := tree(m, mv.Old)
		if err != nil {
			return nil, err
		}
		newT, err := tree(m, mv.New)
		if err != nil {
			return nil, err
		}
		// The go directive is the language version every package of the
		// module compiles at.
		if a, b := goDirective(oldT), goDirective(newT); a != b {
			return nil, fmt.Errorf("%s's go.mod moves its go directive (%q to %q)", m, a, b)
		}
	}
	changed := map[string]string{}
	imports := map[string][]string{}
	for _, p := range pkgs {
		m := owningModule(p, pm.Required)
		if m == "" {
			return nil, fmt.Errorf("%s: go.mod requires no forge module that provides it", p)
		}
		mv, moved := pm.Moves[m]
		if !moved {
			continue
		}
		dir := strings.TrimPrefix(strings.TrimPrefix(p, m), "/")
		if dir == "" {
			dir = "."
		}
		oldT, _ := tree(m, mv.Old)
		newT, _ := tree(m, mv.New)
		a, err := readPkgState(oldT, dir)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", p, mv.Old, err)
		}
		b, err := readPkgState(newT, dir)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", p, mv.New, err)
		}
		imports[p] = append(a.imports, b.imports...)
		if a.exists != b.exists || !maps.Equal(a.files, b.files) {
			changed[p] = forgeName(p) + " changed between " + span
		}
	}
	// Within forge, a package whose import changed changed too: a direct
	// importer in this module then runs, not only the binaries containing it.
	for grew := true; grew; {
		grew = false
		for _, p := range pkgs {
			if _, ok := changed[p]; ok {
				continue
			}
			for _, i := range slices.Sorted(slices.Values(imports[p])) {
				if _, ok := changed[i]; ok {
					changed[p] = forgeName(p) + " imports " + forgeName(i) + ", which changed between " + span
					grew = true
					break
				}
			}
		}
	}
	return changed, nil
}

// forgeName is a forge import path as reasons print it: forge/pkg/svcerr.
func forgeName(p string) string { return strings.TrimPrefix(p, "github.com/reliant-labs/") }

// owningModule is the longest of required that is p or a prefix of it.
func owningModule(p string, required []string) string {
	best := ""
	for _, m := range required {
		if (p == m || strings.HasPrefix(p, m+"/")) && len(m) > len(best) {
			best = m
		}
	}
	return best
}

func goDirective(t fs.FS) string {
	b, err := fs.ReadFile(t, "go.mod")
	if err != nil {
		return ""
	}
	for _, l := range lines(b) {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "go" {
			return f[1]
		}
	}
	return ""
}

// buildExts are the files of a package directory the go command compiles,
// other than .go: cgo, assembly and object files.
var buildExts = []string{".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx", ".m", ".s", ".S", ".sx", ".f", ".F", ".for", ".f90", ".syso", ".swig", ".swigcxx"}

// readPkgState reads what the build of the package in dir reads: its non-test
// sources and every file its //go:embed patterns may match. A pattern is read
// as its literal prefix (templates/*.tmpl as everything under templates/), so
// the set over-approximates, never under.
func readPkgState(t fs.FS, dir string) (pkgState, error) {
	st := pkgState{files: map[string][32]byte{}}
	entries, err := fs.ReadDir(t, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	} else if err != nil {
		return st, err
	}
	add := func(p string) error {
		b, err := fs.ReadFile(t, p)
		if err != nil {
			return err
		}
		st.files[p] = sha256.Sum256(b)
		return nil
	}
	var embeds []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, "_test.go") {
			continue
		}
		p := path.Join(dir, name)
		if strings.HasSuffix(name, ".go") {
			st.exists = true
			b, err := fs.ReadFile(t, p)
			if err != nil {
				return st, err
			}
			st.files[p] = sha256.Sum256(b)
			f, err := parser.ParseFile(fset, p, b, parser.ImportsOnly)
			if err != nil {
				return st, err
			}
			for _, im := range f.Imports {
				if ip, err := strconv.Unquote(im.Path.Value); err == nil && isForgePkg(ip) {
					st.imports = append(st.imports, ip)
				}
			}
			embeds = append(embeds, embedPrefixes(b)...)
			continue
		}
		if slices.Contains(buildExts, path.Ext(name)) {
			if err := add(p); err != nil {
				return st, err
			}
		}
	}
	for _, pre := range embeds {
		root := path.Join(dir, pre)
		err := fs.WalkDir(t, root, func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // matched nothing in this version
			}
			if err != nil || d.IsDir() {
				return err
			}
			return add(p)
		})
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

// embedPrefixes returns, for each //go:embed pattern in src, the directory or
// file every match is under, relative to the package directory ("." when a
// pattern is a glob at the top).
func embedPrefixes(src []byte) []string {
	var out []string
	for _, l := range lines(src) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "//go:embed")
		if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		for _, pat := range embedPatterns(rest) {
			pat = strings.TrimPrefix(pat, "all:")
			if i := strings.IndexAny(pat, `*?[\`); i >= 0 {
				pat = path.Dir(pat[:i] + "x")
			}
			out = append(out, path.Clean(pat))
		}
	}
	return out
}

// embedPatterns splits a //go:embed line's arguments: space-separated, each
// bare, "quoted" or `raw`. An unterminated quote reads as the whole package
// directory.
func embedPatterns(s string) []string {
	var out []string
	s = strings.TrimSpace(s)
	for s != "" {
		var pat string
		switch s[0] {
		case '"', '`':
			end := strings.IndexByte(s[1:], s[0])
			if end < 0 {
				return append(out, ".")
			}
			q := s[:end+2]
			u, err := strconv.Unquote(q)
			if err != nil {
				return append(out, ".")
			}
			pat, s = u, s[end+2:]
		default:
			i := strings.IndexAny(s, " \t")
			if i < 0 {
				i = len(s)
			}
			pat, s = s[:i], s[i:]
		}
		out = append(out, pat)
		s = strings.TrimSpace(s)
	}
	return out
}

// pinFileContents reads, for each pin file the diff changes, its contents at
// base and in the working tree.
func pinFileContents(ctx context.Context, root, base string, changes []Change) (old, cur map[string][]byte, err error) {
	old, cur = map[string][]byte{}, map[string][]byte{}
	for _, ch := range changes {
		if !isPinFile(ch.Path) {
			continue
		}
		if ch.Deleted {
			return nil, nil, fmt.Errorf("%s is deleted", ch.Path)
		}
		o, err := git(ctx, root, "show", base+":"+ch.Path)
		if err != nil {
			return nil, nil, err
		}
		n, err := os.ReadFile(filepath.Join(root, ch.Path))
		if err != nil {
			return nil, nil, err
		}
		old[ch.Path], cur[ch.Path] = []byte(o), n
	}
	return old, cur, nil
}

// planForgePin is the ForgePin of the diff, or nil when neither go.mod nor
// go.sum changed (forge.yaml alone is judged by its rule, as before).
func planForgePin(ctx context.Context, root, base string, changes []Change, whatIf bool, g *Graph, src forgeSource) *ForgePin {
	if !slices.ContainsFunc(changes, func(c Change) bool { return c.Path == "go.mod" || c.Path == "go.sum" }) {
		return nil
	}
	if whatIf {
		return &ForgePin{NotNarrowed: "a what-if file list has no contents to compare"}
	}
	old, cur, err := pinFileContents(ctx, root, base, changes)
	if err != nil {
		return &ForgePin{NotNarrowed: "cannot read the pin files (" + firstLine(err.Error()) + ")"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return loadForgePin(ctx, old, cur, src, forgePackages(g))
}
