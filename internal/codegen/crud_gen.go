package codegen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// crudLibImport is the package every delegating shim imports and hands
// its lifecycle to. The scaffolded header names it from here, so the
// file's claims and its import block can never drift apart.
const crudLibImport = "github.com/reliant-labs/forge/pkg/crud"

// crudCapability is one lifecycle concern the scaffolded shim's
// delegation to pkg/crud actually covers. Label is the words the
// scaffolded header shows a handler author; Symbol is the exported
// pkg/crud identifier that implements it.
//
// The header RENDERS this slice instead of restating it in prose, so a
// capability cannot be advertised without naming code that backs it.
// crud_auth_honesty_test.go resolves every Symbol against pkg/crud's
// real exported surface.
type crudCapability struct{ Label, Symbol string }

// crudDelegatedCapabilities is everything the delegation does for you.
//
// Authentication is absent because pkg/crud does not authenticate: it
// never reads the caller's claims, and it exports no symbol that could.
// One measured run shipped 17 of 20 CRUD RPCs with no auth check at all
// because the scaffolded header claimed the opposite — it listed "auth"
// among the concerns that "live in pkg/crud", and the handler author
// believed the file it was editing. Entries here are load-bearing text
// in every project forge scaffolds; add one only with the symbol that
// makes it true.
var crudDelegatedCapabilities = []crudCapability{
	{Label: "error mapping", Symbol: "ReasonNotFound"},
	{Label: "cursor pagination", Symbol: "PageToken"},
	{Label: "page-size clamping", Symbol: "MaxPageSize"},
	{Label: "ordering", Symbol: "OrderBy"},
	{Label: "total counts", Symbol: "Count"},
	{Label: "update masks", Symbol: "UpdateMasked"},
}

// crudAuthSeamPkg / crudAuthSeamFunc name the app-owned function a
// handler calls to demand a principal. forge scaffolds it once into
// every project (pkg/middleware); nothing in pkg/crud can stand in for
// it, because deciding who may call and which rows they may see is
// application policy, not lifecycle mechanics.
//
// Every place the scaffold tells an author that authentication is theirs
// renders these constants rather than spelling the name, so renaming the
// seam moves the text with it. crud_auth_honesty_test.go fails if the
// scaffolded middleware no longer declares it.
const (
	crudAuthSeamPkg  = "middleware"
	crudAuthSeamFunc = "GetUser"
	crudAuthSeam     = crudAuthSeamPkg + "." + crudAuthSeamFunc
)

// crudAuthSeamClaimsAccessor is the other way a handler can reach the
// caller: the seam package re-exports it alongside crudAuthSeamFunc, and
// it returns the claims without the "missing principal is an error"
// framing. A handler that reads it HAS resolved the caller, so any
// analysis of who-reads-claims has to accept both spellings.
const crudAuthSeamClaimsAccessor = "ClaimsFromContext"

// CRUDAuthSeam returns the qualified app-owned function a handler calls
// to demand a principal ("middleware.GetUser"). It is the same value the
// scaffold renders into every CRUD shim header, exported so analyses that
// ask "does this handler resolve the caller?" resolve the seam from the
// generator rather than hardcoding a name the generator could rename out
// from under them.
func CRUDAuthSeam() string { return crudAuthSeam }

// CRUDAuthSeamFuncs is the set of function names that mean "this code
// resolved the caller" — the seam itself plus the claims accessor the
// seam package re-exports. Returned as a fresh map so callers cannot
// mutate the generator's view of its own seam.
func CRUDAuthSeamFuncs() map[string]bool {
	return map[string]bool{
		crudAuthSeamFunc:           true,
		crudAuthSeamClaimsAccessor: true,
	}
}

// CRUDDelegatePkgName is the package selector a delegating CRUD shim
// calls through ("crud", the last segment of crudLibImport). Derived from
// the import the shim actually stamps, so moving the delegate package
// moves this with it.
func CRUDDelegatePkgName() string {
	if idx := strings.LastIndex(crudLibImport, "/"); idx >= 0 {
		return crudLibImport[idx+1:]
	}
	return crudLibImport
}

// CRUDMethod holds the correlation between an RPC method and a database entity.
type CRUDMethod struct {
	Method    MethodTemplateData // The RPC
	Entity    EntityDef          // The matched entity
	Operation string             // "create", "get", "list", "update", "delete"
}

// DBImportLine is DBImport spelled as a line of an import block.
func (d CRUDTemplateData) DBImportLine() string {
	if alias, path, ok := strings.Cut(d.DBImport, " "); ok {
		return alias + " " + strconv.Quote(path)
	}
	return strconv.Quote(d.DBImport)
}

// CRUDTemplateData holds all data needed to render the CRUD handlers template.
type CRUDTemplateData struct {
	Package      string // Go package name, e.g. "patients"
	Module       string // Go module path, e.g. "github.com/demo-project"
	ProtoPackage string // e.g. "proto/services/patients"
	// DBImport is the generated ORM package as an import spec: its path, or
	// "db <path>" when database.orm.package names a package that is not
	// called db. Every generated CRUD file qualifies the ORM as `db.`, so a
	// relocated ORM is imported under that name rather than threading a
	// second qualifier through every template.
	DBImport      string
	HasPagination bool // true if any list method uses pagination
	HasFilters    bool // true if any list method has filter fields
	HasOrderBy    bool // true if any list method has order_by
	// NeedsORM is true when the ops file names pkg/orm — which every real
	// op now does, because the get/update/delete scoping seams are typed
	// in orm.QueryOption. Also set by a list feature or a jsonb create
	// conversion on a service whose methods are all custom-shaped.
	NeedsORM bool
	// NeedsCRUDLib is true when at least one method emits a real CRUD
	// body (i.e. uses pkg/crud and internal/db). When every
	// method's request/response shape failed validation and we emit
	// only TODO stubs, the template skips those imports to keep the
	// file compiling.
	NeedsCRUDLib bool
	// NeedsOpsFile is true when the Tier-1 ops file must be written:
	// either NeedsCRUDLib (a real op constructor) OR there is at least
	// one entity conversion pair a wired custom-read-shape body depends
	// on. An all-custom service still needs the conversion helpers, so
	// it gets an ops file carrying only the <entity>ToProto/FromProto
	// pairs (no crud/middleware imports — see the template gating).
	NeedsOpsFile bool
	CRUDMethods  []CRUDMethodTemplateData
	// Entities carries the per-entity proto<->struct conversion pairs
	// (<entity>ToProto / <entity>FromProto) emitted alongside the ops.
	Entities []EntityConvTemplateData
	// NeedsTimestamppb gates the timestamppb import (set when any
	// conversion touches a timestamp column).
	NeedsTimestamppb bool
	// NeedsFmt gates the fmt import (set when any conversion's read path
	// emits the corrupt-enum guard, which calls fmt.Errorf).
	NeedsFmt bool
	// NeedsTime gates the stdlib time import (set when any conversion
	// formats or parses a legacy-TEXT timestamp column's RFC3339Nano text).
	NeedsTime bool
}

// CRUDMethodTemplateData holds per-method template data.
//
// Every name here is a Go identifier the rendered code spells, so each is
// derived with the rule of whoever DECLARES that identifier: MethodName,
// InputType, OutputType, EntityPbType and the pb field names
// (PkField, ResponseField, UpdateEntityField, UpdateMaskField, filter
// GoNames) are protoc-gen-go/connect-go's (naming.GoCamelCase,
// naming.GoFieldNames); EntityName and PkDBField/OwnerField are forge's
// db.<Entity> struct's. The proto spellings stay on CRUDMethod.Method.
type CRUDMethodTemplateData struct {
	MethodName string // "CreatePatient" — the Go method the connect handler interface declares
	InputType  string // "CreatePatientRequest" — pb Go type
	OutputType string // "CreatePatientResponse" — pb Go type
	EntityName string // "Patient" — the db.<Entity> struct and db.Create<Entity> funcs
	// EntityPbType is the entity's generated pb message type. It equals
	// EntityName unless the proto name is not already in protoc-gen-go's
	// casing ("Oauth2token" → pb.Oauth2Token, db.Oauth2token).
	EntityPbType string
	EntityLower  string // "patient"
	Operation    string // "create", "get", "list", "update", "delete"
	// AuthRequired is the RPC's (forge.v1.method).auth_required, true
	// unless the proto explicitly opts out. It gates nothing — it selects
	// which sentence the method's comment shows the handler author, so
	// the scaffold states the RPC's declared intent next to the fact that
	// the delegation does not honour it.
	AuthRequired bool
	// AuthSeam is the app-owned function that resolves the caller, e.g.
	// "middleware.GetUser". Rendered rather than spelled in the template
	// so renaming the seam moves every scaffolded mention with it.
	AuthSeam     string
	PkField      string // "Id" — the PK's field on the pb request/entity message
	PkDBField    string // "Id" — the PK's field on the db.<Entity> struct (naming.ColumnGoName)
	PkColumnName string // "id" (raw DB column name)
	PkGoType     string // "int64"
	HasPkInInput bool   // true if the request message likely has an ID field
	// ResponseField is the pb Go field of the response that carries the
	// entity — the single entity for create/get/update ("Patient"), the
	// repeated one for list ("Patients"). Read off the response message's
	// own fields, so an entity whose name protoc-gen-go re-cases
	// ("LLMKey" → field llm_key → LlmKey) packs the field that exists.
	ResponseField   string
	HasPagination   bool   // true when List method's InputType follows AIP-158 convention
	PaginationStyle string // "cursor" (default for now)
	HasFilters      bool   // true if list method has filter fields
	FilterFields    []FilterFieldData
	HasOrderBy      bool // true if list method has order_by field
	// HasDescending is true when the list request declares a companion
	// `bool descending` field alongside order_by. The OrderBy closure only
	// dereferences req.Descending when this is set; an order_by-only request
	// (no descending field) generates a closure that returns a constant
	// false (ascending) so the generated app compiles instead of referencing
	// a nonexistent req.Descending.
	HasDescending bool
	// HasTotalCount is true when the list RESPONSE message declares a
	// total_count field (the entity scaffolder emits one). It wires a COUNT
	// over the same filters so the response reports the real total instead of
	// a constant 0.
	HasTotalCount     bool
	UpdateEntityField string // e.g., "Project" — Go field name in the update request that holds the entity
	// UpdateMaskField is the Go field name of the update request's
	// google.protobuf.FieldMask field (e.g. "UpdateMask"). Empty when the
	// request carries no mask — the generated UpdateOp then omits
	// Mask/PersistMasked and pkg/crud keeps the legacy full-replace path.
	// When set, the op wires both hooks so HandleUpdate honors AIP-134:
	// concrete mask paths write only the named columns via
	// db.Update<Entity>Masked.
	UpdateMaskField string
	// ReadOnlyColumns names the entity's columns whose WIRE field carries
	// `forge:read-only` or `forge:computed` — present only on an "update"
	// method. The generated UpdateOp carries them as op.ReadOnly (an
	// update_mask naming one is refused) and its Persist passes them to
	// crud.Preserve (a full replace leaves them as stored), so a client can
	// neither set a server-owned column nor reset it by omitting it.
	//
	// Read from the proto on every generate, not declared in the
	// migration: who may write a column THROUGH THE API is a fact about the
	// API, and the storage layer's own write policy (forge:immutable,
	// ,skipupdate) is left alone because the custom RPCs that own these
	// columns write them through the same repository. See ReadOnlyColumns.
	ReadOnlyColumns []string
	CreateFields    []CreateFieldData // fields from the create request message
	// ShapeMismatch is true when the request/response message shapes
	// observed in svc.Messages don't line up with what the CRUD body
	// templates assume (AIP-158 page_size/page_token for list, an `id`
	// scalar key for get/update/delete, an entity-typed response field,
	// etc.). That's a legitimate domain decision, not an error — the
	// template emits a WIRED but naive entity query (best-effort filters
	// onto declared columns, response left TODO) rather than CRUD-body
	// code that wouldn't compile against the real proto, and the user
	// refines the custom shape in the owned shim. See validateCRUDShape
	// for the rules. It carries a `forge:custom-read-shape` marker plus
	// MismatchReason so the user (and `forge project audit`, under
	// crud_stubs) can spot it. That is a DIFFERENT marker from
	// `forge:gen unwired-stub` on purpose: this method is wired and
	// returns rows, so the readers of the unwired-stub marker — excision,
	// orphan_stubs, out-of-tree orchestrators — must not claim it as
	// unimplemented. (Markers emitted before this release spelled it
	// FORGE_CRUD_SHAPE_MISMATCH; audit still recognizes that string for
	// one release.)
	ShapeMismatch  bool
	MismatchReason string
	// CustomFilters are the request fields that DID map to a declared
	// column on the entity (best-effort), used to seed the wired
	// custom-read-shape body's []orm.QueryOption skeleton. Unlike the
	// strict FilterFields (which fail the generate on an unmappable
	// field), these are advisory: fields that don't map to a column are
	// silently skipped so the scaffold always compiles. Only populated
	// when ShapeMismatch is true.
	CustomFilters []FilterFieldData
	// CreateAssigns are the precomputed `e.X = req.X` statements that
	// map create-request fields onto the entity struct (with timestamp/
	// wrapper/array/width conversions baked in).
	CreateAssigns []string
	// FillHandlerColumns names the entity's `forge:fill=handler` columns —
	// present only on a "create" method, and only when at least one such
	// column exists. When non-empty, ensureCRUDShimFile scaffolds an
	// op.Entity wrapper around the create delegation carrying a
	// FORGE_SCAFFOLD reminder to stamp each one, instead of the bare
	// crud.HandleCreate(...) delegation.
	FillHandlerColumns []string
	// OwnerColumn names the entity's `forge:owner` column — the column
	// that says which principal a row belongs to — or "" when the table
	// declares none.
	//
	// It changes no generated behavior: forge injects no WHERE clause,
	// because only the application knows how a caller's claims map onto
	// this column's values. What it does is aim the shim's scaffolded
	// reminder at a NAMED column, with middleware.GetUser already called
	// and the op seam already wrapped, so satisfying the `unscoped_auth`
	// gate is filling in one expression rather than writing ten lines.
	//
	// Only the first such column is carried. A table with two owner
	// columns is expressing a composite scope this scaffold cannot guess
	// at, and the FORGE_SCAFFOLD marker still fails lint until a person
	// writes the real predicate.
	OwnerColumn string
	// OwnerField is OwnerColumn as the Go field name on the generated
	// entity struct (e.g. "company_id" -> "CompanyId"), for the Create
	// scaffold, which stamps the column rather than filtering on it.
	OwnerField string
	// Scoped is true when the shim should scaffold the scoping wrapper:
	// an OwnerColumn exists AND the RPC is authenticated.
	//
	// Both halves, because they are the same two halves the
	// `unscoped_auth` gate compares. An owned table reached by a
	// deliberately public RPC is not a finding there and must not get a
	// GetUser call here — that call would reject the credential-less
	// callers the proto advertises as welcome, which is forge quietly
	// overruling a declaration the author made.
	Scoped bool
}

