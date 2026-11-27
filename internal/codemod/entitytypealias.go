package codemod

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// The v1.11.0 entity type alias: a generated list page imported its entity's
// message type under the entity's own name, `import type { Link } from
// "@/gen/…_pb"`. An entity named like anything else the page imports — next/link's
// Link, @tanstack/react-router's Link, the component library's Button or
// Resource — declared that identifier twice, and the frontend stopped
// type-checking the moment the entity was scaffolded. From v1.11.0 a page binds
// the type under naming.EntityTypeLocal (`import type { Link as LinkEntity }`).
//
// Pages are scaffold-once, so a project scaffolded earlier keeps the broken
// import. This is the playbook's (migrations/v1.11.0/entity-type-alias)
// mechanical half. It edits only a file that has the collision — a type-only
// import from a protobuf-es module (`…_pb`) binding a name another import of
// the same file also binds — and in it:
//
//   - the specifier becomes `X as XEntity`;
//   - every use of X as a type argument (`ResourceColumn<X>`, `<Resource<X>`,
//     `Foo<X, Y>`, `Foo<A, X>`) and as an array type (`X[]`) becomes XEntity;
//     JSX (`<X …>`, `</X>`) is the other import's and stays.
//
// Every remaining reference (including values and JSX) is reported with its
// file:line, as the edit to make by hand when it names the entity type. A file whose alias
// is already bound by something else is left alone and reported. Re-running
// it changes nothing: a fixed file no longer has the collision.

func init() {
	register(Codemod{
		Name:    "entity-type-alias",
		Version: "v1.11.0",
		Short:   "Alias entity type imports that collide with another import (`import type { Link as LinkEntity }`) in pages scaffolded before v1.11.0",
		Long: `Repair generated pages whose entity type import collides with another import
of the same file: an entity named Link, Image, Head, Script, Form, Button,
Resource, or anything else a page imports for itself, made the scaffolded page
declare that identifier twice (` + "`import type { Link }`" + ` beside
` + "`import Link from \"next/link\"`" + `), so the frontend failed to type-check.

Since v1.11.0 a page binds the entity type under a suffixed name
(` + "`import type { Link as LinkEntity }`" + `). Pages are scaffolded once and are
yours, so this rewrites the ones written earlier: in every .ts/.tsx file under a
frontend's src/ (the project root and each frontends/<name>) that has the
collision, the type import gets the alias, and the type's uses as a type argument
(` + "`ResourceColumn<Link>`, `<Resource<Link>`" + `) and array type (` + "`Link[]`" + `) are
renamed. JSX such as ` + "`<Link href=…>`" + ` belongs to the other import and is
not touched. Every remaining reference (including values and JSX) is listed
with its file:line for you to check: if it means the entity type, write the
alias; otherwise leave it. Resolve this first-run report and run tsc before
considering the repair complete. Files without the collision are left exactly as they
are. Re-running it changes nothing.`,
		Scoped: true,
		Run:    runEntityTypeAlias,
	})
}

var (
	// tsImportRe matches one import statement, single- or multi-line, that
	// binds names (a side-effect import has no `from`). Group 1 is `type `
	// for a type-only import, 2 the binding clause, 3 the module.
	tsImportRe = regexp.MustCompile(`(?ms)^[ \t]*import\s+(type\s+)?([^;'"]*?)\s+from\s+["']([^"']+)["'];?`)
	tsIdentRe  = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
	// pbModuleRe is a protobuf-es module: what forge's pages import entity
	// types from (@/gen/…/x_pb, or a relative path to it).
	pbModuleRe = regexp.MustCompile(`_pb(\.js)?$`)
)

// tsImport is one parsed import statement.
type tsImport struct {
	start, end int // byte span of the statement in the file
	typeOnly   bool
	module     string
	// named are the `{ … }` specifiers as written ("A", "B as C",
	// "type D"); named is nil when the clause has no braces.
	named []string
	// braceStart/braceEnd are the span of the text between the braces,
	// relative to the file.
	braceStart, braceEnd int
	// locals are the names the statement binds.
	locals []string
}

