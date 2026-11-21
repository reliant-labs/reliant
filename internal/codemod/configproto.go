package codemod

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/configproto"
)

// configproto.go — `forge fix config-proto-package`, the v1.0.0 move of a
// project's config proto off the package every forge project shared.
//
// Every project declared `package config.v1` at proto/config/v1, so the
// generated Go of any two of them registered the same proto file path and the
// same message names, and a binary linking both panicked at init ("file
// config/v1/config.proto is already registered"). The layout is now
// project-unique (internal/configproto):
//
//	proto/<project>/config/v1/*.proto   package <project>.config.v1
//	<module>/gen/<project>/config/v1    go_package, Go package configv1
//
// The rewrite is a pure move and rename: the messages, their fields and their
// (forge.v1.config) annotations are untouched, so env var names, flags and the
// KCL schemas forge generates from them are byte-identical, and the Go package
// keeps its name (configv1), so an importer needs only its import path
// changed. That is why `forge project upgrade` runs it on its own.

func init() {
	register(Codemod{
		Name:    configproto.LegacyCodemod,
		Version: "v1.0.0",
		Short:   "Move the config protos to proto/<project>/config/v1, package <project>.config.v1 (no shared config.v1 since v1.0.0)",
		Long: `Move the project's config protos off the proto package every forge project shared.

Every forge project declared package config.v1 at proto/config/v1/config.proto,
so two projects' generated config packages could not be linked into one binary:
protobuf registers each file by path and each message by full name, process-wide,
and Go's runtime panics at init on the second ("file config/v1/config.proto is
already registered"). forge v1.0.0 derives both from the project's name
(forge.yaml name: "control-plane" is control_plane):

  proto/config/v1/<file>.proto        → proto/<project>/config/v1/<file>.proto
  package config.v1;                  → package <project>.config.v1;
  go_package "<mod>/gen/config/v1;…"  → "<mod>/gen/<project>/config/v1;configv1"

and rewrites what points at them:

  import "config/v1/<file>.proto"     in every other proto under proto/
  config.v1.<Message> references      in every proto (fully qualified type names)
  "<mod>/gen/config/v1" Go imports    in the project's own Go files
  "config.v1", "config.v1.Msg"        Go string literals that are exactly the old
                                      package or a name in it (a registry filter
                                      such as RangeFilesByPackage("config.v1", …)
                                      silently matches nothing once it moves)
  proto/config/v1, gen/config/v1      as paths in the project's own protos, Go,
                                      TS/JS, KCL, YAML, shell and Make files
                                      (TS imports of the generated _pb modules too)
  proto/config                        the config tree as a whole path, the same
                                      files (a buf.yaml lint/breaking ignore)

Generated and forge-owned files (gen/, *_gen.go, a forge:hash marker) are not
touched: forge generate rewrites them.

and deletes the stale generated output under gen/config/v1 (and each frontend's
src/gen/config/v1) that the next forge generate would otherwise leave beside
the new package.

Nothing a config DECLARES changes: fields, annotations, env var names, flags
and the generated KCL schemas are the same, and the Go package keeps its name
(configv1). Run forge generate afterwards to regenerate pkg/config, gen/ and
the descriptor. Remaining mentions of the old layout (docs, a Go string that only contains config.v1, an import of another
project's gen/config/v1) are listed
for you with file:line; nothing else is decided for you.`,
		Upgrade:   true,
		CleanTree: true,
		Run:       runConfigProtoPackage,
	})
}

// configV1RefRE matches a reference to a name in the legacy config.v1
// package — `config.v1.AppConfig`, `.config.v1.AppConfig` — and not one that
// merely ends in it (`forge.v1.config`, `acme.config.v1.X`). Group 1 is the
// preceding character (or start), group 2 the optional leading dot.
var configV1RefRE = regexp.MustCompile(`(^|[^\w.])(\.?)config\.v1\.([A-Za-z_])`)

