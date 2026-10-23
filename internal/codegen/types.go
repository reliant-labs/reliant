package codegen

import (
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/naming"
)

// ServiceDef represents a parsed Connect RPC service definition.
type ServiceDef struct {
	Name       string // "EchoService"
	Package    string // "echo.v1"
	GoPackage  string // "github.com/.../gen/proto/echo/v1"
	PkgName    string // "echov1"
	Methods    []Method
	ProtoFile  string
	ModulePath string                       // e.g., "github.com/demo-project"
	Messages   map[string][]MessageFieldDef // message name → fields (e.g., "ListPatientsRequest" → [...])

	// Audience is the trust domain the service serves, from
	// `option (forge.v1.service) = {audience: "..."}`: "user" when the
	// service declares none. A serverkit.Listener mounts the service only
	// on a chain whose Authenticator admits this audience.
	Audience string `json:",omitempty"`

	// Schemas is the deep type graph for full JSON-Schema emission:
	// fully-qualified message name → fields, covering
	// every message transitively reachable from any method's input or
	// output type. Well-known types (google.protobuf.*) are NOT
	// included — consumers map those to fixed JSON encodings matching
	// protojson. Empty/nil on descriptors produced by forge versions
	// before this field existed; consumers must fall back to the
	// shallow Messages map in that case. Keyed by fully-qualified name
	// (e.g. "shop.v1.Address") so cross-package short-name collisions
	// can't alias two different messages.
	Schemas map[string][]SchemaFieldDef `json:",omitempty"`

	// SchemaFiles maps a fully-qualified message name (the same keys used
	// in Schemas) to the proto file that physically DECLARES that message.
	// For a message declared in the service's own proto file this equals
	// ProtoFile; for a message pulled in from another file (e.g. a shared
	// `shared.proto` holding the domain entity messages while the CRUD
	// service lives in `services/<svc>/v1/<svc>.proto`) it differs. Codegen
	// that emits a `*Schema` / type import for a message must resolve the
	// import path from the message's DEFINING file, not the service file —
	// otherwise it imports a symbol from a `_pb.ts` module that doesn't
	// export it. Empty/nil on descriptors produced by forge versions before
	// this field existed; consumers must fall back to the service ProtoFile.
	SchemaFiles map[string]string `json:",omitempty"`

	// Enums maps fully-qualified enum name → declared value names, in
	// proto declaration order, for every enum reachable through
	// Schemas. protojson encodes enums as their value-name strings, so
	// this is exactly the "enum" list a JSON Schema needs.
	Enums map[string][]string `json:",omitempty"`

	// EnumNumbers maps the same fully-qualified enum names to each value's
	// DECLARED wire number, parallel to Enums (EnumNumbers[fq][i] is the
	// number of Enums[fq][i]). Declaration order is not the wire number once
	// an enum reserves a removed value (`reserved 1;`), and protobuf-es types
	// an enum field as its declared numbers, so anything that emits an enum
	// as a number — the frontend mock fixtures — must read it from here.
	// Absent on descriptors written before this field existed; readers then
	// fall back to declaration order.
	EnumNumbers map[string][]int32 `json:",omitempty"`

	// EnumGoRefs maps the same fully-qualified enum names to where
	// protoc-gen-go put the enum: the Go import path of its declaring
	// file's go_package and its Go identifier. The proto package says
	// nothing about this — an enum in a sibling file with another
	// go_package shares the proto package but not the Go package, and its
	// value constants are not reachable through the service's `pb` import.
	// Absent on older descriptors; readers then fall back to the proto
	// package rule (enumWireGoName).
	EnumGoRefs map[string]EnumGoRef `json:",omitempty"`

	// Unmanaged reports a `// forge:unmanaged` marker above the service
	// declaration (ProtoMarkerUnmanaged): its handlers live outside forge's
	// model, so nothing service-scoped is generated for it.
	// ParseServicesFromProtos leaves such services out; ParseDeclaredServices
	// keeps them, for the consumers that project the wire contract itself.
	Unmanaged bool `json:",omitempty"`
}

// EnumGoRef locates a generated Go enum type.
type EnumGoRef struct {
	ImportPath string // e.g. "github.com/acme/app/gen/controlplane/v1"
	GoName     string // protoc-gen-go identifier, e.g. "DaemonType" or "Order_Status"
}

