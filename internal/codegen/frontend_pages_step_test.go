package codegen

import (
	"regexp"
	"testing"
)

// A proto double/float field renders as <input type="number" step="any">.
// Without a step the browser's default step of 1 refuses every fractional
// value on submit ("Please enter a valid value"), so a scaffolded create or
// edit page could not save 9.99 (forge bug report on proto double fields).
// Integer fields keep the default step.
func TestPageForm_FractionalNumberInputsStepAny(t *testing.T) {
	svc := enumSvcForTest()
	svc.Messages["CreateBrandRequest"] = append(svc.Messages["CreateBrandRequest"],
		MessageFieldDef{Name: "price", ProtoType: "double"},
		MessageFieldDef{Name: "weight", ProtoType: "float"})
	for _, msg := range []string{"services.brand.v1.CreateBrandRequest", "services.brand.v1.Brand"} {
		svc.Schemas[msg] = append(svc.Schemas[msg],
			SchemaFieldDef{Name: "price", Kind: "double"},
			SchemaFieldDef{Name: "weight", Kind: "float"})
	}
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, EntityDef{
		Name:    "Brand",
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "name", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "tagline", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "priority", ProtoType: "int32", Kind: FieldKindScalar},
			{Name: "status", ProtoType: "enum", Kind: FieldKindEnum, MessageType: "services.brand.v1.BrandStatus"},
			{Name: "price", ProtoType: "double", Kind: FieldKindScalar},
			{Name: "weight", ProtoType: "float", Kind: FieldKindScalar},
		},
	}, svc)

	out := renderPageTemplate(t, "pages", "create-page.tsx.tmpl", page)
	inputFor := func(id string) string {
		m := regexp.MustCompile(`(?s)<Input\s+type="[^"]*"[^>]*?id="` + id + `"`).FindString(out)
		if m == "" {
			t.Fatalf("no <Input> for %q in the create page:\n%s", id, out)
		}
		return m
	}
	for _, id := range []string{"price", "weight"} {
		if in := inputFor(id); !regexp.MustCompile(`type="number"\s+step="any"`).MatchString(in) {
			t.Errorf("%s input = %q, want type=\"number\" step=\"any\"", id, in)
		}
	}
	if in := inputFor("priority"); regexp.MustCompile(`step=`).MatchString(in) {
		t.Errorf("int32 priority input = %q, want no step (whole numbers)", in)
	}
}
