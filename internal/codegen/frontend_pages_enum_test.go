package codegen

import (
	"strings"
	"testing"
	"text/template"

	"github.com/reliant-labs/forge/internal/templates"
)

// enumSvcForTest builds a ServiceDef shaped exactly like the REAL
// `forge generate` descriptor for a born enum entity:
//
//   - Messages holds ONLY the direct RPC request/response messages —
//     the descriptor's extractMessageFields never recurses, so the
//     entity message itself is NOT in Messages. (The older AIP-134 test
//     fixture put the entity in Messages by hand, which is exactly how
//     the empty-edit-form bug shipped unseen.)
//   - Schemas/SchemaFiles/Enums carry the deep type graph, including
//     the entity message and the enum's declared value names.
//
// Entity Brand: name/tagline (string), priority (int32), and a
// same-package BrandStatus enum — the shape entity birth maps to a
// TEXT + CHECK column.
func enumSvcForTest() ServiceDef {
	return ServiceDef{
		Name:      "BrandService",
		Package:   "services.brand.v1",
		ProtoFile: "proto/services/brand/v1/brand.proto",
		Methods: []Method{
			{Name: "ListBrands", InputType: "ListBrandsRequest", OutputType: "ListBrandsResponse"},
			{Name: "GetBrand", InputType: "GetBrandRequest", OutputType: "GetBrandResponse"},
			{Name: "CreateBrand", InputType: "CreateBrandRequest", OutputType: "CreateBrandResponse",
				InputTypeFQ: "services.brand.v1.CreateBrandRequest"},
			{Name: "UpdateBrand", InputType: "UpdateBrandRequest", OutputType: "UpdateBrandResponse",
				InputTypeFQ: "services.brand.v1.UpdateBrandRequest"},
		},
		Messages: map[string][]MessageFieldDef{
			"CreateBrandRequest": {
				{Name: "name", ProtoType: "string"},
				{Name: "tagline", ProtoType: "string"},
				{Name: "priority", ProtoType: "int32"},
				{Name: "status", ProtoType: "enum"},
			},
			"UpdateBrandRequest": {
				{Name: "brand", ProtoType: "message", MessageType: "services.brand.v1.Brand"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"services.brand.v1.CreateBrandRequest": {
				{Name: "name", Kind: "string"},
				{Name: "tagline", Kind: "string"},
				{Name: "priority", Kind: "int32"},
				{Name: "status", Kind: "enum", TypeName: "services.brand.v1.BrandStatus"},
			},
			"services.brand.v1.UpdateBrandRequest": {
				{Name: "brand", Kind: "message", TypeName: "services.brand.v1.Brand"},
				{Name: "update_mask", Kind: "message", TypeName: "google.protobuf.FieldMask"},
			},
			"services.brand.v1.Brand": {
				{Name: "id", Kind: "string"},
				{Name: "name", Kind: "string"},
				{Name: "tagline", Kind: "string"},
				{Name: "priority", Kind: "int32"},
				{Name: "status", Kind: "enum", TypeName: "services.brand.v1.BrandStatus"},
				{Name: "created_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
			},
		},
		SchemaFiles: map[string]string{
			"services.brand.v1.CreateBrandRequest": "proto/services/brand/v1/brand.proto",
			"services.brand.v1.UpdateBrandRequest": "proto/services/brand/v1/brand.proto",
			"services.brand.v1.Brand":              "proto/services/brand/v1/brand.proto",
		},
		Enums: map[string][]string{
			"services.brand.v1.BrandStatus": {
				"BRAND_STATUS_UNSPECIFIED", "BRAND_STATUS_DRAFT", "BRAND_STATUS_ACTIVE",
			},
		},
	}
}

func enumPageForTest(t *testing.T) PageTemplateData {
	t.Helper()
	svc := enumSvcForTest()
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	entity := EntityDef{
		Name:    "Brand",
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "name", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "tagline", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "priority", ProtoType: "int32", Kind: FieldKindScalar},
			{Name: "status", ProtoType: "enum", Kind: FieldKindEnum, MessageType: "services.brand.v1.BrandStatus"},
		},
	}
	AttachEntityMeta(&page, entity, svc)
	return page
}

// renderPageTemplate renders one embedded page template (the same bytes
// the CLI's loadPageTemplate reads) with the given data.
func renderPageTemplate(t *testing.T, kind, name string, data PageTemplateData) string {
	t.Helper()
	content, err := templates.FrontendTemplates().Get(kind + "/" + name)
	if err != nil {
		t.Fatalf("read template %s/%s: %v", kind, name, err)
	}
	tmpl, err := template.New(name).Funcs(templates.FuncMap()).Parse(string(content))
	if err != nil {
		t.Fatalf("parse template %s/%s: %v", kind, name, err)
	}
	// The shared page partials (the foreign-key <EntityPicker> control and
	// the <EntityName> detail row) live in their own file and are parsed
	// into every page template's set by loadPageTemplate. Mirror that here,
	// or a page with a resolved foreign key fails at EXECUTE time with "no
	// such template" — after this helper has already reported a parse pass.
	partials, err := templates.FrontendTemplates().Get("pages/_partials.tmpl")
	if err != nil {
		t.Fatalf("read page partials: %v", err)
	}
	if _, err := tmpl.Parse(string(partials)); err != nil {
		t.Fatalf("parse page partials: %v", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatalf("render %s/%s: %v", kind, name, err)
	}
	return b.String()
}

// TestExtractCRUDEntities_EnumCreateFieldTypedSelect pins the enum
// half of the create-form fix: an enum-typed field on the (flat) create
// request projects to a "select" form field, not a plain text input —
// the old projection emitted `status: z.string()` + a text input, and
// `mutation.mutate({...values})` failed the frontend build with
// "Type 'string' is not assignable to type 'BrandStatus | undefined'".
func TestExtractCRUDEntities_EnumCreateFieldTypedSelect(t *testing.T) {
	page := enumPageForTest(t)

	var status *PageField
	for i := range page.CreateFields {
		if page.CreateFields[i].Name == "status" {
			status = &page.CreateFields[i]
		}
	}
	if status == nil {
		t.Fatalf("create form lost the status field entirely: %+v", page.CreateFields)
	}
	if status.Type != "select" {
		t.Errorf("status field Type = %q, want %q (typed enum select)", status.Type, "select")
	}
	if status.Required {
		t.Errorf("enum fields are never Required in forms (a select can't be empty)")
	}
	if !status.IsEnum {
		t.Errorf("status field IsEnum = false, want true")
	}
	if status.EnumType != "BrandStatus" {
		t.Errorf("status field EnumType = %q, want %q", status.EnumType, "BrandStatus")
	}
	if status.EnumImportPath != "@/gen/services/brand/v1/brand_pb" {
		t.Errorf("status field EnumImportPath = %q, want %q", status.EnumImportPath, "@/gen/services/brand/v1/brand_pb")
	}
	// Members mirror protobuf-es's local-name rule: the shared
	// BRAND_STATUS_ prefix is stripped; labels are humanized. The zero
	// value is not a choice — it is held separately for the zod refusal.
	wantValues := []PageEnumValue{
		{Ref: "BrandStatus.DRAFT", Label: "Draft"},
		{Ref: "BrandStatus.ACTIVE", Label: "Active"},
	}
	if status.EnumZeroRef != "BrandStatus.UNSPECIFIED" {
		t.Errorf("status field EnumZeroRef = %q, want %q", status.EnumZeroRef, "BrandStatus.UNSPECIFIED")
	}
	if len(status.EnumValues) != len(wantValues) {
		t.Fatalf("EnumValues = %+v, want %+v", status.EnumValues, wantValues)
	}
	for i, want := range wantValues {
		if status.EnumValues[i] != want {
			t.Errorf("EnumValues[%d] = %+v, want %+v", i, status.EnumValues[i], want)
		}
	}

	wantImports := []PageEnumImport{{Path: "@/gen/services/brand/v1/brand_pb", Types: []string{"BrandStatus"}}}
	for name, got := range map[string][]PageEnumImport{
		"CreateEnumImports": page.CreateEnumImports,
		"UpdateEnumImports": page.UpdateEnumImports,
	} {
		if len(got) != 1 || got[0].Path != wantImports[0].Path ||
			len(got[0].Types) != 1 || got[0].Types[0] != "BrandStatus" {
			t.Errorf("%s = %+v, want %+v", name, got, wantImports)
		}
	}
}

// TestProtobufESEnumSharedPrefix pins the exact protobuf-es
// findEnumSharedPrefix mirror. The generated <select> references the TS
// members protobuf-es emits, so any divergence here means the born page
// doesn't compile.
func TestProtobufESEnumSharedPrefix(t *testing.T) {
	tests := []struct {
		name     string
		enumName string
		values   []string
		want     string
	}{
		{"standard prefix stripped", "BrandStatus",
			[]string{"BRAND_STATUS_UNSPECIFIED", "BRAND_STATUS_ACTIVE"}, "brand_status_"},
		{"single word", "Status",
			[]string{"STATUS_UNSPECIFIED", "STATUS_OK"}, "status_"},
		{"initialism never matches (protobuf-es snake-cases per capital)", "LLMStatus",
			[]string{"LLM_STATUS_UNSPECIFIED", "LLM_STATUS_ACTIVE"}, ""},
		{"one unprefixed value keeps all names", "BrandStatus",
			[]string{"BRAND_STATUS_UNSPECIFIED", "ACTIVE"}, ""},
		{"stripped name starting with digit keeps all names", "BrandStatus",
			[]string{"BRAND_STATUS_UNSPECIFIED", "BRAND_STATUS_2ND"}, ""},
		{"empty remainder keeps all names", "BrandStatus",
			[]string{"BRAND_STATUS_"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protobufESEnumSharedPrefix(tt.enumName, tt.values); got != tt.want {
				t.Errorf("protobufESEnumSharedPrefix(%q, %v) = %q, want %q", tt.enumName, tt.values, got, tt.want)
			}
		})
	}
}

// TestResolveFormEnum_ShapeRules pins the resolution boundaries: nested
// enums export as Parent_Enum from the parent's module; cross-package
// enums, unknown enums, and value-less enums have NO compilable
// projection and are excluded from forms (ok=false) — never rendered as
// the old guaranteed-broken text input.
func TestResolveFormEnum_ShapeRules(t *testing.T) {
	svc := enumSvcForTest()
	svc.Enums["services.brand.v1.Brand.Tier"] = []string{"TIER_UNSPECIFIED", "TIER_GOLD"}
	svc.Enums["shared.v1.Region"] = []string{"REGION_UNSPECIFIED"}

	// Nested enum: Parent_Enum from the parent's declaring module.
	meta, ok := resolveFormEnum(svc, "Brand", "services.brand.v1.Brand.Tier")
	if !ok {
		t.Fatalf("nested same-package enum should resolve")
	}
	if meta.TSType != "Brand_Tier" {
		t.Errorf("nested enum TSType = %q, want %q", meta.TSType, "Brand_Tier")
	}
	if meta.ImportPath != "@/gen/services/brand/v1/brand_pb" {
		t.Errorf("nested enum ImportPath = %q, want %q", meta.ImportPath, "@/gen/services/brand/v1/brand_pb")
	}
	// Values are the choices — the zero (TIER_UNSPECIFIED) is held apart.
	if len(meta.Values) != 1 || meta.Values[0].Ref != "Brand_Tier.GOLD" || meta.Values[0].Label != "Gold" {
		t.Errorf("nested enum Values = %+v", meta.Values)
	}
	if meta.ZeroRef != "Brand_Tier.UNSPECIFIED" {
		t.Errorf("nested enum ZeroRef = %q, want %q", meta.ZeroRef, "Brand_Tier.UNSPECIFIED")
	}

	if _, ok := resolveFormEnum(svc, "Brand", "shared.v1.Region"); ok {
		t.Errorf("cross-package enum must not resolve (entity birth TODO-skips its column)")
	}
	if _, ok := resolveFormEnum(svc, "Brand", "services.brand.v1.Unknown"); ok {
		t.Errorf("enum with no recorded values must not resolve")
	}
	if _, ok := resolveFormEnum(svc, "Brand", ""); ok {
		t.Errorf("empty enum type must not resolve")
	}
}

// TestFormPageField_ExcludesNonCompilableFields pins the fail-safe:
// fields with no compilable form representation are EXCLUDED, because
// the old text-input fallback guaranteed the born page failed the
// frontend type-check.
func TestFormPageField_ExcludesNonCompilableFields(t *testing.T) {
	svc := enumSvcForTest()
	excluded := []struct {
		name string
		f    formFieldDef
	}{
		{"repeated enum", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "tags", ProtoType: "[]enum"}, EnumTypeFQ: "services.brand.v1.BrandStatus"}},
		{"unresolvable enum (no type name — old descriptor)", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "status", ProtoType: "enum"}}},
		{"cross-package enum", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "region", ProtoType: "enum"}, EnumTypeFQ: "shared.v1.Region"}},
		{"nested message", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "address", ProtoType: "message", MessageType: "services.brand.v1.Address"}}},
		{"map (shallow-encoded as bare message)", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "attrs", ProtoType: "message"}}},
		{"repeated message", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "items", ProtoType: "[]message", MessageType: "services.brand.v1.Item"}}},
	}
	for _, tt := range excluded {
		t.Run(tt.name, func(t *testing.T) {
			if pf, ok := formPageField(svc, "Brand", tt.f); ok {
				t.Errorf("field must be excluded from forms, got %+v", pf)
			}
		})
	}

	// A singular Timestamp still maps to a date input.
	pf, ok := formPageField(svc, "Brand", formFieldDef{MessageFieldDef: MessageFieldDef{
		Name: "published_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"}})
	if !ok || pf.Type != "date" {
		t.Errorf("singular Timestamp should stay a date field, got ok=%v %+v", ok, pf)
	}
	// Plain scalars are untouched.
	pf, ok = formPageField(svc, "Brand", formFieldDef{MessageFieldDef: MessageFieldDef{
		Name: "name", ProtoType: "string"}})
	if !ok || pf.Type != "text" {
		t.Errorf("string field should stay a text field, got ok=%v %+v", ok, pf)
	}
}