// Method represents a single RPC method.
type Method struct {
	Name            string
	InputType       string
	OutputType      string
	ClientStreaming bool
	ServerStreaming bool
	// AuthRequired is (forge.v1.method).auth_required: true unless the
	// proto explicitly opts out, since the field is `optional bool` and
	// an unannotated RPC is taken to want a principal.
	//
	// It is INFORMATIONAL and FAILS OPEN. Nothing consults it at
	// runtime: it feeds `forge project map`/`graph`,
	// and it selects which sentence the scaffolded handler comment shows
	// the author. An RPC declaring auth_required: true and doing nothing
	// about it serves anonymous callers, which is what a measured run
	// found in 17 of 20 generated CRUD RPCs. Making it fail closed is an
	// interceptor decision, not a codegen one.
	AuthRequired bool
	// Timeout is (forge.v1.method).timeout: the rpc's server-side deadline,
	// projected into pkg/middleware/procedures_gen.go's ProcedureDeadlines
	// for the chain's Deadline slot. Zero (unset) is the slot's default;
	// negative is no server deadline.
	Timeout time.Duration `json:",omitempty"`
	// Errors records the Connect/gRPC error codes this method may return,
	// derived from (forge.v1.method).errors. Values match connect.Code
	// names (e.g. "NotFound", "PermissionDenied"). Surfaced through
	// generated code so handler authors see the typed error contract at
	// a glance. Informational at runtime — no enforcement (yet).
	Errors []string
	// Custom reports a `// forge:custom` marker on the rpc
	// (ProtoMarkerCustom): its semantics are the author's, so it is a custom
	// rpc whatever its name — CRUDShape answers "" for it, and nothing CRUD
	// is projected from it. Absent on descriptors written before the marker.
	Custom bool `json:",omitempty"`
	// InputTypeFQ / OutputTypeFQ are the fully-qualified names of the
	// request/response messages (e.g. "shop.v1.CreateOrderRequest",
	// "google.protobuf.Empty"). They key into ServiceDef.Schemas for
	// deep JSON-Schema emission. Empty on descriptors produced by
	// older forge versions — consumers fall back to the short-name
	// InputType/OutputType + Messages map.
	InputTypeFQ  string `json:",omitempty"`
	OutputTypeFQ string `json:",omitempty"`
	// InputProtoFile / OutputProtoFile record the proto file path that
	// physically declares the input/output message. For RPCs whose
	// request/response live in the same proto file as the service these
	// equal ServiceDef.ProtoFile, but they differ when an RPC references
	// a message from another file (e.g. services/users/v1/users.proto's
	// ListUsers returns shared/v1/types.proto's Page). The frontend hooks
	// generator groups imports by these paths so each cross-file message
	// is imported from its declaring _pb.ts file rather than silently
	// referenced as an unresolved identifier.
	InputProtoFile  string
	OutputProtoFile string
	// Activity is the rpc's activity declaration as forge/pkg/activity
	// resolves it from the compiled descriptor — the same Resolve the
	// runtime interceptor calls, so forgeconv-activity-declared judges
	// exactly what the interceptor will do. Nil on descriptors written
	// before forge recorded it; the lint asks for a regenerate then.
	Activity *MethodActivity `json:",omitempty"`
}

// MethodActivity is one rpc's resolved activity declaration
// (forge/pkg/activity.Resolve / IsMutating), recorded at generate time.
type MethodActivity struct {
	// Mutating reports an rpc that must declare its activity (or none):
	// not named with a read verb and not marked NO_SIDE_EFFECTS.
	Mutating bool
	// Declared reports any declaration: explicit, implied by CRUD shape,
	// or none.
	Declared bool `json:",omitempty"`
	// None is the reason a `none` declaration gives.
	None string `json:",omitempty"`
	// Verb and TargetKind are the declared event's, empty for none.
	Verb       string `json:",omitempty"`
	TargetKind string `json:",omitempty"`
	// Implied reports a declaration derived from forge's CRUD shape.
	Implied bool `json:",omitempty"`
	// Writer is INTERCEPTOR or HANDLER for a declared event.
	Writer string `json:",omitempty"`
	// Error is Resolve's error for a malformed declaration.
	Error string `json:",omitempty"`
}

// MessageFieldDef represents a single field in a proto message definition.
type MessageFieldDef struct {
	Name       string // proto field name: "page_size", "search", "active"
	ProtoType  string // "int32", "string", "bool"
	IsOptional bool   // true if the field has the "optional" label
	// MessageType carries the referenced message's name for message-typed
	// fields (e.g. "Item" for `Item item = 1;`, "google.protobuf.FieldMask"
	// for masks). ProtoType collapses every message field to the literal
	// "message" — which is how the CRUD shape matcher could never match an
	// update request's entity field against the entity name (the false
	// custom-read-shape stub — then spelled FORGE_CRUD_SHAPE_MISMATCH —
	// on forge's own scaffold). Additive:
	// `json:",omitempty"` keeps old descriptors parseable.
	MessageType string `json:",omitempty"`
}