// legacyProtoDirRE and legacyGenDirRE match the legacy config directory and
// its generated output as paths — not a longer path ending in them
// (proto/acme/config/v1).
var (
	legacyProtoDirRE = regexp.MustCompile(`\bproto/config/v1\b`)
	legacyGenDirRE   = regexp.MustCompile(`\bgen/config/v1\b`)
)

// legacyProtoTreeRE matches the legacy config TREE, proto/config, as a whole
// path (an optional trailing slash, then anything that cannot continue a
// path): what a buf.yaml lint or breaking `ignore` names to exempt the config
// protos. Left behind, that ignore silently re-gates them at their new home.
// proto/config/v1 is legacyProtoDirRE's; group 1 is the trailing slash,
// group 2 what follows.
var legacyProtoTreeRE = regexp.MustCompile(`(?m)\bproto/config(/?)([^\w/.\-]|$)`)

// goProtoPackageLiteralRE matches a Go string literal that is exactly the
// legacy proto package — "config.v1" — or a fully-qualified name in it —
// "config.v1.AppConfig" — in an interpreted or raw literal. A literal that
// merely contains the package ("/config.v1.AppConfig", "see config.v1") is
// not matched: whether it should move is a judgement, so it is listed.
// Group 1 is the opening quote, 2 the optional ".Name" tail, 3 the closing.
var goProtoPackageLiteralRE = regexp.MustCompile("([\"`])config\\.v1((?:\\.[A-Za-z_][\\w.]*)?)([\"`])")

// foreignConfigImportRE matches a Go import of some module's legacy
// generated config package.
var foreignConfigImportRE = regexp.MustCompile(`"([^"\s]+)/gen/config/v1"`)

// legacyPackageDeclRE matches the legacy package declaration.
var legacyPackageDeclRE = regexp.MustCompile(`(?m)^(\s*)package\s+config\.v1\s*;`)

// goPackageOptRE matches a go_package option and captures its value.
var goPackageOptRE = regexp.MustCompile(`(?m)^(\s*option\s+go_package\s*=\s*)"([^"]*)"(\s*;)`)

