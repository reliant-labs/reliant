package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// fkPatientPage / fkOrderPage: an Order whose `patient_id` points at a
// fully-CRUD Patient — the ordinary relational shape every scaffolded app
// has, and the one forge's page generator had no field kind for.
func fkPatientPage() codegen.PageTemplateData {
	return codegen.PageTemplateData{
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

func fkOrderPage() codegen.PageTemplateData {
	return codegen.PageTemplateData{
		EntityName:           "Order",
		EntityNamePlural:     "Orders",
		EntitySlug:           "orders",
		HasList:              true,
		HasGet:               true,
		HasCreate:            true,
		HasUpdate:            true,
		HasDelete:            true,
		ListRPC:              "ListOrders",
		GetRPC:               "GetOrder",
		CreateRPC:            "CreateOrder",
		UpdateRPC:            "UpdateOrder",
		DeleteRPC:            "DeleteOrder",
		HooksImportPath:      "@/hooks/clinic-service-hooks",
		TypesImportPath:      "@/gen/services/clinic/v1/clinic_pb",
		EntityTypeImportPath: "@/gen/services/clinic/v1/clinic_pb",
		ItemsField:           "orders",
		PkFieldCamel:         "id",
		CreateFields: []codegen.PageField{
			{Name: "patientId", ProtoName: "patient_id", Label: "Patient Id", Type: "text", ProtoType: "string", Required: true},
			{Name: "notes", ProtoName: "notes", Label: "Notes", Type: "text", ProtoType: "string"},
		},
		UpdateFields: []codegen.PageField{
			{Name: "patientId", ProtoName: "patient_id", Label: "Patient Id", Type: "text", ProtoType: "string", Required: true},
			{Name: "notes", ProtoName: "notes", Label: "Notes", Type: "text", ProtoType: "string"},
		},
		Columns: []codegen.EntityPageField{
			{Name: "patientId", Label: "Patient Id"},
			{Name: "notes", Label: "Notes"},
		},
	}
}

func renderFKPage(t *testing.T, dir, name string, page codegen.PageTemplateData) string {
	t.Helper()
	tmpl, err := loadPageTemplate(dir, name)
	if err != nil {
		t.Fatalf("load %s/%s: %v", dir, name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, page); err != nil {
		t.Fatalf("render %s/%s: %v", dir, name, err)
	}
	return string(templates.CanonicalTSImportOrder(buf.Bytes()))
}

func attachedOrderPage(t *testing.T) codegen.PageTemplateData {
	t.Helper()
	referents := codegen.BuildFKReferents([]codegen.PageTemplateData{fkPatientPage(), fkOrderPage()})
	page := fkOrderPage()
	codegen.AttachForeignKeys(&page, referents)
	if page.CreateFields[0].FK == nil {
		t.Fatal("fixture did not resolve patient_id — the render assertions would be vacuous")
	}
	return page
}

// TestScaffoldedFormsUseEntityPickerForForeignKeys is the render half of the
// biggest finding of the dogfood forensics: forge SHIPPED <EntityPicker> and
// then its own generator emitted
//
//	<input type="text" id="patientId" {...register("patientId")} />
//
// for a UUID on every create and edit form, while entity-picker.tsx in the
// same tree says verbatim "never a raw text input for a UUID". Three units
// fixed it by hand, and only because their charters said to in bold.
func TestScaffoldedFormsUseEntityPickerForForeignKeys(t *testing.T) {
	page := attachedOrderPage(t)

	for _, dir := range []string{"pages", "vite-spa-pages"} {
		t.Run(dir+"/create", func(t *testing.T) {
			out := renderFKPage(t, dir, "create-page.tsx.tmpl", page)

			if strings.Contains(out, `id="patientId"`) && strings.Contains(out, `{...register("patientId")}`) {
				t.Errorf("foreign key still renders a raw text input:\n%s", out)
			}
			for _, want := range []string{
				`import { EntityPicker } from "@/components/entity-picker";`,
				`import { Controller, useForm } from "react-hook-form";`,
				`useList={ useListPatients }`,
				`itemsOf={(res) => res.patients}`,
				`hasMoreOf={(res) => Boolean(res.nextPageToken)}`,
				`optionValue={(item) => item.id}`,
				`optionLabel={(item) => String(item.fullName)}`,
				`buildRequest={(search) => ({ pageSize: 20, search: search || undefined })}`,
				`placeholder="Select a patient"`,
				// The label stops claiming the control edits an id.
				`>\n            Patient *`,
			} {
				want = strings.ReplaceAll(want, `\n`, "\n")
				if !strings.Contains(out, want) {
					t.Errorf("create page missing %q:\n%s", want, out)
				}
			}
			// One merged import per generated hooks module: the entity's own
			// Create hook and the referent's List hook share a service here.
			if n := strings.Count(out, `from "@/hooks/clinic-service-hooks"`); n != 1 {
				t.Errorf("expected ONE import from the shared hooks module, got %d:\n%s", n, out)
			}
			if !strings.Contains(out, `import { useCreateOrder, useListPatients } from "@/hooks/clinic-service-hooks";`) {
				t.Errorf("hook imports not merged:\n%s", out)
			}
			// A create form starts empty, so it must not carry an
			// <EntityName> lookup that can never render.
			if strings.Contains(out, "EntityName") {
				t.Errorf("create page emitted a selectedLabel resolver it never renders:\n%s", out)
			}
		})

		t.Run(dir+"/edit", func(t *testing.T) {
			out := renderFKPage(t, dir, "edit-page.tsx.tmpl", page)

			for _, want := range []string{
				`import { EntityName } from "@/components/entity-name";`,
				`import { EntityPicker } from "@/components/entity-picker";`,
				`useList={ useListPatients }`,
				// The edit form's value arrives from the server, so the CLOSED
				// picker has no row to read a name from — without this it shows
				// the raw UUID, which is the whole defect.
				`selectedLabel={`,
				`useGet={ useGetPatient }`,
				`nameOf={(res) => res.patient?.fullName}`,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("edit page missing %q:\n%s", want, out)
				}
			}
			if !strings.Contains(out, `import { useGetOrder, useGetPatient, useListPatients, useUpdateOrder } from "@/hooks/clinic-service-hooks";`) {
				t.Errorf("edit hook imports not merged:\n%s", out)
			}
		})

		t.Run(dir+"/detail", func(t *testing.T) {
			out := renderFKPage(t, dir, "detail-page.tsx.tmpl", page)

			if strings.Contains(out, "formatValue(item.patientId)") {
				t.Errorf("detail page still prints the raw foreign-key id:\n%s", out)
			}
			for _, want := range []string{
				`import { EntityName } from "@/components/entity-name";`,
				`id={item.patientId}`,
				`useGet={ useGetPatient }`,
				`nameOf={(res) => res.patient?.fullName}`,
				`label: "Patient",`,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("detail page missing %q:\n%s", want, out)
				}
			}
			// Non-FK columns are untouched.
			if !strings.Contains(out, "formatValue(item.notes)") {
				t.Errorf("detail page lost its ordinary columns:\n%s", out)
			}
		})
	}
}

// TestScaffoldedFormsUnchangedWithoutForeignKeys is the "does this help an
// app with ONE entity / no relationships?" gate: an entity with no resolvable
// FK must render EXACTLY what it rendered before — no picker, no Controller,
// no extra imports.
func TestScaffoldedFormsUnchangedWithoutForeignKeys(t *testing.T) {
	page := fkOrderPage()
	// No referents at all — the single-entity project.
	codegen.AttachForeignKeys(&page, codegen.BuildFKReferents([]codegen.PageTemplateData{page}))

	for _, dir := range []string{"pages", "vite-spa-pages"} {
		for _, name := range []string{"create-page.tsx.tmpl", "edit-page.tsx.tmpl", "detail-page.tsx.tmpl"} {
			out := renderFKPage(t, dir, name, page)
			for _, unwanted := range []string{"EntityPicker", "EntityName", "Controller"} {
				if strings.Contains(out, unwanted) {
					t.Errorf("%s/%s pulled in %s with no foreign key present:\n%s", dir, name, unwanted, out)
				}
			}
			if name != "detail-page.tsx.tmpl" && !strings.Contains(out, `{...register("patientId")}`) {
				t.Errorf("%s/%s: an unresolved *_id must keep its plain input:\n%s", dir, name, out)
			}
			if name == "detail-page.tsx.tmpl" && !strings.Contains(out, "formatValue(item.patientId)") {
				t.Errorf("%s/%s: an unresolved *_id must keep printing its raw value:\n%s", dir, name, out)
			}
		}
	}
}

// TestFKPickerTurnsOffSearchWithoutAFilterField: when the referent's List RPC
// declares no free-text filter, `buildRequest` has nowhere to put the search
// text. Shipping a search box that silently does nothing is the same class of
// defect as the dead `?status=` links — so the emitted call site turns it off.
func TestFKPickerTurnsOffSearchWithoutAFilterField(t *testing.T) {
	patient := fkPatientPage()
	patient.SearchFilterField = ""
	patient.NextTokenField = ""

	page := fkOrderPage()
	codegen.AttachForeignKeys(&page, codegen.BuildFKReferents([]codegen.PageTemplateData{patient, page}))

	out := renderFKPage(t, "pages", "create-page.tsx.tmpl", page)
	if !strings.Contains(out, "searchable={false}") {
		t.Errorf("a referent with no server-side text filter must not ship a dead search box:\n%s", out)
	}
	if !strings.Contains(out, "buildRequest={() => ({ pageSize: 20 })}") {
		t.Errorf("buildRequest must drop the unused search parameter:\n%s", out)
	}
	if strings.Contains(out, "hasMoreOf=") {
		t.Errorf("a List response with no next_page_token has no more-rows signal to report:\n%s", out)
	}
}

// TestPagePartialsAreParsedIntoEveryPageTemplate guards the wiring itself: a
// page template that invokes {{template "fkPickerField" .}} without the
// partials parsed into its set fails at RENDER time, which is after the gate
// most people run.
func TestPagePartialsAreParsedIntoEveryPageTemplate(t *testing.T) {
	for _, dir := range []string{"pages", "vite-spa-pages"} {
		for _, name := range []string{"list-page.tsx.tmpl", "detail-page.tsx.tmpl", "create-page.tsx.tmpl", "edit-page.tsx.tmpl"} {
			tmpl, err := loadPageTemplate(dir, name)
			if err != nil {
				t.Fatalf("load %s/%s: %v", dir, name, err)
			}
			for _, partial := range []string{"fkPickerField", "fkNameValue"} {
				if tmpl.Lookup(partial) == nil {
					t.Errorf("%s/%s: partial %q not in the template set", dir, name, partial)
				}
			}
			// The page's own body must survive the partial parse — a
			// second Parse call carrying non-whitespace text would replace it.
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, fkOrderPage()); err != nil {
				t.Fatalf("execute %s/%s: %v", dir, name, err)
			}
			if !strings.Contains(buf.String(), "export default function") {
				t.Errorf("%s/%s rendered an empty body — the partials parse clobbered it:\n%s", dir, name, buf.String())
			}
		}
	}
}
