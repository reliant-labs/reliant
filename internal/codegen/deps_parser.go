package codegen

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DepsField describes one field of a service's `Deps` struct as parsed
// from handlers/<svc>/service.go (or any non-test .go file in the dir).
//
// The wire_gen codegen consumes these to emit one assignment per field
// in the per-service `wireXxxDeps(app, cfg)` function. Type is the
// pretty-printed Go expression (selector / star / ident / etc.) so
// downstream consumers can do simple string contains checks
// (e.g. "*sql.DB", "orm.Context") without re-walking the AST.
type DepsField struct {
	// Name is the exported Go field name as written in the Deps struct,
	// e.g. "Logger", "Config", "Repo", "Audit", "DB".
	Name string

	// Type is the pretty-printed Go type expression, e.g. "*slog.Logger",
	// "*config.Config", "orm.Context", "*sql.DB",
	// "Repository". Used by wire_gen to emit zero-values when no
	// producer matches and to render TODO comments that name the type
	// the user needs to wire.
	Type string

	// Optional is true when the field's doc / inline comment carries
	// the `// forge:optional-dep` marker. Optional fields are
	// intentionally allowed to be nil at construction time:
	//   - validateDeps should NOT enforce them (the user manages
	//     `if s.deps.X != nil { ... }` per RPC as idiomatic Go).
	//   - wire_gen emits the typed zero silently — no TODO comment,
	//     no contribution to the UNRESOLVED header — when no producer
	//     matches.
	// The marker exists because some Deps fields are legitimately
	// optional (rollback-only NATS publisher, optional gateway
	// features, etc.) and the default "must resolve" treatment forces
	// users to either fake-wire them or drop them from validateDeps
	// entirely. Both defeat the design intent.
	Optional bool
}

// ParseServiceDeps reads handlers/<svc>/<*>.go (skipping test files) and
// returns the ordered list of fields declared on the package-level
// `Deps` struct. Returns an empty slice if the directory doesn't exist
// or no Deps struct is found — caller treats those as "service has no
// rich deps to wire".
//
// Modeled on DetectDepsDBField (same fast AST walk, same skip-test-files
// behavior). Kept as a separate function rather than overloaded so
// DetectDepsDBField stays a constant-time predicate that doesn't have
// to allocate a slice.
func ParseServiceDeps(dir string) ([]DepsField, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		// ParseComments so we can read the `// forge:optional-dep`
		// marker that may sit in the field's doc-comment slot. Without
		// it field.Doc / field.Comment are nil and the marker would be
		// invisible to wire_gen / validateDeps.
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "Deps" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				return collectDepsFields(fset, structType), nil
			}
		}
	}
	return nil, nil
}

// DepsLiteralKeys returns every field name a `<pkg>.Deps{…}` composite literal
// may legally key for the Deps struct declared in dir, plus whether a Deps
// struct was FOUND there at all.
//
// This is a DIFFERENT question from ParseServiceDeps, and the difference is the
// whole point. ParseServiceDeps answers "what must forge WIRE?" — so it drops
// unexported and embedded fields, which forge cannot address. DepsLiteralKeys
// answers "what may the literal legally NAME?" — the full key set, embedded and
// unexported included. compose.go's reconciler uses it to drop a key that is
// provably not a field, and it must never mistake a key forge does not emit
// (an embedded `Base:` the author wired by hand) for a dead one. The wiring set
// is a subset of the key set, so the two can never disagree about a key forge
// itself emitted.
//
// The `found` bool is the guard that keeps the removal case from becoming a
// licence to destroy. An empty key set is AMBIGUOUS: it means either "the Deps
// struct genuinely has no fields" or "the package is mid-edit and did not
// parse" — this walk, like ParseServiceDeps, skips a file it cannot parse. Only
// `found` distinguishes them, and a caller that deletes on the strength of an
// absent field must act only on a set that was positively read.
func DepsLiteralKeys(dir string) (keys map[string]bool, found bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "Deps" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				keys = map[string]bool{}
				for _, f := range structType.Fields.List {
					if len(f.Names) == 0 {
						// Embedded: the literal keys it by the type's base
						// identifier (`Base` for `Base`, `*Base` and `pkg.Base`).
						if name := embeddedFieldName(f.Type); name != "" {
							keys[name] = true
						}
						continue
					}
					for _, n := range f.Names {
						keys[n.Name] = true
					}
				}
				return keys, true
			}
		}
	}
	return nil, false
}

