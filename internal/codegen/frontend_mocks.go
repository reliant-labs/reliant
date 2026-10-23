package codegen

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// MockEntityTemplateData holds data for rendering a single entity's TypeScript
// mock data file (e.g., frontends/<fe>/src/mocks/patients.ts).
type MockEntityTemplateData struct {
	EntityName       string       // "Patient" (PascalCase)
	EntityNamePlural string       // "Patients"
	EntitySlug       string       // "patients" (kebab-case for filename)
	SchemaImport     string       // "PatientSchema"
	TypeImport       string       // "Patient"
	ImportPath       string       // "services/clinic/v1/clinic_pb"
	Records          []MockRecord // 10 mock records
}

// MockRecord is a single mock object with field values.
type MockRecord struct {
	Fields []MockFieldValue
}

// MockFieldValue is a field name + its literal TypeScript value.
type MockFieldValue struct {
	Name  string // camelCase field name
	Value string // TypeScript literal: `"abc"`, `42`, `true`
	Last  bool   // true if this is the last field (for comma handling in templates)
}

// MockTransportTemplateData holds data for rendering the mock-transport.ts file.
type MockTransportTemplateData struct {
	Entities []MockTransportEntity
	// SchemaImportGroups carries the per-ImportPath aggregation of
	// response-schema imports the mock-transport template needs. The
	// template iterates these to emit ONE merged `import { ... } from
	// "@/gen/<path>"` statement per source module, instead of one per
	// entity. Pre-aggregating in Go (vs. with a groupBy template helper)
	// keeps the template loop trivially auditable and lets us preserve
	// entity ordering inside each group.
	SchemaImportGroups []MockTransportSchemaImportGroup
}

// FixtureEntities is one entity per fixture module (`@/mocks/<slug>_gen`),
// in first-seen order — what mock-transport.ts imports `* as <plural>Mocks`.
// An entity served by several services has a dispatch row per service but
// one fixture module, and importing it once per row declared the namespace
// twice (TS2300 Duplicate identifier 'thingsMocks').
func (d MockTransportTemplateData) FixtureEntities() []MockTransportEntity {
	seen := map[string]bool{}
	var out []MockTransportEntity
	for _, e := range d.Entities {
		if !seen[e.EntitySlug] {
			seen[e.EntitySlug] = true
			out = append(out, e)
		}
	}
	return out
}

// HasWritableEntities reports whether any entity has a Create or Update
// RPC — gates the MessageInitShape type import in the transport template
// (used only by the mutable-store write paths; an unconditional import
// would trip no-unused-vars on read-only projects).
func (d MockTransportTemplateData) HasWritableEntities() bool {
	for _, e := range d.Entities {
		if e.HasCreate || e.HasUpdate {
			return true
		}
	}
	return false
}

// UpdateResponse is the response message the Update dispatch row names: its
// own when the extraction resolved one, otherwise Create's (the historical
// shape). Never empty for an entity with an Update row — an empty name would
// render as the undefined import `Schema`.
func (e MockTransportEntity) UpdateResponse() string {
	if e.UpdateResponseType != "" {
		return e.UpdateResponseType
	}
	return e.CreateResponseType
}

// UpdateEntityField is the wrapping field on UpdateResponse, with the same
// fallback.
func (e MockTransportEntity) UpdateEntityField() string {
	if e.UpdateEntityFieldCamel != "" {
		return e.UpdateEntityFieldCamel
	}
	return e.CreateEntityFieldCamel
}

// MockTransportSchemaImportGroup bundles every response-schema symbol the
// mock-transport.ts file imports from a single proto-generated module.
// Two entities whose schemas live in the same `@/gen/services/api/v1/api_pb`
// module merge into one import statement; entities pointing at distinct
// modules each get their own group.
type MockTransportSchemaImportGroup struct {
	ImportPath string   // proto module path, e.g. "services/api/v1/api_pb"
	Symbols    []string // schema symbols imported from this module, dedup'd + sorted
}

// NewMockTransportTemplateData is the mock-transport.ts data for entities:
// one merged import statement per proto module, and the name the file
// refers to each imported schema by. Each entity contributes the
// per-RPC schema set its dispatch row names
// (`{ListResponseType,GetResponseType,CreateResponseType}Schema` gated on
// `HasList`/`HasGet`/`HasCreate||HasUpdate`, plus the entity schema for the
// mutable store). Duplicate symbols within a module are collapsed, and both
// levels are sorted for deterministic output (import/order's alphabetize).
//
// The entities belong to different services, whose protos are free to name
// a response message alike (`GetResponse`); such a schema is imported
// `as <module>_<Name>` from each module (tsImportPlan) and the entity's row
// names that — read through the entity's *Schema methods.
func NewMockTransportTemplateData(entities []MockTransportEntity) MockTransportTemplateData {
	buckets := map[string]map[string]struct{}{}
	add := func(path, sym string) {
		if path == "" || sym == "" {
			return
		}
		if buckets[path] == nil {
			buckets[path] = map[string]struct{}{}
		}
		buckets[path][sym] = struct{}{}
	}
	for _, e := range entities {
		if e.HasList {
			add(e.ImportPath, e.ListResponseType+"Schema")
		}
		if e.HasGet {
			add(e.ImportPath, e.GetResponseType+"Schema")
		}
		if e.HasUpdate && e.UpdateResponseType != "" && e.UpdateResponseType != e.CreateResponseType {
			add(e.ImportPath, e.UpdateResponseType+"Schema")
		}
		if e.HasCreate {
			add(e.ImportPath, e.CreateResponseType+"Schema")
		}
		if e.HasCreate || e.HasUpdate {
			// The mutable store builds new entity records on Create/Update,
			// so it needs the entity's own schema — from the file that
			// declares the entity, which may differ from the service file.
			add(e.entityImportPath(), e.SchemaImport)
		}
	}
	plan := newTSImportPlan(buckets)
	data := MockTransportTemplateData{Entities: make([]MockTransportEntity, len(entities))}
	for i, e := range entities {
		e.imports = plan
		data.Entities[i] = e
	}
	for _, g := range plan.Groups(buckets) {
		data.SchemaImportGroups = append(data.SchemaImportGroups, MockTransportSchemaImportGroup(g))
	}
	return data
}

