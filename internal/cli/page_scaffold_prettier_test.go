package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// pageScaffoldShapes are the entity shapes the CRUD page templates branch on:
// a read-only entity with a search filter, one with no filter fields at all
// (the `useListX({}, …)` empty-object case control-plane hit), and a full
// CRUD entity with a foreign key.
func pageScaffoldShapes(t *testing.T) map[string]codegen.PageTemplateData {
	t.Helper()
	noFilter := fkPatientPage()
	noFilter.EntityName, noFilter.EntityNamePlural, noFilter.EntitySlug = "RetainedDatabase", "RetainedDatabases", "retained-databases"
	noFilter.ListRPC, noFilter.GetRPC = "ListRetainedDatabases", "GetRetainedDatabase"
	noFilter.SearchFilterField = ""
	order := attachedOrderPage(t)
	for _, fields := range [][]codegen.PageField{order.CreateFields, order.UpdateFields} {
		for i := range fields {
			fields[i].ZodExpr = "z.string()"
			fields[i].SubmitExpr = "values." + fields[i].Name
			fields[i].PrefillExpr = "item." + fields[i].Name
		}
	}
	return map[string]codegen.PageTemplateData{
		"search":    fkPatientPage(),
		"no-filter": noFilter,
		"crud-fk":   order,
	}
}

// renderPageScaffolds runs every page kind through renderPageScaffoldIfMissing
// — the writer generateFrontendPages uses — into a throwaway forge project,
// so the bytes returned are what lands on disk. projectDir may carry an
// installed prettier; without one the template output stands.
func renderPageScaffolds(t *testing.T, projectDir string) []string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, dir := range []string{"pages", "vite-spa-pages"} {
		for shape, page := range pageScaffoldShapes(t) {
			for _, kind := range []string{"list", "detail", "create", "edit"} {
				tmpl, err := loadPageTemplate(dir, kind+"-page.tsx.tmpl")
				if err != nil {
					t.Fatal(err)
				}
				rel := filepath.Join("frontends", "web", "src", dir+"-"+shape+"-"+kind+".tsx")
				wrote, err := renderPageScaffoldIfMissing(tmpl, page, projectDir, rel)
				if err != nil || !wrote {
					t.Fatalf("%s/%s/%s: wrote=%v err=%v", dir, shape, kind, wrote, err)
				}
				paths = append(paths, filepath.Join(projectDir, rel))
			}
		}
	}
	return paths
}

// TestPageScaffolds_ShapeGuards is the -short guard: the two defects
// control-plane's pre-commit reported, checkable without node.
func TestPageScaffolds_ShapeGuards(t *testing.T) {
	{
		for _, p := range renderPageScaffolds(t, t.TempDir()) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			if strings.Contains(src, "({\n}") || strings.Contains(src, "({ }") {
				t.Errorf("%s: empty object literal spread over lines or padded; prettier prints `{}`", filepath.Base(p))
			}
			if !strings.HasSuffix(src, "\n") || strings.HasSuffix(src, "\n\n") {
				t.Errorf("%s: must end in exactly one newline", filepath.Base(p))
			}
		}
	}
}

// TestPageScaffolds_PrettierClean is the full-mode check: with the
// frontend's own prettier installed (as it is in every real project by the
// time CRUD pages are scaffolded), every page forge writes must pass
// `prettier --check` with the scaffolded config — the check a project's
// pre-commit runs. No template text can guarantee this for every entity
// (long JSX attributes, wrapped call arguments), which is why the writer
// hands the file to the project's formatter.
func TestPageScaffolds_PrettierClean(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the prettier run (needs npm install); TestPageScaffolds_ShapeGuards still runs")
	}
	npx := requireNpxForPrettier(t)
	npm := filepath.Join(filepath.Dir(npx), "npm")
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	if err := os.MkdirAll(fe, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := templates.FrontendTemplates().Get("nextjs/prettier.config.js")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fe, "prettier.config.mjs"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fe, "package.json"), []byte(`{"name":"web","private":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(npm, "install", "--no-audit", "--no-fund", "prettier@"+scaffoldedFrontendPrettierVersion)
	install.Dir = fe
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("npm install prettier: %v\n%s", err, out)
	}

	paths := renderPageScaffolds(t, root)
	check := exec.Command(filepath.Join(fe, "node_modules", ".bin", "prettier"), append([]string{"--check"}, paths...)...)
	check.Dir = fe
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("prettier rewrites scaffolded pages (%v):\n%s", err, out)
	}
}
