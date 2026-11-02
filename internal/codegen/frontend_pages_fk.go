package codegen

import (
	"sort"
	"strings"
)

// Foreign keys in generated pages.
//
// forge shipped <EntityPicker> and <EntityName> — the two halves of "a form
// field holds another entity's id" — and then its own page generator never
// used them. Every scaffolded create/edit form rendered
//
//	<input type="text" id="patientId" {...register("patientId")} />
//
// for a UUID, while entity-picker.tsx in the same tree says verbatim "never a
// raw text input for a UUID". This file closes that: it resolves which form
// fields are foreign keys and hands the templates everything they need to
// drive the referent's GENERATED hooks.
//
// Resolution mirrors the rule the migration writer already uses
// (scaffold.referencedTable): a `<owner>_id` field resolves to the entity
// `<Owner>`, and ONLY if that entity is a live CRUD entity in this project
// with a List RPC to browse and a Get RPC to resolve a single id. It is
// deliberately conservative — plenty of `*_id` columns legitimately point at
// a record in some other system (a Stripe customer, an upstream account), and
// forge must not invent a picker for one. An unresolved `*_id` keeps the
// plain text input, exactly as before.
//
// Nothing here is configurable. The single judgement call — which column is
// the human-readable label — reuses DisplayField, the same whole-label
// heuristic the list and detail pages already title records with (see
// deriveDisplayField), and the emitted call site is an ordinary prop the user
// can retarget.

// PageFieldFK describes the entity a foreign-key field points at, in the
// terms the page templates need to render <EntityPicker> / <EntityName>
// against that entity's generated hooks.
type PageFieldFK struct {
	// EntityName is the referent's PascalCase entity name ("Patient").
	EntityName string
	// EntityLower is the lowercased singular ("patient"), used in
	// placeholder text ("Select a patient").
	EntityLower string
	// ListHook / GetHook are the referent's generated hook identifiers
	// ("useListPatients" / "useGetPatient").
	ListHook string
	GetHook  string
	// HooksModule is the TS module both hooks come from
	// ("@/hooks/admin-service-hooks"). It is often the SAME module as the
	// page's own hooks — CreateHookImports and friends merge them, because
	// two import statements for one module is an eslint error.
	HooksModule string
	// ItemsField is the camelCase repeated field on the referent's List
	// response ("patients") — what <EntityPicker itemsOf> reads.
	ItemsField string
	// NextTokenField is the referent's forward-cursor response field
	// ("nextPageToken"); empty when the List response declares none, in
	// which case the picker renders no "keep typing to narrow" hint.
	NextTokenField string
	// HasPageSize reports whether the referent's List request carries
	// page_size — the picker caps its popover page with it.
	HasPageSize bool
	// SearchFilterField is the referent's free-text List filter
	// ("search"/"query"/"q"). Empty means the referent's List RPC declares
	// no server-side text filter, so the picker CANNOT narrow: the emitted
	// call site turns the search box off rather than shipping one that
	// silently does nothing.
	SearchFilterField string
	// PkFieldCamel is the referent's primary key ("id") — the value a
	// picked option resolves to, and the Get request field.
	PkFieldCamel string
	// LabelField is the referent's human-readable display column
	// ("fullName"), reused from DisplayField. Empty when the entity has no
	// name/title-shaped field; the call site then falls back to the id and
	// carries a comment saying which prop to retarget.
	LabelField string
	// GetEntityField is the camelCase field the referent's Get response
	// wraps the entity in ("patient") — what <EntityName nameOf> reads.
	GetEntityField string
	// ResolveSelectedLabel asks the emitted picker for a <EntityName>
	// selectedLabel. Set on EDIT forms only: the value arrives from the
	// server, so the closed picker has no row to read a name from and would
	// otherwise display the raw UUID — the exact defect the picker exists to
	// remove. A CREATE form starts empty, so the same markup there would be
	// an import and a component that never render.
	ResolveSelectedLabel bool
}

// PageHookImport is one merged `import { a, b } from "module";` line a page
// emits for generated hooks. Merged because a page that references its own
// entity's hooks AND a referent's from the same generated module must emit
// exactly one import statement for it.
type PageHookImport struct {
	Module  string
	Symbols []string
}