// The five *Schema methods below are the names mock-transport.ts refers to
// the entity's imported schemas by: their own, unless another module the
// file imports declares the same name (NewMockTransportTemplateData).

// ListResponseSchema is the local name of the List response's schema.
func (e MockTransportEntity) ListResponseSchema() string {
	return e.imports.Local(e.ImportPath, e.ListResponseType+"Schema")
}

// GetResponseSchema is the local name of the Get response's schema.
func (e MockTransportEntity) GetResponseSchema() string {
	return e.imports.Local(e.ImportPath, e.GetResponseType+"Schema")
}

// CreateResponseSchema is the local name of the Create response's schema.
func (e MockTransportEntity) CreateResponseSchema() string {
	return e.imports.Local(e.ImportPath, e.CreateResponseType+"Schema")
}

// UpdateResponseSchema is the local name of the Update response's schema.
func (e MockTransportEntity) UpdateResponseSchema() string {
	return e.imports.Local(e.ImportPath, e.UpdateResponse()+"Schema")
}

// EntitySchema is the local name of the entity message's own schema.
func (e MockTransportEntity) EntitySchema() string {
	return e.imports.Local(e.entityImportPath(), e.SchemaImport)
}

// entityImportPath is the module declaring the entity message: its own
// proto file when it has one, else the service's.
func (e MockTransportEntity) entityImportPath() string {
	if e.EntityImportPath != "" {
		return e.EntityImportPath
	}
	return e.ImportPath
}

// MockTransportEntity represents one entity in the mock transport routing.
type MockTransportEntity struct {
	EntityName       string // "Patient"
	EntityNamePlural string // "Patients"
	EntitySlug       string // "patients" (for mock data import path)
	ServiceName      string // "ClinicService" (short, used in display only)
	// ServiceTypeName is the FULLY-QUALIFIED proto service name, e.g.
	// "demo.v1.ClinicService". Connect v2's runtime
	// `method.parent.typeName` returns this form, so the mock transport
	// must build case keys from it (`${ServiceTypeName}/${RPC}`) or the
	// fall-through dispatch silently never matches.
	ServiceTypeName string // "demo.v1.ClinicService"
	ListRPC         string // "ListPatients"
	GetRPC          string // "GetPatient"
	CreateRPC       string // "CreatePatient"
	UpdateRPC       string // "UpdatePatient"
	DeleteRPC       string // "DeletePatient"
	HasList         bool
	HasGet          bool
	HasCreate       bool
	HasUpdate       bool
	HasDelete       bool
	// ItemsField is the camelCase (protojson) name of the list response's
	// repeated field — the key the mock List handler must set on the
	// ListXxxResponse it builds. It mirrors PageTemplateData.ItemsField:
	// the ACTUAL repeated proto field name (e.g. `keys`), not the
	// camelCased entity plural. The mock store variable keeps the plural
	// camelCase identifier; only the response-message KEY uses this.
	ItemsField string
	// PkFieldCamel is the camelCase name of the entity message's
	// PRIMARY-KEY field ("id", "usageEventId", ...). The mutable session
	// store keys records by this field; hardcoding "id" breaks for entities
	// whose PK column isn't literally `id` (e.g. a `usage_event_id` PK
	// projects to a message with no `id` field, failing `tsc`).
	PkFieldCamel string
	// GetEntityFieldCamel / CreateEntityFieldCamel are the camelCase names
	// of the field that wraps the entity on the Get / Create+Update RESPONSE
	// messages. The proto is free to name this field anything
	// (`GetLLMKeyResponse { LLMKey key = 1; }` → `key`, not `lLMKey`), so the
	// mock dispatch must read it off the response descriptor instead of
	// assuming `camelCase(EntityName)` — a wrong key fails `tsc` with "object
	// literal may only specify known properties".
	GetEntityFieldCamel    string
	CreateEntityFieldCamel string
	// RequestEntityFieldCamel is the request field an AIP-134 Create/Update
	// nests the entity under ({ base64item: {...}, updateMask }), as
	// protoc-gen-es names it: the update request's wrapper off the
	// descriptor, else the field the entity scaffolder writes.
	RequestEntityFieldCamel string
	ImportPath              string // service proto import path for response-schema imports
	// EntityImportPath is the module declaring the ENTITY message schema
	// ("db/v1/patients_pb"). May differ from ImportPath when the entity
	// lives in its own proto file; the mutable-store Create/Update paths
	// need the entity schema to build new records.
	EntityImportPath string
	TypeImport       string // "Patient"
	SchemaImport     string // "PatientSchema"
	// Response/request type names
	ListResponseType   string
	GetResponseType    string
	CreateRequestType  string
	CreateResponseType string
	UpdateRequestType  string
	GetRequestType     string
	DeleteRequestType  string
	// UpdateResponseType / UpdateEntityFieldCamel are what the Update dispatch
	// row returns. It reuses Create's response shape when the service has a
	// Create RPC, and otherwise the Update RPC's own response — never an empty
	// name, which would render as the undefined import `Schema`.
	UpdateResponseType     string
	UpdateEntityFieldCamel string

	// imports is the file's import plan, set by NewMockTransportTemplateData;
	// nil means every schema is imported under its own name.
	imports tsImportPlan
}