func runConfigProtoPackage(dir string, opts Options) (Report, error) {
	write := opts.Write
	var rep Report
	legacy := configproto.LegacyFiles(dir)
	if len(legacy) == 0 {
		return rep, nil
	}
	module := configproto.ModulePath(dir)
	if module == "" {
		rep.Blocked = append(rep.Blocked, "go.mod: no module path, so the new go_package cannot be derived — run this from the project root")
		return rep, nil
	}
	layout := configproto.ForProject(dir)
	if layout.Dir() == configproto.LegacyDir {
		// Unreachable for any slug Slug returns ("config" would give
		// proto/config/config/v1), but a move onto itself would delete it.
		return rep, fmt.Errorf("layout %s is the legacy directory", layout.Dir())
	}

	// The legacy files' import paths relative to the buf module root, and
	// where each moves.
	moved := map[string]string{} // "config/v1/x.proto" → "<slug>/config/v1/x.proto"
	for _, rel := range legacy {
		base := path.Base(rel)
		moved[path.Join("config", "v1", base)] = path.Join(layout.BufDir(), base)
		dest := path.Join(layout.Dir(), base)
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(dest))); err == nil {
			rep.Blocked = append(rep.Blocked, fmt.Sprintf("%s: already exists, so %s cannot move onto it — merge the two by hand (or delete the one you do not want), then re-run", dest, rel))
		}
	}
	if len(rep.Blocked) > 0 {
		return rep, nil
	}

	// rewritePaths points the project's own paths at the new layout:
	// proto/config/v1 → proto/<project>/config/v1 and gen/config/v1 (the Go
	// and TypeScript output) → gen/<project>/config/v1.
	tree := path.Dir(layout.Dir()) // proto/<project>/config
	rewriteTree := func(src []byte) []byte {
		return legacyProtoTreeRE.ReplaceAll(src, []byte(tree+"${1}${2}"))
	}
	rewritePaths := func(src []byte) []byte {
		out := legacyProtoDirRE.ReplaceAll(src, []byte(layout.Dir()))
		out = rewriteTree(out)
		return legacyGenDirRE.ReplaceAll(out, []byte("gen/"+layout.BufDir()))
	}
	rewriteProto := func(src []byte) []byte {
		out := configV1RefRE.ReplaceAll(src, []byte("${1}${2}"+layout.Package()+".${3}"))
		for from, to := range moved {
			out = bytes.ReplaceAll(out, []byte(`import "`+from+`"`), []byte(`import "`+to+`"`))
			out = bytes.ReplaceAll(out, []byte(`import public "`+from+`"`), []byte(`import public "`+to+`"`))
			out = bytes.ReplaceAll(out, []byte(`import weak "`+from+`"`), []byte(`import weak "`+to+`"`))
		}
		return out
	}

	var changes []configProtoChange
	legacySet := map[string]bool{}
	for _, rel := range legacy {
		legacySet[rel] = true
		src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return rep, err
		}
		out := legacyPackageDeclRE.ReplaceAll(src, []byte("${1}package "+layout.Package()+";"))
		out = goPackageOptRE.ReplaceAllFunc(out, func(m []byte) []byte {
			sub := goPackageOptRE.FindSubmatch(m)
			importPath, _, _ := strings.Cut(string(sub[2]), ";")
			if importPath != module+"/gen/config/v1" {
				rep.Next = append(rep.Next, fmt.Sprintf("%s: go_package %q is not forge's default (%s/gen/config/v1); check it — every config message must generate into one Go package, by default %s", path.Join(layout.Dir(), path.Base(rel)), sub[2], module, layout.GoPackage(module)))
				return m
			}
			return []byte(string(sub[1]) + `"` + layout.GoPackage(module) + `"` + string(sub[3]))
		})
		out = rewritePaths(rewriteProto(out))
		dest := path.Join(layout.Dir(), path.Base(rel))
		changes = append(changes, configProtoChange{from: rel, to: dest, content: out})
		rep.Rewrites = append(rep.Rewrites, fmt.Sprintf("%s → %s: package %s → %s", rel, dest, configproto.LegacyPackage, layout.Package()))
	}

	// Every other file of the project's own that names the old layout.
	// Generated and forge-owned files (a forge:hash marker) are skipped:
	// forge generate rewrites them, and an edit would read as drift.
	oldGo, newGo := `"`+module+`/gen/config/v1"`, `"`+layout.GoImportPath(module)+`"`
	if err := walkProjectFiles(dir, rewritable, func(rel string, src []byte) error {
		if legacySet[rel] || forgeOwned(src) {
			return nil
		}
		var out []byte
		switch {
		case strings.HasSuffix(rel, ".proto"):
			// A proto importing a moved file or naming a config.v1 type.
			out = rewritePaths(rewriteProto(src))
		case strings.HasSuffix(rel, ".go"):
			// Only this module's import: the package name (configv1) does
			// not change, so nothing else in the file moves. Another forge
			// project's gen/config/v1 moves when that project does.
			out = bytes.ReplaceAll(src, []byte(oldGo), []byte(newGo))
			out = legacyProtoDirRE.ReplaceAll(out, []byte(layout.Dir()))
			out = rewriteTree(out)
			// The proto package as a string literal — RangeFilesByPackage(
			// "config.v1", …) — which the Go compiler cannot flag: it silently
			// matches nothing once the package moves.
			out = goProtoPackageLiteralRE.ReplaceAllFunc(out, func(m []byte) []byte {
				sub := goProtoPackageLiteralRE.FindSubmatch(m)
				if !bytes.Equal(sub[1], sub[3]) {
					return m
				}
				return []byte(string(sub[1]) + layout.Package() + string(sub[2]) + string(sub[3]))
			})
			// legacyMentions skips a file this run rewrites, so list the
			// mentions the literal rewrite left in it (a file it leaves
			// alone is listed there).
			if !bytes.Equal(out, src) {
				for i, line := range strings.Split(string(out), "\n") {
					for _, re := range legacyPackageRefREs {
						if re.MatchString(line) {
							rep.Next = append(rep.Next, fmt.Sprintf("%s:%d: still names the old proto package config.v1 in a form the codemod cannot rewrite safely — point it at %s", rel, i+1, layout.Package()))
							break
						}
					}
				}
			}
			for _, m := range foreignConfigImportRE.FindAllSubmatch(out, -1) {
				if string(m[1]) != module {
					rep.Next = append(rep.Next, fmt.Sprintf("%s: imports %s/gen/config/v1 — another forge project's config package; once it moves too, that import is %s/gen/<its project>/config/v1", rel, m[1], m[1]))
				}
			}
		default:
			out = rewritePaths(src)
		}
		if !bytes.Equal(out, src) {
			changes = append(changes, configProtoChange{from: rel, to: rel, content: out})
			rep.Rewrites = append(rep.Rewrites, rel+": the config layout's paths → "+layout.Dir())
		}
		return nil
	}); err != nil {
		return rep, err
	}

	stale, err := staleConfigGenDirs(dir)
	if err != nil {
		return rep, err
	}
	for _, d := range stale {
		rep.Rewrites = append(rep.Rewrites, d+"/: stale generated output of the old package removed (forge generate writes gen/"+layout.BufDir()+")")
	}

	if write {
		if err := applyConfigProtoChanges(dir, changes, stale); err != nil {
			return rep, err
		}
	}

	// What is left is prose and config the codemod does not parse: list it.
	rewritten := map[string]bool{}
	for _, c := range changes {
		rewritten[c.from], rewritten[c.to] = true, true
	}
	mentions, err := legacyMentions(dir, rewritten)
	if err != nil {
		return rep, err
	}
	for _, m := range mentions {
		rep.Next = append(rep.Next, m+": still names the old config layout — point it at "+layout.Dir()+" / "+layout.Package())
	}
	rep.Verify = []string{"forge generate", "go build ./...", "forge lint"}
	return rep, nil
}