// TestExtractCRUDEntities_EditFormFieldsFromDeepSchema pins the
// create/edit asymmetry fix: the AIP-134 update request WRAPS the
// entity, and the entity message lives only in the descriptor's deep
// Schemas map (it is never a direct RPC input/output, so the shallow
// Messages map doesn't carry it). The old extraction looked only in
// Messages, silently bailed out, and birthed a completely EMPTY edit
// form (`const schema = z.object({});`, `values: item ? {} : undefined`,
// `updateMask: { paths: [] }`).
func TestExtractCRUDEntities_EditFormFieldsFromDeepSchema(t *testing.T) {
	page := enumPageForTest(t)

	if page.UpdateEntityFieldCamel != "brand" {
		t.Errorf("UpdateEntityFieldCamel = %q, want %q", page.UpdateEntityFieldCamel, "brand")
	}
	if page.UpdateMaskFieldCamel != "updateMask" {
		t.Errorf("UpdateMaskFieldCamel = %q, want %q", page.UpdateMaskFieldCamel, "updateMask")
	}

	var names []string
	for _, f := range page.UpdateFields {
		names = append(names, f.Name)
	}
	got := strings.Join(names, ",")
	// id comes from the URL param, created_at is machinery, the
	// wrapper/mask are request plumbing — everything else is editable.
	if got != "name,tagline,priority,status" {
		t.Errorf("UpdateFields = %q, want %q (the empty-edit-form bug)", got, "name,tagline,priority,status")
	}
}