// ScenarioRPCEntry is one unary RPC row in the generated typed scenario
// handler map (src/mocks/scenario-rpcs_gen.ts).
type ScenarioRPCEntry struct {
	Key            string // "demo.v1.TaskService/GetTask" — matches method.parent.typeName dispatch
	RequestType    string // "GetTaskRequest"
	ResponseSchema string // "GetTaskResponseSchema"
}

// ScenarioRPCData drives scenario-rpcs.ts.tmpl: a typed handler map keyed
// by `${serviceTypeName}/${methodName}` whose values take the TYPED request
// and must return a MessageInitShape of the response schema. This is what
// kills the snake_case-payload silent failure: a scenario returning
// `{ user_name: "x" }` for a `userName` field fails tsc instead of
// rendering empty cells.
type ScenarioRPCData struct {
	Entries     []ScenarioRPCEntry
	TypeImports []HookImportGroup // type-only imports, grouped per declaring module
}

// BuildScenarioRPCData collects every unary RPC across all services.
// Streaming RPCs are reachable through the map's string index signature —
// there is no canonical typed return shape for an arbitrary stream.
func BuildScenarioRPCData(services []ServiceDef) ScenarioRPCData {
	var data ScenarioRPCData
	buckets := map[string]map[string]struct{}{}
	add := func(path, sym string) {
		set, ok := buckets[path]
		if !ok {
			set = map[string]struct{}{}
			buckets[path] = set
		}
		set[sym] = struct{}{}
	}

	type ref struct{ inPath, outPath string }
	var refs []ref
	for _, svc := range services {
		for _, m := range svc.Methods {
			if m.ClientStreaming || m.ServerStreaming {
				continue
			}
			inPath := m.InputProtoFile
			if inPath == "" {
				inPath = svc.ProtoFile
			}
			outPath := m.OutputProtoFile
			if outPath == "" {
				outPath = svc.ProtoFile
			}
			add(ProtoFileToTSImportPath(inPath), m.InputType)
			add(ProtoFileToTSImportPath(outPath), m.OutputType+"Schema")
			refs = append(refs, ref{ProtoFileToTSImportPath(inPath), ProtoFileToTSImportPath(outPath)})
			data.Entries = append(data.Entries, ScenarioRPCEntry{
				Key:            svc.Package + "." + svc.Name + "/" + m.Name,
				RequestType:    m.InputType,
				ResponseSchema: m.OutputType + "Schema",
			})
		}
	}

	// Every service's messages land in this one file, and two services
	// declaring the same message name (`PingRequest`) is ordinary proto: such
	// a name is imported `as <module>_<Name>` from each (tsImportPlan).
	plan := newTSImportPlan(buckets)
	for i, r := range refs {
		data.Entries[i].RequestType = plan.Local(r.inPath, data.Entries[i].RequestType)
		data.Entries[i].ResponseSchema = plan.Local(r.outPath, data.Entries[i].ResponseSchema)
	}
	data.TypeImports = plan.Groups(buckets)
	return data
}

// ──────────────────────────────────────────────────────────────────────
// Where the frontend's demo data comes from
// ──────────────────────────────────────────────────────────────────────
//
// From the same place the database's does.
//
// This generator used to invent its own: a column called `name` drew from a
// company pool, `status` from {active, pending, inactive, ...}, `title` from a
// documentation-chapter list, a float called `win_probability` from [0,1). The
// seeder ran the same trick until commit 5f8993ab deleted it — what a column
// MEANS is a decision the schema does not carry, so forge cannot derive it —
// leaving one application with TWO demo vocabularies: the database said
// `sample_name_1` where the frontend said "Acme Corp".
//
// Worse than inconsistent, the invented values were not even legal. The mocks
// read no constraints at all, so a `sku` column whose CHECK is
// `^[A-Z]{3}-[0-9]{4}$` mocked as `"sample_sku_3"` — data the very API the
// mock stands in for would reject.
//
// So the mock generator makes no decisions about domain vocabulary. It reads
// the values the project's own seed plan will write, per (table, column, row),
// and renders them as TypeScript. Everything the seeder derives from a
// DECLARATION — db/seeds/vocab.yaml, a CHECK vocabulary, a regex CHECK, length
// and range bounds, the foreign keys, the primary keys — arrives here already
// resolved, and the two can no longer disagree because there is only one
// answer. What the seeder refuses to invent, this refuses to invent too: an
// undeclared column carries seedplan.SyntheticStringPrefix + its own name +
// the row number, in both places.

// SeedProjection is the project's dev dataset as the frontend mock generator
// reads it: the same seedplan.Plan `forge db seed apply` renders, queried
// per cell instead of rendered as SQL.
//
// A nil *SeedProjection is valid everywhere and means "no dataset to agree
// with" — no migrations, no reachable shadow server, or a schema the planner
// refuses (a NOT NULL foreign-key cycle). Every value then falls back to a
// type-correct, self-evidently synthetic literal, which is exactly what the
// seeder would have written for a column nothing describes.
type SeedProjection struct {
	cfg  seedplan.Config
	plan *seedplan.Plan
}