// BuildFKReferents indexes the project's CRUD pages by the entity name a
// `<owner>_id` field would name, so field resolution is a map lookup.
//
// Pages MUST have been through AttachEntityMeta — DisplayField and
// PkFieldCamel come from there. An entity without BOTH a List and a Get RPC
// is skipped: the picker browses with List and the closed-state label
// resolves with Get, so a half-CRUD entity cannot back one.
func BuildFKReferents(pages []PageTemplateData) map[string]PageFieldFK {
	referents := make(map[string]PageFieldFK, len(pages))
	for _, p := range pages {
		if !p.HasList || !p.HasGet {
			continue
		}
		if p.ItemsField == "" || p.PkFieldCamel == "" {
			continue
		}
		key := strings.ToLower(p.EntityName)
		if _, taken := referents[key]; taken {
			// Two services claiming one entity name: the picker cannot
			// choose, so neither wins and the field stays a text input.
			// Ambiguity is not a place to guess.
			referents[key] = PageFieldFK{}
			continue
		}
		referents[key] = PageFieldFK{
			EntityName:        p.EntityName,
			EntityLower:       strings.ToLower(p.EntityName),
			ListHook:          "use" + p.ListRPC,
			GetHook:           "use" + p.GetRPC,
			HooksModule:       p.HooksImportPath,
			ItemsField:        p.ItemsField,
			NextTokenField:    p.NextTokenField,
			HasPageSize:       p.HasPageSize,
			SearchFilterField: p.SearchFilterField,
			PkFieldCamel:      p.PkFieldCamel,
			LabelField:        p.DisplayField,
			GetEntityField:    getResponseEntityField(p),
		}
	}
	// Drop the ambiguous entries recorded above.
	for k, v := range referents {
		if v.EntityName == "" {
			delete(referents, k)
		}
	}
	return referents
}

// AttachForeignKeys marks every create/update form field and every detail
// column of `page` that is a foreign key onto one of `referents`.
//
// A field qualifies when its proto name ends in `_id`, the remaining owner
// resolves to a referent, and the field is a plain string — an integer
// `*_id` is not pointing at a forge entity (whose primary key is TEXT), and
// a repeated or enum field is not a single reference.
//
// A self-reference (`parent_id` on Category resolving to Category) is left
// alone: the picker would browse the very rows being edited, and a tree
// parent picker is a domain decision, not a generated default.
func AttachForeignKeys(page *PageTemplateData, referents map[string]PageFieldFK) {
	if page == nil || len(referents) == 0 {
		return
	}
	for i := range page.CreateFields {
		fk := resolveFieldFK(page, page.CreateFields[i], referents)
		page.CreateFields[i].FK = fk
		if fk != nil {
			page.CreateFields[i].Label = trimIDLabel(page.CreateFields[i].Label)
			requireReference(&page.CreateFields[i])
		}
	}
	for i := range page.UpdateFields {
		fk := resolveFieldFK(page, page.UpdateFields[i], referents)
		if fk != nil {
			// Edit forms load their value from the server, so the closed
			// picker needs <EntityName> to turn that id into a name.
			fk.ResolveSelectedLabel = true
			page.UpdateFields[i].Label = trimIDLabel(page.UpdateFields[i].Label)
			requireReference(&page.UpdateFields[i])
		}
		page.UpdateFields[i].FK = fk
	}
	for i := range page.Columns {
		fk := resolveColumnFK(page, page.Columns[i], referents)
		page.Columns[i].FK = fk
		if fk != nil {
			page.Columns[i].Label = trimIDLabel(page.Columns[i].Label)
		}
	}
}

// requireReference marks a resolved, NON-optional foreign key required.
//
// This is the one required-ness that does not come from a protovalidate
// rule. It adds no inference of its own: the reference was already resolved
// (it is why the field is a picker), by the same `<owner>_id` rule
// scaffold.referencedTable used to birth the column as `NOT NULL REFERENCES
// <parent>` with no DEFAULT. An empty pick can therefore never be stored —
// the form would only trade its own message for a foreign-key violation from
// the database. An `optional` reference is a nullable column: its picker may
// be cleared, and EmptyIsUnset submits it as unset.
func requireReference(pf *PageField) {
	if pf.Optional || pf.Required {
		return
	}
	pf.Required = true
	attachZodExpr(pf)
}

