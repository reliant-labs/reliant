package codegen

import (
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// These render component_test_helpers.go.tmpl with componentTestHelperData,
// the type writeComponentTestHelpers renders it with, so they cannot drift
// from the template's input. They lived in internal/templates with a
// hand-built struct whose element types lacked fields the template reads
// (Optional, RealStore, Reason, ...), which only stayed green because those
// slices were empty (internal/tmplfields).

// TestComponentTestHelpersTemplate_MinimalService verifies that
// component_test_helpers.go.tmpl renders valid Go for the leanest component
// shape: a service with no DB, no migrations, no auto-stubs, no func seams.
//
// This replaces TestBootstrapTestingTemplate_ZeroServices, which pinned the
// zero-COMPONENT state of the retired single-file harness (where every
// helper, and the `testing` import itself, had to be gated on "are there any
// components at all"). One file per component makes that state
// unrepresentable: a component's helper file exists only because the
// component does, so the file always has exactly one component to serve.
func TestComponentTestHelpersTemplate_MinimalService(t *testing.T) {
	data := componentTestHelperData{
		Module:                 "example.com/myproject",
		Package:                "order",
		Name:                   "order",
		FieldName:              "Order",
		IsService:              true,
		ConstructorName:        "New",
		ConnectServiceGoName:   "OrderService",
		ProtoConnectImportPath: "example.com/myproject/gen/services/order/v1/orderv1connect",
		ProtoConnectPkg:        "orderv1connect",
		MountMethod:            "Register",
	}

	content, err := templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", data)
	if err != nil {
		t.Fatalf("Render component_test_helpers.go.tmpl: %v", err)
	}
	rendered := string(content)

	// The package clause is the COMPONENT's own, not <pkg>_test: Go compiles
	// in-package _test.go files INTO the package, which is what lets both
	// internal and external test files in this dir use these helpers.
	if !strings.Contains(rendered, "package order\n") {
		t.Fatalf("helpers must be in `package order`, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "type TestOption func") {
		t.Fatal("missing TestOption type")
	}
	if !strings.Contains(rendered, "func NewTestOrder(") {
		t.Fatal("missing NewTestOrder factory")
	}
	// Unlike the retired shared file, `testing` is ALWAYS imported here —
	// this file only exists for a component that has a factory, and the file
	// is a _test.go so the import never reaches the production binary.
	if !strings.Contains(rendered, `"testing"`) {
		t.Fatal("a component helper file must import testing")
	}
	// Without a DB there must be no orm import and no db field.
	if strings.Contains(rendered, "orm.Context") {
		t.Fatalf("no-DB component must not reference orm.Context:\n%s", rendered)
	}

	// The slog gating bug (journey fr-994db53964): the body uses
	// *slog.Logger unconditionally, so the import must always be present.
	if strings.Contains(rendered, "slog.") && !strings.Contains(rendered, `"log/slog"`) {
		t.Fatalf("rendered helpers reference slog without importing log/slog:\n%s", rendered)
	}

	fset := token.NewFileSet()
	if _, parseErr := parser.ParseFile(fset, "helpers_gen_test.go", rendered, parser.AllErrors); parseErr != nil {
		t.Fatalf("rendered helpers do not parse as valid Go:\n%v\n\nSource:\n%s", parseErr, rendered)
	}

	// Belt-and-braces for the same class of bug on every qualifier the
	// template can emit: any package qualifier used in the body must
	// have a matching import.
	assertQualifiersImported(t, rendered)
}

// assertQualifiersImported parses a rendered Go file and asserts that
// every known package qualifier referenced in the body has a matching
// import path. The qualifier→path table covers the packages
// bootstrap_testing.go.tmpl can emit; extend it when the template grows
// a new import.
func assertQualifiersImported(t *testing.T, src string) {
	t.Helper()
	qualifierImports := map[string]string{
		"slog":     "log/slog",
		"testing":  "testing",
		"context":  "context",
		"http":     "net/http",
		"httptest": "net/http/httptest",
		"connect":  "connectrpc.com/connect",
		"orm":      "github.com/reliant-labs/forge/pkg/orm",
		"testkit":  "github.com/reliant-labs/forge/pkg/testkit",
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "testing.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	imported := map[string]bool{}
	for _, imp := range f.Imports {
		imported[strings.Trim(imp.Path.Value, `"`)] = true
	}
	// Match the qualifier as a whole identifier. A plain
	// strings.Contains(src, qual+".") reports `orderv1connect.NewFooClient`
	// as a reference to the `connect` package — a false positive that would
	// demand an import the file neither has nor needs.
	for qual, path := range qualifierImports {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(qual) + `\.`)
		for _, m := range re.FindAllStringIndex(src, -1) {
			// \b treats '1' as a word char, so `orderv1connect.` does not
			// match — but a leading identifier rune still has to be ruled
			// out for qualifiers that end at a non-word boundary.
			if m[0] > 0 {
				prev := rune(src[m[0]-1])
				if prev == '_' || prev == '.' {
					continue
				}
			}
			if !imported[path] {
				t.Errorf("rendered file references %s.* without importing %q", qual, path)
			}
			break
		}
	}
}

// TestComponentTestHelpersTemplate_PackageContractName pins the helper's
// return type to what the component's constructor ACTUALLY returns.
//
// The template used to emit a literal `Service` for the package branch. A
// package whose contract interface is named for what it does — keywrap's
// `KeyWrapper` — therefore generated `func NewTestKeywrap(...) Service`,
// naming a type that does not exist. Because the file is forge-owned the
// author could not fix it, and `forge generate` failed its own validate step,
// so ONE package's naming broke codegen for the whole repository.
func TestComponentTestHelpersTemplate_PackageContractName(t *testing.T) {
	data := componentTestHelperData{
		Module:          "example.com/myproject",
		Package:         "keywrap",
		Name:            "keywrap",
		FieldName:       "Keywrap",
		IsService:       false,
		ConstructorName: "New",
		ConstructorType: "KeyWrapper",
		Fallible:        true,
	}

	content, err := templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", data)
	if err != nil {
		t.Fatalf("Render component_test_helpers.go.tmpl: %v", err)
	}
	rendered := string(content)

	if !strings.Contains(rendered, "func NewTestKeywrap(t *testing.T, opts ...TestOption) KeyWrapper {") {
		t.Errorf("helper must return the constructor's own type KeyWrapper; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "opts ...TestOption) Service {") {
		t.Error("helper returns Service, a type this package does not declare — the bug that broke forge generate repo-wide")
	}

	// An unset ConstructorType still falls back to Service, which is right
	// for a just-scaffolded package whose New cannot be parsed yet.
	fallback := data
	fallback.ConstructorType = ""
	content, err = templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", fallback)
	if err != nil {
		t.Fatalf("Render (fallback): %v", err)
	}
	if !strings.Contains(string(content), "opts ...TestOption) Service {") {
		t.Error("an unparseable constructor must fall back to Service")
	}
}