// embeddedFieldName returns the field name Go gives an embedded struct field —
// the base identifier of the type, with any pointer and package qualifier
// stripped. Returns "" for a shape that cannot be embedded.
func embeddedFieldName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return embeddedFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr: // generic instantiation: Base[T]
		return embeddedFieldName(t.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// HasOptionalDepMarker returns true if any line in the comment text
// is *exactly* the `forge:optional-dep` directive (after stripping
// leading slashes / whitespace and trailing whitespace). The strict
// match — directive must be the whole line, not embedded inside
// surrounding prose — prevents documentation that references the
// marker (e.g. an example block in the scaffolded service.go that
// says "tag it with the `// forge:optional-dep` marker on the line
// above") from being interpreted as the marker itself.
//
// Exported so the lint rule (forgeconv-optional-dep-marker-position)
// can share the exact same recognition logic the parser uses — the
// rule is "the marker is on a Deps field; anywhere else is a typo /
// misuse" and the lint check needs to scan for the marker in places
// it shouldn't be.
//
// IMPORTANT: callers parsing an *ast.CommentGroup should prefer
// HasOptionalDepMarkerCommentGroup over passing `cg.Text()` here. Go's
// ast.CommentGroup.Text() silently drops `//directive` form comments
// (the no-space variant — same shape as `//go:generate`,
// `//go:noinline`, etc.) which means `//forge:optional-dep` written
// without a space would be invisible to this function when fed via
// .Text(). HasOptionalDepMarkerCommentGroup iterates cg.List directly
// and recognizes both spaced (`// forge:optional-dep`) and unspaced
// (`//forge:optional-dep`) forms, matching the user's intent.
func HasOptionalDepMarker(text string) bool {
	const needle = "forge:optional-dep"
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.Trim(line, "/ \t")
		if trimmed == needle {
			return true
		}
	}
	return false
}

// HasOptionalDepMarkerCommentGroup returns true when any comment in
// the group is the `forge:optional-dep` marker. Unlike
// HasOptionalDepMarker (which takes the post-.Text() string), this
// helper inspects the raw c.Text of each *ast.Comment in cg.List, so
// it recognizes both the spaced form (`// forge:optional-dep`) and
// the unspaced directive form (`//forge:optional-dep`).
//
// The unspaced form is otherwise invisible to ast.CommentGroup.Text()
// because Go treats `//<no-space><alnum>` as a compiler/linter
// directive (same shape as `//go:generate`, `//go:noinline`,
// `//nolint:...`) and strips it from the joined text. That stripping
// is a silent footgun for forge users who omit the space — the marker
// looks present in source but the parser would never see it, so the
// field would be treated as required, validateDeps would reject nil,
// and the user's "I marked it optional" intent would be ignored.
//
// Both shapes are accepted; the marker must still be the whole comment
// (after stripping `//`, `/*`, `*/`, and leading/trailing whitespace).
// A nil group returns false. Inline /* … */ blocks are honored too —
// the parser logic is symmetric across both Comment kinds.
func HasOptionalDepMarkerCommentGroup(cg *ast.CommentGroup) bool {
	if cg == nil {
		return false
	}
	const needle = "forge:optional-dep"
	for _, c := range cg.List {
		if trimCommentMarkers(c.Text) == needle {
			return true
		}
	}
	return false
}

// trimCommentMarkers strips the comment-syntax wrappers from a raw
// ast.Comment.Text value, leaving the inner text trimmed of leading and
// trailing whitespace. Handles both `// ...` line comments (including
// the directive form `//foo`) and `/* ... */` block comments.
//
// Unlike ast.CommentGroup.Text(), this function does not classify any
// shape as a directive to be dropped — it returns the inner content
// verbatim (minus whitespace) so callers can apply their own exact-match
// logic against forge marker spellings.
func trimCommentMarkers(raw string) string {
	switch {
	case strings.HasPrefix(raw, "//"):
		return strings.TrimSpace(strings.TrimPrefix(raw, "//"))
	case strings.HasPrefix(raw, "/*"):
		inner := strings.TrimPrefix(raw, "/*")
		inner = strings.TrimSuffix(inner, "*/")
		return strings.TrimSpace(inner)
	default:
		return strings.TrimSpace(raw)
	}
}

// ExtractPlaceholderType returns the target type from a
// `forge:placeholder: <Type>` comment marker, or "" when no marker
// is present.
//
// Recognition is line-based after stripping leading `//`, leading/
// trailing whitespace; the marker must be the whole line:
//
//	// forge:placeholder: user.Repository
//	UserRepo any
//
// Or the inline-comment slot:
//
//	UserRepo any // forge:placeholder: user.Repository
//
// Both forms are accepted — same convention as `// forge:optional-dep`.
// The colon is required (it carries data, unlike optional-dep which
// is a bare directive). Whitespace after the colon is tolerated.
//
// Exported so the wire-coverage lint can share the exact same
// recognition logic — the rule is "if the field has a placeholder
// AND the field's declared type is `any`, the placeholder is
// unresolved and lint must error."
func ExtractPlaceholderType(text string) string {
	const prefix = "forge:placeholder:"
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.Trim(line, "/ \t")
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		typ := strings.TrimSpace(trimmed[len(prefix):])
		// Tolerate `forge:placeholder: "user.Repository"` (matches
		// the struct-tag spelling) so users can mentally cargo-cult
		// from one shape to the other without being punished.
		typ = strings.Trim(typ, `"`)
		if typ == "" {
			continue
		}
		return typ
	}
	return ""
}

