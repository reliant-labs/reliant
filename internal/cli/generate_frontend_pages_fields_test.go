package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// A field added after the owned pages were born reaches every other layer
// but not them. generate names each page that does not show it, with the
// lines forge would write for it, and leaves the page's bytes alone.
func TestOwnedPagesMissingANewFieldAreReported(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	page := pageScaffoldShapes(t)["crud-fk"]
	kinds := []string{"list", "detail", "create", "edit"}
	rel := func(kind string) string { return filepath.Join("frontends", "web", "src", "order-"+kind+".tsx") }
	render := func(p codegen.PageTemplateData) map[string]bool {
		checksums.ResetPerRunState()
		t.Cleanup(checksums.ResetPerRunState)
		wrote := map[string]bool{}
		for _, kind := range kinds {
			tmpl, err := loadPageTemplate("pages", kind+"-page.tsx.tmpl")
			if err != nil {
				t.Fatal(err)
			}
			w, err := renderPageScaffoldIfMissing(tmpl, p, projectDir, rel(kind))
			if err != nil {
				t.Fatal(err)
			}
			wrote[kind] = w
		}
		return wrote
	}
	for kind, w := range render(page) {
		if !w {
			t.Fatalf("the %s page was not scaffolded", kind)
		}
	}
	before := map[string]string{}
	for _, kind := range kinds {
		b, _ := os.ReadFile(filepath.Join(projectDir, rel(kind)))
		before[kind] = string(b)
	}

	// Unchanged entity: the owned pages show every field; nothing to say.
	render(page)
	if left := checksums.LeftAloneThisRun(); len(left) != 0 {
		t.Fatalf("pages that show every field were reported: %+v", left)
	}

	// The entity gains a column.
	grown := page
	addCol := func(cols []codegen.EntityPageField) []codegen.EntityPageField {
		if len(cols) == 0 {
			return cols
		}
		c := cols[len(cols)-1]
		c.Name = "discountCents"
		return append(append([]codegen.EntityPageField(nil), cols...), c)
	}
	addField := func(fs []codegen.PageField) []codegen.PageField {
		f := fs[len(fs)-1]
		f.Name, f.ProtoName = "discountCents", "discount_cents"
		f.SubmitExpr, f.PrefillExpr = "values.discountCents", "item.discountCents"
		return append(append([]codegen.PageField(nil), fs...), f)
	}
	grown.Columns, grown.ListColumns = addCol(page.Columns), addCol(page.ListColumns)
	grown.CreateFields, grown.UpdateFields = addField(page.CreateFields), addField(page.UpdateFields)

	wrote := render(grown)
	left := map[string]checksums.LeftAlone{}
	for _, l := range checksums.LeftAloneThisRun() {
		left[l.Path] = l
	}
	for _, kind := range kinds {
		if wrote[kind] {
			t.Errorf("the owned %s page was rewritten", kind)
		}
		if b, _ := os.ReadFile(filepath.Join(projectDir, rel(kind))); string(b) != before[kind] {
			t.Errorf("the owned %s page's bytes changed", kind)
		}
		l, ok := left[filepath.ToSlash(rel(kind))]
		if kind == "list" {
			// The fixture lists no columns, so today's list page would not
			// show the field either: nothing to report.
			if ok {
				t.Errorf("the list page was reported for a field forge would not list: %s", l.Why)
			}
			continue
		}
		if !ok {
			t.Errorf("the %s page does not show discountCents and was not reported", kind)
			continue
		}
		if !strings.Contains(l.Why, "`discountCents`") || !strings.Contains(l.Why, "Order") {
			t.Errorf("%s: the reason does not name the field and entity: %s", kind, l.Why)
		}
		if len(l.Detail) == 0 || !strings.HasPrefix(l.Detail[0], "discountCents: ") {
			t.Errorf("%s: no line forge would write for the field: %q", kind, l.Detail)
		}
	}

	// Naming the field in a comment acknowledges it was left out on purpose.
	p := filepath.Join(projectDir, rel("detail"))
	if err := os.WriteFile(p, []byte("// discountCents: shown on the invoice page instead\n"+before["detail"]), 0o644); err != nil {
		t.Fatal(err)
	}
	render(grown)
	for _, l := range checksums.LeftAloneThisRun() {
		if l.Path == filepath.ToSlash(rel("detail")) {
			t.Errorf("a field named in a comment is still reported: %s", l.Why)
		}
	}
}

func TestPageMissingFieldsIsWholeWord(t *testing.T) {
	got := pageMissingFields([]string{"total", "totalCents", "note"}, "item.total item.totalCents item.note", "item.totalCents // notes")
	if strings.Join(got, ",") != "note,total" {
		t.Errorf("got %v, want [note total]: a field is mentioned only as a whole word", got)
	}
}