// BuildSeedProjection resolves the project's seed plan from its migrations.
// cfg is the project's own seed configuration — the salt and row counts the
// dev dataset is built with — passed in rather than re-derived here so the
// mocks and `forge db seed apply` cannot be looking at different plans.
//
// cfg.Now is IGNORED: the projection always anchors relative time at
// seedplan's fixed reference instant. Everything rendered from it is
// committed generated code, which must be a pure function of its inputs; a
// seeded timestamp that followed the caller's clock rewrote every fixture at
// midnight UTC. See newSeedProjection.
func BuildSeedProjection(projectDir string, cfg seedplan.Config) (*SeedProjection, error) {
	migDir := filepath.Join(projectDir, "db", "migrations")
	tables, err := shadowdb.Tables(projectDir, migDir)
	if err != nil {
		var fetchErr *pgtest.FetchError
		if errors.As(err, &fetchErr) {
			return nil, err
		}
		return nil, nil
	}
	if len(tables) == 0 {
		return nil, nil
	}
	// Vocab problems are reported by the seed CLI, which is where a project
	// asks for its dataset; a bad overlay here just means built-ins.
	vocab, _ := seedplan.LoadVocab(seedplan.VocabPath(migDir))
	return newSeedProjection(tables, cfg, vocab), nil
}

// newSeedProjection builds the projection from an already-introspected
// schema. Split out so the agreement between a mock literal and the seeded
// cell can be asserted without a database.
func newSeedProjection(tables []schemadef.Table, cfg seedplan.Config, vocab *seedplan.Vocab) *SeedProjection {
	// Clock-free by construction, whatever the caller passed: the zero Now
	// selects seedplan's fixed reference instant (seedplan.Config.Now). The
	// CLI once handed this the runtime seed config, anchored at today, and
	// every committed src/mocks/*_gen.ts went stale at 00:00Z. Clearing it
	// here — not trusting each caller to — is what keeps the fixtures a
	// pure function of the schema, the vocab and the salt/row counts.
	cfg.Now = time.Time{}
	plan, err := seedplan.BuildPlan(tables, seedplan.PoolsFromTables(tables), cfg)
	if err != nil {
		return nil
	}
	plan.SetBounds(seedplan.BoundsFromTables(tables))
	plan.ApplyVocab(vocab)
	return &SeedProjection{cfg: cfg, plan: plan}
}

// Value returns the value the seeded database holds at (table, column, row),
// raw and unquoted. ok is false for a cell with no plain scalar spelling —
// NULL, an array, a boolean — and for every cell when there is no plan.
func (p *SeedProjection) Value(table, column string, row int) (string, bool) {
	if p == nil || p.plan == nil {
		return "", false
	}
	return p.plan.SeedValue(table, column, row)
}

// SeedFingerprint identifies the schema + seed config the project's fixtures
// are derived FROM, for the generated guard that has to notice when those
// inputs move without a regenerate.
//
// It is deliberately NOT a method on SeedProjection, and that is the whole
// point of its existence. A projection requires a reachable postgres to
// build; the fingerprint requires only files on disk. Hanging it off the
// projection would delete the guard in exactly the case that needs it most —
// no shadow server, so BuildSeedProjection returns nil, every fixture
// degrades to a synthetic placeholder, and the project would then also carry
// no record of which schema those placeholders stood in for. Reading the
// inputs directly means the fixtures and the claim about them fail
// independently.
// It returns BOTH digests, and the caller must keep them straight:
//
//   - files hashes the migrations and vocab.yaml only. This is the one a
//     process with no database can recompute — the emitted TypeScript guard
//     reads exactly these files.
//   - withConfig additionally folds in the seed salt and row counts from
//     forge.yaml, which change every fixture value without touching a
//     migration, and which nothing outside Go can see.
//
// They are returned as a pair rather than selected by a flag because the
// mistake this API exists to prevent is using one where the other belongs.
func SeedFingerprint(projectDir string, cfg seedplan.Config) (files, withConfig seedplan.Fingerprint) {
	// An unreadable input degrades to the empty fingerprint, which every
	// consumer treats as "cannot verify" rather than "verified fresh".
	migDir := filepath.Join(projectDir, "db", "migrations")
	files, _ = seedplan.FingerprintInputs(migDir)
	withConfig, _ = seedplan.FingerprintWithConfig(migDir, cfg)
	return files, withConfig
}

// Rows is how many rows the dataset holds for a table. 0 means "no plan", and
// the caller keeps its own default.
func (p *SeedProjection) Rows(table string) int {
	if p == nil || p.plan == nil {
		return 0
	}
	return p.cfg.EffectiveRows(table)
}