// SchemaFieldDef is the deep-schema sibling of MessageFieldDef: one
// field of a message in ServiceDef.Schemas, carrying enough type
// information to project a full (nested) JSON Schema without consulting
// the proto source. Unlike MessageFieldDef.ProtoType (which collapses
// messages/enums/maps into opaque strings), this keeps the type graph:
// message and enum fields name their fully-qualified target so a schema
// emitter can $ref into a shared definitions block.
type SchemaFieldDef struct {
	Name string `json:"name"` // proto field name, snake_case ("page_size")
	// Kind is the proto scalar kind name ("string", "int32", "bool",
	// "bytes", ...) or one of the structured markers "message", "enum",
	// "map".
	Kind string `json:"kind"`
	// TypeName is the fully-qualified message/enum name when Kind is
	// "message" or "enum" (e.g. "shop.v1.Address",
	// "google.protobuf.Timestamp"). Empty for scalars and maps.
	TypeName string `json:"type_name,omitempty"`
	// Repeated marks proto `repeated` fields (JSON arrays). Always
	// false for maps — proto maps are repeated entry messages under
	// the hood, but their JSON encoding is an object, not an array.
	Repeated bool `json:"repeated,omitempty"`
	// Optional is true when the field carries the explicit `optional`
	// label (proto3 field presence). Optional fields stay out of JSON
	// Schema `required` lists.
	Optional bool `json:"optional,omitempty"`
	// Oneof is the containing oneof group's name for members of a
	// real (non-synthetic) oneof. proto3 `optional` fields use a
	// synthetic oneof internally; those report "" here and Optional
	// true instead.
	Oneof string `json:"oneof,omitempty"`
	// Map-typed fields (Kind == "map") record the key/value kinds.
	// MapValueTypeName names the fully-qualified message/enum when the
	// value kind is "message"/"enum".
	MapKeyKind       string `json:"map_key_kind,omitempty"`
	MapValueKind     string `json:"map_value_kind,omitempty"`
	MapValueTypeName string `json:"map_value_type_name,omitempty"`
	// Validate carries the field's protovalidate (buf.validate.field)
	// rules, projected to a DB CHECK at entity birth and a zod validator
	// in the generated form (the wire is enforced by the interceptor).
	// nil when the field carries no rules. Populated by both extractors —
	// the compiled descriptor and the raw proto scan.
	Validate *FieldConstraints `json:"validate,omitempty"`
	// ValidateOptions is the field's `(buf.validate.field)` inline-options
	// block exactly as authored, collapsed to one line
	// (`[(buf.validate.field).string.min_len = 1]`), or "" when the field
	// carries no protovalidate rule. Unlike Validate — the lossy subset
	// forge projects to SQL/zod — this is the whole declaration, so a
	// request message that FLATTENS this field (Create<Entity>Request) can
	// re-declare the SAME rules and have the wire interceptor enforce them.
	// Populated by the raw proto scan, which reads the authored spelling off
	// the file the injection edits. `json:",omitempty"` keeps old
	// descriptors parseable (additive contract).
	ValidateOptions string `json:"validate_options,omitempty"`
	// Secret marks a field carrying the `// forge:secret` leading-comment
	// marker: the column is real (schema truth), but the generated
	// proto←entity conversion (the toProto read path) NEVER packs it, so
	// Get/List responses omit it — while Create/Update stay free to set it.
	// Read off the compiled descriptor's source-code comments (the raw
	// proto scan does not need it — birth stores the column unconditionally).
	// `json:",omitempty"` keeps old descriptors parseable (additive contract).
	Secret bool `json:"secret,omitempty"`
	// ReadOnly marks a field carrying the `// forge:read-only` marker: the
	// INPUT-side mirror of Secret. The column is real and stays on the entity
	// message + Get/List responses (readable), but the field is OMITTED from
	// the generated Create/Update REQUEST messages — a value (status,
	// computed price, lifecycle timestamp) the client must not write. Read
	// off BOTH extractors (the compiled descriptor's source-code comments AND
	// the raw proto scan — a brand-new `// forge:entity` message births its
	// quintet from the raw scan, before the descriptor knows it).
	// `json:",omitempty"` keeps old descriptors parseable (additive contract).
	ReadOnly bool `json:"read_only,omitempty"`
	// Generated is the SQL expression a `// forge:generated <expr>` marker
	// carries, verbatim (see codegen.ProtoMarkerGenerated), or "" when the
	// field has none. Birth emits the column as `GENERATED ALWAYS AS
	// (<expr>) STORED`; the marker also sets ReadOnly, so the field leaves
	// the born write envelopes. Nothing after birth reads it — the applied
	// schema's GENERATED column is the truth from then on.
	//
	// Read off BOTH extractors, like ReadOnly: the raw scan for a
	// brand-new `// forge:entity` message, and the compiled descriptor for
	// `forge scaffold entity --from-proto <svc>.<Message>`, which would
	// otherwise see a read-only field and birth a plain column nothing ever
	// writes. `json:",omitempty"` keeps old descriptors parseable.
	Generated string `json:"generated,omitempty"`
	// Guards are the `<table>.<column>` targets this field declares with
	// `// forge:guards` — the columns whose writes the ENCLOSING RPC owns.
	//
	// Unlike every other flag here, this one is not a statement about the
	// field it sits on. The field is the attachment point; the subject is
	// a column on some entity, often on a different message entirely
	// (RecordPaymentRequest.amount_cents guards
	// invoices.amount_paid_cents). That indirection is the whole reason
	// the marker names its target instead of being inferred — see
	// ProtoMarkerGuards for the pair of columns that makes name-matching
	// unsound.
	//
	// Read off the compiled descriptor's source-code comments. The raw
	// proto scan does not need it: a guard changes no born schema, only
	// which columns the scaffolded edit page is willing to write.
	// `json:",omitempty"` keeps old descriptors parseable (additive
	// contract).
	Guards []string `json:"guards,omitempty"`
}

