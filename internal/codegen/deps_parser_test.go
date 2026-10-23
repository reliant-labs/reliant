// File: internal/codegen/deps_parser_test.go
//
// Tests for the optional-dep / placeholder marker recognition. The
// regression these tests pin: Go's ast.CommentGroup.Text() silently
// drops comments whose shape is `//<no-space><alnum>` because the
// parser treats them as Go directives (`//go:generate`, `//go:noinline`,
// `//nolint:...`). Forge markers written without the space —
// `//forge:optional-dep`, `//forge:placeholder: ...` — fall into the
// same bucket and are stripped before they ever reach the parser. The
// .Text()-based recognizers were silently blind to that form, so any
// developer who happened to omit the space had their marker ignored
// (validateDeps would reject the field as required even though the
// developer marked it optional; wire_gen would emit a TODO instead of
// the typed accessor for placeholder fields).
//
// The fix added *CommentGroup variants that iterate cg.List directly
// and recognize both spaced and unspaced forms. These tests pin both
// the AST helpers and the higher-level ParseServiceDeps /
// ParseAppFields paths that consume them.

package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseFieldDocs builds a slice of CommentGroups from the package-level
// `Deps` struct fields in src. Used by the AST helper tests so each
// case is a single struct field with its doc / inline comment.
func parseFieldDocs(t *testing.T, src string) []*ast.CommentGroup {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out []*ast.CommentGroup
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			if ts.Name.Name != "Deps" {
				continue
			}
			st := ts.Type.(*ast.StructType)
			for _, f := range st.Fields.List {
				out = append(out, f.Doc, f.Comment)
			}
		}
	}
	return out
}

// TestHasOptionalDepMarkerCommentGroup pins the AST-level recognizer.
// The unspaced-directive form (`//forge:optional-dep`) is the bug-fix
// case — the legacy `.Text()`-based scan silently dropped it because
// Go's parser treats it as a directive.
func TestHasOptionalDepMarkerCommentGroup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "spaced form (canonical)",
			src: `package x
type Deps struct {
	// forge:optional-dep
	A int
}`,
			want: true,
		},
		{
			name: "unspaced directive form — Go strips via .Text(), AST scan must recover",
			src: `package x
type Deps struct {
	//forge:optional-dep
	A int
}`,
			want: true,
		},
		{
			name: "trailing inline spaced",
			src: `package x
type Deps struct {
	A int // forge:optional-dep
}`,
			want: true,
		},
		{
			name: "trailing inline unspaced — same .Text() bug",
			src: `package x
type Deps struct {
	A int //forge:optional-dep
}`,
			want: true,
		},
		{
			name: "block-comment form",
			src: `package x
type Deps struct {
	/* forge:optional-dep */
	A int
}`,
			want: true,
		},
		{
			name: "documentation prose mentioning marker — must NOT match",
			src: `package x
type Deps struct {
	// Tag this field with the // forge:optional-dep marker if optional.
	A int
}`,
			want: false,
		},
		{
			name: "near-miss spelling — must NOT match",
			src: `package x
type Deps struct {
	// forge:optional
	A int
}`,
			want: false,
		},
		{
			name: "extension prefix — must NOT match (whole-line rule)",
			src: `package x
type Deps struct {
	// forge:optional-dep-extra
	A int
}`,
			want: false,
		},
		{
			name: "no doc comment at all",
			src: `package x
type Deps struct {
	A int
}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			groups := parseFieldDocs(t, tc.src)
			got := false
			for _, g := range groups {
				if HasOptionalDepMarkerCommentGroup(g) {
					got = true
					break
				}
			}
			if got != tc.want {
				t.Errorf("HasOptionalDepMarkerCommentGroup() = %v, want %v\nsource:\n%s", got, tc.want, tc.src)
			}
		})
	}
}

// TestHasOptionalDepMarkerCommentGroup_NilSafe — nil CommentGroup is
// the common case for fields without a doc comment slot; helper must
// not panic.
func TestHasOptionalDepMarkerCommentGroup_NilSafe(t *testing.T) {
	t.Parallel()
	if HasOptionalDepMarkerCommentGroup(nil) {
		t.Errorf("nil CommentGroup must return false, not panic")
	}
}

// TestExtractPlaceholderTypeCommentGroup pins the same parity for the
// `forge:placeholder: <Type>` marker. Same .Text()-drops-directive
// failure mode; same AST-iteration fix.
func TestExtractPlaceholderTypeCommentGroup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "spaced form",
			src: `package x
type Deps struct {
	// forge:placeholder: user.Repository
	Repo any
}`,
			want: "user.Repository",
		},
		{
			name: "unspaced directive form — bug-fix case",
			src: `package x
type Deps struct {
	//forge:placeholder: user.Repository
	Repo any
}`,
			want: "user.Repository",
		},
		{
			name: "quoted target (struct-tag-style)",
			src: `package x
type Deps struct {
	// forge:placeholder: "user.Repository"
	Repo any
}`,
			want: "user.Repository",
		},
		{
			name: "no marker",
			src: `package x
type Deps struct {
	Repo any
}`,
			want: "",
		},
		{
			name: "marker without value — ignored",
			src: `package x
type Deps struct {
	// forge:placeholder:
	Repo any
}`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			groups := parseFieldDocs(t, tc.src)
			var got string
			for _, g := range groups {
				if v := ExtractPlaceholderTypeCommentGroup(g); v != "" {
					got = v
					break
				}
			}
			if got != tc.want {
				t.Errorf("ExtractPlaceholderTypeCommentGroup() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseServiceDeps_UnspacedOptionalMarker is the end-to-end pin:
// a real handler-shaped Deps struct with the unspaced marker must
// surface the Optional bit set to true. This is the symptom users hit
// — a typo'd marker silently changed validateDeps' decision and the
// field was treated as required, breaking bootstrap.
func TestParseServiceDeps_UnspacedOptionalMarker(t *testing.T) {
	t.Parallel()

	src := `package daemon

import (
	"log/slog"
)

// Deps carries the daemon service's runtime dependencies.
type Deps struct {
	Logger *slog.Logger

	// NATSPublisher publishes domain events; nil disables rollback.
	//forge:optional-dep
	NATSPublisher EventPublisher
}

type EventPublisher interface{}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	fields, err := ParseServiceDeps(dir)
	if err != nil {
		t.Fatalf("ParseServiceDeps: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %d: %+v", len(fields), fields)
	}
	var natsField *DepsField
	for i := range fields {
		if fields[i].Name == "NATSPublisher" {
			natsField = &fields[i]
			break
		}
	}
	if natsField == nil {
		t.Fatalf("NATSPublisher field missing: %+v", fields)
	}
	if !natsField.Optional {
		t.Errorf("NATSPublisher.Optional = false; want true (unspaced //forge:optional-dep marker must be recognized)")
	}
}

