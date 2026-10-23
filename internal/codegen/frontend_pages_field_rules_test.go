package codegen

import (
	"strings"
	"testing"
)

// The rules a born create/edit form enforces on each field, and the four
// ways a dogfood run (roofers, 2026-10-05) found them wrong:
//
//  1. Every `NOT NULL DEFAULT ''` text column was born `min(1, "Required")`,
//     so Notes, Email and Insurance Carrier were mandatory although the
//     proto declared no rule on them. Required-ness now comes from an
//     explicit rule only: protovalidate `required = true`, or
//     `string.min_len >= 1`.
//  2. An empty input for an `optional int32 roof_installed_year` was
//     submitted as 0 — stored as year 0 instead of NULL. An empty input for
//     a proto3 `optional` scalar now submits as unset.
//  3. The edit select and the list filter offered `*_UNSPECIFIED`, which the
//     born CHECK rejects. The zero value is now never a choice.
//  4. Create navigated back to the list instead of to the record it made.
//
// The fixture is the X4 sample entity, shaped like the real `forge generate`
// descriptor: the entity message and its enum live only in the deep
// Schemas/Enums graph; Messages carries the direct RPC inputs/outputs.

func fieldRulesSvc() ServiceDef {
	u := func(n uint64) *uint64 { return &n }
	thing := []SchemaFieldDef{
		{Name: "id", Kind: "string"},
		{Name: "name", Kind: "string", Validate: &FieldConstraints{MinLen: u(1)}},
		{Name: "notes", Kind: "string"},
		{Name: "year", Kind: "int32", Optional: true},
		{Name: "kind", Kind: "enum", TypeName: "services.things.v1.ThingKind"},
		{Name: "email", Kind: "string", Optional: true, Validate: &FieldConstraints{Email: true}},
		{Name: "code", Kind: "string", Validate: &FieldConstraints{Required: true}},
		{Name: "tagline", Kind: "string", Validate: &FieldConstraints{MaxLen: u(64)}},
		{Name: "created_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
	}
	// The create request flattens the entity's writable fields.
	create := []SchemaFieldDef{}
	for _, f := range thing {
		if f.Name != "id" && f.Name != "created_at" {
			create = append(create, f)
		}
	}
	return ServiceDef{
		Name:      "ThingService",
		Package:   "services.things.v1",
		ProtoFile: "proto/services/things/v1/things.proto",
		Methods: []Method{
			{Name: "ListThings", InputType: "ListThingsRequest", OutputType: "ListThingsResponse",
				InputTypeFQ: "services.things.v1.ListThingsRequest"},
			{Name: "GetThing", InputType: "GetThingRequest", OutputType: "GetThingResponse"},
			{Name: "CreateThing", InputType: "CreateThingRequest", OutputType: "CreateThingResponse",
				InputTypeFQ: "services.things.v1.CreateThingRequest"},
			{Name: "UpdateThing", InputType: "UpdateThingRequest", OutputType: "UpdateThingResponse",
				InputTypeFQ: "services.things.v1.UpdateThingRequest"},
		},
		Messages: map[string][]MessageFieldDef{
			"CreateThingResponse": {
				{Name: "thing", ProtoType: "message", MessageType: "services.things.v1.Thing"},
			},
			"GetThingResponse": {
				{Name: "thing", ProtoType: "message", MessageType: "services.things.v1.Thing"},
			},
			"ListThingsResponse": {
				{Name: "things", ProtoType: "[]message", MessageType: "services.things.v1.Thing"},
				{Name: "next_page_token", ProtoType: "string"},
				{Name: "total_count", ProtoType: "int32"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"services.things.v1.Thing":              thing,
			"services.things.v1.CreateThingRequest": create,
			"services.things.v1.UpdateThingRequest": {
				{Name: "thing", Kind: "message", TypeName: "services.things.v1.Thing"},
				{Name: "update_mask", Kind: "message", TypeName: "google.protobuf.FieldMask"},
			},
			"services.things.v1.ListThingsRequest": {
				{Name: "page_size", Kind: "int32"},
				{Name: "page_token", Kind: "string"},
				{Name: "search", Kind: "string", Optional: true},
				{Name: "kind", Kind: "enum", TypeName: "services.things.v1.ThingKind", Optional: true},
			},
		},
		SchemaFiles: map[string]string{
			"services.things.v1.Thing": "proto/services/things/v1/things.proto",
		},
		Enums: map[string][]string{
			"services.things.v1.ThingKind": {"THING_KIND_UNSPECIFIED", "THING_KIND_RESIDENTIAL", "THING_KIND_COMMERCIAL"},
		},
		EnumNumbers: map[string][]int32{
			"services.things.v1.ThingKind": {0, 1, 2},
		},
	}
}

func fieldRulesPage(t *testing.T, svc ServiceDef) PageTemplateData {
	t.Helper()
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	var fields []EntityField
	for _, f := range svc.Schemas["services.things.v1.Thing"] {
		fields = append(fields, schemaFieldToEntityField(f))
	}
	AttachEntityMeta(&page, EntityDef{Name: "Thing", PkField: "id", Fields: fields}, svc)
	return page
}

func formFieldNamed(t *testing.T, fields []PageField, name string) PageField {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("form has no %q field: %+v", name, fields)
	return PageField{}
}

// TestFormRequired_OnlyFromExplicitRules: a column being NOT NULL is not a
// rule about input. `string notes = 3;` is born NOT NULL with an empty-string
// DEFAULT — the empty string IS its value — so a form that refuses "" refuses
// a row the API, the wire validator and the database all accept.
func TestFormRequired_OnlyFromExplicitRules(t *testing.T) {
	page := fieldRulesPage(t, fieldRulesSvc())

	cases := []struct {
		field    string
		required bool
		zod      string
	}{
		// string.min_len = 1 — the explicit rule.
		{"name", true, `z.string().min(1)`},
		// required = true — the other explicit rule.
		{"code", true, `z.string().min(1)`},
		// No rule at all: the column's DEFAULT '' is a valid value.
		{"notes", false, `z.string()`},
		// A rule that is not about presence stays exactly as projected.
		{"tagline", false, `z.string().max(64)`},
	}
	for _, form := range []struct {
		name   string
		fields []PageField
	}{{"create", page.CreateFields}, {"edit", page.UpdateFields}} {
		for _, tc := range cases {
			f := formFieldNamed(t, form.fields, tc.field)
			if f.Required != tc.required {
				t.Errorf("%s %s: Required = %v, want %v", form.name, tc.field, f.Required, tc.required)
			}
			if f.ZodExpr != tc.zod {
				t.Errorf("%s %s: zod = %s, want %s", form.name, tc.field, f.ZodExpr, tc.zod)
			}
		}
	}

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		create := renderPageTemplate(t, kind, "create-page.tsx.tmpl", page)
		if strings.Contains(create, `notes: z.string().min(1, "Required")`) {
			t.Errorf("%s create page still makes the rule-less Notes column mandatory:\n%s", kind, create)
		}
		if !strings.Contains(create, "Name *") {
			t.Errorf("%s create page lost the required marker on Name (string.min_len = 1)", kind)
		}
		if strings.Contains(create, "Notes *") {
			t.Errorf("%s create page marks the rule-less Notes column required", kind)
		}
	}
}

// TestFormOptionalScalar_EmptyInputSubmitsUnset: an empty input for a proto3
// `optional` field means "absent", which is the only reason the field has
// presence. `z.coerce.number()` turns "" into 0 — the roofers form stored
// roof_installed_year = 0 — and a projected `.email()` rejects the "" an
// empty optional email legitimately is.
func TestFormOptionalScalar_EmptyInputSubmitsUnset(t *testing.T) {
	page := fieldRulesPage(t, fieldRulesSvc())

	for _, form := range []struct {
		name   string
		fields []PageField
	}{{"create", page.CreateFields}, {"edit", page.UpdateFields}} {
		year := formFieldNamed(t, form.fields, "year")
		if want := `z.preprocess((v) => (v === "" ? undefined : v), z.coerce.number().optional())`; year.ZodExpr != want {
			t.Errorf("%s year zod = %s, want %s", form.name, year.ZodExpr, want)
		}
		if year.SubmitExpr != "" {
			t.Errorf("%s year submit = %s, want none (the zod value is already number | undefined)", form.name, year.SubmitExpr)
		}
		if year.PrefillExpr != "item.year" {
			t.Errorf("%s year prefill = %s, want item.year (unset stays unset, not 0)", form.name, year.PrefillExpr)
		}

		email := formFieldNamed(t, form.fields, "email")
		if want := `z.preprocess((v) => (v === "" ? undefined : v), z.string().email().optional())`; email.ZodExpr != want {
			t.Errorf("%s email zod = %s, want %s", form.name, email.ZodExpr, want)
		}
		if email.PrefillExpr != `item.email ?? ""` {
			t.Errorf("%s email prefill = %s, want item.email ?? \"\"", form.name, email.PrefillExpr)
		}
	}

	// Clearing a stored optional value on EDIT only reaches the database if
	// the mask names the field: the masked update writes NULL for a named
	// path the entity leaves unset, and leaves an unnamed one alone.
	for _, kind := range []string{"pages", "vite-spa-pages"} {
		edit := renderPageTemplate(t, kind, "edit-page.tsx.tmpl", page)
		if !strings.Contains(edit, `updateMask: { paths: ["name", "notes", "year", "kind", "email", "code", "tagline"] },`) {
			t.Errorf("%s edit page mask does not name every editable field:\n%s", kind, edit)
		}
		if strings.Contains(edit, "Number(item.year ?? 0)") {
			t.Errorf("%s edit page prefills an unset optional year as 0:\n%s", kind, edit)
		}
	}
}

// TestFormOptionalScalar_EncodedKinds: the two kinds edited as TEXT (int64
// digits, base64 bytes) convert in the submit handler, so the unset case
// has to survive the conversion — BigInt(undefined) throws.
func TestFormOptionalScalar_EncodedKinds(t *testing.T) {
	cases := []struct {
		kind, submit, prefill string
	}{
		{"int64", `values.f === undefined ? undefined : BigInt(values.f)`, `String(item.f ?? "")`},
		{"bytes", `values.f === undefined ? undefined : base64Decode(values.f)`, `item.f ? base64Encode(item.f) : ""`},
	}
	for _, tc := range cases {
		pf, ok := formPageField(ServiceDef{}, "Thing", formFieldDef{MessageFieldDef: MessageFieldDef{Name: "f", ProtoType: tc.kind, IsOptional: true}})
		if !ok {
			t.Fatalf("%s: no form field", tc.kind)
		}
		finalizePageField(&pf, nil)
		if !strings.HasPrefix(pf.ZodExpr, `z.preprocess((v) => (v === "" ? undefined : v), `) {
			t.Errorf("optional %s zod = %s, want the empty-as-unset preprocess", tc.kind, pf.ZodExpr)
		}
		if pf.SubmitExpr != tc.submit {
			t.Errorf("optional %s submit = %s, want %s", tc.kind, pf.SubmitExpr, tc.submit)
		}
		if pf.PrefillExpr != tc.prefill {
			t.Errorf("optional %s prefill = %s, want %s", tc.kind, pf.PrefillExpr, tc.prefill)
		}
	}
}

// TestEnumZeroValue_NeverAChoice: the proto zero is how an implicit-presence
// enum says "unset" — the generated write path maps it to the column
// DEFAULT, and the born CHECK does not admit it. Offering it as an option
// (edit select, list filter) offers a value no row can hold.
func TestEnumZeroValue_NeverAChoice(t *testing.T) {
	page := fieldRulesPage(t, fieldRulesSvc())
	wantChoices := []PageEnumValue{
		{Ref: "ThingKind.RESIDENTIAL", Label: "Residential"},
		{Ref: "ThingKind.COMMERCIAL", Label: "Commercial"},
	}
	assertChoices := func(label string, got []PageEnumValue) {
		t.Helper()
		if len(got) != len(wantChoices) {
			t.Errorf("%s choices = %+v, want %+v", label, got, wantChoices)
			return
		}
		for i := range wantChoices {
			if got[i] != wantChoices[i] {
				t.Errorf("%s choice[%d] = %+v, want %+v", label, i, got[i], wantChoices[i])
			}
		}
	}

	for _, form := range []struct {
		name   string
		fields []PageField
	}{{"create", page.CreateFields}, {"edit", page.UpdateFields}} {
		kind := formFieldNamed(t, form.fields, "kind")
		assertChoices(form.name+" select", kind.EnumValues)
		// The zod mirror of the CHECK: a stored zero (a schema forge did not
		// birth) is refused on both forms rather than written back.
		if want := `z.coerce.number().pipe(z.nativeEnum(ThingKind)).refine((v): boolean => v !== ThingKind.UNSPECIFIED, "Choose a value")`; kind.ZodExpr != want {
			t.Errorf("%s kind zod = %s, want %s", form.name, kind.ZodExpr, want)
		}
	}
	if kind := formFieldNamed(t, page.UpdateFields, "kind"); kind.PrefillExpr != "item.kind" {
		t.Errorf("edit kind prefill = %s, want item.kind (never defaulted to the zero)", kind.PrefillExpr)
	}

	var kindFilter *ListFilterField
	for i := range page.ExactFilterFields {
		if page.ExactFilterFields[i].Name == "kind" {
			kindFilter = &page.ExactFilterFields[i]
		}
	}
	if kindFilter == nil {
		t.Fatalf("list page lost the kind filter: %+v", page.ExactFilterFields)
	}
	assertChoices("list filter", kindFilter.EnumValues)

	for _, tmplKind := range []string{"pages", "vite-spa-pages"} {
		for _, name := range []string{"create-page.tsx.tmpl", "edit-page.tsx.tmpl", "list-page.tsx.tmpl"} {
			out := renderPageTemplate(t, tmplKind, name, page)
			for _, offered := range []string{
				"<option value={ ThingKind.UNSPECIFIED }>",
				"<option value={String(ThingKind.UNSPECIFIED)}>",
				"[ThingKind.UNSPECIFIED,",
			} {
				if strings.Contains(out, offered) {
					t.Errorf("%s/%s offers the UNSPECIFIED zero value (%s):\n%s", tmplKind, name, offered, out)
				}
			}
		}
		// Required enum on Create: defaulted to the column DEFAULT — the
		// first real member — which is also what the API stores when the
		// field is omitted.
		create := renderPageTemplate(t, tmplKind, "create-page.tsx.tmpl", page)
		if !strings.Contains(create, "defaultValue={ ThingKind.RESIDENTIAL }") {
			t.Errorf("%s create select is not defaulted to the first real member:\n%s", tmplKind, create)
		}
		if strings.Contains(create, "Select kind…") {
			t.Errorf("%s create select still renders an empty placeholder for a NOT NULL enum:\n%s", tmplKind, create)
		}
		list := renderPageTemplate(t, tmplKind, "list-page.tsx.tmpl", page)
		for _, member := range []string{"ThingKind.RESIDENTIAL", "ThingKind.COMMERCIAL"} {
			if !strings.Contains(list, "<option value={String("+member+")}>") {
				t.Errorf("%s list filter lost the real member %s:\n%s", tmplKind, member, list)
			}
		}
		// The Next.js page parses the URL token against this array.
		if tmplKind == "pages" && !strings.Contains(list, "[ThingKind.RESIDENTIAL, ThingKind.COMMERCIAL] as const") {
			t.Errorf("%s list filter options are not exactly the real members:\n%s", tmplKind, list)
		}
	}
}

// TestEnumZeroValue_ByWireNumber: the zero is identified by its declared
// NUMBER, not its position or its name — an enum that reserved a removed
// value keeps every real member, and a descriptor without numbers falls
// back to proto3's rule that the first value is the zero.
func TestEnumZeroValue_ByWireNumber(t *testing.T) {
	svc := fieldRulesSvc()
	svc.Enums["services.things.v1.ThingKind"] = []string{"THING_KIND_NONE", "THING_KIND_RESIDENTIAL", "THING_KIND_COMMERCIAL"}
	svc.EnumNumbers["services.things.v1.ThingKind"] = []int32{0, 2, 3}
	meta, ok := resolveFormEnum(svc, "Thing", "services.things.v1.ThingKind")
	if !ok {
		t.Fatal("ThingKind should resolve")
	}
	if meta.ZeroRef != "ThingKind.NONE" {
		t.Errorf("ZeroRef = %q, want ThingKind.NONE", meta.ZeroRef)
	}
	if len(meta.Values) != 2 || meta.Values[0].Ref != "ThingKind.RESIDENTIAL" || meta.Values[1].Ref != "ThingKind.COMMERCIAL" {
		t.Errorf("Values = %+v, want RESIDENTIAL, COMMERCIAL", meta.Values)
	}

	delete(svc.EnumNumbers, "services.things.v1.ThingKind")
	meta, _ = resolveFormEnum(svc, "Thing", "services.things.v1.ThingKind")
	if meta.ZeroRef != "ThingKind.NONE" || len(meta.Values) != 2 {
		t.Errorf("without EnumNumbers: ZeroRef = %q, Values = %+v; want the first value as the zero", meta.ZeroRef, meta.Values)
	}
}

// TestEnumOptional_NoneIsAChoice: an `optional` enum is a nullable column —
// "none" is a real answer, so the select offers it and an empty choice
// submits as unset rather than the zero.
func TestEnumOptional_NoneIsAChoice(t *testing.T) {
	svc := fieldRulesSvc()
	for name, fields := range svc.Schemas {
		for i := range fields {
			if fields[i].Name == "kind" && name != "services.things.v1.ListThingsRequest" {
				fields[i].Optional = true
			}
		}
	}
	page := fieldRulesPage(t, svc)

	kind := formFieldNamed(t, page.CreateFields, "kind")
	if want := `z.preprocess((v) => (v === "" ? undefined : v), z.coerce.number().pipe(z.nativeEnum(ThingKind)).refine((v): boolean => v !== ThingKind.UNSPECIFIED, "Choose a value").optional())`; kind.ZodExpr != want {
		t.Errorf("optional kind zod = %s, want %s", kind.ZodExpr, want)
	}
	for _, tmplKind := range []string{"pages", "vite-spa-pages"} {
		for _, name := range []string{"create-page.tsx.tmpl", "edit-page.tsx.tmpl"} {
			out := renderPageTemplate(t, tmplKind, name, page)
			if !strings.Contains(out, `<option value="">None</option>`) {
				t.Errorf("%s/%s optional enum select has no None choice:\n%s", tmplKind, name, out)
			}
		}
		create := renderPageTemplate(t, tmplKind, "create-page.tsx.tmpl", page)
		if !strings.Contains(create, `defaultValue=""`) {
			t.Errorf("%s create optional enum select should start at None:\n%s", tmplKind, create)
		}
	}
}

// TestCreatePage_NavigatesToCreatedRecord: Create lands on the record it
// just made. Its detail page is where the next step starts, and returning
// to the list makes the author find the row they were just looking at.
func TestCreatePage_NavigatesToCreatedRecord(t *testing.T) {
	page := fieldRulesPage(t, fieldRulesSvc())
	if page.CreateEntityFieldCamel != "thing" {
		t.Fatalf("CreateEntityFieldCamel = %q, want thing (CreateThingResponse.thing)", page.CreateEntityFieldCamel)
	}
	want := map[string]string{
		"pages":          "onSuccess: ({ thing: created }) => router.push(created ? entityViewHref(\"things\", created.id) : \"/things\"),",
		"vite-spa-pages": "onSuccess: ({ thing: created }) => void navigate({ to: created ? `/things/${created.id}` : \"/things\" }),",
	}
	for kind, line := range want {
		create := renderPageTemplate(t, kind, "create-page.tsx.tmpl", page)
		if !strings.Contains(create, line) {
			t.Errorf("%s create page does not navigate to the created record; want %s in:\n%s", kind, line, create)
		}
	}

	// A create response that does not carry the entity has no id to go to:
	// the page falls back to the list rather than guessing.
	svc := fieldRulesSvc()
	svc.Messages["CreateThingResponse"] = []MessageFieldDef{{Name: "ok", ProtoType: "bool"}}
	bare := fieldRulesPage(t, svc)
	if bare.CreateEntityFieldCamel != "" {
		t.Errorf("CreateEntityFieldCamel = %q for a response without the entity, want empty", bare.CreateEntityFieldCamel)
	}
	if out := renderPageTemplate(t, "pages", "create-page.tsx.tmpl", bare); !strings.Contains(out, `onSuccess: () => router.push("/things"),`) {
		t.Errorf("create page without a returned entity should fall back to the list:\n%s", out)
	}
}

// TestForeignKey_RequiredOnlyWhenNotOptional: a resolved foreign key is
// born `NOT NULL REFERENCES <parent>` with no DEFAULT, so an empty pick can
// never be stored — the picker requires a choice. An `optional` reference is
// a nullable column, and clearing the picker submits it as unset.
func TestForeignKey_RequiredOnlyWhenNotOptional(t *testing.T) {
	svc := fieldRulesSvc()
	add := func(msg string, f SchemaFieldDef) {
		svc.Schemas[msg] = append(svc.Schemas[msg], f)
	}
	for _, msg := range []string{"services.things.v1.Thing", "services.things.v1.CreateThingRequest"} {
		add(msg, SchemaFieldDef{Name: "owner_id", Kind: "string"})
		add(msg, SchemaFieldDef{Name: "crew_id", Kind: "string", Optional: true})
	}
	page := fieldRulesPage(t, svc)
	referents := map[string]PageFieldFK{
		"owner": {EntityName: "Owner", ListHook: "useListOwners", GetHook: "useGetOwner", HooksModule: "@/hooks/x", ItemsField: "owners", PkFieldCamel: "id"},
		"crew":  {EntityName: "Crew", ListHook: "useListCrews", GetHook: "useGetCrew", HooksModule: "@/hooks/x", ItemsField: "crews", PkFieldCamel: "id"},
	}
	AttachForeignKeys(&page, referents)

	for _, form := range []struct {
		name   string
		fields []PageField
	}{{"create", page.CreateFields}, {"edit", page.UpdateFields}} {
		owner := formFieldNamed(t, form.fields, "ownerId")
		if owner.FK == nil || !owner.Required || owner.ZodExpr != `z.string().min(1, "Required")` {
			t.Errorf("%s ownerId: FK=%v Required=%v zod=%s; want a required picker", form.name, owner.FK != nil, owner.Required, owner.ZodExpr)
		}
		crew := formFieldNamed(t, form.fields, "crewId")
		if crew.FK == nil || crew.Required || crew.ZodExpr != `z.preprocess((v) => (v === "" ? undefined : v), z.string().optional())` {
			t.Errorf("%s crewId: FK=%v Required=%v zod=%s; want an optional picker whose cleared value is unset", form.name, crew.FK != nil, crew.Required, crew.ZodExpr)
		}
	}
}