// IsInputEmpty returns true if the input type is google.protobuf.Empty.
func (m Method) IsInputEmpty() bool {
	return m.InputType == "google.protobuf.Empty"
}

// IsOutputEmpty returns true if the output type is google.protobuf.Empty.
func (m Method) IsOutputEmpty() bool {
	return m.OutputType == "google.protobuf.Empty"
}

// GoName is the Go method protoc-gen-connect-go declares for this RPC on
// the service's Handler interface ("CreateOauth2token" →
// CreateOauth2Token). Name is the proto spelling; anything matched against
// Go source — a handler method, a stub, a mock — must use this.
func (m Method) GoName() string { return naming.GoCamelCase(m.Name) }

// GoInputType returns the Go type reference for the input (handles Empty).
func (m Method) GoInputType() string {
	if m.IsInputEmpty() {
		return "emptypb.Empty"
	}
	return "pb." + naming.GoCamelCase(m.InputType)
}

// GoOutputType returns the Go type reference for the output (handles Empty).
func (m Method) GoOutputType() string {
	if m.IsOutputEmpty() {
		return "emptypb.Empty"
	}
	return "pb." + naming.GoCamelCase(m.OutputType)
}

// GoName is the base protoc-gen-connect-go builds this service's
// identifiers from (naming.ConnectServiceGoName): the handler interface is
// <GoName>Handler, the client <GoName>Client. Name is the proto spelling —
// which is also what the generated `<Name>Name` constant is spelled from
// (naming.ConnectServiceNameConst).
func (s ServiceDef) GoName() string { return naming.ConnectServiceGoName(s.Name) }

// EntityDef is a database entity: the join of an introspected table
// from the APPLIED schema (db/migrations executed against the shadow
// DB — the storage truth) with the service-proto CRUD message shape
// (the wire truth).
//
// Columns drive the ORM and entity structs; Fields drive the frontend
// and the proto<->entity conversion in the CRUD wiring. SoftDelete /
// Timestamps are conventions read off real columns
// (deleted_at, created_at+updated_at) — never annotations.
type EntityDef struct {
	Name      string        // "Patient"
	TableName string        // "patients"
	PkField   string        // "id"
	PkGoType  string        // "string"
	Fields    []EntityField // wire-message fields (service proto)
	ProtoFile string        // proto file declaring the wire message
	// Columns is the introspected applied schema for the entity's table.
	Columns []EntityColumn `json:",omitempty"`
	// SearchColumns are the text columns the generated list search
	// filter matches against (convention: every text column).
	SearchColumns []string `json:",omitempty"`
	SoftDelete    bool     `json:",omitempty"`
	Timestamps    bool     `json:",omitempty"`
	// AppendOnly marks a table declared `forge:append-only` in its catalog
	// comment (COMMENT ON TABLE): an immutable ledger. Every generator that
	// emits a WRITE path must omit the mutating half for this entity —
	// notably the store interface, whose UpdateX/DeleteX otherwise
	// type-check against a table postgres will refuse at runtime.
	AppendOnly bool `json:",omitempty"`
	// ReadOnly marks a VIEW opted into the data layer (`COMMENT ON VIEW v
	// IS 'forge:entity'`): its store reads — List, Count, and Get by its key
	// column when it has one — and writes nothing.
	ReadOnly bool `json:",omitempty"`
	// Constraints are the table's named UNIQUE / CHECK / FOREIGN KEY
	// constraints. They project to per-entity name constants so a service
	// can branch on orm.ConstraintName(err) against something the
	// compiler checks. See EntityConstraintsFromTable for why the primary
	// key is not among them.
	Constraints []EntityConstraint `json:",omitempty"`
}