// configProtoChange is one file the codemod writes: from == to for an
// in-place rewrite, otherwise the file moves.
type configProtoChange struct {
	from, to string // project-relative
	content  []byte
}

// applyConfigProtoChanges writes the changes, removes the legacy directories
// that emptied, and deletes the stale generated output.
func applyConfigProtoChanges(dir string, changes []configProtoChange, stale []string) error {
	for _, c := range changes {
		destAbs := filepath.Join(dir, filepath.FromSlash(c.to))
		if c.from == c.to {
			if err := writeFile(destAbs, c.content); err != nil {
				return err
			}
			continue
		}
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(c.from)))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(destAbs, c.content, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(c.from))); err != nil {
			return err
		}
	}
	removeEmptyDirs(dir, configproto.LegacyDir, "proto/config")
	for _, d := range stale {
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(d))); err != nil {
			return err
		}
		removeEmptyDirs(dir, path.Dir(d), path.Dir(path.Dir(d)))
	}
	return nil
}

// rewritable reports whether rel is a file whose mentions of the old
// layout's paths the codemod rewrites: protos, Go, TypeScript/JavaScript and
// the build, deploy and CI config beside them. Prose (Markdown, text) is
// listed for a person instead — it may be describing the history.
func rewritable(rel string) bool {
	switch path.Ext(rel) {
	case ".proto", ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts",
		".k", ".yaml", ".yml", ".sh", ".mk", ".toml":
		return true
	}
	base := path.Base(rel)
	return base == "Makefile" || base == ".gitattributes" || base == ".gitignore" || base == ".dockerignore" ||
		strings.HasPrefix(base, "Dockerfile")
}

