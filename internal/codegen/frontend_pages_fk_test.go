package codegen

import (
	"testing"
)

// patientReferent is the shape BuildFKReferents produces for a fully-CRUD
// Patient entity — the thing an `order.patient_id` field must resolve to.
func patientPage() PageTemplateData {
	return PageTemplateData{
		EntityName:        "Patient",
		EntityNamePlural:  "Patients",
		EntitySlug:        "patients",
		HasList:           true,
		HasGet:            true,
		ListRPC:           "ListPatients",
		GetRPC:            "GetPatient",
		HooksImportPath:   "@/hooks/clinic-service-hooks",
		ItemsField:        "patients",
		NextTokenField:    "nextPageToken",
		HasPageSize:       true,
		SearchFilterField: "search",
		PkFieldCamel:      "id",
		DisplayField:      "fullName",
	}
}

func orderPage() PageTemplateData {
	return PageTemplateData{
		EntityName:      "Order",
		EntitySlug:      "orders",
		HasList:         true,
		HasGet:          true,
		HasCreate:       true,
		HasUpdate:       true,
		ListRPC:         "ListOrders",
		GetRPC:          "GetOrder",
		CreateRPC:       "CreateOrder",
		UpdateRPC:       "UpdateOrder",
		HooksImportPath: "@/hooks/clinic-service-hooks",
		ItemsField:      "orders",
		PkFieldCamel:    "id",
		CreateFields: []PageField{
			{Name: "patientId", ProtoName: "patient_id", Label: "Patient Id", Type: "text", ProtoType: "string", Required: true},
			{Name: "notes", ProtoName: "notes", Label: "Notes", Type: "text", ProtoType: "string"},
		},
		UpdateFields: []PageField{
			{Name: "patientId", ProtoName: "patient_id", Label: "Patient Id", Type: "text", ProtoType: "string", Required: true},
		},
		Columns: []EntityPageField{
			{Name: "patientId", Label: "Patient Id"},
			{Name: "notes", Label: "Notes"},
		},
	}
}

// TestAttachForeignKeys_ResolvesEntityReferences is the unit half of the
// defect the dogfood run named the biggest of the run: forge shipped
// <EntityPicker>/<EntityName> and its own page generator had no FK field
// kind, so every scaffolded form rendered a raw text input for a UUID —
// while entity-picker.tsx in the same tree says verbatim "never a raw text
// input for a UUID".
func TestAttachForeignKeys_ResolvesEntityReferences(t *testing.T) {
	referents := BuildFKReferents([]PageTemplateData{patientPage(), orderPage()})
	page := orderPage()
	AttachForeignKeys(&page, referents)

	fk := page.CreateFields[0].FK
	if fk == nil {
		t.Fatal("patient_id did not resolve to the Patient entity — the form still renders a UUID textbox")
	}
	if fk.EntityName != "Patient" || fk.ListHook != "useListPatients" || fk.GetHook != "useGetPatient" {
		t.Errorf("referent wired wrong: %+v", *fk)
	}
	if fk.ItemsField != "patients" || fk.PkFieldCamel != "id" || fk.LabelField != "fullName" {
		t.Errorf("referent projection wrong: %+v", *fk)
	}
	if fk.SearchFilterField != "search" || !fk.HasPageSize || fk.NextTokenField != "nextPageToken" {
		t.Errorf("referent list-shape projection wrong: %+v", *fk)
	}
	if fk.ResolveSelectedLabel {
		t.Error("a CREATE form starts empty — it must not emit a selectedLabel lookup that never renders")
	}

	// The label stops lying: the control shows a name, so "Patient Id" is
	// wrong, and the field's own wording survives.
	if page.CreateFields[0].Label != "Patient" {
		t.Errorf("FK label = %q, want %q", page.CreateFields[0].Label, "Patient")
	}

	// Non-FK fields are untouched.
	if page.CreateFields[1].FK != nil {
		t.Errorf("a plain string field must not resolve to an entity: %+v", *page.CreateFields[1].FK)
	}

	// Edit forms resolve the server-loaded id to a name.
	if page.UpdateFields[0].FK == nil || !page.UpdateFields[0].FK.ResolveSelectedLabel {
		t.Error("an EDIT form's picker must resolve its server-loaded value through <EntityName>")
	}

	// Detail columns resolve too — one row, one lookup.
	if page.Columns[0].FK == nil {
		t.Error("the detail page must resolve a foreign-key column to a name")
	}
	if page.Columns[0].Label != "Patient" {
		t.Errorf("FK column label = %q, want %q", page.Columns[0].Label, "Patient")
	}
	if page.Columns[1].FK != nil {
		t.Error("a plain column must not resolve to an entity")
	}
}