// EntityConstraint is one named constraint on an entity's table.
type EntityConstraint struct {
	// Name is the identifier postgres reports in a violation's structured
	// constraint field — auto-derived (`jobs_estimate_id_key`) for an
	// inline declaration, explicit for a named one. It is carried verbatim
	// from the catalog: re-deriving postgres's naming rules in Go would be
	// forge grading its own homework against the only authority that
	// matters.
	Name string
	// Kind is "unique", "check" or "foreign_key" — see config.ConstraintKind.
	Kind string
	// Columns are the columns the constraint spans, in key order. Empty for
	// an expression-keyed unique index, whose key cannot be described by
	// bare columns without describing a stricter table than exists.
	Columns []string `json:",omitempty"`
}

// EntityColumn is one introspected column of an entity's table.
type EntityColumn struct {
	Name string // column name, snake_case
	// Type is the canonical type: "string", "int64", "float64",
	// "bool", "time", "json", "bytes" (matches schemadef.CanonicalType).
	Type    string
	IsArray bool
	NotNull bool
	IsPK    bool
	// DeclType is the declared SQL type verbatim ("TIMESTAMPTZ").
	DeclType string `json:",omitempty"`
	Default  string `json:",omitempty"`
	// IsGenerated marks GENERATED ALWAYS AS (...) STORED columns — the DB
	// computes them, so the ORM must never write them (Bun's ,scanonly).
	IsGenerated bool `json:",omitempty"`
	// IsIdentity marks a GENERATED ... AS IDENTITY column (its value comes
	// from an implicit sequence, so Default is empty). With Default it is how
	// the projection knows a key is server-allocated (schemadef.Column.
	// DatabaseSupplied) rather than guessing from the key's type.
	IsIdentity bool `json:",omitempty"`
	// Immutable marks a column declared `forge:immutable` in its catalog
	// comment (COMMENT ON COLUMN). It projects to Bun's `skipupdate`, so a
	// full-replace UPDATE leaves the stored value alone.
	Immutable bool `json:",omitempty"`
	// Version marks a column declared `forge:version` in its catalog
	// comment: the entity opts into optimistic concurrency control. See
	// forge/pkg/crud's meta.versionColumn for the write-path behavior.
	Version bool `json:",omitempty"`
	// FillStrategy carries a `forge:fill=<strategy>` declaration verbatim
	// ("ulid" or "handler"), "" when the column declares none. It names WHO
	// fills a column the wire never carries a value for — "ulid" makes the
	// ORM generate one at Create (see forge/pkg/crud's meta.fillULIDCols);
	// "handler" is pure acknowledgement that suppresses the
	// unsatisfiable-column lint and changes no codegen behavior. See
	// schemadef.ColumnMarkerFill for the full grammar and rationale.
	FillStrategy string `json:",omitempty"`
	// Owner marks a column declared `forge:owner` in its catalog comment:
	// the rows of this table belong to distinct principals, and this
	// column says whose. It injects no WHERE clause — only the
	// application knows how a caller's claims map onto these values — but
	// it names the column in the CRUD shim's scaffolded scoping
	// reminder, and it arms the `unscoped_auth` audit gate. See
	// schemadef.ColumnMarkerOwner.
	Owner bool `json:",omitempty"`
	// GoType is a `forge:go-type=<spec>` declaration verbatim
	// (schemadef.ColumnMarkerGoType, parsed by ParseGoTypeOverride): the Go
	// type the entity struct declares for the column instead of the one its
	// SQL type projects to. "" for none.
	GoType string `json:",omitempty"`

	// ZeroRejected marks a column a single-column CHECK keeps from ever
	// holding its Go zero — an enum vocabulary without '' (or 0), a bound
	// that excludes 0. Its zero then means "unset" without ambiguity, so a
	// DEFAULT applies through ,nullzero rather than a pointer field
	// (ColumnInsertDefault).
	ZeroRejected bool `json:",omitempty"`
}

// FieldKind classifies a proto field for code generation branching.
type FieldKind string

// The FieldKind values a proto field can be classified as. Kinds drive
// which conversion path the CRUD generator emits for the field.
const (
	FieldKindScalar          FieldKind = "scalar"
	FieldKindEnum            FieldKind = "enum"
	FieldKindMessage         FieldKind = "message"
	FieldKindMap             FieldKind = "map"
	FieldKindRepeatedScalar  FieldKind = "repeated_scalar"
	FieldKindRepeatedMessage FieldKind = "repeated_message"
	FieldKindWrapper         FieldKind = "wrapper"   // google.protobuf.*Value
	FieldKindTimestamp       FieldKind = "timestamp" // google.protobuf.Timestamp
)