// TestCreatePage_EnumFieldTypedSelect pins the rendered create page for
// both frontend kinds: zod validates the coerced select value as an
// enum member, the options reference the protobuf-es TS enum members
// (prefix-stripped), and the enum is imported from its _pb module.
func TestCreatePage_EnumFieldTypedSelect(t *testing.T) {
	page := enumPageForTest(t)

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		t.Run(kind, func(t *testing.T) {
			create := renderPageTemplate(t, kind, "create-page.tsx.tmpl", page)

			for _, want := range []string{
				`import { BrandStatus } from "@/gen/services/brand/v1/brand_pb";`,
				// Enum zod refuses the zero — the mirror of the born CHECK.
				`status: z.coerce.number().pipe(z.nativeEnum(BrandStatus)).refine((v): boolean => v !== BrandStatus.UNSPECIFIED, "Choose a value"),`,
				`<select`,
				// A NOT NULL enum starts on its column DEFAULT, the first
				// real member — what Create stores when the field is omitted.
				`defaultValue={ BrandStatus.DRAFT }`,
				`{...register("status")}`,
				`<option value={ BrandStatus.DRAFT }>Draft</option>`,
				`<option value={ BrandStatus.ACTIVE }>Active</option>`,
				// The plain fields still render as before.
				`{...register("name")}`,
				`{...register("priority")}`,
			} {
				if !strings.Contains(create, want) {
					t.Errorf("create page missing %q:\n%s", want, create)
				}
			}

			// The UNSPECIFIED (zero) option is never a choice (F12).
			if strings.Contains(create, `<option value={ BrandStatus.UNSPECIFIED }>Unspecified</option>`) {
				t.Errorf("create page still offers the UNSPECIFIED zero-value option:\n%s", create)
			}

			// The broken projection: enum typed as a plain string input.
			if strings.Contains(create, "status: z.string()") {
				t.Errorf("create page still types the enum field as z.string() — the frontend build fails (MessageInit wants the TS enum):\n%s", create)
			}
		})
	}
}