// mockSeedNamespace is a fixed UUID namespace. Arbitrary UUIDv4 chosen once:
// changing it changes every generated id.
var mockSeedNamespace = [16]byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// mockDeterministicUUID generates a UUID v5-style deterministic UUID from a
// name. It is reached only when there is no seed plan — with one, a primary
// key's mock value is the primary key the dataset actually holds.
func mockDeterministicUUID(name string) string {
	h := sha1.New()
	h.Write(mockSeedNamespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// mockRecordCount is how many rows of fixtures an entity gets when the
// dataset has at least that many — enough to fill a list page. A dataset
// configured with fewer rows than this caps it: the fixtures are the first N
// rows of the dev database, and padding past its end with invented values
// would put the two back into disagreement on exactly the rows nobody looked
// at.
const mockRecordCount = 10

// EntityDefToMockData converts an EntityDef (parsed from proto) and its
// associated ServiceDef into MockEntityTemplateData for template rendering.
// seed is the project's dev dataset (nil when there is none) — see
// SeedProjection: every value below is the one the database holds for the
// same row.
func EntityDefToMockData(entity EntityDef, svc ServiceDef, seed *SeedProjection) MockEntityTemplateData {
	plural := inflection.Plural(entity.Name)
	// The entity's Schema (PatientSchema, ProductSchema, etc.) lives in the
	// entity's proto file, which may be separate from the service proto
	// (typically `db/v1/*.proto` vs `services/<svc>/v1/*.proto`). Using
	// the service's file silently emits imports for symbols that don't
	// exist in that file — the bundle fails to load at runtime.
	importSource := entity.ProtoFile
	if importSource == "" {
		importSource = svc.ProtoFile
	}
	importPath := ProtoFileToTSImportPath(importSource)

	rowCount := mockRecordCount
	if n := seed.Rows(entity.TableName); n > 0 && n < rowCount {
		rowCount = n
	}
	records := make([]MockRecord, rowCount)
	for i := 0; i < rowCount; i++ {
		var fieldValues []MockFieldValue
		for j, f := range entity.Fields {
			ef := f
			ef.ProtoType = effectiveMockProtoType(f)
			val := mockGenerateValue(seed, entity.TableName, ef, i, svc)
			fieldValues = append(fieldValues, MockFieldValue{
				Name:  fieldNameToCamel(f.Name),
				Value: val,
				Last:  j == len(entity.Fields)-1,
			})
		}
		records[i] = MockRecord{Fields: fieldValues}
	}

	return MockEntityTemplateData{
		EntityName:       entity.Name,
		EntityNamePlural: plural,
		EntitySlug:       PascalToKebab(plural),
		SchemaImport:     entity.Name + "Schema",
		TypeImport:       entity.Name,
		ImportPath:       importPath,
		Records:          records,
	}
}

// effectiveMockProtoType returns the proto type string the mock generators
// (protoTypeToTSType, mockGenerateValue) should see for a field. Those
// helpers detect repeated scalars via a `repeated `/`[]` prefix on the proto
// type, but EntityField.ProtoType carries only the element kind ("string")
// for repeated scalars — the repeated-ness lives in Kind
// (FieldKindRepeatedScalar) / GoType ("[]string"). Without this, a
// `repeated string models` field mocks a scalar `"sample_models_1"` and
// fails `tsc` against the protobuf-es `string[]` field. Re-encoding the
// prefix here keeps the fix local to mock codegen and leaves the
// ORM/migration consumers of ProtoType untouched.
func effectiveMockProtoType(f EntityField) string {
	if alreadyRepeated(f.ProtoType) {
		return f.ProtoType
	}
	// Both repeated scalars AND repeated messages carry only the element kind
	// in ProtoType (a repeated `string` field is ProtoType "string"; a repeated
	// message is ProtoType "message") — the repeated-ness lives in
	// Kind/GoType. Without re-encoding the prefix, a repeated-message field
	// would mock `{}` (an object) against the protobuf-es `Foo[]` array type and
	// fail `tsc`, exactly as a repeated scalar mocked a bare scalar.
	if isRepeatedEntityField(f) {
		return "repeated " + f.ProtoType
	}
	return f.ProtoType
}

// isRepeatedEntityField reports whether an entity wire field is `repeated`.
//
// There is no single flag to read: schemaFieldToEntityField encodes the
// descriptor's repeated bit differently per kind — in Kind for scalars and
// messages, and in the GoType "[]" prefix for enums, whose Kind stays
// FieldKindEnum because the conversion generator branches on it.
//
// `bytes` is the trap in the prefix test and the reason this is not one
// strings.HasPrefix: its SINGULAR Go type is already []byte, so for that
// one kind the prefix means nothing.
func isRepeatedEntityField(f EntityField) bool {
	switch f.Kind {
	case FieldKindRepeatedScalar, FieldKindRepeatedMessage:
		return true
	case FieldKindEnum:
		return strings.HasPrefix(f.GoType, "[]")
	}
	return false
}

// alreadyRepeated reports whether a proto-type string already carries a
// repeated/array marker (so re-encoding the prefix would double it).
func alreadyRepeated(protoType string) bool {
	return strings.HasPrefix(protoType, "repeated ") || strings.HasPrefix(protoType, "[]")
}

// mockPkFieldCamel returns the camelCase name of the entity's primary-key
// field for use as the mutable-store map key, defaulting to "id" when the
// entity carries no explicit PK.
//
// The store keys MOCK records, which are projections of the entity's WIRE
// message (EntityDef.Fields) — so the chosen field must EXIST on that wire
// message, or `e.<field>` fails `tsc`. The DB PK column ("id") is the natural
// first choice, but it is not always a wire field: a table can carry a
// surrogate `id` PK while the published proto exposes a domain key
// (`usage_event_id`) and omits `id` entirely. Resolution order:
//  1. the DB PK field, if it appears among the wire fields;
//  2. a `<entity_singular>_id` wire field (the conventional domain key);
//  3. the first wire field;
//  4. "id" as a last resort (matches the page generator's fallback).
func mockPkFieldCamel(e EntityDef) string {
	if key, ok := EntityWireKey(e); ok {
		return fieldNameToCamel(key)
	}
	if len(e.Fields) > 0 {
		return fieldNameToCamel(e.Fields[0].Name)
	}
	return "id"
}

// responseEntityField returns the camelCase name of the field on respType
// (a Get/Create/Update response message) that wraps the entity message. The
// proto names this field freely (`GetLLMKeyResponse { LLMKey key = 1; }` →
// `key`), so the mock dispatch reads it off the response descriptor by
// matching the field's message type against the entity, falling back to the
// camelCased entity name for older descriptors / unresolvable cases.
func responseEntityField(svc ServiceDef, respType, entityName string) string {
	if respType != "" && svc.Messages != nil {
		if fields, ok := svc.Messages[respType]; ok {
			for _, f := range fields {
				if fieldMatchesEntity(f, entityName) {
					return fieldNameToCamel(f.Name)
				}
			}
		}
	}
	return entityFieldCamel(entityName)
}

// ExtractMockTransportEntities builds MockTransportEntity data from services
// and entity definitions. It pairs CRUD page data with entity info.
func ExtractMockTransportEntities(services []ServiceDef, entities []EntityDef) []MockTransportEntity {
	// Build a lookup from entity name to EntityDef
	entityMap := make(map[string]EntityDef, len(entities))
	for _, e := range entities {
		entityMap[e.Name] = e
	}

	var result []MockTransportEntity
	for _, svc := range services {
		pages := ExtractCRUDEntities(svc)
		importPath := ProtoFileToTSImportPath(svc.ProtoFile)

		for _, page := range pages {
			// Only include entities that have corresponding entity definitions
			// (i.e., actual database entities with mock data). Non-CRUD RPCs
			// like GetStatus match CRUD patterns but don't have entities.
			entityDef, ok := entityMap[page.EntityName]
			if !ok {
				continue
			}
			updateResp, updateField := page.CreateResponseType, responseEntityField(svc, page.CreateResponseType, page.EntityName)
			if updateResp == "" {
				updateResp, updateField = page.UpdateResponseType, responseEntityField(svc, page.UpdateResponseType, page.EntityName)
			}
			hasUpdate := page.HasUpdate && updateResp != ""
			entityImportPath := importPath
			if entityDef.ProtoFile != "" {
				entityImportPath = ProtoFileToTSImportPath(entityDef.ProtoFile)
			}
			requestField := page.UpdateEntityFieldCamel
			if requestField == "" {
				requestField = entityFieldCamel(page.EntityName)
			}
			result = append(result, MockTransportEntity{
				EntityName:              page.EntityName,
				EntityNamePlural:        page.EntityNamePlural,
				EntitySlug:              page.EntitySlug,
				ServiceName:             svc.Name,
				ServiceTypeName:         svc.Package + "." + svc.Name,
				ListRPC:                 page.ListRPC,
				GetRPC:                  page.GetRPC,
				CreateRPC:               page.CreateRPC,
				UpdateRPC:               page.UpdateRPC,
				DeleteRPC:               page.DeleteRPC,
				HasList:                 page.HasList,
				HasGet:                  page.HasGet,
				HasCreate:               page.HasCreate,
				HasUpdate:               hasUpdate,
				HasDelete:               page.HasDelete,
				ItemsField:              page.ItemsField,
				PkFieldCamel:            mockPkFieldCamel(entityDef),
				GetEntityFieldCamel:     responseEntityField(svc, page.GetResponseType, page.EntityName),
				CreateEntityFieldCamel:  responseEntityField(svc, page.CreateResponseType, page.EntityName),
				RequestEntityFieldCamel: requestField,
				ImportPath:              importPath,
				EntityImportPath:        entityImportPath,
				TypeImport:              page.EntityName,
				SchemaImport:            page.EntityName + "Schema",
				ListResponseType:        page.ListResponseType,
				GetResponseType:         page.GetResponseType,
				CreateRequestType:       page.CreateRequestType,
				CreateResponseType:      page.CreateResponseType,
				UpdateRequestType:       page.UpdateRequestType,
				UpdateResponseType:      updateResp,
				UpdateEntityFieldCamel:  updateField,
				GetRequestType:          page.GetRequestType,
				DeleteRequestType:       page.DeleteRequestType,
			})
		}
	}
	// Deterministic, alphabetical entity order: the transport template
	// emits one `import * as <x>Mocks from "@/mocks/<slug>"` per entity,
	// and import/order's alphabetize check requires ascending paths.
	sort.Slice(result, func(i, j int) bool { return result[i].EntitySlug < result[j].EntitySlug })
	return result
}

// protoTypeToTSType maps proto field types to TypeScript types. Scalars
// come from the one projection (protoScalarTS); the remaining arms cover
// the structured kinds a descriptor can carry.
func protoTypeToTSType(protoType string) string {
	// Repeated scalars ("repeated string" from entity descriptors,
	// "[]string" from message descriptors) project to element[] arrays.
	if base, ok := strings.CutPrefix(protoType, "repeated "); ok {
		return protoTypeToTSType(base) + "[]"
	}
	if base, ok := strings.CutPrefix(protoType, "[]"); ok {
		return protoTypeToTSType(base) + "[]"
	}
	if ts, ok := protoScalarTSType(protoType); ok {
		return ts
	}
	switch protoType {
	case "google.protobuf.Timestamp":
		return "string"
	case "enum":
		return "number"
	case "message":
		return "object"
	default:
		return "string"
	}
}

// mockGenerateValue produces a TypeScript literal value for the given entity
// field and row index.
//
// The value is the one the project's dev dataset holds at that cell whenever
// the dataset can answer — vocabulary, CHECK-constrained values, keys and
// their references all arrive already resolved, so the mocks and the database
// cannot disagree. Everything below the projection is the no-dataset
// fallback: type-correct, deterministic, and self-evidently invented.
func mockGenerateValue(seed *SeedProjection, tableName string, f EntityField, i int, svc ServiceDef) string {
	col := f.Name
	protoType := f.ProtoType

	// Repeated scalar fields — emit a small deterministic array of
	// element-typed mocks so the fixture type-checks against the
	// protobuf-es `element[]` field.
	if base, ok := strings.CutPrefix(protoType, "repeated "); ok {
		elem := f
		elem.ProtoType = base
		a := mockGenerateValue(seed, tableName, elem, i, svc)
		b := mockGenerateValue(seed, tableName, elem, i+1, svc)
		return fmt.Sprintf("[%s, %s]", a, b)
	}

	ts, isScalar := protoScalarTSType(protoType)

	// What the database will hold at this cell, when there is a database to
	// agree with.
	if raw, ok := seed.Value(tableName, col, i); ok {
		if lit, ok := mockSeededLiteral(raw, f, ts, isScalar); ok {
			return lit
		}
		// An enum column's cell is the VALUE NAME the seeder wrote
		// ("ORDER_STATUS_ACTIVE"); protobuf-es represents an enum field as
		// its wire NUMBER, so the name has to be resolved through the
		// enum's declaration order before it can be a TypeScript literal.
		if lit, ok := mockSeededEnumLiteral(raw, f, protoType, svc); ok {
			return lit
		}
	}

	// Primary key, with no dataset to take it from. UUIDs are the project
	// default, but an identifier is a value like any other and its literal
	// has to be typed like any other: a project using distributed counters
	// types its id as int64, which protobuf-es emits as bigint, and a UUID
	// string will not type-check against it.
	if isScalar && col == "id" {
		if ts == "bigint" {
			return fmt.Sprintf("BigInt(%q)", fmt.Sprintf("%d", i+1))
		}
		if ts == "string" {
			return fmt.Sprintf("%q", mockDeterministicUUID(fmt.Sprintf("%s.%d", tableName, i)))
		}
	}

	// Timestamp fields — decided by the field's DECLARED type, never by its
	// name. A `string issued_at` or an epoch `int64 expires_at` is not a
	// Timestamp, and a `google.protobuf.Timestamp valid_from` is one; a
	// name test gets both backwards and emits TypeScript the frontend
	// typecheck lane rejects on a file the author is not allowed to edit.
	if isTimestampProtoField(f) {
		return mockGenerateTimestamp(col, i)
	}

	// Scalars: one literal per TypeScript type protobuf-es declares, so a
	// kind forge has never seen before cannot silently mock as a string.
	if isScalar {
		switch ts {
		case "boolean":
			if i%2 == 0 {
				return "true"
			}
			return "false"
		case "bigint":
			// 64-bit integers are bigint under protobuf-es's default
			// jstype — a plain number literal fails `tsc --noEmit`.
			return mockGenerateIntegerValue(i) + "n"
		case "number":
			if protoType == "float" || protoType == "double" {
				return mockGenerateFloatValue(i)
			}
			return mockGenerateIntegerValue(i)
		case "Uint8Array":
			return mockGenerateBytesValue(i)
		case "string":
			return fmt.Sprintf("%q", mockGenerateStringValue(col, i))
		}
	}

	// Enum fields — the first DECLARED non-zero value. Zero is the enum's
	// UNSPECIFIED ("unset"), which a fixture should not model; and the
	// literal must be a number the enum declares, or the fixture fails the
	// frontend typecheck against the protobuf-es enum type. A hardcoded `1`
	// did exactly that for any enum that reserved its value 1.
	if protoType == "enum" {
		return mockDefaultEnumLiteral(f, svc)
	}

	// Message fields — use empty object
	if protoType == "message" {
		return "{}"
	}

	// Anything left is a kind with no scalar projection (a well-known
	// type carried through as its own name); a quoted string is the
	// inert fallback.
	return fmt.Sprintf("%q", mockGenerateStringValue(col, i))
}

// mockSeededLiteral renders one seeded cell as a TypeScript literal of the
// field's own declared type. ok is false when the raw value cannot BE that
// type — a text placeholder against a `double` field, say, which happens when
// the proto and the column disagree — and the caller then falls back rather
// than emitting TypeScript that fails `tsc` on a file the author may not edit.
func mockSeededLiteral(raw string, f EntityField, ts string, isScalar bool) (string, bool) {
	// A Timestamp field's cell is the instant the seeder wrote; protobuf-es
	// v2 wants a Timestamp object, not the ISO string.
	if isTimestampProtoField(f) {
		return fmt.Sprintf("timestampFromDate(new Date(%q))", raw), true
	}
	if !isScalar {
		return "", false
	}
	switch ts {
	case "string":
		return strconv.Quote(raw), true
	case "boolean":
		// The seeded cell is the SQL keyword the INSERT carries, which is
		// also the TypeScript spelling. Anything else against a bool field
		// (a proto/column disagreement) falls back rather than emitting a
		// literal `tsc` rejects.
		if raw != "true" && raw != "false" {
			return "", false
		}
		return raw, true
	case "number":
		if !isNumericLiteral(raw) {
			return "", false
		}
		return raw, true
	case "bigint":
		if !isNumericLiteral(raw) {
			return "", false
		}
		// 64-bit integers are bigint under protobuf-es's default jstype;
		// BigInt("…") keeps an id that outruns Number.MAX_SAFE_INTEGER exact.
		return fmt.Sprintf("BigInt(%q)", raw), true
	}
	return "", false
}

// mockSeededEnumLiteral renders one seeded cell of an ENUM column as the
// TypeScript literal protobuf-es expects: the value's wire NUMBER.
//
// The seeder writes the value NAME into the column (a text column whose
// CHECK vocabulary is the enum's members, or a native pg enum), because
// that is what the database stores. protobuf-es represents an enum field
// as a number at runtime, so a quoted name would fail `tsc` against the
// generated field type. The number is the value's DECLARED number, read
// from the enum's own declaration (svc.EnumNumbers, parallel to the names
// in svc.Enums) and never guessed from the seeded string.
//
// ok is false when the field is not an enum, the enum is unresolvable
// (cross-package, or a descriptor without the deep schema), or the
// seeded value is not one of its declared members. The caller then falls
// back rather than emitting an ordinal that means a different member.
func mockSeededEnumLiteral(raw string, f EntityField, protoType string, svc ServiceDef) (string, bool) {
	if protoType != "enum" && f.Kind != FieldKindEnum {
		return "", false
	}
	values, ok := mockEnumValueNames(f, svc)
	if !ok {
		return "", false
	}
	numbers := mockEnumValueNumbers(f, svc, values)
	for i, name := range values {
		if name == raw {
			return strconv.Itoa(int(numbers[i])), true
		}
	}
	return "", false
}

// mockDefaultEnumLiteral is the enum literal for a cell no dataset answers:
// the first DECLARED value whose number is non-zero, as its number. An enum
// the service cannot resolve (cross-package, or a descriptor without the deep
// schema) keeps the historical `1`, the first non-zero number of a gap-free
// enum — there is nothing better to read.
func mockDefaultEnumLiteral(f EntityField, svc ServiceDef) string {
	values, ok := mockEnumValueNames(f, svc)
	if !ok {
		return "1"
	}
	for _, n := range mockEnumValueNumbers(f, svc, values) {
		if n != 0 {
			return strconv.Itoa(int(n))
		}
	}
	// Only a zero value is declared: it is the only number that type-checks.
	return "0"
}

// mockEnumValueNumbers returns the declared wire number of each value in
// values (parallel to it). They come from the descriptor (ServiceDef.
// EnumNumbers), because declaration order is not the wire number once an
// enum reserves a removed value. A descriptor written before EnumNumbers
// existed carries names only; declaration order is then the only signal, and
// it is right for the zero-based, gap-free enums forge scaffolds.
func mockEnumValueNumbers(f EntityField, svc ServiceDef, values []string) []int32 {
	if numbers := svc.EnumNumbers[f.MessageType]; len(numbers) == len(values) {
		return numbers
	}
	ordinals := make([]int32, len(values))
	for i := range values {
		ordinals[i] = int32(i) //nolint:gosec // an enum's value count is far below 2^31
	}
	return ordinals
}

// mockEnumValueNames resolves the declared value names of an enum-typed
// field, in proto declaration order. ok is false when the field carries no
// resolvable enum type — the same unresolvable cases crud_convert's
// enumWireGoName refuses.
func mockEnumValueNames(f EntityField, svc ServiceDef) ([]string, bool) {
	fq := f.MessageType
	if fq == "" {
		return nil, false
	}
	values := svc.Enums[fq]
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}

// isNumericLiteral reports whether raw is a bare number — what a numeric
// column's seeded cell decodes to, and what a TypeScript number/bigint
// literal may be built from without quoting.
func isNumericLiteral(raw string) bool {
	if raw == "" {
		return false
	}
	if _, err := strconv.ParseFloat(raw, 64); err != nil {
		return false
	}
	return true
}

// mockGenerateFloatValue is the float a column carries when no dataset
// answers for it: a deterministic run, and no claim about what the column
// means. It used to read the column's NAME — `probability`/`ratio`/`_rate`
// drew from [0,1), `percent`/`_pct` from [0,100] — which is the same guess
// the seeder deleted: a bounded column states its bounds in a range CHECK,
// and the plan reads them.
func mockGenerateFloatValue(i int) string {
	return fmt.Sprintf("%.2f", float64(i+1)*10.5)
}

// mockGenerateBytesValue produces the deterministic Uint8Array literal a
// `bytes` column mocks as.
//
// The bytes are a short deterministic run and nothing more: a BYTEA
// column is opaque by declaration — the schema does not say whether it
// holds a thumbnail, a signature or a protobuf — so any "plausible"
// content would be a domain guess the generator has no basis for, and
// the string pools that serve `name`/`title` are not even the right
// TYPE here. Length varies with the row so a fixture set doesn't imply
// every blob is the same size.
func mockGenerateBytesValue(i int) string {
	n := 3 + i%4
	parts := make([]string, 0, n)
	for b := 0; b < n; b++ {
		parts = append(parts, fmt.Sprintf("%d", (i*7+b*31+1)%256))
	}
	return "new Uint8Array([" + strings.Join(parts, ", ") + "])"
}

// isTimestampProtoField reports whether a field is declared as a
// google.protobuf.Timestamp. Descriptors collapse every message field's
// ProtoType to the literal "message" and carry the fully-qualified name in
// MessageType, while some projections spell the FQ name (or the bare
// "Timestamp") in ProtoType directly — both are the same declaration.
func isTimestampProtoField(f EntityField) bool {
	const wkt = "google.protobuf.Timestamp"
	return f.MessageType == wkt || f.ProtoType == wkt || f.ProtoType == "Timestamp"
}

func mockGenerateTimestamp(col string, i int) string {
	day := (i % 28) + 1
	var hour int
	switch col {
	case "updated_at":
		hour = 12
	default:
		hour = 8
	}
	// protobuf-es v2 expects Timestamp objects, not ISO strings.
	// Use timestampFromDate() from @bufbuild/protobuf/wkt.
	return fmt.Sprintf(`timestampFromDate(new Date("2024-01-%.2dT%.2d:00:00Z"))`, day, hour)
}

// mockGenerateIntegerValue is the integer a column carries when no dataset
// answers for it. It used to read the column's NAME — `age` became 20-70,
// `price`/`amount`/`*_cents` became thousands, `quantity`/`count` multiples of
// five — the same guess about what a column MEANS that the seeder deleted.
// What a number should be is either declared (a range CHECK, which the plan
// reads) or it is not forge's to know.
func mockGenerateIntegerValue(i int) string {
	return fmt.Sprintf("%d", i+1)
}

// mockGenerateStringValue is the string a column carries when no dataset
// answers for it: the seeder's own stamp, the column's name as a LABEL, and
// the row number — the identical spelling pkg/seedplan gives a column
// nothing describes, so the two agree even here.
//
// It used to dispatch on the column's name into vocabulary pools: `name` drew
// from a company list, `status` from {active, pending, ...}, `title` from a
// documentation-chapter list. That is a decision about what a column MEANS,
// the schema does not carry it, and forge cannot derive it — the seeder
// deleted the identical dispatch in 5f8993ab. A project teaches BOTH its
// vocabulary in one place, db/seeds/vocab.yaml, and it arrives here through
// the plan.
func mockGenerateStringValue(col string, i int) string {
	return seedplan.SyntheticStringPrefix + col + "_" + strconv.Itoa(i+1)
}
