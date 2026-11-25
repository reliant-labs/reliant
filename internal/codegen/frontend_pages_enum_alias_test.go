package codegen

import (
	"strings"
	"testing"
)

// An enum is named by the user exactly as an entity is, and the pages use it
// as a VALUE (`<option value={ Link.PRIMARY }>`, `enumType={ Link }`, a zod
// pipe), so an enum `Link` imported under its own name declared `Link` twice
// beside next/link's default export — the collision
// TestPages_EntityNamedLikeAnImportBindsEveryIdentifierOnce pins for entity
// types. So a page binds every enum under naming.EnumTypeLocal
// (`import { Link as LinkEnum }`) and spells each reference with it.

// renameEnumInSvc returns svc with the same-package enum `from` renamed `to`:
// every field typed by it, and its value list.
func renameEnumInSvc(svc ServiceDef, from, to string) ServiceDef {
	fromFQ, toFQ := svc.Package+"."+from, svc.Package+"."+to
	schemas := make(map[string][]SchemaFieldDef, len(svc.Schemas))
	for msg, fields := range svc.Schemas {
		out := make([]SchemaFieldDef, len(fields))
		for i, f := range fields {
			if f.TypeName == fromFQ {
				f.TypeName = toFQ
			}
			out[i] = f
		}
		schemas[msg] = out
	}
	svc.Schemas = schemas
	enums := make(map[string][]string, len(svc.Enums))
	for fq, values := range svc.Enums {
		if fq == fromFQ {
			fq = toFQ
		}
		enums[fq] = values
	}
	svc.Enums = enums
	return svc
}

// enumNamedPages builds the two enum-bearing fixtures with their enum renamed
// `name`: Brand (create/edit select, detail badge) and Order (list filter and
// list badge column).
func enumNamedPages(t *testing.T, name string) []PageTemplateData {
	t.Helper()
	var pages []PageTemplateData
	for _, fx := range []struct {
		svc    ServiceDef
		enum   string
		entity EntityDef
	}{
		{enumSvcForTest(), "BrandStatus", EntityDef{Name: "Brand", PkField: "id", Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "name", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "status", ProtoType: "enum", Kind: FieldKindEnum, MessageType: "services.brand.v1." + name},
		}}},
		{listFilterSvcForTest(), "OrderStatus", EntityDef{Name: "Order", PkField: "id", Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "customer", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "status", ProtoType: "enum", Kind: FieldKindEnum, MessageType: "services.orders.v1." + name},
		}}},
	} {
		svc := renameEnumInSvc(fx.svc, fx.enum, name)
		extracted := ExtractCRUDEntities(svc)
		if len(extracted) != 1 {
			t.Fatalf("enum %s: expected 1 CRUD entity, got %d", name, len(extracted))
		}
		page := extracted[0]
		AttachEntityMeta(&page, fx.entity, svc)
		pages = append(pages, page)
	}
	return pages
}

func TestPages_EnumNamedLikeAnImportBindsEveryIdentifierOnce(t *testing.T) {
	templates := []struct{ kind, name string }{
		{"pages", "list-page.tsx.tmpl"},
		{"pages", "detail-page.tsx.tmpl"},
		{"pages", "create-page.tsx.tmpl"},
		{"pages", "edit-page.tsx.tmpl"},
		{"vite-spa-pages", "list-page.tsx.tmpl"},
		{"vite-spa-pages", "detail-page.tsx.tmpl"},
		{"vite-spa-pages", "create-page.tsx.tmpl"},
		{"vite-spa-pages", "edit-page.tsx.tmpl"},
	}
	for _, enum := range frameworkNamedEntities {
		t.Run(enum, func(t *testing.T) {
			specifier := enum + " as " + enum + "Enum"
			for _, page := range enumNamedPages(t, enum) {
				imported := false
				for _, tc := range templates {
					if (tc.name == "create-page.tsx.tmpl" && !page.HasCreate) || (tc.name == "edit-page.tsx.tmpl" && !page.EmitsEditPage()) ||
						(tc.name == "detail-page.tsx.tmpl" && !page.EmitsDetailPage()) {
						continue
					}
					out := renderPageTemplate(t, tc.kind, tc.name, page)
					for name, n := range tsBindings(out) {
						if n > 1 {
							t.Errorf("%s/%s for %s with enum %s binds %q %d times (tsc: Duplicate identifier):\n%s",
								tc.kind, tc.name, page.EntityName, enum, name, n, out)
						}
					}
					if strings.Contains(out, "import { "+specifier+" }") {
						imported = true
					}
					// No reference may use the exported name: it is not bound.
					for _, bad := range []string{enum + ".", "enumType={ " + enum + " }", "(" + enum + ")", "as " + enum + ")"} {
						if strings.Contains(out, bad) {
							t.Errorf("%s/%s for %s refers to the enum by its exported name (%q), which the page does not bind:\n%s",
								tc.kind, tc.name, page.EntityName, bad, out)
						}
					}
				}
				if !imported {
					t.Errorf("no page of %s imports the enum as %q — this guard has gone blind", page.EntityName, specifier)
				}
			}
		})
	}
}