// ExtractPlaceholderTypeCommentGroup is the AST-level analog of
// ExtractPlaceholderType. Same parity reason as
// HasOptionalDepMarkerCommentGroup: Go's CommentGroup.Text() drops the
// no-space directive form, so a comment written as
// `//forge:placeholder: user.Repository` (without the space) would be
// silently invisible to the .Text()-based scan. Iterating cg.List and
// trimming the raw markers preserves both shapes.
//
// Returns the placeholder target type, or "" when no marker is present.
func ExtractPlaceholderTypeCommentGroup(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	const prefix = "forge:placeholder:"
	for _, c := range cg.List {
		trimmed := trimCommentMarkers(c.Text)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		typ := strings.TrimSpace(trimmed[len(prefix):])
		typ = strings.Trim(typ, `"`)
		if typ == "" {
			continue
		}
		return typ
	}
	return ""
}

// extractPlaceholderTag reads a struct tag's `forge:placeholder:"<Type>"`
// value, mirroring ExtractPlaceholderType for the struct-tag shape.
// Returns "" when no tag, no `forge:placeholder` key, or an empty value.
//
// The struct-tag form is the second of two accepted shapes — the
// comment shape is preferred (matches `// forge:optional-dep`), but
// users coming from the `protobuf:` / `json:` tag world reach for the
// tag form first. Honoring both shapes costs ~10 lines and saves a
// foot-gun.
//
// Note: Go's standard `reflect.StructTag.Lookup` splits on the first
// colon in a key, so it can't directly retrieve a key literally named
// `forge:placeholder`. We do a small bespoke scan instead — search for
// the literal `forge:placeholder:"<value>"` substring inside the raw
// tag text. The shape is narrow enough that a regex is overkill.
func extractPlaceholderTag(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return ""
	}
	const key = `forge:placeholder:"`
	idx := strings.Index(raw, key)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(key):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// collectDepsFields flattens the Deps struct into one DepsField per
// declared name. A single field declaration like `A, B *slog.Logger`
// emits two fields with the same Type. Anonymous (embedded) fields are
// skipped — wire_gen wouldn't know what to name on the assignment side.
//
// The Optional bit is set when either field.Doc (the comment block
// directly above the field) or field.Comment (the inline trailing
// comment on the same line) contains `// forge:optional-dep`. Multi-
// name field declarations (`A, B *T`) propagate the marker to every
// name — they share the same comment slot.
func collectDepsFields(fset *token.FileSet, st *ast.StructType) []DepsField {
	var out []DepsField
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			continue
		}
		typeStr := printType(fset, field.Type)
		// AST-level marker check — see HasOptionalDepMarkerCommentGroup
		// for why we don't go through field.Doc.Text() / field.Comment.Text().
		// Short version: Go's CommentGroup.Text() silently strips
		// `//forge:optional-dep` (no space after `//`) because it parses
		// it as a Go directive, the same way `//go:generate` is dropped.
		// Iterating the raw cg.List recovers both spaced and unspaced
		// forms so users who omit the space don't get silently ignored.
		optional := HasOptionalDepMarkerCommentGroup(field.Doc) ||
			HasOptionalDepMarkerCommentGroup(field.Comment)
		for _, name := range field.Names {
			if !ast.IsExported(name.Name) {
				continue
			}
			out = append(out, DepsField{
				Name:     name.Name,
				Type:     typeStr,
				Optional: optional,
			})
		}
	}
	return out
}

// printType pretty-prints an ast.Expr as the Go source it came from.
// Uses go/printer so selector expressions ("orm.Context") and pointer
// types ("*slog.Logger") render verbatim instead of needing a custom
// switch over every possible Expr kind.
func printType(fset *token.FileSet, expr ast.Expr) string {
	var buf strings.Builder
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}