// parseTSImports reads every import statement of src.
func parseTSImports(src string) []tsImport {
	var out []tsImport
	for _, m := range tsImportRe.FindAllStringSubmatchIndex(src, -1) {
		imp := tsImport{start: m[0], end: m[1], typeOnly: m[2] >= 0, module: src[m[6]:m[7]]}
		clause := src[m[4]:m[5]]
		rest := clause
		if i := strings.Index(clause, "{"); i >= 0 {
			j := strings.LastIndex(clause, "}")
			if j < i {
				continue // not a clause this parser reads; bind nothing
			}
			imp.braceStart, imp.braceEnd = m[4]+i+1, m[4]+j
			for _, spec := range strings.Split(clause[i+1:j], ",") {
				if strings.TrimSpace(spec) == "" {
					continue
				}
				imp.named = append(imp.named, spec)
				local := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(spec), "type "))
				if k := strings.Index(local, " as "); k >= 0 {
					local = strings.TrimSpace(local[k+4:])
				}
				imp.locals = append(imp.locals, local)
			}
			rest = clause[:i]
		}
		rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), ","))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "* as "))
		if tsIdentRe.MatchString(rest) {
			imp.locals = append(imp.locals, rest)
		}
		out = append(out, imp)
	}
	return out
}

// entityAliasEdit is the rewrite of one file.
type entityAliasEdit struct {
	content string
	// renamed are the collisions fixed, "Link → LinkEntity (2 type uses)".
	renamed []string
	// next are the uses left to check, and the collisions not fixed.
	next []string
}