// trimIDLabel drops the trailing " Id" from a resolved foreign key's display
// label. The control shows a NAME now, so "Patient Id" is a lie — and the
// field's own wording is kept ("Prescribing Provider Id" → "Prescribing
// Provider"), which a bare entity name would have thrown away.
func trimIDLabel(label string) string {
	if trimmed, ok := strings.CutSuffix(label, " Id"); ok && trimmed != "" {
		return trimmed
	}
	if trimmed, ok := strings.CutSuffix(label, " ID"); ok && trimmed != "" {
		return trimmed
	}
	return label
}

func resolveFieldFK(page *PageTemplateData, f PageField, referents map[string]PageFieldFK) *PageFieldFK {
	if f.IsEnum || f.IsRepeated || f.ProtoType != "string" {
		return nil
	}
	return lookupFKByProtoName(page, f.ProtoName, f.Name, referents)
}

func resolveColumnFK(page *PageTemplateData, c EntityPageField, referents map[string]PageFieldFK) *PageFieldFK {
	if c.IsBadge || c.IsMoney || c.EnumType != "" {
		return nil
	}
	// Columns carry only the camelCase name, so derive the proto name back.
	return lookupFKByProtoName(page, "", c.Name, referents)
}

// lookupFKByProtoName resolves `<owner>_id` / `<owner>Id` to a referent.
// protoName may be empty (columns don't carry it), in which case camelName
// is used.
func lookupFKByProtoName(page *PageTemplateData, protoName, camelName string, referents map[string]PageFieldFK) *PageFieldFK {
	owner := fkOwner(protoName, camelName)
	if owner == "" {
		return nil
	}
	ref, ok := referents[owner]
	if !ok {
		return nil
	}
	// Never a self-reference: browsing the entity being edited to pick its
	// own parent is a domain decision, not a generated default.
	if strings.EqualFold(ref.EntityName, page.EntityName) {
		return nil
	}
	return &ref
}

// getResponseEntityField returns the camelCase field a Get response wraps the
// entity in ("patient" on GetPatientResponse).
//
// It is GetEntityFieldCamel — the SAME field the referent's own detail and
// edit pages read the record off. Sharing it is the point: the picker's label
// and the page it links to read the same field, so if it is ever wrong for an
// entity it is wrong in one place. Page data built without it falls back to
// the AIP-134 update request's entity wrapper (read off the real descriptor),
// then to the field the entity scaffolder writes, as protoc-gen-es spells it.
//
// Templates reach it as PageTemplateData.GetEntityAccessor.
func getResponseEntityField(p PageTemplateData) string {
	if p.GetEntityFieldCamel != "" {
		return p.GetEntityFieldCamel
	}
	if p.UpdateEntityFieldCamel != "" {
		return p.UpdateEntityFieldCamel
	}
	if p.EntityName == "" {
		return ""
	}
	return entityFieldCamel(p.EntityName)
}

// GetEntityAccessor is the property the detail and edit pages read the
// record off a Get response (`data?.<accessor>`): GetEntityFieldCamel, with
// getResponseEntityField's fallbacks for page data assembled without it.
func (p PageTemplateData) GetEntityAccessor() string { return getResponseEntityField(p) }