// AppField describes one exported field on the project-generated *App
// struct (pkg/app/bootstrap.go) plus any user-extension files in the
// same package. wire_gen consumes these to resolve unconventional
// service Deps fields by name → app.<Field>.
//
// Type is parallel to DepsField.Type so callers can do an exact
// string-equal match when they want to be conservative, or a contains
// check when they want to tolerate alias differences.
type AppField struct {
	Name string
	Type string

	// Placeholder is the target type the AppExtras field should be
	// tightened to once a sibling lane lands the real type. Set when
	// the field carries a `// forge:placeholder: <Type>` doc/inline
	// comment marker. Empty for fields without the marker.
	//
	// The annotation exists because parallel agents writing into
	// app_extras.go often need to declare a field whose typed Repository /
	// Client / Provider lives in a sibling lane that hasn't merged yet.
	// Typing the field as `any` keeps the build green; the placeholder
	// marker says "I know this is `any` — wire_gen should treat me as
	// <Type>, and lint should error until I'm tightened to <Type>".
	//
	// wire_gen renders a typed `resolve<Field>(app)` accessor when the
	// marker is present so call sites can consume the field as the
	// promised type rather than `any`. When the user finally tightens
	// the field declaration from `any` to <Type>, the accessor stays
	// correct (the type assertion is a no-op for a value already typed
	// as <Type>).
	Placeholder string
}

// ParseAppFields walks every non-test .go file in pkg/app and returns
// the union of fields reachable as `app.<Name>` on a *App. The set
// includes:
//
//   - Direct exported fields of the `App` struct itself (forge-owned in
//     pkg/app/app_gen.go: Services, Workers, Operators, Packages, DB,
//     ORM).
//   - Direct exported fields of the `AppExtras` struct (user-owned in
//     pkg/app/app_extras.go). AppExtras is embedded into App as a
//     pointer; Go's field promotion rules make those fields reachable
//     via `app.<Field>` at the call site even though they live on a
//     different struct. wire_gen treats them identically.
//
// AppExtras fields may carry a `// forge:placeholder: <Type>` doc or
// inline comment marker, which surfaces as AppField.Placeholder. See
// the AppField docstring for the why.
//
// Anonymous (embedded) fields on App are skipped from the result —
// they don't have a usable selector name on their own, and the only
// embedded type we generate (*AppExtras) is unwrapped above.
//
// Returns an empty slice if pkg/app doesn't exist yet (initial scaffold
// path) — caller treats that as "wire_gen has nothing to look up by
// name" and falls back to the conventional set.
func ParseAppFields(appDir string) ([]AppField, error) {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Collect by struct name across all files in pkg/app, then unify.
	// This is the cleanest way to support the "App in app_gen.go +
	// AppExtras in app_extras.go" split without picking a file order.
	byStruct := map[string][]AppField{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(appDir, entry.Name())
		// ParseComments so we can read the `// forge:placeholder: <Type>`
		// marker from doc-comment / inline-comment slots on AppExtras
		// fields. Without it field.Doc / field.Comment are nil and the
		// marker would be invisible.
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if typeSpec.Name.Name != "App" && typeSpec.Name.Name != "AppExtras" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					if len(field.Names) == 0 {
						// Anonymous/embedded — skip. The only one we
						// expect on App is *AppExtras, whose fields
						// we collect from the AppExtras struct itself.
						continue
					}
					typeStr := printType(fset, field.Type)
					// Only AppExtras fields are user-extendable, so
					// the placeholder marker is meaningful only there.
					// Reading from forge-owned App would invite a
					// mistake (a placeholder on a forge-managed field
					// wouldn't be honored across regenerates).
					placeholder := ""
					if typeSpec.Name.Name == "AppExtras" {
						// AST-level marker check — same parity reason as
						// HasOptionalDepMarkerCommentGroup. .Text()
						// strips `//forge:placeholder:` (no space) as a
						// Go directive; iterating the raw cg.List
						// recovers both spaced and unspaced forms.
						placeholder = ExtractPlaceholderTypeCommentGroup(field.Doc)
						if placeholder == "" {
							placeholder = ExtractPlaceholderTypeCommentGroup(field.Comment)
						}
						if placeholder == "" {
							placeholder = extractPlaceholderTag(field.Tag)
						}
					}
					for _, name := range field.Names {
						if !ast.IsExported(name.Name) {
							continue
						}
						byStruct[typeSpec.Name.Name] = append(
							byStruct[typeSpec.Name.Name],
							AppField{
								Name:        name.Name,
								Type:        typeStr,
								Placeholder: placeholder,
							},
						)
					}
				}
			}
		}
	}

	// Merge App + AppExtras. App fields take precedence on collision —
	// a forge-owned field name (e.g. "DB") shouldn't be shadowed by an
	// accidental same-name field on AppExtras.
	seen := map[string]bool{}
	var fields []AppField
	for _, f := range byStruct["App"] {
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		fields = append(fields, f)
	}
	for _, f := range byStruct["AppExtras"] {
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		fields = append(fields, f)
	}
	return fields, nil
}