// EntityField represents a single field in an entity.
type EntityField struct {
	Name string // Proto field name: "patient_id"
	// GoName is the field on the GENERATED pb message ("PatientId";
	// "base64item" → "Base64Item") — naming.GoFieldNames. The db.<Entity>
	// struct spells the same column naming.ColumnGoName, which can differ.
	GoName    string
	ProtoType string    // "int64", "string", etc.
	GoType    string    // "int64", "string", etc.
	Kind      FieldKind // scalar, enum, message, etc.
	IsFK      bool
	FKTable   string // "patients" (if FK)
	// Optional is true when the wire field carries the explicit proto3
	// `optional` label. protobuf-go generates a POINTER struct field
	// (*string, *int64, ...) for explicit-presence scalars, so
	// conversion codegen must pair optional scalars like wrappers
	// (nil-safe pointer handling — see promoteOptionalScalar), never
	// like plain scalars. GoType/Kind here stay the PLAIN spelling; the
	// conversion layer promotes at its boundary so other consumers
	// (frontend projections) are untouched. Additive:
	// `json:",omitempty"` keeps old descriptors parseable.
	Optional bool `json:",omitempty"`
	// MessageType carries the fully-qualified message name for
	// message-typed fields (e.g. "google.protobuf.Timestamp"). ProtoType
	// collapses these to "message", which made every timestamp column
	// degrade to TEXT in plan-based migrations/ORM. Additive.
	MessageType string `json:",omitempty"`
	// Secret carries the field's `// forge:secret` marker through to the
	// conversion generator: a secret field is skipped in the toProto (read)
	// direction — never packed onto a Get/List response — while staying
	// settable on Create/Update. Additive (`json:",omitempty"`).
	Secret bool `json:",omitempty"`
	// ReadOnly carries the field's `// forge:read-only` marker: the field is
	// not client-writable, so the CRUD test's mutable/clobber-field
	// selection (and any other client-input projection) skips it. The value
	// stays on the entity + read responses; only the client-settable request
	// surfaces exclude it. Additive (`json:",omitempty"`).
	ReadOnly bool `json:",omitempty"`
	// MapValueKind is the proto kind of a map field's VALUE ("string",
	// "int64", "message", "enum", ...). GoType collapses every map to the
	// marker "map[string]string", so without this the conversion generator
	// cannot tell a map of scalars (which JSON round-trips as-is) from a
	// map of messages or enums (which do NOT — they would store protobuf
	// struct internals and enum wire NUMBERS respectively). Empty for
	// non-map fields. Additive (`json:",omitempty"`).
	MapValueKind string `json:",omitempty"`
}

// ConfigField represents a single field in a config proto message
// with ConfigFieldOptions annotations.
type ConfigField struct {
	Name         string // Proto field name (e.g., "database_url")
	GoName       string // Go field name (e.g., "DatabaseUrl")
	GoType       string // Go type (e.g., "string", "int32", "bool")
	ProtoType    string // Proto type (e.g., "string", "int32", "bool")
	EnvVar       string // From config_field.env_var
	Flag         string // From config_field.flag
	DefaultValue string // From config_field.default_value
	Required     bool   // From config_field.required
	Description  string // From config_field.description
	Sensitive    bool   // From config_field.sensitive — projects to a Secret in deploy
	Category     string // From config_field.category — groups fields in deploy gen

	// Optional is (forge.v1.config).optional: the application tolerates this
	// field being unset. For a SENSITIVE field it is the only way to say so —
	// a declared secret_ref with no value in the store is an error by
	// default, because discovering a missing credential at runtime is the
	// failure that check exists to prevent. `json:",omitempty"` keeps old
	// descriptors readable (additive contract).
	Optional bool `json:",omitempty"`

	// Role is the (forge.v1.config).role annotation as the bare enum spelling
	// (e.g. "CONFIG_FIELD_ROLE_MODE"; "" for UNSPECIFIED). Config codegen
	// keys semantic behavior (Mode()) on THIS, never on the
	// field NAME — so renaming a field never changes behavior, and naming a
	// field "environment" without the annotation never auto-enables dev mode.
	// `json:",omitempty"` keeps old descriptors readable (additive contract).
	Role string `json:",omitempty"`

	// MessageType names the referenced config message when this field is
	// a component config-block reference (ProtoType == "message"), e.g. a
	// root `AppConfig` field `TraderConfig trader = 21;` records
	// MessageType "TraderConfig". Empty for scalar fields. Block-reference
	// fields carry no env_var/flag of their own — env binding lives on the
	// referenced message's leaf fields. `json:",omitempty"` keeps old
	// descriptors readable (additive contract, see audit-json skill).
	MessageType string `json:",omitempty"`

	// ProtoFile is the path of the proto file declaring this field, e.g.
	// "proto/<project>/config/v1/config.proto". It carries no codegen behavior: it
	// exists so a generate-time refusal can NAME the sources it is talking
	// about. Fields from several protos are flattened into one KCL schema,
	// so when two of them collide the field name alone does not tell an
	// author which protos to go edit (see CheckDuplicateConfigFields).
	// `json:",omitempty"` keeps descriptors written by older forge binaries
	// readable (additive contract, see the audit-json skill).
	ProtoFile string `json:",omitempty"`

	// EnvVarAliases is (forge.v1.config).env_var_aliases: the further env
	// EnvPrefix and EnvSuffix bind a map<string, scalar> field to an env
	// FAMILY: every variable named <EnvPrefix><KEY><EnvSuffix> is one entry
	// ((forge.v1.config).env_prefix / env_suffix). Empty for an ordinary
	// field. MapValueType is the map's value kind ("string", "bool", ...).
	EnvPrefix    string `json:",omitempty"`
	EnvSuffix    string `json:",omitempty"`
	MapValueType string `json:",omitempty"`
	// AllowedValues is the field's closed value set
	// ((forge.v1.config).allowed_values); empty for an open one.
	AllowedValues []string `json:",omitempty"`
	// vars the runtime loader reads, in order, when EnvVar is unset. No
	// generator projects them — the KCL env map, the secret store key and the
	// flag's help name EnvVar alone, so forge's own setters converge on one
	// name — but generate refuses an alias that collides with another name
	// the same process reads (ValidateConfigEnvAliases). `json:",omitempty"`
	// keeps old descriptors readable (additive contract).
	EnvVarAliases []EnvVarAlias `json:",omitempty"`

	// KCLBlock / KCLBlockType place a composed config block's LEAF in the KCL
	// projection: KCLBlock is the composing field's name on the root message
	// (`stripe` for `StripeConfig stripe = 40;`) and KCLBlockType the block
	// message (`StripeConfig`). Set only by the block flattener
	// (config_block_flatten.go); empty for a root-level field. The KCL
	// emitters project such a leaf into a NESTED schema — authored as
	// `stripe.secret_key` — so two blocks may each declare a leaf of the same
	// name. Never serialized: it is derived from MessageType on every run.
	KCLBlock     string `json:"-"`
	KCLBlockType string `json:"-"`
}