// fkOwner returns the lowercased entity name a foreign-key field names, or
// "" when the field is not `<owner>_id` shaped. `id` itself is the primary
// key, not a reference.
func fkOwner(protoName, camelName string) string {
	name := protoName
	if name == "" {
		name = camelName
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Accept both spellings — proto fields are snake_case, columns camel.
	owner, ok := strings.CutSuffix(name, "_id")
	if !ok {
		owner, ok = strings.CutSuffix(name, "Id")
	}
	if !ok || owner == "" {
		return ""
	}
	// `patient_ids` (repeated) never reaches here; `id` has no owner.
	return strings.ToLower(strings.ReplaceAll(owner, "_", ""))
}

// ── Import merging ───────────────────────────────────────────────────────
//
// Each page emits ONE import statement per generated hooks module. A page
// whose entity and whose referent live on the same service share a module,
// and two `import … from "@/hooks/admin-service-hooks"` lines is an eslint
// import/no-duplicates error on a file forge just wrote.

// CreateHookImports returns the merged generated-hook imports the create
// page needs: its own Create hook plus the List hook of every FK field.
func (p PageTemplateData) CreateHookImports() []PageHookImport {
	m := newHookImportSet()
	if p.HasCreate {
		m.add(p.HooksImportPath, "use"+p.CreateRPC)
	}
	for _, f := range p.CreateFields {
		if f.FK != nil {
			m.add(f.FK.HooksModule, f.FK.ListHook)
		}
	}
	return m.sorted()
}

// EditHookImports returns the merged generated-hook imports the edit page
// needs: its own Get + Update hooks, plus each FK field's List hook (the
// picker) and Get hook (the closed picker's <EntityName> label).
func (p PageTemplateData) EditHookImports() []PageHookImport {
	m := newHookImportSet()
	if p.HasGet {
		m.add(p.HooksImportPath, "use"+p.GetRPC)
	}
	if p.HasUpdate {
		m.add(p.HooksImportPath, "use"+p.UpdateRPC)
	}
	for _, f := range p.UpdateFields {
		if f.FK != nil {
			m.add(f.FK.HooksModule, f.FK.ListHook)
			m.add(f.FK.HooksModule, f.FK.GetHook)
		}
	}
	return m.sorted()
}

// DetailHookImports returns the merged generated-hook imports the detail
// page needs: its own Get (+ Delete) hooks plus each FK column's Get hook.
func (p PageTemplateData) DetailHookImports() []PageHookImport {
	m := newHookImportSet()
	if p.HasGet {
		m.add(p.HooksImportPath, "use"+p.GetRPC)
	}
	if p.HasDelete {
		m.add(p.HooksImportPath, "use"+p.DeleteRPC)
	}
	for _, c := range p.Columns {
		if c.FK != nil {
			m.add(c.FK.HooksModule, c.FK.GetHook)
		}
	}
	return m.sorted()
}

// HasCreateFK reports whether the create form has a foreign-key field, which
// is what gates the <EntityPicker> / <EntityName> / Controller imports on the
// create page.
func (p PageTemplateData) HasCreateFK() bool { return anyFieldFK(p.CreateFields) }

// HasUpdateFK is HasCreateFK for the update form.
func (p PageTemplateData) HasUpdateFK() bool { return anyFieldFK(p.UpdateFields) }

// HasDetailFK reports whether any listed column is a foreign key, which is
// what gates the <EntityName> import on the detail page.
func (p PageTemplateData) HasDetailFK() bool {
	for _, c := range p.Columns {
		if c.FK != nil {
			return true
		}
	}
	return false
}

func anyFieldFK(fields []PageField) bool {
	for _, f := range fields {
		if f.FK != nil {
			return true
		}
	}
	return false
}

// HasCreateRegistered reports whether the create form has any field driven by
// react-hook-form's register() — i.e. any NON-foreign-key field. Foreign keys
// go through <Controller>, so a form whose every field is an FK must not
// destructure `register` at all: an unused binding is an eslint error on a
// file forge just wrote.
func (p PageTemplateData) HasCreateRegistered() bool { return anyNonFK(p.CreateFields) }

// HasUpdateRegistered is HasCreateRegistered for the update form.
func (p PageTemplateData) HasUpdateRegistered() bool { return anyNonFK(p.UpdateFields) }

func anyNonFK(fields []PageField) bool {
	for _, f := range fields {
		if f.FK == nil {
			return true
		}
	}
	return false
}

type hookImportSet struct {
	byModule map[string]map[string]bool
}

func newHookImportSet() *hookImportSet {
	return &hookImportSet{byModule: map[string]map[string]bool{}}
}

func (h *hookImportSet) add(module, symbol string) {
	if module == "" || symbol == "" {
		return
	}
	if h.byModule[module] == nil {
		h.byModule[module] = map[string]bool{}
	}
	h.byModule[module][symbol] = true
}

func (h *hookImportSet) sorted() []PageHookImport {
	modules := make([]string, 0, len(h.byModule))
	for m := range h.byModule {
		modules = append(modules, m)
	}
	sort.Strings(modules)
	out := make([]PageHookImport, 0, len(modules))
	for _, m := range modules {
		symbols := make([]string, 0, len(h.byModule[m]))
		for s := range h.byModule[m] {
			symbols = append(symbols, s)
		}
		sort.Strings(symbols)
		out = append(out, PageHookImport{Module: m, Symbols: symbols})
	}
	return out
}