// TestAttachForeignKeys_RefusesWhatItCannotResolve pins the conservative
// half. forge must not invent a picker: plenty of `*_id` columns point at a
// record in some other system entirely (a Stripe customer, an upstream
// account), and a picker over the wrong entity is worse than a text input.
func TestAttachForeignKeys_RefusesWhatItCannotResolve(t *testing.T) {
	listOnly := patientPage()
	listOnly.EntityName = "Provider"
	listOnly.ListRPC = "ListProviders"
	listOnly.ItemsField = "providers"
	listOnly.HasGet = false // no Get RPC → nothing can resolve a single id

	referents := BuildFKReferents([]PageTemplateData{patientPage(), listOnly, orderPage()})

	cases := []struct {
		name  string
		field PageField
		want  bool
	}{
		{"unknown entity", PageField{Name: "stripeCustomerId", ProtoName: "stripe_customer_id", ProtoType: "string"}, false},
		{"referent has no Get RPC", PageField{Name: "providerId", ProtoName: "provider_id", ProtoType: "string"}, false},
		{"not a string", PageField{Name: "patientId", ProtoName: "patient_id", ProtoType: "int64"}, false},
		{"repeated", PageField{Name: "patientId", ProtoName: "patient_id", ProtoType: "string", IsRepeated: true}, false},
		{"the primary key itself", PageField{Name: "id", ProtoName: "id", ProtoType: "string"}, false},
		{"resolves", PageField{Name: "patientId", ProtoName: "patient_id", ProtoType: "string"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := orderPage()
			page.CreateFields = []PageField{tc.field}
			page.UpdateFields = nil
			page.Columns = nil
			AttachForeignKeys(&page, referents)
			if got := page.CreateFields[0].FK != nil; got != tc.want {
				t.Errorf("resolved = %v, want %v for %+v", got, tc.want, tc.field)
			}
		})
	}
}

// TestAttachForeignKeys_SkipsSelfReference: a `parent_id` on Category
// resolving to Category would browse the very rows being edited. Whether a
// tree parent is pickable, and which rows are legal parents, is a domain
// decision — not a generated default.
func TestAttachForeignKeys_SkipsSelfReference(t *testing.T) {
	category := patientPage()
	category.EntityName = "Category"
	category.ListRPC = "ListCategories"
	category.GetRPC = "GetCategory"
	category.ItemsField = "categories"

	page := category
	page.CreateFields = []PageField{
		{Name: "categoryId", ProtoName: "category_id", Label: "Category Id", ProtoType: "string"},
	}
	AttachForeignKeys(&page, BuildFKReferents([]PageTemplateData{category}))
	if page.CreateFields[0].FK != nil {
		t.Error("a self-referencing FK must stay a plain input")
	}
}

// TestAttachForeignKeys_AmbiguousEntityNameResolvesToNothing: two services
// both owning an entity called Patient give the picker no way to choose,
// and guessing one is worse than leaving the field alone.
func TestAttachForeignKeys_AmbiguousEntityNameResolvesToNothing(t *testing.T) {
	other := patientPage()
	other.HooksImportPath = "@/hooks/billing-service-hooks"

	referents := BuildFKReferents([]PageTemplateData{patientPage(), other})
	if _, ok := referents["patient"]; ok {
		t.Error("an entity name claimed by two services must not resolve")
	}
}

// TestPageHookImports_MergePerModule: a page whose entity and whose referent
// live on the SAME service share a generated hooks module, and two import
// statements for one module is an eslint import/no-duplicates error on a
// file forge just wrote.
func TestPageHookImports_MergePerModule(t *testing.T) {
	referents := BuildFKReferents([]PageTemplateData{patientPage(), orderPage()})
	page := orderPage()
	AttachForeignKeys(&page, referents)

	create := page.CreateHookImports()
	if len(create) != 1 {
		t.Fatalf("same-module hooks must merge into ONE import: %+v", create)
	}
	wantCreate := []string{"useCreateOrder", "useListPatients"}
	if got := create[0].Symbols; len(got) != len(wantCreate) || got[0] != wantCreate[0] || got[1] != wantCreate[1] {
		t.Errorf("create hook symbols = %v, want %v", got, wantCreate)
	}

	edit := page.EditHookImports()
	if len(edit) != 1 {
		t.Fatalf("same-module hooks must merge into ONE import: %+v", edit)
	}
	wantEdit := []string{"useGetOrder", "useGetPatient", "useListPatients", "useUpdateOrder"}
	if got := edit[0].Symbols; len(got) != len(wantEdit) {
		t.Errorf("edit hook symbols = %v, want %v", got, wantEdit)
	}

	// A referent on a DIFFERENT service gets its own import line.
	crossService := patientPage()
	crossService.HooksImportPath = "@/hooks/registry-service-hooks"
	page2 := orderPage()
	AttachForeignKeys(&page2, BuildFKReferents([]PageTemplateData{crossService, orderPage()}))
	if got := page2.CreateHookImports(); len(got) != 2 {
		t.Errorf("a cross-service referent needs its own import line: %+v", got)
	}
}