// TestEditPage_EnumFieldAndEntityFields pins the rendered edit page:
// the form carries the entity's editable fields (per the deep-schema
// fix), the enum renders as the same typed select as the create page,
// the prefill comes from the fetched entity WITHOUT String() coercion,
// and the AIP-134 mask names exactly the form's fields.
func TestEditPage_EnumFieldAndEntityFields(t *testing.T) {
	page := enumPageForTest(t)

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		t.Run(kind, func(t *testing.T) {
			edit := renderPageTemplate(t, kind, "edit-page.tsx.tmpl", page)

			for _, want := range []string{
				`import { BrandStatus } from "@/gen/services/brand/v1/brand_pb";`,
				`status: z.coerce.number().pipe(z.nativeEnum(BrandStatus)).refine((v): boolean => v !== BrandStatus.UNSPECIFIED, "Choose a value"),`,
				// Prefill from the fetched entity — the field is the TS
				// enum (a number), never String()-coerced, and never
				// defaulted to the zero.
				"status: item.status,",
				`<select`,
				`{...register("status")}`,
				`<option value={ BrandStatus.ACTIVE }>Active</option>`,
				// The sibling plain fields made it into the form too.
				`{...register("name")}`,
				`{...register("tagline")}`,
				// AIP-134 mask names exactly the form's fields.
				`updateMask: { paths: ["name", "tagline", "priority", "status"] },`,
				// Entity nested under the wrapper with the PK inside.
				"brand: {",
				"id: id,",
			} {
				if !strings.Contains(edit, want) {
					t.Errorf("edit page missing %q:\n%s", want, edit)
				}
			}

			if strings.Contains(edit, "status: z.string()") {
				t.Errorf("edit page still types the enum field as z.string():\n%s", edit)
			}
			if strings.Contains(edit, "BrandStatus.UNSPECIFIED }>") {
				t.Errorf("edit page offers the UNSPECIFIED zero value as a choice:\n%s", edit)
			}
			if strings.Contains(edit, "String(item.status") {
				t.Errorf("edit page String()-coerces the enum prefill — the request needs the TS enum value:\n%s", edit)
			}
		})
	}
}