// EnvVarAlias is one (forge.v1.config).env_var_aliases entry.
type EnvVarAlias struct {
	Name       string
	Deprecated bool `json:",omitempty"`
	// Values maps the alias's own values onto the field's
	// (EnvVarAlias.values in forge.proto); empty for a plain alias.
	Values map[string]string `json:",omitempty"`
}

// KCLPath is the dotted path a config.k instance authors this field by, and
// the lambda reads it by: `secret_key` for a root field, `stripe.secret_key`
// for a leaf of the `stripe` block.
func (f ConfigField) KCLPath() string {
	if f.KCLBlock == "" {
		return f.Name
	}
	return f.KCLBlock + "." + f.Name
}

// ConfigMessage represents a parsed config proto message.
type ConfigMessage struct {
	Name   string        // Message name (e.g., "AppConfig")
	Fields []ConfigField // Fields with config_field annotations

	// Binary is the cmd/<name> leaf this config belongs to, from the
	// message-level (forge.v1.binary_config).binary annotation. Empty means
	// the message is not bound to a binary: either the project's single
	// project-global AppConfig (the common case — nothing to annotate) or a
	// shared block composed onto other configs (BaseConfig).
	//
	// It is what makes per-binary ownership DISJOINT: a binary's generated
	// loader, KCL schema and env projection are built from the message
	// carrying its name, so deleting a field from one binary's config
	// cannot reach another's. Keyed on the ANNOTATION, never on the message
	// NAME — renaming AdminConfig does not re-point it at another binary.
	// `json:",omitempty"` keeps descriptors written by older forge binaries
	// readable (additive contract, see the audit-json skill).
	Binary string `json:",omitempty"`

	// Command is the cobra command path below Binary whose process loads
	// this config, from (forge.v1.binary_config).command — ["server", "api"]
	// for `reliant server api`. Empty means the binary's ROOT config: the
	// binary-wide surface every one of its processes loads. Non-empty means
	// exactly the process running that subcommand loads it, alongside the
	// root config. A binding is keyed on (Binary, Command).
	Command []string `json:",omitempty"`

	// Subtrees are the command paths at or below which this config is
	// loaded, from (forge.v1.binary_config).subtrees: every process whose
	// command path has one of them as a prefix loads it, alongside the root
	// config and its exact command config. Exclusive with Command.
	Subtrees [][]string `json:",omitempty"`

	// Frontend is the forge.yaml frontends[].name whose BUNDLE loads this
	// config, from the message-level (forge.v1.frontend_config).frontend
	// annotation. Empty means the message is not bound to a frontend.
	//
	// It plays the same disjoint-ownership role Binary does, one layer out:
	// a frontend's generated TypeScript module, KCL schema and per-env
	// projection are built from the message carrying its name. The
	// difference is what the binding IMPLIES about secrecy — a
	// frontend-bound message is delivered to a browser, so every field it
	// carries is public and a `sensitive` field reaching one is a
	// generate-time error (see ValidateFrontendConfigs).
	//
	// A message may carry BOTH annotations only if that is genuinely
	// intended; the shared-definition pattern is to compose one unannotated
	// block (BaseConfig) onto an annotated binary config AND an annotated
	// frontend config, so the shared fact is declared once.
	Frontend string `json:",omitempty"`

	// ProtoFile and Line locate the message's (forge.v1.binary_config)
	// option — or the message itself when the option has no recorded
	// position — so a generate-time refusal of the binding names the line
	// an author edits (see ValidateBinaryConfigBindings). ProtoFile is
	// relative to the buf module root ("acme/config/v1/config.proto"), as on
	// ConfigField; Line is 1-based and 0 when unknown (a request without
	// source info). Neither carries codegen behavior.
	ProtoFile string `json:",omitempty"`
	Line      int    `json:",omitempty"`

	// GoImportPath is the Go package the message generates into — its
	// file's go_package ("<module>/gen/acme/config/v1"). pkg/config imports
	// it, so it is read off the compiled proto rather than re-derived: a
	// go_package the author changed is followed, not contradicted. Empty in
	// descriptors written by older forge binaries; the loader then falls
	// back to the project's configproto layout.
	GoImportPath string `json:",omitempty"`
}