// TestHasCreateRegistered: a form whose every field is a foreign key drives
// them all through <Controller>, so destructuring `register` at all would
// ship an unused binding — an eslint error on a freshly-scaffolded page.
func TestHasCreateRegistered(t *testing.T) {
	referents := BuildFKReferents([]PageTemplateData{patientPage(), orderPage()})

	mixed := orderPage()
	AttachForeignKeys(&mixed, referents)
	if !mixed.HasCreateRegistered() {
		t.Error("a form with a plain field still needs register()")
	}

	allFK := orderPage()
	allFK.CreateFields = allFK.CreateFields[:1] // patientId only
	AttachForeignKeys(&allFK, referents)
	if allFK.HasCreateRegistered() {
		t.Error("an all-FK form must not destructure register()")
	}
	if !allFK.HasCreateFK() {
		t.Error("HasCreateFK must gate the Controller/EntityPicker imports")
	}
}

// TestDeriveDisplayField_ResolvesTheWholeHumanLabel pins the widening.
//
// The FK picker, the detail-page title and the breadcrumb ALL read
// DisplayField. It used to match only the literal names `name` and `title`, so
// a Patient carrying `full_name` — the most common spelling for a person —
// resolved to "" and every one of the three fell back to the primary key. A
// human cannot pick a patient from a list of ULIDs, and the picker forge
// shipped to end exactly that defect reintroduced it one layer down.
func TestDeriveDisplayField_ResolvesTheWholeHumanLabel(t *testing.T) {
	str := func(names ...string) []EntityField {
		out := make([]EntityField, 0, len(names)+1)
		out = append(out, EntityField{Name: "id", ProtoType: "string", Kind: FieldKindScalar})
		for _, n := range names {
			out = append(out, EntityField{Name: n, ProtoType: "string", Kind: FieldKindScalar})
		}
		return out
	}

	cases := []struct {
		entity string
		fields []EntityField
		want   string
		why    string
	}{
		{"Patient", str("full_name"), "fullName", "full_name is the whole label; the defect that motivated this"},
		{"Patient", str("display_name"), "displayName", "display_name likewise"},
		{"Patient", str("patient_name"), "patientName", "<entity>_name: the label qualified by its own entity"},
		{"CarePlan", str("care_plan_title"), "carePlanTitle", "<entity>_title, snake-cased from a multi-word entity name"},
		{"Task", str("name", "title", "full_name"), "name", "name outranks every other spelling"},
		{"Task", str("title", "full_name"), "title", "title outranks full_name"},
		{"Task", str("full_name", "display_name"), "displayName", "display_name outranks full_name regardless of field order"},

		// The honest gaps. Each keeps "" so the id fallback and its
		// retarget-this-prop comment stay visible instead of a wrong guess.
		{"Person", str("first_name", "last_name"), "", "a part-of-the-name column labels forty rows \"John\" and hides the surname"},
		{"User", str("email"), "", "an email is not a name-shaped column"},
		{"Reading", str("value"), "", "no human label at all"},
		{"Patient", str("other_name"), "", "<other>_name names a DIFFERENT entity's label, not this one's"},
	}
	for _, tc := range cases {
		got := deriveDisplayField(EntityDef{Name: tc.entity, PkField: "id", Fields: tc.fields})
		if got != tc.want {
			t.Errorf("deriveDisplayField(%s%v) = %q, want %q — %s", tc.entity, fieldNames(tc.fields), got, tc.want, tc.why)
		}
	}

	// A non-string column that happens to be called "name" is not a label.
	got := deriveDisplayField(EntityDef{Name: "Sensor", PkField: "id", Fields: []EntityField{
		{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
		{Name: "name", ProtoType: "int64", Kind: FieldKindScalar},
		{Name: "full_name", ProtoType: "string", Kind: FieldKindScalar},
	}})
	if got != "fullName" {
		t.Errorf("a non-string `name` must not win over a string `full_name`; got %q", got)
	}
}

func fieldNames(fs []EntityField) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}