// aliasCollidingEntityTypes rewrites src (the file at rel). It returns nil
// when the file has no collision.
func aliasCollidingEntityTypes(rel, src string) *entityAliasEdit {
	imports := parseTSImports(src)
	bound := map[string]int{}
	for _, imp := range imports {
		for _, l := range imp.locals {
			bound[l]++
		}
	}
	type target struct {
		imp  int // index into imports
		spec int // index into imports[imp].named
		name string
	}
	var targets []target
	for i, imp := range imports {
		if !imp.typeOnly || !pbModuleRe.MatchString(imp.module) {
			continue
		}
		for j, spec := range imp.named {
			name := strings.TrimSpace(spec)
			if tsIdentRe.MatchString(name) && bound[name] > 1 {
				targets = append(targets, target{imp: i, spec: j, name: name})
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	edit := &entityAliasEdit{}
	// Rewrite the specifiers back to front so earlier offsets stay valid.
	out := src
	var names []string
	for k := len(targets) - 1; k >= 0; k-- {
		tg := targets[k]
		local := naming.EntityTypeLocal(tg.name)
		if bound[local] > 0 {
			edit.next = append(edit.next, fmt.Sprintf("%s: %s is imported twice, and its alias %s is already bound in the file; rename one import by hand", rel, tg.name, local))
			continue
		}
		imp := imports[tg.imp]
		specs := append([]string(nil), imp.named...)
		specs[tg.spec] = strings.Replace(specs[tg.spec], tg.name, tg.name+" as "+local, 1)
		imports[tg.imp].named = specs
		names = append(names, tg.name)
	}
	if len(names) == 0 {
		return edit
	}
	// Re-emit each edited import's brace content, back to front.
	for i := len(imports) - 1; i >= 0; i-- {
		imp := imports[i]
		if imp.named == nil {
			continue
		}
		joined := strings.Join(imp.named, ",")
		if joined != out[imp.braceStart:imp.braceEnd] {
			out = out[:imp.braceStart] + joined + out[imp.braceEnd:]
		}
	}
	sort.Strings(names)
	for _, name := range names {
		local := naming.EntityTypeLocal(name)
		var n int
		out, n = renameTypeUses(out, name, local)
		edit.renamed = append(edit.renamed, fmt.Sprintf("%s → %s (%d type use(s))", name, local, n))
		edit.next = append(edit.next, ambiguousUses(rel, out, name, local)...)
	}
	edit.content = out
	return edit
}

// typeUseRes are the positions a name can only hold as a type, never as a
// JSX element or a value: a type argument (the `<` follows an identifier, so
// it is not JSX), or an array type.
func typeUseRes(name string) []*regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return []*regexp.Regexp{
		regexp.MustCompile(`([\w$]<\s*)` + q + `(\s*[>,\[])`),            // Foo<X>, Foo<X, Y>, Foo<X[]>
		regexp.MustCompile(`([\w$]<[^<>;=()]*,\s*)` + q + `(\s*[>,\[])`), // Foo<A, X>
		regexp.MustCompile(`(^|[^\w$.])` + q + `(\[\])`),                 // X[]
	}
}

// renameTypeUses renames name to local wherever it is used as a type, outside
// import statements, and counts the uses renamed.
func renameTypeUses(src, name, local string) (string, int) {
	total := 0
	for _, re := range typeUseRes(name) {
		// A match can end where the next one starts (`Foo<X, X>`), so
		// apply each pattern until it no longer matches.
		for {
			n := 0
			src = replaceOutsideImports(src, re, func(m []string) string {
				n++
				return m[1] + local + m[2]
			})
			total += n
			if n == 0 {
				break
			}
		}
	}
	return src, total
}

// replaceOutsideImports applies re to every part of src that is not an
// import statement.
func replaceOutsideImports(src string, re *regexp.Regexp, repl func([]string) string) string {
	var b strings.Builder
	last := 0
	sub := func(seg string) string {
		return re.ReplaceAllStringFunc(seg, func(s string) string { return repl(re.FindStringSubmatch(s)) })
	}
	for _, imp := range parseTSImports(src) {
		b.WriteString(sub(src[last:imp.start]))
		b.WriteString(src[imp.start:imp.end])
		last = imp.end
	}
	b.WriteString(sub(src[last:]))
	return b.String()
}

// ambiguousUses lists every remaining reference, not just a selection of
// type-looking patterns. TypeScript has more type positions than this regex
// codemod can parse (aliases, conditional types, tuples, nested generics, ...).
// A conservative report may include the other import's values or JSX, but must
// never silently declare an unsupported type reference repaired.
func ambiguousUses(rel, src, name, local string) []string {
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`(^|[^\w$])` + q + `([^\w$]|$)`)
	inImport := make([]bool, len(src)+1)
	for _, imp := range parseTSImports(src) {
		for i := imp.start; i < imp.end; i++ {
			inImport[i] = true
		}
	}
	var out []string
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if !re.MatchString(line) {
			continue
		}
		if off := lineOffset(src, i); off < len(inImport) && inImport[off] {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d: %#q — if %s here is the entity type, write %s", rel, i+1, trimmed, name, local))
	}
	return out
}

// lineOffset is the byte offset of line i (0-based) of src.
func lineOffset(src string, i int) int {
	off := 0
	for ; i > 0; i-- {
		j := strings.IndexByte(src[off:], '\n')
		if j < 0 {
			return len(src)
		}
		off += j + 1
	}
	return off
}

func runEntityTypeAlias(dir string, opts Options) (Report, error) {
	var rep Report
	frontends, err := zodFrontendDirs(dir)
	if err != nil {
		return rep, err
	}
	for _, fe := range frontends {
		src := filepath.Join(dir, filepath.FromSlash(fe), "src")
		err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == src {
					return nil // no src/: nothing to repair
				}
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "gen":
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !(strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".ts")) ||
				strings.HasSuffix(name, ".d.ts") || strings.Contains(name, "_gen.") {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			rel = filepath.ToSlash(rel)
			if !opts.Scope.Contains(rel) {
				return nil
			}
			b, err := os.ReadFile(path) //nolint:gosec // a project source file
			if err != nil {
				return err
			}
			edit := aliasCollidingEntityTypes(rel, string(b))
			if edit == nil {
				return nil
			}
			rep.Next = append(rep.Next, edit.next...)
			if len(edit.renamed) == 0 {
				return nil
			}
			if opts.Write {
				if err := writeFile(path, []byte(edit.content)); err != nil {
					return err
				}
			}
			rep.Rewrites = append(rep.Rewrites, rel+": "+strings.Join(edit.renamed, ", "))
			return nil
		})
		if err != nil {
			return rep, err
		}
	}
	if rep.Changed() || len(rep.Next) > 0 {
		rep.Verify = []string{"npx tsc --noEmit"}
	}
	return rep, nil
}