// OwnerColumn returns the entity's `forge:owner` column name, or "" when
// the table declares none. See CRUDMethodTemplateData.OwnerColumn.
func OwnerColumn(entity EntityDef) string {
	for _, col := range entity.Columns {
		if col.Owner {
			return col.Name
		}
	}
	return ""
}

// CreateFieldData holds a field mapping from a create request to the ORM entity.
type CreateFieldData struct {
	ProtoGoName  string    // Go field name on the proto request message, e.g. "Name"
	EntityGoName string    // Go field name on the ORM entity, e.g. "Name"
	Kind         FieldKind // scalar, enum, message, wrapper, timestamp, etc.
	GoType       string    // Go type: "string", "int32", "*timestamppb.Timestamp", etc.
	EnumGoType   string    // For enum fields: the pb.EnumType name
}

// FilterFieldData describes a filter field extracted from a List request message.
type FilterFieldData struct {
	ProtoName  string // e.g., "active", "search", "status"
	GoName     string // PascalCase: "Active", "Search", "Status"
	ColumnName string // DB column: "active", "status"
	FieldType  string // "bool", "string", "int32", "int64", "enum"
	FilterType string // "exact", "search"
	IsOptional bool   // proto optional keyword
	// IsEnum marks a pb-enum-typed filter field. Enum entity columns
	// store the proto enum VALUE NAMES as TEXT (CHECK (col IN (...))),
	// while the pb field is int32-kinded — binding the enum value raw
	// (`WhereEq("status", *req.Status)`) hit Postgres with `text =
	// integer` and errored on the first filtered call. The template binds
	// req.<F>.String() — the stored name — instead.
	//
	// UNSPECIFIED semantics: a PROVIDED enum filter always applies —
	// filtering by <ENUM>_UNSPECIFIED (0) matches the rows stored under
	// the UNSPECIFIED value name rather than being silently skipped
	// (skipping would make one declared value unfilterable). `optional`
	// on the request field is the way to express "no filter" (nil).
	IsEnum bool
	// EnumNumber marks an IsEnum filter whose column is an INTEGER, which
	// stores the wire number rather than the value name (a schema the author
	// declared — forge births TEXT). The template binds int32(req.<F>):
	// binding the name would hit Postgres with `integer = text`.
	EnumNumber bool
	// SearchColumns is the entity's declared string columns (minus the
	// PK) that a "search" filter spans via orm.WhereILikeAny. A search
	// field never maps to a column of its own — the historical
	// WhereILike("search", ...) hit a phantom column and either errored
	// or (SQLite double-quote fallback) silently matched nothing.
	SearchColumns []string
}

// MatchCRUDMethods correlates a service's RPC methods with entity definitions
// and returns the matched CRUD methods. Only unary RPCs are matched.
func MatchCRUDMethods(svc ServiceDef, entities []EntityDef) []CRUDMethod {
	entityMap := make(map[string]EntityDef)
	for _, e := range entities {
		entityMap[strings.ToLower(e.Name)] = e
	}

	var matches []CRUDMethod
	for _, m := range svc.Methods {
		op, entityName := CRUDShape(m)
		if op == "" {
			continue
		}

		// Try to find the entity (case-insensitive)
		entity, ok := entityMap[strings.ToLower(entityName)]
		if !ok {
			// For "list", the method name uses plural — try singular
			if op == "list" {
				singular := inflection.Singular(entityName)
				entity, ok = entityMap[strings.ToLower(singular)]
			}
			if !ok {
				continue
			}
		}

		mtd := MethodTemplateData{
			Name:         m.Name,
			InputType:    m.InputType,
			OutputType:   m.OutputType,
			AuthRequired: m.AuthRequired,
		}

		matches = append(matches, CRUDMethod{
			Method:    mtd,
			Entity:    entity,
			Operation: op,
		})
	}
	return matches
}

// CRUDShape is the one answer to "does this rpc's DECLARATION claim CRUD,
// and on which entity name?" — every projection that classifies an rpc asks
// it, so the server wiring, the entity set, the frontend, the seed scope and
// the audit cannot disagree about one rpc.
//
// An rpc claims CRUD when it is unary, named Create<E>/Get<E>/Update<E>/
// Delete<E>/List<Es>, and carries no `// forge:custom` marker
// (ProtoMarkerCustom). The claim is not the verdict: the rpc is CRUD only
// when <E> also has a table and a wire message, which MatchCRUDMethods and
// BuildSchemaEntities decide. Everything CRUDShape refuses is custom.
//
// The entity name is as written — List's plural is NOT singularized here,
// because each caller resolves it against a different inventory.
func CRUDShape(m Method) (operation, entityName string) {
	if m.Custom || m.ClientStreaming || m.ServerStreaming {
		return "", ""
	}
	return parseCRUDOperation(m.Name)
}

// ParseCRUDOperation extracts the CRUD operation and entity name from a
// method NAME alone. Returns ("", "") if the name doesn't match a CRUD
// pattern. Name-only, so it cannot see `// forge:custom` or streaming:
// classify an rpc you hold as a Method with CRUDShape instead.
func ParseCRUDOperation(methodName string) (operation, entityName string) {
	return parseCRUDOperation(methodName)
}

// parseCRUDOperation extracts the CRUD operation and entity name from a method name.
// Returns ("", "") if the method doesn't match a CRUD pattern.
func parseCRUDOperation(methodName string) (operation, entityName string) {
	prefixes := []struct {
		prefix string
		op     string
	}{
		{"Create", "create"},
		{"Get", "get"},
		{"List", "list"},
		{"Update", "update"},
		{"Delete", "delete"},
	}

	for _, p := range prefixes {
		if strings.HasPrefix(methodName, p.prefix) {
			name := strings.TrimPrefix(methodName, p.prefix)
			if name != "" {
				return p.op, name
			}
		}
	}
	return "", ""
}

// crudEntity is the frontend projections' answer to "is this rpc CRUD, and
// on which entity?" — the hook generator's invalidation scope and the page
// generator's entity set both ask it.
//
// The verb split alone is not an answer. parseCRUDOperation reads
// `UpdateJobStatus` as (update, JobStatus) and `ListJobsByCrew` as (list,
// JobsByCrew); taken at its word, the hooks invalidated a `jobStatus` scope no
// query is keyed under (so the Job list the mutation changed stayed cached)
// and the page generator birthed a `jobs-by-crews` CRUD page. The server side
// never made that mistake: BuildSchemaEntities admits an entity only when its
// table exists AND the service declares its wire message. The frontend cannot
// see the table, so it applies the half it can — the declaresWireMessage gate
// — and a custom rpc whose name merely starts with a CRUD verb stays custom on
// both sides of the wire.
//
// For List, the plural is singularized first (ListJobs → Job), falling back
// to the name as written (ListJob, or a plural inflection does not invert).
// A `// forge:custom` rpc is custom here too (CRUDShape): it births no page
// and its hook keys and invalidates at the service scope.
func crudEntity(svc ServiceDef, m Method) (operation, entity string) {
	op, name := CRUDShape(m)
	if op == "" {
		return "", ""
	}
	if op == "list" {
		if singular := inflection.Singular(name); declaresEntityMessage(svc, singular) || !declaresEntityMessage(svc, name) {
			name = singular
		}
	}
	if !declaresEntityMessage(svc, name) {
		return "", ""
	}
	return op, name
}

// declaresEntityMessage is declaresWireMessage restricted to evidence that can
// actually contain an entity. The shallow Messages map holds only rpc
// request/response types — an entity message is never one of those — so a
// descriptor with no deep inventory (written before Schemas/SchemaFiles
// existed) proves nothing and keeps the name-only answer.
func declaresEntityMessage(svc ServiceDef, name string) bool {
	if len(svc.SchemaFiles) == 0 && len(svc.Schemas) == 0 {
		return true
	}
	return declaresWireMessage(svc, name)
}

// CRUDResult is what one CRUD generation call did, so its caller reports
// that rather than what it asked for. A run used to print "✅ Generated
// handlers/<svc> CRUD wiring" and "✅ Generated …/handlers_crud_test.go"
// after calls that returned early having written nothing — every rpc was
// hand-written, or the scaffold-once test was already born — and the files
// it named did not exist.
type CRUDResult struct {
	// Wrote is true when the call emitted (or refreshed) its file.
	Wrote bool
	// Skipped, when Wrote is false, says why nothing was written, phrased to
	// follow "nothing written: ".
	Skipped string
	// Routine marks a skip that is the steady state on every re-run (the
	// scaffold-once test already exists), reported only under --verbose.
	Routine bool
}

// crudMethodList names crudMethods' rpcs for a message.
func crudMethodList(crudMethods []CRUDMethod) string {
	names := make([]string, 0, len(crudMethods))
	for _, cm := range crudMethods {
		names = append(names, cm.Method.Name)
	}
	return strings.Join(names, ", ")
}

// GenerateCRUDHandlers generates handlers_crud_gen.go for a service with CRUD methods.
// It skips methods that already exist in user-owned handler files.
//
// cs is the project's checksum tracker. Passing it ensures the rendered
// handlers_crud_gen.go is recorded so it doesn't show up as an orphan in
// `forge project audit`. A nil cs is tolerated.
func GenerateCRUDHandlers(svc ServiceDef, crudMethods []CRUDMethod, modulePath string, projectDir string, cs *checksums.FileChecksums) (CRUDResult, error) {
	// Disk-first: handlers_crud_gen.go lands inside the EXISTING handler
	// dir and must declare that dir's real package clause. Re-synthesizing
	// the path from the proto name is how the historical
	// handlers/adminserver-vs-admin_server duplicate-dir bug was created.
	res, resErr := ResolveServiceComponent(projectDir, svc.Name)
	if resErr != nil {
		return CRUDResult{}, resErr
	}
	pkg := res.PackageName
	targetDir := res.Dir

	// Stub→CRUD transition: an RPC that was a pristine `forge:gen
	// unwired-stub` method in its own rpc_<name>.go (or, in a project born
	// before the per-RPC split, in handlers.go) and has now become entity-backed
	// must have that marked stub EXCISED. Otherwise the package scan below
	// counts it as implemented by hand and CRUD gen leaves it alone —
	// leaving the RPC stuck on the CodeUnimplemented stub (or, if the shim
	// were forced, a duplicate-method compile error). Only pristine marked
	// stubs for methods CRUD is about to implement are removed; a stub the
	// user has edited (marker removed or body changed) is left in place.
	// Keyed by Go method name: excision matches the *Service methods the
	// handler files declare.
	crudNames := make(map[string]bool, len(crudMethods))
	for _, cm := range crudMethods {
		crudNames[cm.Method.GoName()] = true
	}
	if excised, xerr := ExciseUnwiredStubs(projectDir, targetDir, crudNames); xerr != nil {
		return CRUDResult{}, fmt.Errorf("excise unwired stubs for %s: %w", pkg, xerr)
	} else if len(excised) > 0 {
		fmt.Printf("  ✂️  Excised %d now-entity-backed unwired stub(s) so CRUD can take over: %s\n", len(excised), strings.Join(excised, ", "))
	}

	// Which ops the package needs is read from what its code references,
	// across every file — never from which file declares a method. See
	// crud_op_demand.go for the rule and the incident behind it.
	scan, err := scanHandlerPackage(targetDir)
	if err != nil && !os.IsNotExist(err) {
		return CRUDResult{}, fmt.Errorf("scan handler package %s: %w", pkg, err)
	}
	opMethods, helperEntities := crudOpsDemand(scan, crudMethods)

	relDir := filepath.Join("internal", "handlers", filepath.FromSlash(res.ImportLeaf))
	opsRel := filepath.Join(relDir, "handlers_crud_ops_gen.go")

	// The pre-split Tier-1 implementation file is dead: forge no longer
	// emits RPC method bodies as generated code. Sweep it (and its
	// manifest entry) unless the user took ownership of it. Snapshot the
	// methods it really implemented FIRST: if one of them is about to be
	// re-scaffolded as a custom-read-shape stub, that replaces a working
	// implementation with CodeUnimplemented and must be LOUD — a
	// downstream agent had a near-miss with live traffic on exactly this.
	previouslyImplemented := implementedMethodsIn(filepath.Join(projectDir, relDir, "handlers_crud_gen.go"))
	removeRetiredForgeFile(projectDir, filepath.Join(relDir, "handlers_crud_gen.go"), cs)

	if len(opMethods) == 0 && len(helperEntities) == 0 {
		// Every CRUD RPC is implemented by hand and nothing calls into
		// the ops file — drop it.
		removeRetiredForgeFile(projectDir, opsRel, cs)
		return CRUDResult{Skipped: "every CRUD rpc (" + crudMethodList(crudMethods) +
			") is implemented by hand and nothing calls a CRUD op"}, nil
	}

	// Ensure the Deps struct in service.go has a DB field for CRUD
	// operations. Conversion helpers alone never touch s.deps.DB.
	if len(opMethods) > 0 {
		if err := ensureDepsDBField(targetDir); err != nil {
			return CRUDResult{}, fmt.Errorf("ensure Deps DB field for %s: %w", pkg, err)
		}
	}

	// Build template data. Package is overridden with the disk-resolved
	// clause so the emitted file always matches the directory it lands in
	// (buildCRUDTemplateData's synthesis only holds for fresh scaffolds).
	// A filter-mapping failure here is a hard error by design: a filter
	// field that maps to no declared column must fail the generate, not
	// ship a phantom-column query.
	data, err := buildCRUDTemplateData(svc, opMethods, helperEntities, modulePath)
	if err != nil {
		return CRUDResult{}, err
	}
	data.Package = pkg
	data.DBImport = ORMImportSpec(modulePath, ProjectORMPackage(projectDir))

	// Tier-1 projection: the per-entity wiring ops + conversion helpers.
	// Emitted when at least one method passed shape validation OR a wired
	// custom-read-shape body needs the entity conversion helpers (an
	// all-custom service still gets the <entity>ToProto/FromProto pairs).
	if data.NeedsOpsFile {
		content, rerr := templates.ServiceTemplates().Render("handlers_crud_ops_gen.go.tmpl", data)
		if rerr != nil {
			return CRUDResult{}, fmt.Errorf("render handlers_crud_ops_gen.go.tmpl: %w", rerr)
		}
		if werr := writeForgeOwned(projectDir, opsRel, content, cs); werr != nil {
			return CRUDResult{}, fmt.Errorf("write handlers_crud_ops_gen.go: %w", werr)
		}
	} else {
		removeRetiredForgeFile(projectDir, opsRel, cs)
	}

	// User-owned implementation: scaffold handlers_crud.go once; on later
	// runs append shims for newly-added CRUD RPCs only (existing content
	// is never modified). A shim is owed only to an RPC no file of the
	// package declares — a demanded op whose method already exists (a
	// shim moved to a sibling file) must not be declared twice.
	var unshimmed []CRUDMethodTemplateData
	for _, m := range data.CRUDMethods {
		if !scan.methods[m.MethodName] {
			unshimmed = append(unshimmed, m)
		}
	}
	if err := ensureCRUDShimFile(projectDir, relDir, data, unshimmed, cs, previouslyImplemented); err != nil {
		return CRUDResult{}, err
	}
	return CRUDResult{Wrote: true}, nil
}