// TestParseServiceDeps_BothMarkerForms — sanity that the spaced form
// still works post-fix (regression guard for the back-compat case).
func TestParseServiceDeps_BothMarkerForms(t *testing.T) {
	t.Parallel()

	src := `package daemon

type Deps struct {
	// A is unspaced.
	//forge:optional-dep
	A int

	// B is spaced.
	// forge:optional-dep
	B int

	// C is unmarked.
	C int

	// D is trailing-inline unspaced.
	D int //forge:optional-dep

	// E is trailing-inline spaced.
	E int // forge:optional-dep
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	fields, err := ParseServiceDeps(dir)
	if err != nil {
		t.Fatalf("ParseServiceDeps: %v", err)
	}

	want := map[string]bool{"A": true, "B": true, "C": false, "D": true, "E": true}
	got := map[string]bool{}
	for _, f := range fields {
		got[f.Name] = f.Optional
	}
	for name, wantOpt := range want {
		if got[name] != wantOpt {
			t.Errorf("field %s.Optional = %v, want %v", name, got[name], wantOpt)
		}
	}
}

// TestParseAppFields_UnspacedPlaceholderMarker — same regression guard
// for the placeholder marker. cp-forge has multiple AppExtras fields
// carrying the placeholder annotation; a typo that drops the space
// would silently break wire_gen's resolver emission.
func TestParseAppFields_UnspacedPlaceholderMarker(t *testing.T) {
	t.Parallel()

	src := `package app

type AppExtras struct {
	// Repo is the database repository.
	//forge:placeholder: user.Repository
	Repo any
}

type App struct {
	*AppExtras
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app_extras.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	fields, err := ParseAppFields(dir)
	if err != nil {
		t.Fatalf("ParseAppFields: %v", err)
	}
	var repo *AppField
	for i := range fields {
		if fields[i].Name == "Repo" {
			repo = &fields[i]
			break
		}
	}
	if repo == nil {
		t.Fatalf("Repo field missing: %+v", fields)
	}
	if repo.Placeholder != "user.Repository" {
		t.Errorf("Repo.Placeholder = %q, want %q (unspaced //forge:placeholder: marker must be recognized)",
			repo.Placeholder, "user.Repository")
	}
}

// TestTrimCommentMarkers exercises the small helper directly — both
// comment kinds, both with and without inner whitespace.
func TestTrimCommentMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want string
	}{
		{"// hello", "hello"},
		{"//hello", "hello"},
		{"//forge:optional-dep", "forge:optional-dep"},
		{"// forge:optional-dep", "forge:optional-dep"},
		{"/* hello */", "hello"},
		{"/*hello*/", "hello"},
		{"/* forge:placeholder: foo.Bar */", "forge:placeholder: foo.Bar"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := trimCommentMarkers(tc.raw); got != tc.want {
			t.Errorf("trimCommentMarkers(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// Sanity check the doc claim about .Text() — kept here so a future
	// reader can see the bug shape inline rather than chase Go internals.
	src := "package x\nfunc F() {\n\t//forge:optional-dep\n\tvar _ = 0\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse sanity src: %v", err)
	}
	for _, cg := range f.Comments {
		if strings.Contains(cg.Text(), "forge:optional-dep") {
			t.Errorf("regression: Go's CommentGroup.Text() now preserves //directive form — review whether the AST helper is still required")
		}
	}
}