// forgeOwned reports whether src is a file forge writes — generated, or
// forge-owned under a forge:hash marker — which forge generate rewrites and
// an edit would turn into drift.
func forgeOwned(src []byte) bool {
	head := src
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("Code generated")) || bytes.Contains(head, []byte("@generated")) ||
		bytes.Contains(head, []byte("forge:hash="))
}

// walkProjectFiles calls fn with every file under dir whose relative path
// match accepts, skipping what skipDir skips (generated gen/ trees,
// dependencies, fixtures, dot directories) and nested modules.
func walkProjectFiles(dir string, match func(rel string) bool, fn func(rel string, src []byte) error) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if p == dir {
				return nil
			}
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			if _, statErr := os.Stat(filepath.Join(p, "go.mod")); statErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !match(rel) {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return fn(rel, src)
	})
}

// staleConfigGenDirs lists the directories holding generated output of the
// legacy package — gen/config/v1 and every frontend's src/gen/config/v1 —
// whose every file is generated, relative to dir. A directory holding a file
// a person wrote is left alone.
func staleConfigGenDirs(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if p != dir && (name == "node_modules" || name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "gen/config/v1" && !strings.HasSuffix(rel, "/gen/config/v1") {
			return nil
		}
		if allGenerated(p) {
			out = append(out, rel)
		}
		return filepath.SkipDir
	})
	sort.Strings(out)
	return out, err
}

// allGenerated reports whether every file under root carries a generated-code
// marker in its first lines.
func allGenerated(root string) bool {
	ok, seen := true, false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !ok || d.IsDir() {
			return nil //nolint:nilerr // an unreadable file is not proven generated; ok stays as is
		}
		seen = true
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			ok = false
			return nil
		}
		head := src
		if len(head) > 2048 {
			head = head[:2048]
		}
		if !bytes.Contains(head, []byte("Code generated")) && !bytes.Contains(head, []byte("@generated")) {
			ok = false
		}
		return nil
	})
	return ok && seen
}

// removeEmptyDirs removes each named directory (relative to dir), in order,
// if it is empty.
func removeEmptyDirs(dir string, rels ...string) {
	for _, rel := range rels {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if entries, err := os.ReadDir(p); err == nil && len(entries) == 0 {
			_ = os.Remove(p)
		}
	}
}

// legacyRefREs match a mention of the legacy layout: its directories as
// paths, a buf-relative path into it, and a name in its package.
var legacyRefREs = append([]*regexp.Regexp{
	legacyProtoDirRE,
	legacyProtoTreeRE,
	legacyGenDirRE,
	regexp.MustCompile(`(^|[^\w/])config/v1/\w+\.proto`),
}, legacyPackageRefREs...)

// legacyPackageRefREs match a mention of the legacy proto package by name —
// config.v1 on its own, or as the prefix of a qualified name — and not a
// longer package that ends in it (acme.config.v1).
var legacyPackageRefREs = []*regexp.Regexp{
	regexp.MustCompile(`(^|[^\w.])config\.v1\b`),
}

// legacyMentions lists file:line of every remaining mention of the legacy
// layout in the project's text files — prose, and what the rewrite does not
// parse — at most 30. Forge-owned files (generate rewrites them) and the
// files in skip (the ones this run rewrites, by old and new path) are not
// listed.
func legacyMentions(dir string, skip map[string]bool) ([]string, error) {
	var out []string
	textual := func(rel string) bool {
		switch path.Ext(rel) {
		case ".md", ".txt", ".json":
			return true
		}
		return rewritable(rel)
	}
	err := walkProjectFiles(dir, textual, func(rel string, src []byte) error {
		if len(out) >= 30 || skip[rel] || forgeOwned(src) {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, re := range legacyRefREs {
				if re.MatchString(line) {
					out = append(out, fmt.Sprintf("%s:%d", rel, i+1))
					break
				}
			}
			if len(out) >= 30 {
				break
			}
		}
		return nil
	})
	return out, err
}