// DetermineFieldKind classifies a field based on its ProtoType and GoType.
func DetermineFieldKind(protoType, goType string) FieldKind {
	switch protoType {
	case "enum":
		return FieldKindEnum
	case "message":
		// Check for well-known wrapper/timestamp types via GoType
		switch {
		case goType == "*timestamppb.Timestamp":
			return FieldKindTimestamp
		case strings.HasPrefix(goType, "*") && isWrapperGoType(goType):
			return FieldKindWrapper
		case strings.HasPrefix(goType, "map["):
			return FieldKindMap
		case strings.HasPrefix(goType, "[]") && strings.HasPrefix(goType[2:], "*"):
			return FieldKindRepeatedMessage
		default:
			return FieldKindMessage
		}
	default:
		// Scalar types — a "[]" prefix means `repeated`, EXCEPT for
		// `bytes`, whose singular Go type is already a slice. A `bytes`
		// field classified as repeated_scalar would be paired against an
		// array column and converted element-wise, so the discriminator
		// for it is the extra slice level: []byte is one value, [][]byte
		// is many.
		if protoType == "bytes" {
			if goType == "[][]byte" {
				return FieldKindRepeatedScalar
			}
			return FieldKindScalar
		}
		if strings.HasPrefix(goType, "[]") {
			return FieldKindRepeatedScalar
		}
		return FieldKindScalar
	}
}

// isWrapperGoType returns true if the Go type is a well-known protobuf wrapper
// that unwraps to a scalar (e.g. *string from StringValue, *int32 from Int32Value).
func isWrapperGoType(goType string) bool {
	switch goType {
	case "*string", "*int32", "*int64", "*uint32", "*uint64",
		"*bool", "*float32", "*float64":
		return true
	}
	return false
}

// ProtoTypeToGoType converts a proto SCALAR kind to the Go type
// protoc-gen-go generates for it.
//
// This is not forge's choice to make: the generated `pb` package already
// declares the field, and every projection that disagrees with it produces
// code that either refuses a legal shape or does not compile. Nine of the
// fifteen kinds used to answer "string" here — every unsigned, zigzag and
// fixed-width integer, plus `bytes` — with two different consequences:
//
//   - `bytes` and the eight integer kinds were rejected by the CRUD
//     conversion gate, so an app declaring one could not run
//     `forge generate` at all;
//   - `repeated bytes` PASSED, because the wire side answered []string and
//     the column side (dbBaseGoType, collapsing every non-BIGINT[] array)
//     also answered []string. Two wrong answers agreeing looked exactly
//     like a mapped pairing, and the generator emitted
//     `append([]string(nil), m.Chunks...)` against protoc-gen-go's real
//     [][]byte field.
//
// A non-scalar kind ("message", "enum", "map", "group") has no scalar Go
// type; callers branch on those BEFORE reaching here (see
// schemaFieldToEntityField, classifyFilterField), and the "string" answer
// is the inert fallback for a kind that never should have arrived. It is
// spelled here, at the one site that needs it, rather than as a `default:`
// arm on the table: a fallback inside the projection is reached by an
// unrecognised kind too, and cannot be told apart from a mapping.
func ProtoTypeToGoType(protoType string) string {
	if goType, ok := ProtoScalarGoType(protoType); ok {
		return goType
	}
	return "string"
}