// removeRetiredForgeFile deletes a generated file forge no longer
// emits — but never a file the user owns. Ownership is read from the
// file itself: a VERIFYING forge:hash marker certifies the bytes as
// forge output (delete); a Modified marker means the user edited it
// (keep); no marker falls back to the legacy "Code generated by forge"
// banner check for pre-marker projects. Disowned paths are never
// touched.
func removeRetiredForgeFile(projectDir, relPath string, cs *checksums.FileChecksums) {
	if cs.IsDisowned(relPath) {
		return // user-owned by recorded intent
	}
	fullPath := filepath.Join(projectDir, relPath)
	body, err := os.ReadFile(fullPath)
	if err != nil {
		return
	}
	// Journaled deletes: an aborted run must restore what it swept.
	switch checksums.Verify(body) {
	case checksums.Pristine:
		_ = checksums.RemoveJournaled(fullPath)
	case checksums.Modified:
		return // hand-edited — not forge's to delete
	case checksums.NoMarker:
		// Pre-marker forge output: only sweep what self-identifies.
		if bytes.Contains(body, []byte("Code generated by forge")) {
			_ = checksums.RemoveJournaled(fullPath)
		}
	}
}

// crudShimHeader renders the birth header of the user-owned
// handlers_crud.go.
//
// This text is the last thing a handler author reads before writing a
// handler, so every claim in it has to be true. It used to say the CRUD
// lifecycle "(auth, pagination, error mapping)" lived in pkg/crud; that
// package has never authenticated anything, and a measured run shipped
// 17 of 20 CRUD RPCs with no auth check at all — an anonymous POST
// created rows — because the author believed the file it was editing.
//
// So the capability list is DATA (crudDelegatedCapabilities), each entry
// backed by an exported pkg/crud symbol, and everything the delegation
// does not do is stated as plainly as what it does.
func crudShimHeader(data CRUDTemplateData) string {
	labels := make([]string, 0, len(crudDelegatedCapabilities))
	for _, c := range crudDelegatedCapabilities {
		labels = append(labels, c.Label)
	}
	middlewarePkg := data.Module + "/pkg/" + crudAuthSeamPkg
	anyScoped := false
	for _, m := range data.CRUDMethods {
		if m.Scoped {
			anyScoped = true
			break
		}
	}

	paragraphs := []string{
		"yours: scaffolded once, never touched again — forge will not overwrite this file",
		"",
		"Each method is a thin delegation. " + crudLibImport + " does the lifecycle mechanics — " +
			strings.Join(labels, ", ") + " — and the per-entity wiring is generated in " +
			"handlers_crud_ops_gen.go (Tier-1, regenerated every run). Because these bodies " +
			"never name an entity field, proto/schema changes flow through the regenerated ops " +
			"file without touching this one.",
		"",
		"WHAT THE DELEGATION DOES NOT DO IS READ THE CALLER. pkg/crud has no notion of one: it " +
			"never touches the context's claims, so every delegation below treats every caller " +
			"identically. That is not the same as being open — the auth interceptor runs " +
			"FAIL-CLOSED and turned away anyone without a valid token before this file was " +
			"reached, unless this RPC's proto declares auth_required: false, which publishes it " +
			"deliberately (see pkg/" + crudAuthSeamPkg + "/procedures_gen.go for the set).",
		"",
		"WHO MAY DO WHAT is yours. The interceptor proved the caller HAS an identity; it decided " +
			"nothing about whether this caller may perform this operation. Import \"" + middlewarePkg +
			"\" and open each method that needs the principal with:",
		"",
		"\tclaims, err := " + crudAuthSeam + "(ctx)",
		"\tif err != nil {",
		"\t\treturn nil, err // connect.CodeUnauthenticated",
		"\t}",
		"",
		"WHICH ROWS THEY MAY SEE is yours too. Scope rows to the caller (\"a customer sees only " +
			"their own orders\") by wrapping the generated op's Filters right here — a visible " +
			"WHERE clause on those claims. Role checks are ordinary handler code in the same " +
			"place. See `forge skill load auth`.",
	}

	// A file whose methods DO read the caller must not open with three
	// paragraphs saying they do not. The scoped scaffold exists precisely
	// so the header's instruction is already carried out below, and a
	// header that contradicts the code teaches the reader to stop
	// believing the header — which is how the sixteen-delegation incident
	// happened in the first place.
	if anyScoped {
		paragraphs = append(paragraphs,
			"",
			"SOME METHODS BELOW ARE ALREADY SCOPED, so the three paragraphs above describe the "+
				"OTHERS. An entity here declares a `forge:owner` column, and forge scaffolded "+
				"the scoping for its authenticated RPCs: claims resolved, op seam wrapped, "+
				"column named. What forge could not write is the POLICY — how a caller's "+
				"claims map onto that column's values — so each scoped method leaves that one "+
				"expression under a FORGE_SCAFFOLD marker, and `forge lint` fails while the "+
				"marker stands.")
	}

	paragraphs = append(paragraphs, crudShimAuthLegend(data.CRUDMethods)...)
	paragraphs = append(paragraphs,
		"",
		"To customize an RPC, replace its delegation with a real implementation right here. CRUD "+
			"RPCs added later are appended to this file by `forge generate`; your existing "+
			"content is never modified.",
	)

	var b strings.Builder
	for _, p := range paragraphs {
		for _, line := range wrapCommentText(p, 72) {
			switch {
			case line == "":
				b.WriteString("//\n")
			case strings.HasPrefix(line, "\t"):
				// godoc code block: the tab must follow "//" directly.
				b.WriteString("//" + line + "\n")
			default:
				b.WriteString("// " + line + "\n")
			}
		}
	}
	return b.String()
}

// crudShimAuthLegend explains, once, the `Auth:` tag each delegating method
// carries (handlers_crud_shim_method.go.tmpl). The per-method line used to
// repeat this whole explanation above every method — the same ten lines
// five times per entity, in a file agents read constantly — which buried the
// one fact that differs between methods.
//
// Only the tags present at birth are described: the header describes this
// file, and a fully scoped file must not define an UNSCOPED case it does not
// contain (crud_owner_scaffold_test.go). A method appended later carries a
// tag line that reads on its own.
func crudShimAuthLegend(methods []CRUDMethodTemplateData) []string {
	var unscoped, public, scoped bool
	for _, m := range methods {
		switch {
		case m.ShapeMismatch:
			// Its own comment covers it: the body is a wired query, not a delegation.
		case m.Scoped:
			scoped = true
		case m.AuthRequired:
			unscoped = true
		default:
			public = true
		}
	}
	if !unscoped && !public && !scoped {
		return nil
	}
	out := []string{
		"",
		"EACH METHOD'S `Auth:` LINE says which case it is, read from its rpc's auth_required and " +
			"its table's forge:owner column:",
	}
	if unscoped {
		out = append(out, "",
			"AUTHENTICATED, UNSCOPED — only a valid token reaches it, and nothing in its body "+
				"reads who that is yet: the two paragraphs above are the to-do.")
	}
	if public {
		out = append(out, "",
			"PUBLIC — auth_required: false lets a credential-less caller through. If that is "+
				"wrong, the fix is the annotation on the rpc, not a "+crudAuthSeam+"(ctx) call "+
				"here, which would reject callers the proto (and `forge project graph`) advertise "+
				"as welcome.")
	}
	if scoped {
		out = append(out, "",
			"SCOPED — the table declares a forge:owner column, so the body already resolves the "+
				"caller and scopes the rows it touches; only its FORGE_SCAFFOLD owner mapping is "+
				"left to you.")
	}
	return out
}

// wrapCommentText word-wraps one paragraph to width runes. Blank and
// tab-indented paragraphs (godoc code blocks) pass through untouched.
func wrapCommentText(text string, width int) []string {
	if text == "" || strings.HasPrefix(text, "\t") {
		return []string{text}
	}
	var lines []string
	var cur string
	for _, word := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = word
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// crudShimImports computes the import block for the user-owned
// handlers_crud.go shim from the methods it will contain.
func crudShimImports(data CRUDTemplateData) []string {
	imports := []string{"context", "connectrpc.com/connect"}
	hasMismatch := false
	hasReal := false
	needsDB := false
	scoped := false
	hasCreate := false
	for _, m := range data.CRUDMethods {
		if !m.ShapeMismatch && m.Operation == "create" {
			hasCreate = true
		}
		if m.ShapeMismatch {
			hasMismatch = true
		} else {
			hasReal = true
		}
		if len(m.FillHandlerColumns) > 0 {
			needsDB = true
		}
		if m.Scoped {
			// The scoping scaffold names *db.<Entity> in its op wrappers
			// (except on delete, which is keyed by id — but any owned
			// service has more than one shape, and an unused import
			// would not compile, so this is decided per method below via
			// the same flag the template branches on).
			scoped = true
			if m.Operation != "delete" {
				needsDB = true
			}
		}
	}
	if hasReal {
		imports = append(imports, crudLibImport)
	}
	if hasCreate {
		// The Create shim records the scaffolded example metric
		// (internal/metrics/metrics.go, born with the command tree).
		imports = append(imports, data.Module+"/internal/metrics")
	}
	if scoped {
		// The GetUser call the scoping scaffold opens with.
		imports = append(imports, data.Module+"/pkg/"+crudAuthSeamPkg)
	}
	if hasMismatch {
		// The WIRED custom-read-shape body runs a real query: it builds
		// orm.QueryOption filters and calls db.List<Entity>, projecting
		// rows via the generated <entity>ToProto helper.
		imports = append(imports,
			"github.com/reliant-labs/forge/pkg/svcerr",
			data.DBImport)
	} else if needsDB {
		// The forge:fill=handler scaffold's op.Entity wrapper and the
		// scoping scaffold's op wrappers both name the *db.<Entity> type.
		imports = append(imports, data.DBImport)
	}
	// orm reaches this file from either scaffold — the custom-read-shape
	// body's filters or the scoping predicate — so it is added once,
	// after both, rather than by each branch (two appends of the same
	// path produce a duplicate import that does not compile).
	if scoped || hasMismatch {
		imports = append(imports, "github.com/reliant-labs/forge/pkg/orm")
	}
	imports = append(imports, "pb "+data.Module+"/gen/"+data.ProtoPackage+"/v1")
	return imports
}

// renderCRUDShimMethods renders the per-RPC shim blocks (delegation or
// explanatory Unimplemented stub) for the given methods.
func renderCRUDShimMethods(methods []CRUDMethodTemplateData) (string, error) {
	var b strings.Builder
	for _, m := range methods {
		block, err := templates.ServiceTemplates().Render("handlers_crud_shim_method.go.tmpl", m)
		if err != nil {
			return "", fmt.Errorf("render handlers_crud_shim_method.go.tmpl for %s: %w", m.MethodName, err)
		}
		b.WriteString("\n")
		b.Write(bytes.TrimSpace(block))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ensureCRUDShimFile maintains the user-owned handlers_crud.go for the
// RPCs in unshimmed — those no file of the package declares a method for:
//
//   - absent  → scaffold the whole file (the ONLY full write, ever);
//   - present → append shims for them (a newly-annotated entity, a new
//     RPC). Existing content is never rewritten; needed imports are
//     inserted into the existing import block if missing.
//
// The caller computed unshimmed over EVERY file of the package, so an
// append never collides with a method declared in a sibling file —
// whether that method is a hand-written handler or a shim moved there.
func ensureCRUDShimFile(projectDir, relDir string, data CRUDTemplateData, unshimmed []CRUDMethodTemplateData, cs *checksums.FileChecksums, previouslyImplemented map[string]bool) error {
	shimRel := filepath.Join(relDir, "handlers_crud.go")
	fullPath := filepath.Join(projectDir, shimRel)

	existing, readErr := os.ReadFile(fullPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", shimRel, readErr)
	}

	if os.IsNotExist(readErr) {
		if len(unshimmed) == 0 {
			// Every RPC already has a method somewhere in the package:
			// nothing to scaffold, and an empty shim file helps no one.
			return nil
		}
		blocks, err := renderCRUDShimMethods(unshimmed)
		if err != nil {
			return err
		}
		warnCustomReadShapeStubs(unshimmed, shimRel, previouslyImplemented)
		// Header and imports describe the file being written, so they
		// are computed from the methods it will actually contain.
		scaffolded := data
		scaffolded.CRUDMethods = unshimmed
		var b strings.Builder
		b.WriteString(crudShimHeader(scaffolded))
		b.WriteString("package " + data.Package + "\n\n")
		b.WriteString("import (\n")
		for _, imp := range crudShimImports(scaffolded) {
			if alias, path, ok := strings.Cut(imp, " "); ok {
				b.WriteString("\t" + alias + " \"" + path + "\"\n")
			} else {
				b.WriteString("\t\"" + imp + "\"\n")
			}
		}
		b.WriteString(")\n")
		b.WriteString(blocks)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return err
		}
		content := []byte(b.String())
		if err := writeUserScaffold(fullPath, content); err != nil {
			return fmt.Errorf("write %s: %w", shimRel, err)
		}
		recordTier2(cs, shimRel, content)
		return nil
	}

	// Append shims for methods missing from the package. The textual
	// check is a backstop for a handlers_crud.go that does not parse (the
	// package scan skipped it): never append a second declaration of a
	// method the file visibly already has.
	var missing []CRUDMethodTemplateData
	for _, m := range unshimmed {
		if !bytes.Contains(existing, []byte("func (s *Service) "+m.MethodName+"(")) {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	blocks, err := renderCRUDShimMethods(missing)
	if err != nil {
		return err
	}
	warnCustomReadShapeStubs(missing, shimRel, previouslyImplemented)
	content := string(existing)
	for _, imp := range crudShimImports(CRUDTemplateData{
		Module:       data.Module,
		ProtoPackage: data.ProtoPackage,
		DBImport:     data.DBImport,
		CRUDMethods:  missing,
	}) {
		content = ensureImportLine(content, imp)
	}
	content = strings.TrimRight(content, "\n") + "\n" + blocks
	if err := writeUserScaffold(fullPath, []byte(content)); err != nil {
		return fmt.Errorf("append CRUD shims to %s: %w", shimRel, err)
	}
	recordTier2(cs, shimRel, []byte(content))
	fmt.Printf("  ✅ Appended %d new CRUD shim(s) to %s (user-owned)\n", len(missing), shimRel)
	return nil
}

// warnCustomReadShapeStubs prints one loud line per RPC whose shim is
// being written as a WIRED custom-read-shape scaffold. The scaffold is the
// system WORKING — the request/response shape is a legitimate domain
// decision and the body is the user's to refine — but it must never land
// silently: the wired body runs a naive query and returns an EMPTY
// response (a `// TODO: refine` marker on the response packing) until the
// user finishes it, so a deploy ships a procedure that 200s with no data.
// When the RPC previously had a real generated implementation (the retired
// handlers_crud_gen.go carried a non-stub body for it), say so
// explicitly — that's a behavior change on a live procedure, and a
// downstream agent filed a near-miss with live traffic on exactly this
// transition.
func warnCustomReadShapeStubs(methods []CRUDMethodTemplateData, shimRel string, previouslyImplemented map[string]bool) {
	for _, m := range methods {
		if !m.ShapeMismatch {
			continue
		}
		if previouslyImplemented[m.MethodName] {
			fmt.Printf("  ⚠️  %s: REPLACING a previously generated implementation with a WIRED custom-read-shape scaffold in %s (custom read shape: %s) — finish wiring the response before serving traffic\n",
				m.MethodName, shimRel, m.MismatchReason)
			continue
		}
		fmt.Printf("  ⚠️  %s: custom read shape (%s) — scaffolded a WIRED starting point in %s (runs a real query; finish wiring the response onto pb.%s)\n",
			m.MethodName, m.MismatchReason, shimRel, m.OutputType)
	}
}

// implementedMethodsIn scans a retired generated handler file for
// *Service methods that had REAL bodies — i.e. methods NOT tagged with
// a shape-mismatch marker (`forge:custom-read-shape`, or the legacy
// `FORGE_CRUD_SHAPE_MISMATCH` spelling) in the comment block above the
// declaration. Returns nil when the file doesn't exist (the common,
// post-migration case). Line-based on purpose: the file is forge's own
// past output with a fixed shape, and this runs once per generate.
func implementedMethodsIn(path string) map[string]bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	implemented := map[string]bool{}
	pendingMarker := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "// forge:custom-read-shape:") ||
			strings.HasPrefix(trimmed, "// FORGE_CRUD_SHAPE_MISMATCH:") {
			pendingMarker = true
			continue
		}
		if !strings.HasPrefix(trimmed, "func (s *Service) ") {
			// The marker stays pending until the next func declaration —
			// the template emits it inside the doc comment directly above
			// the func, so being lenient about intervening comment lines
			// is safe and being strict would miss reformatted output.
			continue
		}
		rest := strings.TrimPrefix(trimmed, "func (s *Service) ")
		if end := strings.IndexAny(rest, "(\t "); end > 0 {
			if !pendingMarker {
				implemented[rest[:end]] = true
			}
		}
		pendingMarker = false
	}
	return implemented
}

// recordTier2 marks a user-owned scaffold as touched this run. Tier-2
// files carry no certification marker (user-owned from birth), so the
// stale sweep can never target them; the written-this-run mark is kept
// for parity with the chokepoint writers.
func recordTier2(cs *checksums.FileChecksums, relPath string, content []byte) {
	_ = cs
	_ = content
	checksums.MarkWrittenThisRun(relPath)
}

// ensureImportLine inserts an import (optionally "alias path") into the
// file's first import block when no line for that path exists yet. Same
// minimal-mutation approach as ensureDepsDBField: the file is user-owned,
// so we touch only what the appended code needs to compile.
func ensureImportLine(content, imp string) string {
	alias, path, hasAlias := strings.Cut(imp, " ")
	if !hasAlias {
		path = imp
		alias = ""
	}
	if strings.Contains(content, "\""+path+"\"") {
		return content
	}
	idx := strings.Index(content, "import (")
	if idx < 0 {
		return content
	}
	closing := strings.Index(content[idx:], ")")
	if closing < 0 {
		return content
	}
	line := "\t\"" + path + "\"\n"
	if alias != "" {
		line = "\t" + alias + " \"" + path + "\"\n"
	}
	insertPos := idx + closing
	return content[:insertPos] + line + content[insertPos:]
}

// validateCRUDShape decides whether the request/response messages observed
// in svc.Messages match the AIP-158-style shape the CRUD body template
// emits. It returns ok=true when:
//
//   - svc.Messages is empty (legacy/no-descriptor path — preserve old
//     behaviour and let the existing template fire), OR
//   - the input/output messages for this RPC are absent from
//     svc.Messages (we can't validate, so be lenient), OR
//   - every shape rule for the operation holds.
//
// When ok=false the returned reason describes the first failing rule.
// Callers should mark the method ShapeMismatch and skip emitting CRUD-body
// fields that would dereference unavailable proto fields (PageSize,
// PageToken, the entity-typed PK accessor, the repeated-entity response
// field). The shim template renders a `forge:custom-read-shape`-tagged
// CodeUnimplemented stub in that branch so the user-owned file still
// compiles against bespoke proto shapes (Limit/enum filters, string Ticker
// keys, repeated-message responses) — the custom shape is the user's to
// implement, by design.
//
// The rules deliberately stay conservative — they only fail when we can
// see message fields and prove they don't fit. Anything ambiguous (no
// Messages map at all, or this particular RPC's messages missing from it)
// is treated as ok so existing projects whose protos do match AIP-158
// keep generating the same code they did before this check landed.
func validateCRUDShape(svc ServiceDef, cm CRUDMethod) (ok bool, reason string) {
	if len(svc.Messages) == 0 {
		return true, ""
	}

	inputFields, inputKnown := svc.Messages[cm.Method.InputType]
	outputFields, outputKnown := svc.Messages[cm.Method.OutputType]

	inputByName := make(map[string]MessageFieldDef, len(inputFields))
	for _, f := range inputFields {
		inputByName[f.Name] = f
	}
	outputByName := make(map[string]MessageFieldDef, len(outputFields))
	for _, f := range outputFields {
		outputByName[f.Name] = f
	}

	switch cm.Operation {
	case "get", "delete":
		if !inputKnown {
			return true, ""
		}
		// PkField on the entity is the snake_case proto name (e.g.
		// "id", "ticker"). The CRUD template emits `req.<PascalPk>`
		// and downstream code calls Get/DeleteByID with that value.
		// If the request has no field with that name at all, the
		// generated body won't compile.
		if _, has := inputByName[cm.Entity.PkField]; !has {
			return false, fmt.Sprintf("request %s has no %s field matching entity PK (observed fields: %s)", cm.Method.InputType, cm.Entity.PkField, describeFields(inputFields))
		}
	case "update":
		if !inputKnown {
			return true, ""
		}
		// Update body dereferences `req.<EntityField>` and expects
		// it to be a *db.<Entity>. Validate the request actually
		// carries an entity-typed field.
		found := false
		for _, f := range inputFields {
			if fieldMatchesEntity(f, cm.Entity.Name) {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("request %s carries no %s message field (observed fields: %s)", cm.Method.InputType, cm.Entity.Name, describeFields(inputFields))
		}
	case "list":
		// Two valid AIP-132 list shapes are Tier-1:
		//
		//   - cursor-paginated: BOTH page_size and page_token present
		//     (the template emits req.PageSize / req.PageToken and packs
		//     NextPageToken);
		//   - non-paginated: NEITHER present — a plain filtered/ordered
		//     list that returns all matches (no cursor dereferences).
		//
		// Only an INCONSISTENT pair (exactly one of the two) falls to the
		// custom path: the cursor template needs both, and a lone
		// page_size (kalshi-trader's `Limit`/offset style, say) is a
		// bespoke pagination contract the user must wire by hand.
		if inputKnown && strings.HasPrefix(cm.Method.InputType, "List") && strings.HasSuffix(cm.Method.InputType, "Request") {
			_, hasSize := inputByName["page_size"]
			_, hasTok := inputByName["page_token"]
			if hasSize != hasTok {
				missing := "page_token"
				if hasTok {
					missing = "page_size"
				}
				return false, fmt.Sprintf("request %s has one half of AIP-158 cursor pagination but not the other (missing %s); add both for a cursor-paginated list, or neither for a plain filtered list (observed fields: %s)", cm.Method.InputType, missing, describeFields(inputFields))
			}
		}
		// List response template emits `<EntityPlural>: items` and
		// optionally `NextPageToken: nextPageToken`. Validate the
		// response carries a repeated entity field by that name.
		//
		// The field name is derived through naming.EntityListFieldName —
		// the SAME helper the entity scaffolder emits (`forge scaffold entity`)
		// and the ops emitter's Go field derives from — so the comparison
		// is against the actual snake_case proto field ("module_configs"),
		// not a concatenated-lowercase form ("moduleconfigs") that matched
		// no proto and broke every multi-word entity's CRUD.
		if outputKnown {
			listField := naming.EntityListFieldName(cm.Entity.Name)
			if _, has := outputByName[listField]; !has {
				return false, fmt.Sprintf("response %s lacks repeated %s field %s (observed fields: %s)", cm.Method.OutputType, cm.Entity.Name, listField, describeFields(outputFields))
			}
		}
	case "create":
		// Create response template emits `<EntityName>: entity`.
		// Validate the response carries a single field of that type
		// (named after the entity in snake_case). Same helper as the
		// scaffolder/emitter (see the list case) so multi-word entities
		// (module_config, order_item, …) match instead of falling to the
		// custom-read-shape stub on the concatenated-lowercase mismatch.
		if outputKnown {
			entityField := naming.EntityFieldName(cm.Entity.Name)
			if _, has := outputByName[entityField]; !has {
				return false, fmt.Sprintf("response %s lacks %s field %s (observed fields: %s)", cm.Method.OutputType, cm.Entity.Name, entityField, describeFields(outputFields))
			}
		}
	}
	return true, ""
}

// detectListPagination reports whether a list RPC gets cursor pagination
// wired in. Cursor pagination is opt-in: the request must follow
// List*Request naming AND actually carry both page_size and page_token.
// A List*Request without them is a valid non-paginated AIP-132 list
// (filter/order only) that validateCRUDShape admits to Tier-1 — its body
// must NOT dereference req.PageSize/req.PageToken (they don't exist on
// that proto). When there is no field descriptor at all (legacy path),
// the historical assume-paginated behavior is preserved. Returns
// ("", false-equivalents) for non-list / shape-mismatched methods.
func detectListPagination(svc ServiceDef, cm CRUDMethod, shapeOK bool) (bool, string) {
	if !shapeOK || cm.Operation != "list" {
		return false, ""
	}
	if !strings.HasPrefix(cm.Method.InputType, "List") || !strings.HasSuffix(cm.Method.InputType, "Request") {
		return false, ""
	}
	fields, ok := svc.Messages[cm.Method.InputType]
	if !ok {
		// No descriptor — preserve the historical assume-cursor default.
		return true, "cursor"
	}
	hasSize, hasTok := false, false
	for _, f := range fields {
		switch f.Name {
		case "page_size":
			hasSize = true
		case "page_token":
			hasTok = true
		}
	}
	if hasSize && hasTok {
		return true, "cursor"
	}
	return false, ""
}

// crudMethodFacts derives the per-method CRUDMethodTemplateData facts shared
// by BOTH the handler builder (buildCRUDTemplateData) and the test builder
// (buildCRUDTestTemplateData). Both previously re-derived this same set
// independently with explicit keep-in-sync comments; computing it once here is
// the single source of truth. The handler builder consumes the result
// directly; the test builder calls this then layers entity-grouping on top.
//
// strictFilters is the ONE axis on which the two callers differ. The handler
// path (strictFilters=true) classifies list filters via
// classifyEntityFilterField, which fails the generate LOUDLY when a filter
// field names no declared column — shipping a phantom-column query is the
// review-confirmed silence bug. The test path (strictFilters=false) classifies
// via classifyFilterField, which is advisory (no SearchColumns, never fatal):
// the test template never dereferences filter facts, so it must not abort the
// generate on a bespoke field. Errors are only ever returned when
// strictFilters is true.
func crudMethodFacts(svc ServiceDef, cm CRUDMethod, strictFilters bool) (CRUDMethodTemplateData, error) {
	// Validate the request/response shape up front. When the observed proto
	// messages don't match the AIP-158-style body the template emits, we
	// still emit a method (so the proto's RPC interface is satisfied) but
	// route it to a tagged CodeUnimplemented stub instead of the body. This
	// keeps handlers_crud_gen.go compiling against bespoke shapes
	// (Limit/enum filters, string Ticker keys, repeated-message responses).
	// The test scaffold mirrors the same gate so it doesn't emit per-RPC
	// rows that dereference fields the request type doesn't have.
	shapeOK, shapeReason := validateCRUDShape(svc, cm)

	// Cursor pagination is opt-in (both page_size + page_token present); a
	// List*Request without them is a plain non-paginated AIP-132
	// filtered/ordered list, still Tier-1.
	hasPagination, paginationStyle := detectListPagination(svc, cm, shapeOK)

	// Detect filters and ordering from request message fields. Skip when the
	// shape didn't match: classifyFilterField would otherwise happily turn a
	// bespoke field like `ticker` (a string PK) or a `kalshi_status` enum
	// into a synthetic `WhereEq("ticker", req.Ticker)` clause that fails to
	// compile against the real request type.
	var filterFields []FilterFieldData
	hasOrderBy := false
	hasDescending := false
	if shapeOK && cm.Operation == "list" && svc.Messages != nil {
		if msgFields, ok := svc.Messages[cm.Method.InputType]; ok {
			for _, mf := range msgFields {
				// A companion `bool descending` field is what the OrderBy
				// closure dereferences as req.Descending. Detect it before
				// classifySkipField (which lists "descending" among the
				// non-filter control fields) so an order_by WITHOUT a
				// descending field generates a closure that doesn't
				// reference the missing field.
				if mf.Name == "descending" && mf.ProtoType == "bool" {
					hasDescending = true
				}
				if classifySkipField(mf.Name) {
					continue
				}
				if mf.Name == "order_by" {
					hasOrderBy = true
					continue
				}
				var ff FilterFieldData
				if strictFilters {
					var ferr error
					ff, ferr = classifyEntityFilterField(mf, cm.Entity)
					if ferr != nil {
						// LOUD by design: a filter the generator cannot map
						// to a declared column must fail the generate, not
						// ship a phantom-column query that silently returns
						// nothing (or leaks SQL errors) at runtime.
						return CRUDMethodTemplateData{}, fmt.Errorf("%s.%s: %w", svc.Name, cm.Method.Name, ferr)
					}
				} else {
					ff = classifyFilterField(mf)
				}
				ff.GoName = messageFieldGoName(msgFields, mf.Name)
				ff.EnumNumber = ff.IsEnum && entityColumnType(cm.Entity, mf.Name) == "int64"
				filterFields = append(filterFields, ff)
			}
		}
	}

	// total_count population: only when the list RESPONSE message declares a
	// total_count field (the entity scaffolder emits one). Wiring the COUNT
	// otherwise would pack a field the proto doesn't have.
	hasTotalCount := false
	if shapeOK && cm.Operation == "list" && svc.Messages != nil {
		if outFields, ok := svc.Messages[cm.Method.OutputType]; ok {
			for _, f := range outFields {
				if f.Name == "total_count" {
					hasTotalCount = true
					break
				}
			}
		}
	}

	// Determine the entity field name in the update request message.
	// Proto generates a field named after the entity (e.g., "Project project = 1;"
	// becomes Go field "Project"). We look it up in the parsed message fields;
	// see updateEntityGoField for the fallback.
	updateEntityField := ""
	if cm.Operation == "update" {
		updateEntityField = updateEntityGoField(svc, cm.Method.InputType, cm.Entity.Name)
	}

	// AIP-134: a FieldMask field on the update request wires the
	// masked-persistence hooks. Skip on shape mismatch — the stub
	// branch never dereferences it.
	updateMaskField := ""
	var readOnlyColumns []string
	if shapeOK && cm.Operation == "update" {
		updateMaskField = updateMaskGoField(svc, cm.Method.InputType)
		readOnlyColumns = ReadOnlyColumns(cm.Entity)

		// A forge:version column the wire message does not carry makes
		// this Update un-runnable after a row's first edit: the caller
		// has no way to present the version the compare-and-swap
		// matches on, so the predicate is always `version = 0`. Raised
		// on the update method because that is the one the marker
		// changes, and only on the handler path — the test builder
		// (strictFilters=false) must keep scaffolding for an entity
		// whose proto is mid-edit.
		if strictFilters {
			if err := UnversionableEntitiesError(FindUnversionableEntities(cm.Entity)); err != nil {
				return CRUDMethodTemplateData{}, fmt.Errorf("%s.%s: %w", svc.Name, cm.Method.Name, err)
			}
		}
	}

	// Best-effort filter mapping for the WIRED custom-read-shape body.
	// Only when the shape didn't match (the stub branch): seed the
	// scaffold's []orm.QueryOption skeleton from request fields that
	// happen to name a declared column. Advisory, never fatal.
	var customFilters []FilterFieldData
	if !shapeOK {
		customFilters = buildCustomFilters(svc, cm)
	}

	// Precompute the create-request -> entity-struct assignments.
	// Skip on shape mismatch — the stub doesn't reference these.
	var createAssigns []string
	var fillHandlerColumns []string
	if shapeOK && cm.Operation == "create" {
		m := Method{
			Name:        cm.Method.Name,
			InputType:   cm.Method.InputType,
			OutputType:  cm.Method.OutputType,
			InputTypeFQ: svc.Package + "." + cm.Method.InputType,
		}
		var unmapped []UnmappedField
		createAssigns, unmapped = buildCreateAssigns(svc, m, cm.Entity)
		// LOUD by design, on the same axis as the list-filter check above:
		// a create-request field with a column and no conversion accepts a
		// value from the caller and stores the column DEFAULT. The advisory
		// (test-builder) caller keeps the partial assigns instead — a
		// scaffolded test that exercises fewer fields is not a data-loss bug.
		if strictFilters {
			if err := UnmappedFieldsError(unmapped); err != nil {
				return CRUDMethodTemplateData{}, fmt.Errorf("%s.%s: %w", svc.Name, cm.Method.Name, err)
			}
			// A NOT NULL column with no DB DEFAULT and no wire assignment
			// (forge:read-only, and no forge:fill= acknowledgement) is
			// provably impossible to INSERT: every Create for this entity
			// fails at the database. Derived purely from schema+wire facts
			// forge already joined into cm.Entity, so it fires whether or
			// not the author has ever heard of forge:fill.
			if err := UnsatisfiableColumnsError(FindUnsatisfiableColumns(cm.Entity)); err != nil {
				return CRUDMethodTemplateData{}, fmt.Errorf("%s.%s: %w", svc.Name, cm.Method.Name, err)
			}
		}
		fillHandlerColumns = FillHandlerColumns(cm.Entity)
	}

	// The owner column seeds the shim's scoping scaffold. Only for a
	// shape the shim actually delegates: a custom-read-shape method has
	// no op to wrap, and its body already carries its own UNSCOPED note.
	ownerColumn, ownerField := "", ""
	if shapeOK {
		if ownerColumn = OwnerColumn(cm.Entity); ownerColumn != "" {
			ownerField = naming.ColumnGoName(ownerColumn)
		}
	}

	return CRUDMethodTemplateData{
		MethodName:         cm.Method.GoName(),
		InputType:          cm.Method.InputGoName(),
		OutputType:         cm.Method.OutputGoName(),
		EntityName:         cm.Entity.Name,
		EntityPbType:       naming.GoCamelCase(cm.Entity.Name),
		EntityLower:        strings.ToLower(cm.Entity.Name),
		Operation:          cm.Operation,
		AuthRequired:       cm.Method.AuthRequired,
		AuthSeam:           crudAuthSeam,
		PkField:            naming.GoCamelCase(cm.Entity.PkField),
		PkDBField:          naming.ColumnGoName(cm.Entity.PkField),
		PkColumnName:       cm.Entity.PkField,
		PkGoType:           cm.Entity.PkGoType,
		HasPkInInput:       cm.Operation == "get" || cm.Operation == "update" || cm.Operation == "delete",
		ResponseField:      responseEntityGoField(svc, cm.Method.OutputType, cm.Entity.Name, cm.Operation == "list"),
		HasPagination:      hasPagination,
		PaginationStyle:    paginationStyle,
		HasFilters:         len(filterFields) > 0,
		FilterFields:       filterFields,
		HasOrderBy:         hasOrderBy,
		HasDescending:      hasDescending,
		HasTotalCount:      hasTotalCount,
		UpdateEntityField:  updateEntityField,
		UpdateMaskField:    updateMaskField,
		ReadOnlyColumns:    readOnlyColumns,
		CreateAssigns:      createAssigns,
		ShapeMismatch:      !shapeOK,
		MismatchReason:     shapeReason,
		CustomFilters:      customFilters,
		FillHandlerColumns: fillHandlerColumns,
		OwnerColumn:        ownerColumn,
		OwnerField:         ownerField,
		Scoped:             ownerColumn != "" && cm.Method.AuthRequired,
	}, nil
}

// appendOnlyCRUDConflict reports the mutating-RPC-on-an-immutable-table
// contradiction, in terms of the two declarations that disagree and the two
// ways out. Nil for every other combination, including Create/Get/List on
// an append-only table — append-only removes the mutating verbs only.
func appendOnlyCRUDConflict(cm CRUDMethod) error {
	if !cm.Entity.AppendOnly {
		return nil
	}
	if cm.Operation != "update" && cm.Operation != "delete" {
		return nil
	}
	verb := map[string]string{"update": "Update", "delete": "Delete"}[cm.Operation]
	return fmt.Errorf(
		"rpc %s cannot be generated: table %q is declared append-only "+
			"(COMMENT ON TABLE %s IS 'forge:append-only', or forge's %s_append_only guard trigger), "+
			"so forge emits no db.%s%s delegate for it and the database rejects the write outright. "+
			"Either drop the %s RPC and its request/response messages from the proto — correcting a bad "+
			"row in a ledger means appending its reversal — or, if the table was never meant to be "+
			"immutable, remove the declaration in a migration",
		cm.Method.Name, cm.Entity.TableName, cm.Entity.TableName, cm.Entity.TableName,
		verb, cm.Entity.Name, cm.Method.Name)
}

// buildCRUDTemplateData builds the ops-file data for crudMethods (the RPCs
// whose op is demanded) plus a conversion pair for each of helperEntities
// (entities no demanded op covers whose helpers the package still calls —
// see crudOpsDemand).
func buildCRUDTemplateData(svc ServiceDef, crudMethods []CRUDMethod, helperEntities []EntityDef, modulePath string) (CRUDTemplateData, error) {
	// Synthesized Package is a placeholder only: GenerateCRUDHandlers
	// overrides it with the disk-resolved package clause before rendering
	// (the file lands inside an EXISTING handler dir).
	pkg := naming.ServicePackage(svc.Name)

	// Build ProtoPackage path (same logic as mapServiceDefToTemplateData)
	protoPackage := protoImportPath(svc)

	var methods []CRUDMethodTemplateData
	for _, cm := range crudMethods {
		// An Update/Delete RPC against a `forge:append-only` table is a
		// contradiction between two things the USER owns — the RPC in the
		// proto and the table comment in the migration — so forge cannot
		// pick a winner. It can only choose which failure the user gets.
		//
		// Generating the op anyway produces the worst one: the ops
		// template calls db.Update<Entity> / db.Delete<Entity>, which the
		// ORM generator no longer emits for an append-only table, so the
		// build fails with `undefined: db.UpdatePayment` inside a
		// forge-owned file the user is told never to edit — a compile
		// error that names neither declaration that caused it. Name the
		// disagreement here instead, while both halves are still in hand.
		if err := appendOnlyCRUDConflict(cm); err != nil {
			return CRUDTemplateData{}, err
		}

		// Strict filter classification: an unmappable list filter fails the
		// generate LOUDLY (phantom-column queries silently match nothing).
		mtd, err := crudMethodFacts(svc, cm, true)
		if err != nil {
			return CRUDTemplateData{}, err
		}
		methods = append(methods, mtd)
	}

	// Check if any method has pagination, filters, ordering, or a real
	// (non-stub) body. Mismatched stubs contribute nothing to the file's
	// import needs so we don't pull in pkg/crud or pkg/orm for a file
	// that only emits TODO stubs.
	hasPagination := false
	hasFilters := false
	hasOrderBy := false
	needsCRUDLib := false
	for _, m := range methods {
		if m.HasPagination {
			hasPagination = true
		}
		if m.HasFilters {
			hasFilters = true
		}
		if m.HasOrderBy {
			hasOrderBy = true
		}
		if !m.ShapeMismatch {
			needsCRUDLib = true
		}
	}
	// Any real op names orm.QueryOption now: the get/update/delete shims
	// take the variadic scoping options their op fields declare, so the
	// import is no longer conditional on a list feature.
	needsORM := needsCRUDLib || hasPagination || hasFilters || hasOrderBy
	// A json/jsonb pairing reaches pkg/orm (and fmt) from the create
	// closure too, and a legacy-TEXT timestamp reaches `time` the same way.
	// The create closure is built per METHOD, so it sits outside the
	// conversion pair the Conv* gates inspect.
	createNeedsORM, createNeedsFmt, createNeedsTime := false, false, false
	for _, m := range methods {
		if AssignsContain(m.CreateAssigns, "orm.") {
			createNeedsORM = true
		}
		if AssignsContain(m.CreateAssigns, "fmt.Errorf") {
			createNeedsFmt = true
		}
		if AssignsContain(m.CreateAssigns, timeImportToken) {
			createNeedsTime = true
		}
	}
	needsORM = needsORM || createNeedsORM

	// One conversion pair per entity referenced by ANY CRUD body —
	// real (non-stub) OR a wired custom-read-shape stub. The wired
	// custom body calls <entity>ToProto to project the rows it fetches
	// onto the wire, so the projection pair must exist even when every
	// method on the service is custom (no real op constructor at all).
	//
	// Plus one pair per helper entity: no demanded op uses it, but the
	// package's own code calls its <entity>ToProto / <entity>FromProto
	// (a custom RPC, a custom-read-shape body, a hand-written handler).
	var convs []EntityConvTemplateData
	var unmapped []UnmappedField
	seenConv := map[string]bool{}
	addConv := func(entity EntityDef) {
		if seenConv[entity.Name] {
			return
		}
		seenConv[entity.Name] = true
		conv, u := BuildEntityConv(svc, entity)
		convs = append(convs, conv)
		unmapped = append(unmapped, u...)
	}
	for _, cm := range crudMethods {
		addConv(cm.Entity)
	}
	for _, e := range helperEntities {
		addConv(e)
	}
	// Every (wire field, column) pair with no conversion fails the whole
	// generate, listing all of them at once. The alternative this replaced
	// was a comment in the generated body naming the dead field — a guard
	// that reports green, which is how a `repeated OrderLineItem` over a
	// JSONB column shipped an API that accepted line items and stored none.
	if err := UnmappedFieldsError(unmapped); err != nil {
		return CRUDTemplateData{}, fmt.Errorf("%s: %w", svc.Name, err)
	}

	// The ops file is emitted when there's at least one real op to wire
	// OR at least one conversion pair a wired custom body depends on.
	needsOpsFile := needsCRUDLib || len(convs) > 0

	return CRUDTemplateData{
		Package:          pkg,
		Module:           modulePath,
		ProtoPackage:     protoPackage,
		DBImport:         ORMImportSpec(modulePath, config.ORMConfig{}),
		HasPagination:    hasPagination,
		HasFilters:       hasFilters,
		HasOrderBy:       hasOrderBy,
		NeedsORM:         needsORM || ConvNeedsORM(convs),
		NeedsCRUDLib:     needsCRUDLib,
		NeedsOpsFile:     needsOpsFile,
		CRUDMethods:      methods,
		Entities:         convs,
		NeedsTimestamppb: ConvNeedsTimestamppb(convs),
		NeedsFmt:         ConvNeedsFmt(convs) || createNeedsFmt,
		NeedsTime:        ConvNeedsTime(convs) || createNeedsTime,
	}, nil
}

// CRUDTestTemplateData holds all data needed to render the CRUD test template.
type CRUDTestTemplateData struct {
	// UnfilledDeps mirrors ServiceTemplateData.UnfilledDeps: when the
	// harness cannot construct a required Deps field, the lifecycle test
	// passes WithPlaceholderDeps() so it constructs as written.
	UnfilledDeps []string
	Package      string                   // Go package name, e.g. "patients"
	Module       string                   // Go module path, e.g. "github.com/demo-project"
	ProtoPackage string                   // e.g. "proto/services/patients"
	Entities     []CRUDTestEntityData     // Grouped per-entity test data
	CRUDMethods  []CRUDMethodTemplateData // All CRUD methods (for individual error tests)
	// NeedsFieldMask gates the fieldmaskpb import: true when at least one
	// entity's lifecycle test emits the AIP-134 masked-update block.
	NeedsFieldMask bool
	// TestHelperName mirrors ServiceTemplateData.TestHelperName: the suffix
	// the bootstrap testing generator emits on `NewTest<X>` /
	// `NewTest<X>Server`. CRUD test scaffolds use this rather than
	// pascal-casing Package so the call site matches the actual factory
	// when an internal package shares the service's leaf name.
	TestHelperName string
	// ServiceImportPath is the internal/handlers/ directory leaf, used to
	// import the handler package whose in-package helpers_gen_test.go
	// declares NewTest<X>. The scaffolded test stays `package <pkg>_test`
	// (external), and Go compiles a package's in-package _test.go files
	// INTO the package under test — so the external test package sees
	// these helpers through this ordinary import.
	ServiceImportPath string
}

// CRUDTestEntityData groups CRUD operations by entity for lifecycle tests.
type CRUDTestEntityData struct {
	EntityName    string // "Patient"
	EntityLower   string // "patient"
	PkField       string // "Id"
	PkGoType      string // "int64"
	HasCreate     bool
	HasGet        bool
	HasList       bool
	HasUpdate     bool
	HasDelete     bool
	HasAllCRUD    bool // true if all 5 operations exist
	HasTimestamps bool // table has created_at AND the proto message exposes it — created_at is asserted set on the wire
	// MutableStringField is the Go name of the first non-PK string field
	// (e.g. "Name") — the field the lifecycle test mutates to prove
	// update actually writes. Empty when the entity has none.
	MutableStringField string
	// MutableStringFieldPath is MutableStringField's proto field name
	// (snake_case) — the AIP-134 update_mask path for it.
	MutableStringFieldPath string
	// SecondStringField/-Path name a SECOND mutable string field when the
	// entity has one. The masked-update test
	// loads it with a clobber value the mask does NOT name, then asserts
	// it survived — proving the mask restricts the write. Empty when the
	// entity has only one string field (the non-clobber assertion is then
	// skipped; the masked write itself is still exercised).
	SecondStringField     string
	SecondStringFieldPath string
	CreateMethod          CRUDMethodTemplateData
	GetMethod             CRUDMethodTemplateData
	ListMethod            CRUDMethodTemplateData
	UpdateMethod          CRUDMethodTemplateData
	DeleteMethod          CRUDMethodTemplateData
	Fields                []CRUDTestFieldData // entity proto message fields (minus PK, minus deleted_at)
	UpdateEntityField     string              // Go field name holding entity in UpdateRequest, e.g. "Project"
}

// CRUDTestFieldData holds per-field data for generating test values.
type CRUDTestFieldData struct {
	ProtoName string    // "Name"
	GoType    string    // "string"
	Kind      FieldKind // scalar, enum, message, wrapper, timestamp, etc.
	TestValue string    // variant 0's literal: `"test-value"`, a seeded FK id, a CHECK-pool value, ...
	// TestValue2 is variant 1's literal — distinct from TestValue wherever
	// the column admits a second value, so a UNIQUE index added later cannot
	// collide the two rows a test creates.
	TestValue2 string
}

// GenerateCRUDTests generates handlers_crud_gen_test.go (unit-test frames,
// no build tag — runs in the default `go test ./...`) and
// handlers_crud_integration_test.go (lifecycle / pagination /
// filter / NotFound suites guarded by `//go:build integration`) for a
// service with CRUD methods.
//
// cs is the project's checksum tracker. Both scaffold files are recorded
// when actually written; once the user clears every FORGE_SCAFFOLD marker
// the file becomes user-owned and forge stops re-rendering it (and stops
// updating the checksum). A nil cs is tolerated.
func GenerateCRUDTests(svc ServiceDef, crudMethods []CRUDMethod, modulePath string, projectDir string, cs *checksums.FileChecksums) (CRUDResult, error) {
	// Disk-first: same handler-dir + package-clause resolution as
	// GenerateCRUDHandlers (the two MUST land in the same directory and
	// declare the same package).
	res, resErr := ResolveServiceComponent(projectDir, svc.Name)
	if resErr != nil {
		return CRUDResult{}, resErr
	}
	pkg := res.PackageName
	targetDir := res.Dir
	relDir := filepath.Join("internal", "handlers", filepath.FromSlash(res.ImportLeaf))

	// Retire the marker-scaffold test pair this generator used to emit
	// (per-RPC AnyOutcome frames + a build-tag-gated integration suite).
	// Their replacement is ONE user-owned lifecycle test with real
	// assertions; files where the user already cleared every
	// FORGE_SCAFFOLD marker are theirs and are left alone.
	removeRetiredScaffoldTest(projectDir, filepath.Join(relDir, "handlers_crud_gen_test.go"), cs)
	removeRetiredScaffoldTest(projectDir, filepath.Join(relDir, "handlers_crud_integration_test.go"), cs)

	// Mirror the dedup GenerateCRUDHandlers applies: methods the user
	// implemented by hand keep their own tests.
	existingMethods, err := ScanExistingMethods(targetDir, false)
	if err != nil && !os.IsNotExist(err) {
		return CRUDResult{}, fmt.Errorf("scan existing methods for %s tests: %w", pkg, err)
	}
	var filteredMethods []CRUDMethod
	for _, cm := range crudMethods {
		if existingMethods[cm.Method.GoName()] {
			continue
		}
		filteredMethods = append(filteredMethods, cm)
	}
	if len(filteredMethods) == 0 {
		return CRUDResult{Skipped: "every CRUD rpc (" + crudMethodList(crudMethods) +
			") is implemented by hand, and a hand-written rpc keeps its own tests"}, nil
	}

	// The lifecycle test covers string-PK entities with create+get (the
	// forge CRUD convention). Nothing qualifying → nothing to scaffold.
	hasCreate := map[string]bool{}
	hasGet := map[string]bool{}
	for _, cm := range filteredMethods {
		switch cm.Operation {
		case "create":
			hasCreate[cm.Entity.Name] = true
		case "get":
			hasGet[cm.Entity.Name] = true
		}
	}
	qualifying := false
	for _, cm := range filteredMethods {
		if hasCreate[cm.Entity.Name] && hasGet[cm.Entity.Name] && cm.Entity.PkGoType == "string" {
			qualifying = true
			break
		}
	}
	if !qualifying {
		return CRUDResult{Skipped: "the lifecycle test needs an entity with both a Create and a Get rpc over a string primary key, and none has"}, nil
	}

	// Scaffold-once: handlers_crud_test.go is user-owned from line one —
	// which includes the user's right to DELETE it. The decision is the
	// birth ledger's, not os.Stat's: an author who hardened their
	// migrations past what the scaffolded fixtures satisfy removes this
	// file, and a presence check read that removal as "absent, write it"
	// and handed the file back on the next three generate runs.
	lifecycleRel := filepath.Join(relDir, "handlers_crud_test.go")
	lifecyclePath := filepath.Join(projectDir, lifecycleRel)
	if !checksums.ScaffoldOnceDecision(projectDir, lifecycleRel) {
		return CRUDResult{Skipped: "scaffolded once already — the file is yours (or you deleted it)", Routine: true}, nil
	}

	// The file carries NO fixture data. Its rows come from the regenerated
	// create-request factories in factories_gen_test.go (see
	// create_request_factory.go), which re-derive values and FK parents from
	// the applied schema on every generate — so a migration edit made after
	// birth (a GENERATED column, a one-way status CHECK) flows into them,
	// instead of breaking literals frozen into a file forge never rewrites.
	//
	// The schema model is still read here, for one decision that IS frozen at
	// birth: which string column the update leg mutates. It must be one no
	// constraint governs, or the mutation literal is itself rejected.
	// Introspection happens only on this first-scaffold path — never on the
	// (common) already-scaffolded re-generate. A nil model (no migrations)
	// degrades to the first plain string field.
	fix := buildCRUDTestFixtures(projectDir, nil)
	defer fix.close()

	// Package + TestHelperName are overridden with the disk-resolved
	// package clause so the emitted test file always matches the directory
	// it lands in AND calls the `app.NewTest<X>` factory the bootstrap
	// testing generator (which uses the same resolver) actually emitted.
	data := buildCRUDTestTemplateData(svc, filteredMethods, modulePath, projectDir, fix)
	data.Package = pkg
	data.TestHelperName = ComputeTestHelperName(pkg, projectDir)
	data.UnfilledDeps = UnfilledDepsFor(targetDir)
	// relDir is internal/handlers/<leaf>; the import needs the leaf.
	data.ServiceImportPath = filepath.ToSlash(filepath.Base(relDir))

	content, err := templates.ServiceTemplates().Render("handlers_crud_test.go.tmpl", data)
	if err != nil {
		return CRUDResult{}, fmt.Errorf("render handlers_crud_test.go.tmpl: %w", err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return CRUDResult{}, err
	}
	if err := writeUserScaffold(lifecyclePath, content); err != nil {
		return CRUDResult{}, fmt.Errorf("write handlers_crud_test.go: %w", err)
	}
	checksums.RecordScaffold(projectDir, lifecycleRel)
	recordTier2(cs, lifecycleRel, content)
	return CRUDResult{Wrote: true}, nil
}

// removeRetiredScaffoldTest deletes a retired marker-scaffold test file —
// but ONLY while it still carries FORGE_SCAFFOLD markers (i.e. forge
// still owned it). A file the user customized (markers cleared) is
// user-owned and stays, manifest entry and all.
func removeRetiredScaffoldTest(projectDir, relPath string, cs *checksums.FileChecksums) {
	fullPath := filepath.Join(projectDir, relPath)
	body, err := os.ReadFile(fullPath)
	if err != nil {
		return
	}
	if !bytes.Contains(body, []byte("FORGE_SCAFFOLD:")) {
		return
	}
	// Journaled: an aborted run restores the retired scaffold test.
	_ = checksums.RemoveJournaled(fullPath)
	_ = cs
}

// fix carries the schema model, consulted only to keep constrained columns out
// of the update leg's mutation targets; nil falls back to the first plain
// string field. The create requests are not built here — the lifecycle test
// calls the regenerated New<CreateRequest> factories (see
// buildCreateRequestFields).
func buildCRUDTestTemplateData(svc ServiceDef, crudMethods []CRUDMethod, modulePath, projectDir string, fix *crudTestFixtures) CRUDTestTemplateData {
	// Synthesized Package/TestHelperName are placeholders only:
	// GenerateCRUDTests overrides both with disk-resolved values before
	// rendering — see the call site for the rationale.
	pkg := naming.ServicePackage(svc.Name)

	// Build ProtoPackage path (same logic as buildCRUDTemplateData)
	protoPackage := protoImportPath(svc)

	// Group by entity
	entityMap := make(map[string]*CRUDTestEntityData)
	var entityOrder []string

	// Build all CRUDMethodTemplateData
	var allMethods []CRUDMethodTemplateData

	for _, cm := range crudMethods {
		// Same per-method facts the handler builder derives, with the ONE
		// difference: advisory (non-strict) filter classification. The test
		// scaffold never dereferences filter facts and must not abort the
		// generate on a bespoke list field, so classifyEntityFilterField's
		// fatal phantom-column check is off here — crudMethodFacts therefore
		// never returns an error in this path. The shape gate, pagination,
		// update-entity-field, and AIP-134 mask facts all stay in lockstep
		// with the handler because they come from the same function.
		mtd, _ := crudMethodFacts(svc, cm, false)
		allMethods = append(allMethods, mtd)

		ent, ok := entityMap[cm.Entity.Name]
		if !ok {
			// Build proto service message field set for this entity
			protoFieldSet := make(map[string]bool)
			if svc.Messages != nil {
				if msgFields, ok := svc.Messages[cm.Entity.Name]; ok {
					for _, mf := range msgFields {
						protoFieldSet[mf.Name] = true
					}
				}
			}

			// Build entity fields (for update test): fields in both DB entity AND proto service message, minus PK
			var fields []CRUDTestFieldData
			protoNameByGoName := make(map[string]string)
			optionalByGoName := make(map[string]bool)
			readOnlyByGoName := make(map[string]bool)
			secretByGoName := make(map[string]bool)
			for _, f := range cm.Entity.Fields {
				if f.Name == cm.Entity.PkField {
					continue
				}
				if len(protoFieldSet) > 0 && !protoFieldSet[f.Name] {
					continue
				}
				kind := DetermineFieldKind(f.ProtoType, f.GoType)
				fields = append(fields, CRUDTestFieldData{
					ProtoName: f.GoName,
					GoType:    f.GoType,
					Kind:      kind,
					TestValue: testValueForType(f.GoType),
				})
				protoNameByGoName[f.GoName] = f.Name
				optionalByGoName[f.GoName] = f.Optional
				readOnlyByGoName[f.GoName] = f.ReadOnly
				secretByGoName[f.GoName] = f.Secret
			}

			// Determine update entity field name from UpdateRequest message
			updateEntityField := updateEntityGoField(svc, "Update"+cm.Entity.Name+"Request", cm.Entity.Name)

			mutable, second := "", ""
			for _, f := range fields {
				if f.GoType != "string" {
					continue
				}
				// Explicit-presence (`optional`) fields are *string on the
				// wire struct — the lifecycle test's plain-string mutation
				// (`row.X = "lifecycle-updated"`) would not compile. The
				// mutation targets are PLAIN string fields only.
				if optionalByGoName[f.ProtoName] {
					continue
				}
				// A column under a schema constraint — a foreign key, or
				// any CHECK (enum vocabulary, email regex, char_length,
				// ...) — rejects the arbitrary "lifecycle-updated" /
				// "MUST-NOT-PERSIST" mutation literals. Constrained
				// columns are create-fixture territory, not mutation
				// targets.
				if fix.columnWriteConstrained(cm.Entity, protoNameByGoName[f.ProtoName]) {
					continue
				}
				// `// forge:read-only` fields are not client-settable. The born
				// test must NOT set one (as the mutated field OR the masked
				// clobber-proof value), so skip it here; the update lifecycle
				// test then never references it.
				if readOnlyByGoName[f.ProtoName] {
					continue
				}
				// `// forge:secret` fields are stripped from read responses and
				// preserved on a maskless full-replace Update (they change only
				// via Create or a mask that names them). So a secret field is
				// useless as the born test's mutation target (the re-read can't
				// observe the change) OR its clobber-proof value (a stripped
				// read is always ""), and picking it as the maskless-mutated
				// field would make that assertion fail. Skip it, exactly like
				// read-only.
				if secretByGoName[f.ProtoName] {
					continue
				}
				if mutable == "" {
					mutable = f.ProtoName
					continue
				}
				second = f.ProtoName
				break
			}

			// The lifecycle test asserts create sets created_at by reading
			// the WIRE getter first.Msg.Get<Entity>().GetCreatedAt(). That
			// getter only exists when the entity's proto MESSAGE declares a
			// created_at field — which is independent of whether the TABLE
			// has the column. A born-from-proto entity gets a created_at
			// COLUMN in its minimal migration (so entity.Timestamps is true
			// off the schema), but its proto message often has no created_at
			// field, so pb.<Entity> has no GetCreatedAt() and the assertion
			// would not compile. Gate the wire assertion on the wire message
			// actually exposing the field.
			protoHasCreatedAt := false
			for _, f := range cm.Entity.Fields {
				if f.Name == "created_at" {
					protoHasCreatedAt = true
					break
				}
			}

			ent = &CRUDTestEntityData{
				EntityName:             cm.Entity.Name,
				EntityLower:            strings.ToLower(cm.Entity.Name),
				PkField:                naming.GoCamelCase(cm.Entity.PkField),
				PkGoType:               cm.Entity.PkGoType,
				HasTimestamps:          cm.Entity.Timestamps && protoHasCreatedAt,
				MutableStringField:     mutable,
				MutableStringFieldPath: protoNameByGoName[mutable],
				SecondStringField:      second,
				SecondStringFieldPath:  protoNameByGoName[second],
				Fields:                 fields,
				UpdateEntityField:      updateEntityField,
			}
			entityMap[cm.Entity.Name] = ent
			entityOrder = append(entityOrder, cm.Entity.Name)
		}

		switch cm.Operation {
		case "create":
			ent.HasCreate = true
			ent.CreateMethod = mtd
		case "get":
			ent.HasGet = true
			ent.GetMethod = mtd
		case "list":
			// Only a Tier-1 list shape gets a lifecycle assertion. When the
			// shape matcher routed this RPC to the custom read path, the
			// response carries no repeated <Entity> field — that absence IS
			// the classification — so `Get<Entity>s()` names a method the
			// wire type does not have and the scaffold does not compile.
			//
			// Deriving the real accessor instead would be worse, not better:
			// the wired custom body runs the query and then leaves the
			// projection as an explicit TODO, returning an EMPTY response. A
			// row-count assertion against it would compile and then fail on
			// the author's first `go test`, inside a scaffold-once file they
			// were told not to rewrite. Forge cannot assert a result it
			// deliberately declined to populate.
			if !mtd.ShapeMismatch {
				ent.HasList = true
				ent.ListMethod = mtd
			}
		case "update":
			ent.HasUpdate = true
			ent.UpdateMethod = mtd
		case "delete":
			ent.HasDelete = true
			ent.DeleteMethod = mtd
		}
	}

	// Compute HasAllCRUD and build ordered slice
	var entities []CRUDTestEntityData
	for _, name := range entityOrder {
		ent := entityMap[name]
		ent.HasAllCRUD = ent.HasCreate && ent.HasGet && ent.HasList && ent.HasUpdate && ent.HasDelete
		entities = append(entities, *ent)
	}

	// fieldmaskpb is imported only when some lifecycle test will actually
	// emit a masked-update block — mirror the template's emission gates
	// exactly or the generated file has an unused import.
	needsFieldMask := false
	for _, e := range entities {
		if e.HasCreate && e.HasGet && e.PkGoType == "string" &&
			e.HasUpdate && e.MutableStringField != "" &&
			e.UpdateMethod.UpdateMaskField != "" {
			needsFieldMask = true
			break
		}
	}

	return CRUDTestTemplateData{
		Package:        pkg,
		Module:         modulePath,
		ProtoPackage:   protoPackage,
		Entities:       entities,
		CRUDMethods:    allMethods,
		NeedsFieldMask: needsFieldMask,
		TestHelperName: ComputeTestHelperName(pkg, projectDir),
	}
}

// testValueForType returns a Go literal suitable for test data based on the Go type.
func testValueForType(goType string) string {
	switch goType {
	case "string":
		return `"test-value"`
	case "int32":
		return "1"
	case "int64":
		return "1"
	case "uint32":
		return "1"
	case "uint64":
		return "1"
	case "float32":
		return "1.0"
	case "float64":
		return "1.0"
	case "bool":
		return "true"
	case "[]byte":
		return `[]byte("test")`
	case "timestamppb.Timestamp", "*timestamppb.Timestamp":
		return "timestamppb.Now()"
	// Wrapper types (google.protobuf.*Value)
	case "*string":
		return `wrapperspb.String("test-value")`
	case "*int32":
		return "wrapperspb.Int32(42)"
	case "*int64":
		return "wrapperspb.Int64(42)"
	case "*uint32":
		return "wrapperspb.UInt32(42)"
	case "*uint64":
		return "wrapperspb.UInt64(42)"
	case "*bool":
		return "wrapperspb.Bool(true)"
	case "*float32":
		return "wrapperspb.Float(1.0)"
	case "*float64":
		return "wrapperspb.Double(1.0)"
	default:
		// Enum types (single-word Go ident like "Status") → use zero value
		// Repeated/map/message types → nil
		if strings.HasPrefix(goType, "[]") || strings.HasPrefix(goType, "map[") || strings.HasPrefix(goType, "*") {
			return "nil"
		}
		// Likely an enum type — use 0
		return "0"
	}
}

// testValueForType2 is testValueForType for the lifecycle test's SECOND
// create. The two rows must not be identical: the born test is
// scaffold-once, the schema is not, and the first UNIQUE index the author
// adds — which forge's own commented migration suggestions recommend —
// would otherwise fail create #2 in a file they were told not to rewrite.
// Types with no second value (a lone enum sentinel, nil) repeat the first;
// nothing can be invented there.
func testValueForType2(goType string) string {
	switch goType {
	case "string":
		return `"test-value-2"`
	case "int32", "int64", "uint32", "uint64":
		return "2"
	case "float32", "float64":
		return "2.0"
	case "bool":
		return "false"
	case "[]byte":
		return `[]byte("test-2")`
	case "*string":
		return `wrapperspb.String("test-value-2")`
	case "*int32":
		return "wrapperspb.Int32(43)"
	case "*int64":
		return "wrapperspb.Int64(43)"
	case "*uint32":
		return "wrapperspb.UInt32(43)"
	case "*uint64":
		return "wrapperspb.UInt64(43)"
	case "*bool":
		return "wrapperspb.Bool(false)"
	case "*float32":
		return "wrapperspb.Float(2.0)"
	case "*float64":
		return "wrapperspb.Double(2.0)"
	default:
		return testValueForType(goType)
	}
}

// classifySkipField returns true if the field should be skipped during filter classification.
// Pagination fields, ordering companions, and order_by itself are not filters.
func classifySkipField(name string) bool {
	switch name {
	case "page_size", "page_token", "descending", "desc", "sort_order":
		return true
	}
	return false
}

// classifyEntityFilterField builds a FilterFieldData validated against the
// entity's DECLARED columns:
//
//   - search-style fields (search/query/q) span the entity's string
//     columns (minus the PK) via orm.WhereILikeAny — they never map to a
//     column of their own;
//   - every other filter field must name a declared column, or the
//     generate fails loudly. Shipping a filter on a phantom column is the
//     review-confirmed silence bug: the query either errors at runtime or
//     (SQLite double-quoted fallback) silently matches nothing.
func classifyEntityFilterField(mf MessageFieldDef, entity EntityDef) (FilterFieldData, error) {
	ff := classifyFilterField(mf)
	if ff.FilterType == "search" {
		cols := entityStringColumns(entity)
		if len(cols) == 0 {
			return ff, fmt.Errorf(
				"list filter %q: entity %s has no non-PK string columns to search (declared columns: %s); drop the filter or implement the RPC by hand",
				mf.Name, entity.Name, entityColumnList(entity))
		}
		ff.SearchColumns = cols
		return ff, nil
	}
	for _, c := range entity.Columns {
		if c.Name == mf.Name {
			return ff, nil
		}
	}
	return ff, fmt.Errorf(
		"list filter field %q has no matching column on entity %s in the applied schema (columns: %s); rename the request field to a real column (or add the column via a migration), or implement the RPC by hand — a method, in any file of the handler package, that does not call its generated crud<Rpc>Op",
		mf.Name, entity.Name, entityColumnList(entity))
}

// buildCustomFilters maps a custom-read-shape RPC's request fields onto
// entity columns BEST-EFFORT, for seeding the wired scaffold body's
// []orm.QueryOption skeleton. Unlike classifyEntityFilterField (which
// fails the generate on an unmappable field — the strict Tier-1 list
// path), this is advisory: a field that doesn't name a declared column
// (or isn't a search field) is silently skipped, so the scaffold always
// compiles regardless of how bespoke the request is. Pagination/cursor/
// ordering control fields are skipped via classifySkipField. The result
// is a starting point the user refines, not a contract.
func buildCustomFilters(svc ServiceDef, cm CRUDMethod) []FilterFieldData {
	if svc.Messages == nil {
		return nil
	}
	msgFields, ok := svc.Messages[cm.Method.InputType]
	if !ok {
		return nil
	}
	var out []FilterFieldData
	for _, mf := range msgFields {
		if classifySkipField(mf.Name) || mf.Name == "order_by" {
			continue
		}
		// Only scalar fields make sense as WhereEq/ILike predicates;
		// message/enum/repeated fields are the user's to interpret.
		switch mf.ProtoType {
		case "message", "enum", "":
			continue
		}
		ff := classifyFilterField(mf)
		ff.GoName = messageFieldGoName(msgFields, mf.Name)
		if ff.FilterType == "search" {
			cols := entityStringColumns(cm.Entity)
			if len(cols) == 0 {
				continue // nothing to search; let the user wire it
			}
			ff.SearchColumns = cols
			out = append(out, ff)
			continue
		}
		// exact: keep only when the field names a real column.
		for _, c := range cm.Entity.Columns {
			if c.Name == mf.Name {
				out = append(out, ff)
				break
			}
		}
	}
	return out
}

// entityStringColumns returns the columns a "search" filter spans: the
// entity's text columns by convention (SearchColumns is populated from
// the introspected schema — see schemadef.DetectConventions).
func entityStringColumns(entity EntityDef) []string {
	return entity.SearchColumns
}

// entityColumnType is the canonical type of the entity's column named name,
// or "" when it declares none.
func entityColumnType(entity EntityDef, name string) string {
	for _, c := range entity.Columns {
		if c.Name == name {
			return c.Type
		}
	}
	return ""
}

// entityColumnList renders the applied schema's column names for diagnostics.
func entityColumnList(entity EntityDef) string {
	names := make([]string, 0, len(entity.Columns))
	for _, c := range entity.Columns {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

// classifyFilterField builds a FilterFieldData from a proto message field.
func classifyFilterField(mf MessageFieldDef) FilterFieldData {
	goType := ProtoTypeToGoType(mf.ProtoType)
	// Enum filter fields bind their VALUE NAME, never a Go scalar —
	// FieldType "enum" keeps them out of the scalar template branches
	// (the "string" fallback used to emit a `text = integer` bind).
	isEnum := mf.ProtoType == "enum"
	if isEnum {
		goType = "enum"
	}

	filterType := "exact"
	switch mf.Name {
	case "search", "query", "q":
		filterType = "search"
	}

	return FilterFieldData{
		ProtoName: mf.Name,
		// Exact unless the field collides with a sibling or a generated
		// method; callers holding the whole request message correct it
		// with messageFieldGoName.
		GoName:     naming.GoCamelCase(mf.Name),
		ColumnName: mf.Name,
		FieldType:  goType,
		FilterType: filterType,
		IsOptional: mf.IsOptional,
		IsEnum:     isEnum,
	}
}

// ensureDepsDBField checks the service.go Deps struct for a DB field and adds
// one if missing. The generated CRUD ops reference s.deps.DB, so it MUST be
// present for the project to compile.
//
// This runs ONLY when forge is about to wire generated CRUD ops — the caller
// (GenerateCRUDHandlers) invokes it only after the dedup pass leaves at least
// one CRUD method for forge to emit. Every such method's op body dereferences
// s.deps.DB (db.Create<E>(ctx, s.deps.DB, ...) etc.), so the DB field is a
// hard requirement, not an optional convenience. Injecting it is the field's
// declared extension point ("Add your dependencies here"), not a stomp.
//
// Historically this was gated on the ABSENCE of a sibling handlers.go, on the
// theory that a hand-written handlers.go meant "I manage Deps myself, keep out
// of service.go" (fr 3d7c1711, kalshi-trader). That gate is gone: `forge
// project new` now SHIPS a handlers.go with the example entity's stubs, so the
// gate fired on every fresh project and left the required DB field uninjected
// — the moment a real CRUD entity was added, `go build` broke on an undefined
// s.deps.DB with no actionable fix. The genuine opt-out (a user who owns all
// their handlers) is already served by the dedup: if the user implements a
// CRUD RPC by hand, ScanExistingMethods filters it out and forge emits no op
// for it, so ensureDepsDBField is never reached for a service the user fully
// owns. Whenever we DO reach here, forge is emitting an s.deps.DB reference and
// the field is genuinely needed.
//
// The injection stays idempotent and non-destructive: it no-ops when the
// canonical `DB orm.Context` field is already present, warns (never overwrites)
// on a foreign DB field, and only ever adds — see the guards below.
func ensureDepsDBField(serviceDir string) error {
	servicePath := filepath.Join(serviceDir, "service.go")
	data, err := os.ReadFile(servicePath)
	if err != nil {
		return err
	}

	content := string(data)
	original := content

	// The generated CRUD REQUIRES the Deps.DB field to be typed exactly
	// `orm.Context`. This is load-bearing in two places that must never
	// silently diverge:
	//   - the ops call `db.Create<E>(ctx, s.deps.DB, ...)`, whose db-func
	//     parameter is the orm.Context interface; AND
	//   - the CRUD test harness — DetectDepsDBField keys the WithDB /
	//     NewMigratedTestDB emission on an `orm.Context` field, and the
	//     generated testing.go assigns `deps.DB = cfg.db` (an orm.Context).
	// A `DB *orm.Client` field compiles the ops but SILENTLY suppresses the
	// test helpers (`undefined: app.WithDB`) and breaks the deps.DB
	// assignment. Rather than accept orm.Client as an equivalent (the old
	// hidden switch), the requirement is made EXPLICIT: detect the canonical
	// orm.Context field via the SAME AST predicate the test-helper trigger
	// uses; if a DB field of another type is present, say so LOUDLY; otherwise
	// inject the canonical field.
	hasORMContext, _ := DetectDepsDBField(serviceDir)
	if !hasORMContext && depsDeclaresForeignDBField(content) {
		fmt.Printf("⚠️  %s declares a Deps.DB field that is not orm.Context — the generated CRUD handlers and their tests require `DB orm.Context` (the ORM client satisfies it). Retype the field to enable WithDB / NewMigratedTestDB.\n", servicePath)
	}

	// If the Deps struct doesn't have the canonical orm.Context DB field yet
	// (and no foreign DB field is blocking a clean inject), add it.
	injectedDBField := false
	if !hasORMContext && !depsDeclaresForeignDBField(content) {
		injectedDBField = true
		// Find the Deps struct and inject the DB field after the opening line.
		marker := "// Add your dependencies here."
		if !strings.Contains(content, marker) {
			// Try to find the Deps struct opening brace
			marker = "type Deps struct {"
			idx := strings.Index(content, marker)
			if idx < 0 {
				return nil // Can't find Deps struct, skip
			}
			// Insert after the opening brace line
			newlineIdx := strings.Index(content[idx:], "\n")
			if newlineIdx < 0 {
				return nil
			}
			insertPos := idx + newlineIdx + 1
			dbField := "\tDB orm.Context\n"
			content = content[:insertPos] + dbField + content[insertPos:]
		} else {
			// Insert the DB field before the marker comment
			content = strings.Replace(content, marker, "DB orm.Context\n\t"+marker, 1)
		}

		// Ensure the orm import is present
		if !strings.Contains(content, "\"github.com/reliant-labs/forge/pkg/orm\"") {
			// Find the import block and add the orm import
			importIdx := strings.Index(content, "import (")
			if importIdx >= 0 {
				// Find the closing paren of the import block
				closingIdx := strings.Index(content[importIdx:], ")")
				if closingIdx >= 0 {
					insertPos := importIdx + closingIdx
					content = content[:insertPos] + "\n\t\"github.com/reliant-labs/forge/pkg/orm\"\n" + content[insertPos:]
				}
			}
		}
	}

	// The DB field is REQUIRED for the generated CRUD handlers — gate it
	// in validateDeps so a missing database fails at boot with an
	// actionable error, not as a nil-interface panic on the first RPC.
	// (wire_gen passes a true nil interface via app.ORMContext(), so this
	// check actually fires.) Inject at the scaffold's extension marker;
	// services whose validateDeps was rewritten by hand are left alone —
	// the marker is the opt-in surface.
	content = injectValidateDepsDBCheck(content)

	if content == original {
		return nil
	}
	if injectedDBField {
		// Not a silent stomp: the generated CRUD ops just started
		// dereferencing s.deps.DB, so forge added the field they need at the
		// Deps struct's declared extension point. Say so.
		fmt.Printf("  ✅ Added `DB orm.Context` to Deps in %s (required by the generated CRUD ops)\n", servicePath)
	}
	return writeUserScaffold(servicePath, []byte(content))
}

// depsDeclaresForeignDBField reports whether service.go already carries a
// database-ish Deps field of a type OTHER than orm.Context — the two shapes
// the generated CRUD cannot use (`*orm.Client`, `*sql.DB`/`sql.DB`). It exists
// so ensureDepsDBField can make the orm.Context requirement explicit: when
// this is true and the canonical orm.Context field is absent, forge warns
// rather than silently injecting a duplicate `DB` field (a compile error) or
// silently tolerating a type that disables the CRUD test helpers. Detection is
// text-based, matching ensureDepsDBField's own minimal-mutation style; the
// caller already used the AST predicate (DetectDepsDBField) for the positive
// orm.Context case.
func depsDeclaresForeignDBField(content string) bool {
	return strings.Contains(content, "orm.Client") ||
		strings.Contains(content, "*sql.DB") ||
		strings.Contains(content, "sql.DB")
}

// injectValidateDepsDBCheck inserts `if d.DB == nil { ... }` into the
// scaffolded validateDeps() ahead of the "Add checks" marker comment.
// No-ops when the check (any `d.DB == nil` mention) already exists or
// the marker is gone (user-rewritten validateDeps).
func injectValidateDepsDBCheck(content string) string {
	if strings.Contains(content, "d.DB == nil") {
		return content
	}
	const marker = "// Add checks for your required Deps fields here."
	idx := strings.Index(content, marker)
	if idx < 0 {
		return content
	}
	check := "if d.DB == nil {\n" +
		"\t\treturn fmt.Errorf(\"Deps.DB is required by the generated CRUD handlers — set DATABASE_URL (the generated bootstrap constructs the ORM client from it)\")\n" +
		"\t}\n\t"
	return content[:idx] + check + content[idx:]
}

// protoTypeMatchesEntity checks if a proto field type references the given entity.
// Handles both bare types ("Patient") and qualified types ("db.v1.Patient").
func protoTypeMatchesEntity(protoType, entityName string) bool {
	return protoType == entityName || protoType == "db.v1."+entityName
}

// fieldMatchesEntity reports whether a message field references the given
// entity type. The modern descriptor carries the referenced message name
// in MessageType ("Item", or fully-qualified "services.api.v1.Item");
// ProtoType alone is the literal "message" for every message field, which
// is unmatchable — relying on it produced a false custom-read-shape stub
// (then spelled FORGE_CRUD_SHAPE_MISMATCH) on forge's own scaffold (the F2 bug).
func fieldMatchesEntity(f MessageFieldDef, entityName string) bool {
	if f.MessageType != "" {
		if f.MessageType == entityName || strings.HasSuffix(f.MessageType, "."+entityName) {
			return true
		}
	}
	return protoTypeMatchesEntity(f.ProtoType, entityName)
}

// updateMaskGoField returns the Go field name of the AIP-134
// google.protobuf.FieldMask field on the given request message ("" when
// the message declares none, or when no field data is available). The
// modern descriptor carries the referenced message name in MessageType;
// the suffix match also accepts a bare "FieldMask" from older descriptors.
func updateMaskGoField(svc ServiceDef, inputType string) string {
	if svc.Messages == nil {
		return ""
	}
	fields := svc.Messages[inputType]
	for _, f := range fields {
		if f.MessageType == "google.protobuf.FieldMask" || strings.HasSuffix(f.MessageType, ".FieldMask") || f.MessageType == "FieldMask" {
			return messageFieldGoName(fields, f.Name)
		}
	}
	return ""
}

// updateEntityGoField is the pb Go field of update request inputType that
// carries the entity (`Project project = 1;` → Project). When the
// descriptor does not carry the request's fields, or none of them is typed
// as the entity, it falls back to the field the entity scaffolder writes
// (naming.EntityFieldName), camel-cased by protoc-gen-go's rule.
func updateEntityGoField(svc ServiceDef, inputType, entityName string) string {
	fields := svc.Messages[inputType]
	for _, f := range fields {
		if fieldMatchesEntity(f, entityName) {
			return messageFieldGoName(fields, f.Name)
		}
	}
	return naming.GoCamelCase(naming.EntityFieldName(entityName))
}

// responseEntityGoField is the pb Go field of response message outputType
// that carries the entity: the field named the way the entity scaffolder
// names it (naming.EntityFieldName, or EntityListFieldName for a list),
// else the first field typed as the entity. The scaffolded name, camel-cased
// by protoc-gen-go's rule, is the fallback when the descriptor does not carry
// the response's fields.
//
// It reads the field rather than restating the entity name because the two
// are spelled by different rules: entity "LLMKey" is carried in field
// llm_key, which protoc-gen-go names LlmKey — `LLMKey: m` packs a field the
// response does not have.
func responseEntityGoField(svc ServiceDef, outputType, entityName string, list bool) string {
	want := naming.EntityFieldName(entityName)
	if list {
		want = naming.EntityListFieldName(entityName)
	}
	fields := svc.Messages[outputType]
	for _, f := range fields {
		if f.Name == want {
			return messageFieldGoName(fields, f.Name)
		}
	}
	for _, f := range fields {
		if fieldMatchesEntity(f, entityName) {
			return messageFieldGoName(fields, f.Name)
		}
	}
	return naming.GoCamelCase(want)
}

// describeFields renders an observed-field list ("name type, ...") for
// shape-mismatch diagnostics, so a wrong matcher self-incriminates: the
// reason shows exactly what the matcher saw, not just what it wanted.
func describeFields(fields []MessageFieldDef) string {
	if len(fields) == 0 {
		return "(no fields)"
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		t := f.ProtoType
		if f.MessageType != "" {
			t = f.MessageType
		}
		parts = append(parts, f.Name+" "+t)
	}
	return strings.Join(parts, ", ")
}
