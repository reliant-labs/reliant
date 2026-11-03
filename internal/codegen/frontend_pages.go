package codegen

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/naming"
)

// PageTemplateData holds data for rendering a single entity's CRUD pages.
type PageTemplateData struct {
	EntityName       string // "Task" (PascalCase)
	EntityNamePlural string // "Tasks"
	EntitySlug       string // "tasks" (kebab-case for URL)
	ServiceName      string // "TaskService"
	ServiceNameCamel string // "taskService"
	HooksImportPath  string // "@/hooks/task-service-hooks"
	TypesImportPath  string // "@/gen/services/tasks/v1/tasks_pb"
	ListRPC          string // "ListTasks" (PascalCase, matching hook name)
	GetRPC           string // "GetTask"
	CreateRPC        string // "CreateTask"
	UpdateRPC        string // "UpdateTask"
	DeleteRPC        string // "DeleteTask"
	HasList          bool
	HasGet           bool
	HasCreate        bool
	HasUpdate        bool
	HasDelete        bool
	// ItemsField is the camelCase (protojson) accessor for the list
	// response's repeated field — the array the list hook's `data` holds.
	// It is the ACTUAL repeated proto field name on the ListXxxResponse
	// message (e.g. `keys` for `ListLLMKeysResponse { repeated LLMKey keys
	// = 1; }`), NOT the camelCased entity plural. They usually coincide
	// ("tasks" for ListTasksResponse.tasks) but diverge whenever the proto
	// names the field differently — and a wrong accessor silently yields
	// `undefined`, breaking the list page, the dashboard count tile, and
	// the mock transport's response shape all at once. Falls back to the
	// camelCased plural for older descriptors that don't carry the field.
	ItemsField   string
	CreateFields []PageField // Fields for the create form
	UpdateFields []PageField // Fields for the edit form
	// GuardedFields are the columns a custom RPC owns
	// (`// forge:guards <table>.<column>`), removed from UpdateFields and
	// rendered as disabled rows naming the RPC instead. They are NOT in
	// the update_mask — that is the point — and they are not in
	// CreateFields either, since a guard is a rule about transitions
	// rather than about the initial value.
	GuardedFields []GuardedPageField
	// HasGuardedFields gates the edit form's read-only guarded section.
	HasGuardedFields bool
	// readOnlyEditFields are the entity's `forge:read-only` fields, kept off
	// the edit form (and so off the update_mask) but held here so a guard on
	// one still surfaces as a GuardedFields row. Never rendered directly.
	readOnlyEditFields []PageField
	// UpdateEntityFieldCamel is the camelCase request field wrapping the
	// entity when the update request follows AIP-134 ("task" for
	// `Task task = 1;`). The edit page then nests the form values under
	// it (with the PK inside) instead of spreading them at the top level.
	// Empty for flat update requests (legacy id+fields shape).
	UpdateEntityFieldCamel string
	// UpdateMaskFieldCamel is the camelCase google.protobuf.FieldMask
	// field on the update request ("updateMask"); the edit page sends a
	// mask naming exactly the form's fields so the server's masked write
	// can't clobber columns the form never edits. Empty when the request
	// has no mask.
	UpdateMaskFieldCamel string
	// CreateEntityFieldCamel is the camelCase field the Create RESPONSE
	// wraps the new entity in ("thing" for `CreateThingResponse { Thing
	// thing = 1; }`), read off the descriptor. The create page uses it to
	// land on the record it just made. Empty when the response does not
	// carry the entity — the page then returns to the list, since there is
	// no id to navigate to.
	CreateEntityFieldCamel string
	// GetEntityFieldCamel is the TS property of the Get RESPONSE field that
	// carries the entity — what the detail and edit pages read the record
	// off (`data?.base64item`, via GetEntityAccessor). Read off the
	// descriptor, falling back to the scaffolded field name
	// (entityFieldCamel); never the entity name with its first letter
	// lowered, which protoc-gen-es does not generate for "Base64Item"
	// (base64item) or "LLMKey" (llmKey).
	GetEntityFieldCamel string
	// Response type names for imports
	ListResponseType   string // "ListTasksResponse"
	GetResponseType    string // "GetTaskResponse"
	CreateRequestType  string // "CreateTaskRequest"
	CreateResponseType string // "CreateTaskResponse"
	UpdateRequestType  string // "UpdateTaskRequest"
	UpdateResponseType string // "UpdateTaskResponse"
	GetRequestType     string // "GetTaskRequest"
	DeleteRequestType  string // "DeleteTaskRequest"

	// ── Entity-derived metadata (AttachEntityMeta) ──────────────────
	// The generator KNOWS the entity's fields from the proto descriptor,
	// so page templates emit typed column/field declarations instead of
	// casting proto messages to Record<string, unknown> and reflecting
	// over Object.keys at runtime.

	// EntityTypeImportPath is the TS module declaring the entity type
	// ("@/gen/db/v1/tasks_pb"). May differ from TypesImportPath when the
	// entity message lives in its own proto file.
	EntityTypeImportPath string
	// Columns drives the DETAIL page's field rows. Only renderable kinds
	// are included (scalars, enums, timestamps, repeated scalars) — nested
	// messages and maps don't belong in a table cell.
	Columns []EntityPageField
	// ListColumns drives the LIST page's typed column array. It is Columns
	// minus the identity/audit fields — see listColumnExcluded for why a
	// table and a detail view want different sets.
	ListColumns []EntityPageField
	// SearchFields are the camelCase string-typed fields client-side
	// search filters over. Empty → the list page omits the search box.
	//
	// DEPRECATED for the list page: the born list page now filters
	// SERVER-SIDE via the List RPC's own filter fields (SearchFilterField /
	// ExactFilterFields below), so it no longer client-side-filters a single
	// fetched page. SearchFields is retained because DisplayField derivation
	// shares this pass and older tooling still reads it.
	SearchFields []string
	// DisplayField is the camelCase string field used as the human title.
	// See deriveDisplayField for the ranking and for what is deliberately
	// left out; empty when the entity carries no whole-label column, and
	// every reader then falls back to the primary key.
	DisplayField string
	// PkFieldCamel is the camelCase primary-key field ("id").
	PkFieldCamel string
	// HasBadgeColumns reports whether any column renders as a Badge —
	// gates the Badge / enumBadgeVariant / humanizeEnum imports in page
	// templates.
	HasBadgeColumns bool
	// HasMoneyColumns reports whether any column is an integer-cents money
	// field — gates the formatMoneyCents import in page templates.
	HasMoneyColumns bool
	// HasDateCreateFields / HasDateUpdateFields gate the timestamp
	// conversion imports (timestampFromDate / toDatetimeLocal) in the
	// create and edit form templates.
	HasDateCreateFields bool
	HasDateUpdateFields bool
	// CreateEnumImports / UpdateEnumImports are the `import { X } from
	// "path"` lines the create/edit form pages need for their enum-typed
	// fields (one entry per _pb module, type names deduped and sorted).
	// Precomputed here because the templates render each page's import
	// block once while the fields repeat per input.
	CreateEnumImports []PageEnumImport
	UpdateEnumImports []PageEnumImport

	// ── List-page server-side filter + cursor-pagination projection ──
	// The born list page adopts the forge frontend runtime <Resource>
	// container: filtering and pagination happen SERVER-SIDE via the List
	// RPC's own request fields (the same filter fields the backend CRUD op
	// filters on), instead of fetching one capped page and filtering it in
	// the browser. These fields drive that projection.

	// SearchFilterField is the camelCase List-request field wired to the
	// <Resource> debounced search box — a "search"/"query"/"q" filter the
	// backend turns into an ILIKE across the entity's string columns. Empty
	// when the List request declares no free-text search filter.
	SearchFilterField string
	// ExactFilterFields are the List request's discrete (non-search) filter
	// fields — enum, bool, or plain scalar — rendered as filter controls
	// that feed the List RPC server-side. Empty → the page renders no
	// discrete filter controls (it still paginates + shows the tristate).
	ExactFilterFields []ListFilterField
	// ListFilterEnumImports are the `import { X } from "path"` lines the enum
	// filter <select>s need (deduped + sorted, like Create/UpdateEnumImports).
	ListFilterEnumImports []PageEnumImport
	// HasScalarExactFilter is true when any ExactFilterField is a plain
	// text/number scalar — those inputs are debounced (useDebouncedValue), so
	// the list page imports the hook and holds a raw-input/debounced-value
	// pair per scalar filter.
	HasScalarExactFilter bool
	// ColumnEnumImports are the `import { X } from "path"` lines the enum
	// BADGE COLUMNS need, because the cell passes the enum OBJECT itself to
	// <StatusBadge enumType={EnumType}>. Used by the detail page and merged
	// into ListEnumImports for the list page. Deduped + sorted.
	ColumnEnumImports []PageEnumImport
	// ListEnumImports is the merged, deduped set of enum imports the LIST page
	// needs — filter <select>s (ListFilterEnumImports) plus badge columns
	// (ColumnEnumImports) — so an enum that is both a filter and a column is
	// imported exactly once (a duplicate import is a TS error).
	ListEnumImports []PageEnumImport
	// HasCursorPagination is true when the List REQUEST carries a page_token
	// field AND the List RESPONSE carries next_page_token — the cursor pair
	// the <Resource> Prev/Next footer walks. When false the page renders the
	// server's first/only page with no pager (graceful degrade).
	HasCursorPagination bool
	// HasPageSize is true when the List request carries a page_size field —
	// gates passing PAGE_SIZE in the request init.
	HasPageSize bool
	// NextTokenField is the camelCase response field holding the forward
	// cursor ("nextPageToken"); empty when the response declares none.
	NextTokenField string
	// HasTotalCount / TotalCountField expose the camelCase response count
	// field ("totalCount") — wave 4 populates it server-side — so the list
	// reports the real total instead of the current page's length.
	HasTotalCount   bool
	TotalCountField string
}

// ListFilterField is one discrete (non-search) server-side filter control
// projected from a List RPC request field. The born list page holds a piece
// of state per field and threads its value into the List call, so filtering
// is done by the backend query — never by fetching a page and filtering it
// in the browser.
type ListFilterField struct {
	Name       string // camelCase request field: "status", "active"
	NamePascal string // "Status", "Active" — the React setter is set<NamePascal>
	Label      string // display label: "Status", "Active"
	// Kind selects the control the template renders and how the value is
	// coerced into the request: "enum" (typed <select>), "bool" (tri-state
	// All/Yes/No <select>), "text" (string exact input), "number".
	Kind string
	// IsBigInt is true for a "number" filter over a 64-bit int field
	// (int64/uint64/…) — protobuf-es types those as bigint, so the request
	// value is BigInt(...) not Number(...).
	IsBigInt bool
	// EnumType / EnumImport / EnumValues are populated only for Kind "enum":
	// the protobuf-es TS enum identifier, the _pb module to import it from,
	// and the <option> refs/labels — the real members only, never the zero
	// value (see formEnumMeta.Values).
	EnumType   string
	EnumImport string
	EnumValues []PageEnumValue
}

// PageEnumImport is one `import { A, B } from "path"` line a form page
// needs for its enum-typed fields.
type PageEnumImport struct {
	Path  string   // TS module: "@/gen/services/brand/v1/brand_pb"
	Types []string // enum type identifiers, sorted: ["BrandStatus"]
}

// PageEnumValue is one option of a generated enum <select>.
type PageEnumValue struct {
	// Ref is the full protobuf-es TS member reference the option's value
	// uses ("BrandStatus.ACTIVE"). Member names mirror protobuf-es's
	// local-name rule: the enum-name prefix is stripped from the proto
	// value name when every value carries it.
	Ref string
	// Label is the humanized display text ("Active", "In Review").
	Label string
}

// EntityPageField is one renderable entity field for list columns /
// detail rows.
type EntityPageField struct {
	Name    string // camelCase TS field name: "createdAt"
	Label   string // display label: "Created At"
	IsBadge bool   // render as a status Badge (enum kind or enum-like string name)
	// EnumType / EnumImportPath are set ONLY for real protobuf enum columns
	// (FieldKindEnum). protobuf-es enums are NUMBERS at runtime — a raw
	// item.status renders "2" — so the cell hands the enum OBJECT to
	// <StatusBadge value={item.field} enumType={EnumType}>, which resolves the
	// value to its member name once for BOTH the label and the colour. Empty
	// for enum-LIKE string columns (already a string) and non-enum columns.
	EnumType       string
	EnumImportPath string
	// IsMoney marks integer minor-unit (cents) columns — proto field name
	// ends in "_cents". The cell formats them as currency (formatMoneyCents)
	// instead of printing the raw bigint digits.
	IsMoney bool
	// CurrencyField is the camelCase name of a sibling ISO-4217 currency
	// field ("currency" / "currencyCode") on the SAME entity, set ONLY for
	// money columns whose entity carries one. The money cell passes it as
	// formatMoneyCents' currency arg so a JPY row renders "¥180", not
	// "$180". Empty → the entity has no currency field, so the cell keeps
	// the formatter's USD default.
	CurrencyField string
	// FK is non-nil when the column is a FOREIGN KEY onto another CRUD
	// entity. The DETAIL page then resolves it through <EntityName> — one
	// row, one lookup — instead of printing a UUID. The LIST page keeps
	// printing the raw id on purpose: resolving names per row costs a
	// request per distinct id, and the right fix there is a server-side
	// join the application owns. See AttachForeignKeys.
	FK *PageFieldFK
}

// PageField represents a form field derived from a proto message field.
type PageField struct {
	Name  string // "title" (camelCase)
	Label string // "Title" (display name)
	Type  string // "text", "number", "checkbox", "date", "textarea"
	// ProtoName is the original snake_case proto field name ("created_at")
	// — the AIP-134 update_mask path for this field.
	ProtoName string
	// Required is true only when an explicit declaration says the field
	// cannot be empty: a protovalidate `required = true` or
	// `string.min_len >= 1` on the entity field (see requiredByRule), or a
	// non-optional foreign key, whose born column is `NOT NULL REFERENCES`
	// with no DEFAULT (see AttachForeignKeys). A NOT NULL column alone is
	// NOT a rule about input: `string notes` is born `NOT NULL DEFAULT ''`,
	// and the empty string is a value the API, the wire validator and the
	// database all accept.
	Required bool
	// Optional is the proto3 `optional` label: the field has presence, so
	// "absent" is a value the wire can carry and the born column is
	// nullable. An empty input for such a field submits as unset — see
	// EmptyIsUnset.
	Optional  bool
	ProtoType string // original proto type for reference
	// IsRepeated marks repeated scalar fields (descriptor ProtoType
	// "[]string" etc.). The form renders a comma-separated text input and
	// the submit handler splits it back into the array the RPC expects —
	// without the split, the generated page assigned a string to a
	// string[] request field and failed the TypeScript build.
	IsRepeated bool
	// TSType is the TypeScript type protobuf-es declares for this field's
	// ELEMENT ("string", "boolean", "number", "bigint", "Uint8Array"), from
	// the one projection in frontend_ts_scalars.go. Empty for enums,
	// timestamps and anything else with no scalar projection. It is what
	// ZodExpr / SubmitExpr / PrefillExpr are derived from, so a kind the
	// emitters have never seen cannot quietly be treated as a string.
	TSType string
	// ZodExpr is the whole zod schema expression for this field —
	// `z.coerce.number().gte(0)`, `z.string().min(1)`,
	// `z.string().regex(/^(-?\d+)?$/, "expected a whole number")`, or one
	// of those wrapped in the empty-as-unset preprocess (EmptyIsUnset).
	// Nothing is added at render time.
	//
	// It is one string rather than a branch chain in each template because
	// there are FOUR page templates (Next.js and Vite × create and edit)
	// and the chain has to agree with SubmitExpr in every one of them: the
	// zod line declares the type the submit expression consumes. Six
	// divergent copies of that agreement is how the form came to feed
	// number[] into bigint[].
	ZodExpr string
	// SubmitExpr is the TypeScript expression the submit handler assigns
	// to this field, over the form's `values`. Empty when the zod value IS
	// already the wire type (a plain string, a number, a checkbox boolean,
	// a typed enum select) and the `...values` spread carries it.
	SubmitExpr string
	// SubmitNote is a one-line comment the submit handler emits above
	// SubmitExpr when the conversion is not self-evident. Empty for none.
	SubmitNote string
	// PrefillExpr is the TypeScript expression the EDIT form's `values:`
	// block reads out of the fetched entity (`item`) to seed this input.
	// It is the inverse of SubmitExpr and must land on the zod schema's
	// type, not the wire type.
	PrefillExpr string
	// IsEnum marks fields that render as a typed <select> over the
	// enum's declared values (Type "select"). The zod schema validates
	// the coerced option value as an enum member
	// (z.coerce.number().pipe(z.nativeEnum(X))) so the submitted form
	// value IS the protobuf-es TS enum — spreading it into
	// mutation.mutate() satisfies MessageInit without casts. Enum fields
	// that can't be resolved to a typed select (cross-package enums, old
	// descriptors without the deep schema) are EXCLUDED from forms
	// entirely: the pre-fix z.string() text-input projection guaranteed
	// a frontend type error at the mutate() call.
	IsEnum bool
	// EnumType is the TS enum identifier ("BrandStatus", or
	// "Brand_Status" for an enum nested in a message).
	EnumType string
	// EnumValues are the select options in proto declaration order: the
	// REAL members, never the zero value (see formEnumMeta.Values).
	EnumValues []PageEnumValue
	// EnumZeroRef is the enum's zero member ("BrandStatus.UNSPECIFIED"),
	// which the zod schema refuses — the zod mirror of the born CHECK that
	// does not admit it. Empty when the enum declares no zero value.
	EnumZeroRef string
	// EnumImportPath is the _pb module declaring the enum; page-level
	// import lines are aggregated from it (Create/UpdateEnumImports).
	EnumImportPath string
	// ZodChain is the trailing zod validator chain projected from the
	// field's protovalidate rules (e.g. ".gte(0).lte(100)" or
	// ".min(2).max(64).email()"). Empty when the field carries no
	// projected rules. The form template appends it to the base builder
	// (z.coerce.number() / z.string()) — see the create/edit page
	// templates. The SAME rules are enforced on the wire by the
	// protovalidate interceptor.
	ZodChain string
	// FK is non-nil when this field is a FOREIGN KEY onto another CRUD
	// entity in the same project — `patient_id` on Order pointing at
	// Patient. The form then renders an <EntityPicker> over the referent's
	// generated List hook instead of a raw text input for a UUID. Resolved
	// by AttachForeignKeys; see frontend_pages_fk.go for the (deliberately
	// conservative) resolution rule.
	FK *PageFieldFK

	// chainRequiresValue records that the projected ZodChain already
	// carries the length floor a required field needs (a string `.min(N)`
	// from the same rule that made it Required), so attachZodExpr does not
	// stack a second `.min(1, "Required")` in front of it.
	chainRequiresValue bool
}

// EnumDefaultRef is the option a CREATE form's NOT NULL enum select starts
// on: the first real member. It is the born column's DEFAULT
// (scaffold.bornEnumDefault — the first value the CHECK admits) and the
// value the generated write path stores when the field is omitted, so the
// form shows the value Create would store anyway instead of inventing a
// "pick one" rule the API does not have.
func (f PageField) EnumDefaultRef() string {
	if len(f.EnumValues) == 0 {
		return ""
	}
	return f.EnumValues[0].Ref
}

// EmptyIsUnset reports whether an EMPTY input for this field means "unset":
// the field is a proto3 `optional` that no rule makes required. Its zod
// schema then turns "" into undefined before validating, so the request
// omits the field — an empty `optional int32 year` is stored as NULL, not
// as year 0, and an empty optional email is not run through `.email()`.
//
// Checkboxes, timestamps and repeated fields are excluded: a checkbox is
// never empty, and the other two already submit an empty input as absent.
func (f PageField) EmptyIsUnset() bool {
	return f.Optional && !f.Required && f.TSType != "boolean" && f.Type != "date" && !f.IsRepeated
}

// requiredByRule reports whether a field's declared protovalidate rules
// forbid an empty value: `required = true`, or a `min_len` of at least 1.
// These are the only rules that say "this input must be filled in"; every
// other projected rule (max_len, email, pattern, numeric bounds) constrains
// a value without demanding one, and stays exactly as ZodChain projects it.
func requiredByRule(c *FieldConstraints) bool {
	if c == nil {
		return false
	}
	return c.Required || (c.MinLen != nil && *c.MinLen >= 1)
}

// isRepeatedScalarProtoType reports whether a descriptor message-field
// proto type is a repeated scalar ("[]string", "[]int32", ...).
// Repeated message fields carry "[]message" and are not form-mappable.
func isRepeatedScalarProtoType(protoType string) bool {
	base, ok := strings.CutPrefix(protoType, "[]")
	if !ok {
		return false
	}
	return base != "message" && base != "enum"
}

// protoTypeToFormField maps proto field types to the form control the
// page templates render. Scalars route through the one TypeScript
// projection so a control is chosen for the type the field ACTUALLY has;
// the remaining arm is the timestamp well-known type.
func protoTypeToFormField(protoType string) string {
	// Repeated scalars render as a comma-separated text input; the page's
	// submit handler splits the value back into the array.
	if strings.HasPrefix(protoType, "[]") {
		return "text"
	}
	if ts, ok := protoScalarTSType(protoType); ok {
		return tsFormControl(ts)
	}
	switch protoType {
	case "google.protobuf.Timestamp", "Timestamp":
		return "date"
	default:
		return "text"
	}
}

// fieldNameToLabel converts a snake_case or camelCase field name to a display label.
// "first_name" → "First Name", "email" → "Email"
func fieldNameToLabel(name string) string {
	// Handle snake_case
	if strings.Contains(name, "_") {
		parts := strings.Split(name, "_")
		for i, p := range parts {
			if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		return strings.Join(parts, " ")
	}
	// Handle camelCase — insert spaces before uppercase letters
	var result strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			result.WriteRune(' ')
		}
		if i == 0 {
			result.WriteRune(unicode.ToUpper(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// fieldNameToCamel is the property protoc-gen-es generates for a proto
// field — naming.EsFieldName: "first_name" → "firstName", "email" →
// "email", "address_line_2" → "addressLine2", "to_string" → "toString$".
// Every generated TS reference to a message field goes through it.
func fieldNameToCamel(name string) string {
	return naming.EsFieldName(name)
}

// entityFieldCamel is the TS property of the field that carries ONE
// entityName on a message, when the descriptor cannot say: the field the
// entity scaffolder writes (naming.EntityFieldName), as protoc-gen-es
// spells it. "Base64Item" is carried in base64item, "LLMKey" in llmKey —
// never the entity name with its first letter lowered.
func entityFieldCamel(entityName string) string {
	return naming.EsFieldName(naming.EntityFieldName(entityName))
}

// createFieldSkipList contains field names that should not appear in create forms.
var createFieldSkipList = map[string]bool{
	"id":          true,
	"created_at":  true,
	"updated_at":  true,
	"deleted_at":  true,
	"create_time": true,
	"update_time": true,
	"delete_time": true,
}

// formFieldDef is a message field enriched for form projection: the
// shallow MessageFieldDef shape both extraction sources normalize to,
// plus the fully-qualified enum type for enum-typed fields (only the
// deep Schemas source carries it — MessageFieldDef.MessageType is
// message-only by contract).
type formFieldDef struct {
	MessageFieldDef
	EnumTypeFQ string
	// ReadOnly carries the entity field's `// forge:read-only` marker
	// (deep Schemas source only). The edit form skips these — a field the
	// client cannot write must not become an editable input whose value
	// would be named in the update_mask and clobber the stored one.
	ReadOnly bool
}

// schemaFieldToFormFieldDef converts one deep-schema field into the
// same encoding extractMessageFields produces for the shallow Messages
// map (repeated fields carry a "[]" ProtoType prefix; maps collapse to
// a bare "message"), so the form projection treats both sources
// identically.
func schemaFieldToFormFieldDef(d SchemaFieldDef) formFieldDef {
	fd := MessageFieldDef{Name: d.Name, IsOptional: d.Optional}
	var enumFQ string
	switch d.Kind {
	case "message":
		fd.ProtoType = "message"
		if d.Repeated {
			fd.ProtoType = "[]message"
		}
		fd.MessageType = d.TypeName
	case "map":
		// Maps are repeated entry messages under the hood, but the
		// shallow encoding records them as a bare "message" (the "[]"
		// prefix explicitly excludes maps).
		fd.ProtoType = "message"
	case "enum":
		fd.ProtoType = "enum"
		if d.Repeated {
			fd.ProtoType = "[]enum"
		}
		enumFQ = d.TypeName
	default: // scalar kinds
		fd.ProtoType = d.Kind
		if d.Repeated {
			fd.ProtoType = "[]" + d.Kind
		}
	}
	return formFieldDef{MessageFieldDef: fd, EnumTypeFQ: enumFQ, ReadOnly: d.ReadOnly}
}

// formFieldsForMessage resolves a message's fields for form projection.
// It prefers the deep Schemas map (keyed by fully-qualified name): the
// shallow Messages map only carries DIRECT RPC inputs/outputs, so the
// AIP-134 update request's wrapped entity message — and every enum's
// type name — exist only in Schemas. Falls back to Messages for older
// descriptors (and fixtures) that predate the deep graph. ok=false when
// neither source knows the message.
func formFieldsForMessage(svc ServiceDef, shortName, fqName string) ([]formFieldDef, bool) {
	if fqName != "" {
		if defs, ok := svc.Schemas[fqName]; ok && defs != nil {
			out := make([]formFieldDef, 0, len(defs))
			for _, d := range defs {
				out = append(out, schemaFieldToFormFieldDef(d))
			}
			return out, true
		}
	}
	defs, ok := svc.Messages[shortName]
	if !ok {
		return nil, false
	}
	out := make([]formFieldDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, formFieldDef{MessageFieldDef: d})
	}
	return out, true
}

// protobufESEnumSharedPrefix mirrors protobuf-es's findEnumSharedPrefix
// (names.ts): the prefix — the enum's short name as lower_snake_case
// plus "_" — is stripped from TS enum member names ONLY when every
// declared value carries it and no stripped remainder is empty or
// starts with a digit. Returns "" when the prefix must be kept. The
// generated <select> references these members, so this MUST match what
// protobuf-es emits or the born page doesn't compile.
func protobufESEnumSharedPrefix(enumShortName string, valueNames []string) string {
	var b strings.Builder
	for i, r := range enumShortName {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	prefix := strings.ToLower(b.String()) + "_"
	for _, v := range valueNames {
		if !strings.HasPrefix(strings.ToLower(v), prefix) {
			return ""
		}
		short := v[len(prefix):]
		if short == "" {
			return ""
		}
		if short[0] >= '0' && short[0] <= '9' {
			return ""
		}
	}
	return prefix
}

// formEnumMeta is the resolved TS projection of one enum type.
type formEnumMeta struct {
	TSType     string
	ImportPath string
	// Values are the CHOICES a form select or list filter offers: every
	// declared member except the zero value. proto3's zero is how an
	// implicit-presence enum says "unset" — the generated write path maps
	// it to the column DEFAULT, and the born CHECK does not admit it — so
	// offering it offers a value no row can hold. An `optional` enum says
	// "unset" with an explicit None choice instead (see EmptyIsUnset).
	Values []PageEnumValue
	// ZeroRef is the zero member's TS reference ("ThingKind.UNSPECIFIED"),
	// which the zod schema refuses. Empty when no value is numbered 0.
	ZeroRef string
}

// resolveFormEnum resolves an enum-typed form field to its protobuf-es
// TS surface: the exported enum identifier, the _pb module to import it
// from, and the member references/labels for the <select> options.
// ok=false means there is no projection the generated page could
// compile against — the caller excludes the field from the form.
//
//   - Top-level same-package enums import from the ENTITY message's
//     declaring module. That is where the proto conventions (proto
//     skill "Enum Conventions", entity birth's same-package rule) place
//     an entity field's enum — adjacent to the entity message.
//   - Enums nested in a same-package message export as Parent_Enum from
//     the parent's declaring module (known exactly via SchemaFiles).
//   - Cross-package enums are not resolved: entity birth TODO-skips
//     those columns, so a form input for one would have no backing
//     column anyway.
func resolveFormEnum(svc ServiceDef, entityName, enumFQ string) (formEnumMeta, bool) {
	if enumFQ == "" || svc.Package == "" {
		return formEnumMeta{}, false
	}
	valueNames := svc.Enums[enumFQ]
	if len(valueNames) == 0 {
		return formEnumMeta{}, false
	}
	pkgPrefix := svc.Package + "."
	if !strings.HasPrefix(enumFQ, pkgPrefix) {
		return formEnumMeta{}, false
	}
	local := strings.TrimPrefix(enumFQ, pkgPrefix)
	segs := strings.Split(local, ".")

	var declFile, tsType, shortName string
	switch len(segs) {
	case 1:
		shortName = segs[0]
		tsType = segs[0]
		declFile = svc.SchemaFiles[pkgPrefix+entityName]
		if declFile == "" {
			// Older descriptor without SchemaFiles — single-file layout.
			declFile = svc.ProtoFile
		}
	case 2:
		parentFile, ok := svc.SchemaFiles[pkgPrefix+segs[0]]
		if !ok || parentFile == "" {
			return formEnumMeta{}, false
		}
		shortName = segs[1]
		tsType = segs[0] + "_" + segs[1]
		declFile = parentFile
	default:
		return formEnumMeta{}, false
	}

	meta := formEnumMeta{
		TSType:     tsType,
		ImportPath: "@/gen/" + ProtoFileToTSImportPath(declFile),
	}
	// The zero is identified by its declared wire NUMBER — the fact proto3
	// gives it meaning by — never by its name. A descriptor that predates
	// EnumNumbers carries names only; proto3 requires the first declared
	// value to be the zero, so position 0 is then exact.
	// numbers is parallel to valueNames when the descriptor carried every
	// value's number; without them, proto3's rule that the zero value comes
	// first identifies it.
	numbers := svc.EnumNumbers[enumFQ]
	numbered := len(numbers) == len(valueNames)
	prefix := protobufESEnumSharedPrefix(shortName, valueNames)
	for i, v := range valueNames {
		member := v
		if prefix != "" {
			member = v[len(prefix):]
		}
		ref := tsType + "." + member
		if (numbered && numbers[i] == 0) || (!numbered && i == 0) {
			if meta.ZeroRef == "" {
				meta.ZeroRef = ref
			}
			continue
		}
		meta.Values = append(meta.Values, PageEnumValue{
			Ref:   ref,
			Label: fieldNameToLabel(strings.ToLower(member)),
		})
	}
	return meta, true
}

// formPageField projects one message field into a form field; ok=false
// when the field has no COMPILABLE form representation and must be
// excluded (nested messages/maps, repeated enums, unresolvable enums).
// Exclusion is the fail-safe: the pre-fix behavior rendered these as
// z.string() text inputs, which guaranteed the born page failed the
// frontend type-check at mutation.mutate({...values}).
func formPageField(svc ServiceDef, entityName string, f formFieldDef) (PageField, bool) {
	base := strings.TrimPrefix(f.ProtoType, "[]")
	repeated := strings.HasPrefix(f.ProtoType, "[]")
	switch base {
	case "message":
		// Only a singular Timestamp has a form control (datetime-local);
		// other nested messages, maps, and repeated messages don't.
		if repeated || f.MessageType != "google.protobuf.Timestamp" {
			return PageField{}, false
		}
	case "enum":
		if repeated {
			return PageField{}, false
		}
		meta, ok := resolveFormEnum(svc, entityName, f.EnumTypeFQ)
		if !ok || len(meta.Values) == 0 {
			// No real member to choose: a select over nothing.
			return PageField{}, false
		}
		pf := pageFieldFromMessageField(f.MessageFieldDef)
		pf.Type = "select"
		pf.IsEnum = true
		pf.EnumType = meta.TSType
		pf.EnumValues = meta.Values
		pf.EnumZeroRef = meta.ZeroRef
		pf.EnumImportPath = meta.ImportPath
		return pf, true
	}
	pf := pageFieldFromMessageField(f.MessageFieldDef)
	// A repeated scalar whose elements have no lossless text spelling gets
	// no control at all. `repeated bool` is the case: the comma-separated
	// input is TEXT, and text → bool has no honest cast — "1", "yes" and
	// "TRUE" all silently become false while the form reports success.
	// Excluding it is the same answer forge already gives a repeated enum
	// and a nested message, and the same answer dead263b gave a lossy
	// pairing on the Go side.
	if repeated && !repeatedScalarHasForm(pf.TSType) {
		return PageField{}, false
	}
	return pf, true
}

// collectEnumImports aggregates the per-field enum import needs of one
// form into sorted, deduped import lines.
func collectEnumImports(fields []PageField) []PageEnumImport {
	byPath := map[string]map[string]bool{}
	for _, f := range fields {
		if !f.IsEnum || f.EnumImportPath == "" || f.EnumType == "" {
			continue
		}
		if byPath[f.EnumImportPath] == nil {
			byPath[f.EnumImportPath] = map[string]bool{}
		}
		byPath[f.EnumImportPath][f.EnumType] = true
	}
	if len(byPath) == 0 {
		return nil
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]PageEnumImport, 0, len(paths))
	for _, p := range paths {
		types := make([]string, 0, len(byPath[p]))
		for tn := range byPath[p] {
			types = append(types, tn)
		}
		sort.Strings(types)
		out = append(out, PageEnumImport{Path: p, Types: types})
	}
	return out
}

// attachListMeta projects the List RPC's request/response shape onto the
// list page so filtering + pagination happen SERVER-SIDE through the forge
// runtime <Resource> container:
//
//   - the List request's own filter fields become the page's filter state
//     (a "search"/"query"/"q" field drives the <Resource> debounced box;
//     enum/bool/plain-scalar fields become discrete filter controls) — the
//     SAME filter fields the backend CRUD op filters on, so the browser
//     never fetches one capped page and filters it locally;
//   - page_token (request) + next_page_token (response) wire the cursor
//     the <Resource> Prev/Next footer walks;
//   - total_count (response) reports the real total.
//
// The deep Schemas source is preferred (it carries each enum filter's type
// name, needed for the typed <select>); the shallow Messages map is the
// fallback for older descriptors. An enum filter that can't be resolved to
// a typed select is DROPPED rather than fabricated as an untyped control.
func attachListMeta(page *PageTemplateData, svc ServiceDef, entityName, listReq, listReqFQ, listResp string) {
	hasPageToken := false
	if listReq != "" { //nolint:nestif // List-RPC shape derivation: one arm per request-field form the page model can consume.
		if fields, ok := formFieldsForMessage(svc, listReq, listReqFQ); ok {
			for _, f := range fields {
				switch f.Name {
				case "page_token":
					hasPageToken = true
					continue
				case "page_size":
					page.HasPageSize = true
					continue
				case "order_by":
					continue
				}
				if classifySkipField(f.Name) {
					continue
				}
				base := strings.TrimPrefix(f.ProtoType, "[]")
				repeated := strings.HasPrefix(f.ProtoType, "[]")
				if repeated || base == "message" {
					// No discrete control for repeated / nested-message filters.
					continue
				}
				// A free-text search filter drives the <Resource> debounced
				// search box (the backend spans the entity's string columns).
				if base == "string" {
					switch f.Name {
					case "search", "query", "q":
						page.SearchFilterField = fieldNameToCamel(f.Name)
						continue
					}
				}
				camel := fieldNameToCamel(f.Name)
				lf := ListFilterField{
					Name:       camel,
					NamePascal: upperFirst(camel),
					Label:      fieldNameToLabel(f.Name),
				}
				switch {
				case base == "enum":
					meta, ok := resolveFormEnum(svc, entityName, f.EnumTypeFQ)
					if !ok || len(meta.Values) == 0 {
						// Can't type the <select> against the enum (or it has
						// no real member to filter on) — drop the filter rather
						// than emit an untyped or empty control.
						continue
					}
					lf.Kind = "enum"
					lf.EnumType = meta.TSType
					lf.EnumImport = meta.ImportPath
					lf.EnumValues = meta.Values
				case base == "bool":
					lf.Kind = "bool"
				case base == "string":
					lf.Kind = "text"
					page.HasScalarExactFilter = true
				case isNumericProtoScalar(base):
					lf.Kind = "number"
					lf.IsBigInt = isBigIntProtoType(base)
					page.HasScalarExactFilter = true
				default:
					continue
				}
				page.ExactFilterFields = append(page.ExactFilterFields, lf)
			}
		}
	}

	// Enum-filter <select> imports (deduped + sorted), reusing the same
	// aggregation the create/edit forms use.
	if len(page.ExactFilterFields) > 0 {
		asPageFields := make([]PageField, 0, len(page.ExactFilterFields))
		for _, lf := range page.ExactFilterFields {
			if lf.Kind != "enum" {
				continue
			}
			asPageFields = append(asPageFields, PageField{
				IsEnum:         true,
				EnumType:       lf.EnumType,
				EnumImportPath: lf.EnumImport,
			})
		}
		page.ListFilterEnumImports = collectEnumImports(asPageFields)
	}

	// Response-side cursor + count fields. Read from the shallow Messages
	// map (the same source crudMethodFacts uses to detect total_count).
	hasNextToken := false
	if listResp != "" && svc.Messages != nil {
		if outFields, ok := svc.Messages[listResp]; ok {
			for _, f := range outFields {
				switch f.Name {
				case "next_page_token":
					hasNextToken = true
					page.NextTokenField = fieldNameToCamel(f.Name)
				case "total_count":
					page.HasTotalCount = true
					page.TotalCountField = fieldNameToCamel(f.Name)
				}
			}
		}
	}
	page.HasCursorPagination = hasPageToken && hasNextToken
}

// ExtractCRUDEntities analyzes a service's methods and returns PageTemplateData
// for each entity that has CRUD-pattern RPCs.
func ExtractCRUDEntities(svc ServiceDef) []PageTemplateData { //nolint:gocognit,funlen // proto -> page-model derivation: one branch per RPC shape and per field kind, all flat.
	// Group methods by entity name
	type entityMethods struct {
		listRPC     string
		getRPC      string
		createRPC   string
		updateRPC   string
		deleteRPC   string
		listReq     string
		listReqFQ   string
		listResp    string
		getResp     string
		createReq   string
		createReqFQ string
		createResp  string
		updateReq   string
		updateResp  string
		updateReqFQ string
		getReq      string
		deleteReq   string
	}

	entities := make(map[string]*entityMethods)
	entityOrder := []string{} // preserve discovery order

	for _, m := range svc.Methods {
		if m.ClientStreaming || m.ServerStreaming {
			continue
		}

		// crudEntity singularizes a List's plural and refuses a name that
		// merely starts with a CRUD verb (ListJobsByCrew) — the server
		// projection's rule, so a custom rpc births no page.
		op, entityName := crudEntity(svc, m)
		if op == "" {
			continue
		}

		em, ok := entities[entityName]
		if !ok {
			em = &entityMethods{}
			entities[entityName] = em
			entityOrder = append(entityOrder, entityName)
		}

		switch op {
		case "list":
			em.listRPC = m.Name
			em.listReq = m.InputType
			em.listReqFQ = m.InputTypeFQ
			em.listResp = m.OutputType
		case "get":
			em.getRPC = m.Name
			em.getReq = m.InputType
			em.getResp = m.OutputType
		case "create":
			em.createRPC = m.Name
			em.createReq = m.InputType
			em.createReqFQ = m.InputTypeFQ
			em.createResp = m.OutputType
		case "update":
			em.updateRPC = m.Name
			em.updateReq = m.InputType
			em.updateResp = m.OutputType
			em.updateReqFQ = m.InputTypeFQ
		case "delete":
			em.deleteRPC = m.Name
			em.deleteReq = m.InputType
		}
	}

	hooksFile := strings.TrimSuffix(naming.ServiceHookFile(svc.Name), ".ts")
	importPath := ProtoFileToTSImportPath(svc.ProtoFile)

	var pages []PageTemplateData
	for _, entityName := range entityOrder {
		em := entities[entityName]

		// Only generate pages for real entities with at least a List RPC
		// or both Get and Create. A lone Get (e.g., GetStatus) is not
		// sufficient — it's likely a non-CRUD RPC.
		if em.listRPC == "" && (em.getRPC == "" || em.createRPC == "") {
			continue
		}

		plural := inflection.Plural(entityName)
		slug := PascalToKebab(plural)

		itemsField := listItemsField(svc, em.listResp, entityName)

		data := PageTemplateData{
			EntityName:         entityName,
			EntityNamePlural:   plural,
			EntitySlug:         slug,
			ServiceName:        svc.Name,
			ServiceNameCamel:   toCamelCaseFromPascal(svc.Name),
			HooksImportPath:    "@/hooks/" + hooksFile,
			TypesImportPath:    "@/gen/" + importPath,
			ListRPC:            em.listRPC,
			GetRPC:             em.getRPC,
			CreateRPC:          em.createRPC,
			UpdateRPC:          em.updateRPC,
			DeleteRPC:          em.deleteRPC,
			HasList:            em.listRPC != "",
			HasGet:             em.getRPC != "",
			HasCreate:          em.createRPC != "",
			HasUpdate:          em.updateRPC != "",
			HasDelete:          em.deleteRPC != "",
			ItemsField:         itemsField,
			ListResponseType:   em.listResp,
			GetResponseType:    em.getResp,
			CreateRequestType:  em.createReq,
			CreateResponseType: em.createResp,
			GetRequestType:     em.getReq,
			UpdateRequestType:  em.updateReq,
			UpdateResponseType: em.updateResp,
			DeleteRequestType:  em.deleteReq,
		}
		data.CreateEntityFieldCamel = wrappedEntityField(svc, em.createResp, entityName)
		data.GetEntityFieldCamel = wrappedEntityField(svc, em.getResp, entityName)
		if data.GetEntityFieldCamel == "" {
			data.GetEntityFieldCamel = entityFieldCamel(entityName)
		}

		// protovalidate rules are declared ONCE on the entity message; the
		// create request flattens the entity's fields (losing the options)
		// and the update request wraps the entity, so we project the zod
		// validators from the entity's own constraints, matched by proto
		// field name onto whichever form carries the field.
		entityConstraints := entityConstraintMap(svc, entityName)

		// Extract form fields from the create request message. The deep
		// Schemas source (formFieldsForMessage) carries each enum field's
		// type name, which the typed-select projection needs; the shallow
		// Messages fallback covers older descriptors.
		if em.createReq != "" {
			if fields, ok := formFieldsForMessage(svc, em.createReq, em.createReqFQ); ok {
				for _, f := range fields {
					if createFieldSkipList[f.Name] {
						continue
					}
					pf, ok := formPageField(svc, entityName, f)
					if !ok {
						continue
					}
					finalizePageField(&pf, entityConstraints)
					if pf.Type == "date" {
						data.HasDateCreateFields = true
					}
					data.CreateFields = append(data.CreateFields, pf)
				}
			}
		}

		// Extract form fields for the edit page. The canonical generated
		// update request follows AIP-134 — it WRAPS the entity
		// (`Task task = 1;`) and carries a `google.protobuf.FieldMask
		// update_mask` — so the form fields come from the ENTITY message,
		// the submit nests them under the wrapper field, and the mask
		// names exactly the fields the form edits (without it the
		// server's update clobbers every column the form doesn't carry).
		// A flat request (id + scalar fields) keeps the legacy top-level
		// spread with no mask.
		//
		// The entity message is resolved through the DEEP Schemas map:
		// it is never a direct RPC input/output, so the shallow Messages
		// map doesn't carry it — the old Messages-only lookup silently
		// bailed out here and birthed a completely EMPTY edit form
		// (z.object({}), values {}, mask paths []) for every AIP-134
		// entity while the create form carried all the fields.
		if em.updateReq != "" { //nolint:nestif // Update-RPC shape derivation (22): one arm per field-mask/entity spelling; each maps to a different emitted form.
			if fields, ok := formFieldsForMessage(svc, em.updateReq, em.updateReqFQ); ok {
				formFields := fields
				for _, f := range fields {
					if isFieldMaskField(f.MessageFieldDef) {
						data.UpdateMaskFieldCamel = fieldNameToCamel(f.Name)
						continue
					}
					if fieldMatchesEntity(f.MessageFieldDef, entityName) {
						data.UpdateEntityFieldCamel = fieldNameToCamel(f.Name)
						if entityFields, ok := formFieldsForMessage(svc, localMessageName(f.MessageType), f.MessageType); ok {
							formFields = entityFields
						}
					}
				}
				for _, f := range formFields {
					if createFieldSkipList[f.Name] {
						continue
					}
					// Skip the id field — it's set from the URL param
					if f.Name == "id" {
						continue
					}
					// `// forge:read-only` fields are not client-writable:
					// keep them off the edit form, so they're never named in
					// the update_mask and the stored value is never clobbered.
					// (The create form reads the CreateRequest, which already
					// omits them, so only the entity-driven edit form needs this.)
					// They are parked rather than dropped: a read-only column a
					// custom rpc also guards still renders as the disabled row
					// naming that rpc (attachGuardedFields).
					if f.ReadOnly {
						if pf, ok := formPageField(svc, entityName, f); ok {
							data.readOnlyEditFields = append(data.readOnlyEditFields, pf)
						}
						continue
					}
					// Never render the mask or the entity wrapper itself
					// as form inputs (pre-AIP-134 pages did, producing a
					// dead "Update Mask" text box).
					if isFieldMaskField(f.MessageFieldDef) {
						continue
					}
					if data.UpdateEntityFieldCamel != "" && fieldMatchesEntity(f.MessageFieldDef, entityName) {
						continue
					}
					pf, ok := formPageField(svc, entityName, f)
					if !ok {
						continue
					}
					finalizePageField(&pf, entityConstraints)
					if pf.Type == "date" {
						data.HasDateUpdateFields = true
					}
					data.UpdateFields = append(data.UpdateFields, pf)
				}
			}
		}

		data.CreateEnumImports = collectEnumImports(data.CreateFields)
		data.UpdateEnumImports = collectEnumImports(data.UpdateFields)

		// Project the List RPC's server-side filter fields + cursor /
		// total-count response shape onto the list page (the <Resource>
		// container consumes them). Reads the SAME List-request filter
		// fields the backend CRUD op filters on.
		attachListMeta(&data, svc, entityName, em.listReq, em.listReqFQ, em.listResp)

		pages = append(pages, data)
	}

	return pages
}

// listItemsField returns the camelCase (protojson) accessor for the
// repeated field on a ListXxxResponse message — i.e. the array the list
// hook's `data` actually holds. It reads the response descriptor's first
// repeated field (descriptors encode repeated fields with a "[]" ProtoType
// prefix) rather than deriving the camelCased entity plural, because the
// proto is free to name the field differently (e.g.
// `ListLLMKeysResponse { repeated LLMKey keys = 1; }` → `keys`, not
// `llmKeys`). When the descriptor carries no repeated field (older
// descriptors, or a non-standard list response) it falls back to the
// field the entity scaffolder writes (naming.EntityListFieldName), as
// protoc-gen-es spells it.
func listItemsField(svc ServiceDef, listResp, entityName string) string {
	if listResp != "" && svc.Messages != nil {
		if fields, ok := svc.Messages[listResp]; ok {
			for _, f := range fields {
				if strings.HasPrefix(f.ProtoType, "[]") {
					return fieldNameToCamel(f.Name)
				}
			}
		}
	}
	return naming.EsFieldName(naming.EntityListFieldName(entityName))
}

// wrappedEntityField returns the camelCase field of response message
// respMsg that carries ONE entityName ("thing" on `CreateThingResponse {
// Thing thing = 1; }`), or "" when the response carries none. Read from the
// shallow Messages map, which holds every direct RPC output.
//
// Unlike the mock transport's responseEntityField it has no fallback: the
// create page destructures this field off the typed response, so a guessed
// name is a type error on a file forge just wrote.
func wrappedEntityField(svc ServiceDef, respMsg, entityName string) string {
	if respMsg == "" {
		return ""
	}
	for _, f := range svc.Messages[respMsg] {
		if strings.HasPrefix(f.ProtoType, "[]") {
			continue
		}
		if fieldMatchesEntity(f, entityName) {
			return fieldNameToCamel(f.Name)
		}
	}
	return ""
}

// entityConstraintMap indexes an entity message's protovalidate rules by
// proto field name, so the create form (which flattens the entity's
// fields) and the edit form (which wraps it) both project the same zod
// validators. Empty when the entity has no fields in the deep schema or
// carries no rules.
func entityConstraintMap(svc ServiceDef, entityName string) map[string]*FieldConstraints {
	defs, ok := svc.Schemas[svc.Package+"."+entityName]
	if !ok {
		return nil
	}
	out := make(map[string]*FieldConstraints)
	for _, d := range defs {
		if d.Validate.HasAny() {
			out[d.Name] = d.Validate
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyZodChain attaches the projected zod validator chain to a form field
// from the matching entity constraint. Only plain scalar text/number/date
// inputs receive it — enum selects, checkboxes, and repeated (comma-joined
// text) inputs have no single-value zod base to refine.
//
// The two ENCODED controls — base64 for `bytes`, digit text for a 64-bit
// integer — are excluded for the same reason: the zod value there is a
// text encoding of the field, not the field. A bytes rule's min_len counts
// BYTES while the value is base64 (a third longer), and an int64's
// gte/lte are numeric bounds while zod's .min() on that digit string would
// measure LENGTH. Either projection rejects values the wire accepts. See
// tsZodBase, which decides the same question for the base expression.
func applyZodChain(pf *PageField, constraints map[string]*FieldConstraints) {
	if constraints == nil || pf.IsEnum || pf.IsRepeated {
		return
	}
	if _, allowChain := tsZodBase(pf.TSType); !allowChain {
		return
	}
	c := constraints[pf.ProtoName]
	if !c.HasAny() {
		return
	}
	switch pf.Type {
	case "number":
		pf.ZodChain = c.ZodChain("number")
	case "text", "date":
		pf.ZodChain = c.ZodChain(pf.Type)
		// The string chain opens with `.min(N)` exactly when the rule that
		// makes the field Required is present.
		pf.chainRequiresValue = requiredByRule(c)
	}
}

// pageFieldFromMessageField builds the form-field projection of one proto
// message field. Timestamp message fields map to a date input via their
// MessageType (descriptors collapse every message field's ProtoType to the
// literal "message", which the form-type switch can't match).
func pageFieldFromMessageField(f MessageFieldDef) PageField {
	effectiveType := f.ProtoType
	if f.MessageType == "google.protobuf.Timestamp" {
		effectiveType = f.MessageType
	}
	elemTS, _ := protoScalarTSType(strings.TrimPrefix(f.ProtoType, "[]"))
	pf := PageField{
		Name:       fieldNameToCamel(f.Name),
		Label:      fieldNameToLabel(f.Name),
		Type:       protoTypeToFormField(effectiveType),
		ProtoName:  f.Name,
		Optional:   f.IsOptional,
		ProtoType:  f.ProtoType,
		TSType:     elemTS,
		IsRepeated: isRepeatedScalarProtoType(f.ProtoType),
	}
	return pf
}

// finalizePageField completes a form field once its enum projection is
// resolved: whether its rules make it required, the projected
// protovalidate chain, the zod schema line, and the two TypeScript
// expressions that move the value between the form and the wire. The
// three have to agree — the zod line declares the type the submit
// expression consumes and the prefill expression must produce — so one
// function owns all three.
func finalizePageField(pf *PageField, constraints map[string]*FieldConstraints) {
	// A checkbox has no empty state for a rule to forbid.
	pf.Required = pf.TSType != "boolean" && requiredByRule(constraints[pf.ProtoName])
	applyZodChain(pf, constraints)
	attachTSConversions(pf)
	attachZodExpr(pf)
}

// attachTSConversions fills in the two TypeScript expressions the form
// pages need for a field: how a submitted zod value becomes the wire
// value (SubmitExpr) and how a fetched wire value seeds the input
// (PrefillExpr). Both are functions of the field's TypeScript type, so
// they live here — beside the projection that states it — rather than as
// a branch chain repeated across the Next.js and Vite create/edit
// templates. Six copies of that chain is how `repeated int64` came to
// submit number[] into a bigint[] field: `.map(Number)` was the only
// element conversion any of them knew.
func attachTSConversions(pf *PageField) {
	n := pf.Name
	switch {
	case pf.Type == "date":
		pf.SubmitExpr = fmt.Sprintf("values.%s ? timestampFromDate(new Date(values.%s)) : undefined", n, n)
		pf.PrefillExpr = fmt.Sprintf("toDatetimeLocal(item.%s)", n)
	case pf.IsEnum:
		// The zod value IS the protobuf-es TS enum (a nativeEnum pipe),
		// so the spread carries it unconverted — and the prefill is the
		// stored value as-is. Defaulting it (the old `?? <first value>`)
		// named the zero, which is not a state a row can be in.
		pf.PrefillExpr = fmt.Sprintf("item.%s", n)
	case pf.EmptyIsUnset():
		attachUnsetTSConversions(pf)
	case pf.IsRepeated:
		mapCall := ""
		if conv, _ := tsFromFormString(pf.TSType); conv != "" {
			mapCall = fmt.Sprintf(".map((s) => %s)", conv)
		}
		pf.SubmitExpr = fmt.Sprintf(`values.%s.split(",").map((s) => s.trim()).filter(Boolean)%s`, n, mapCall)
		pf.SubmitNote = "Repeated scalar: comma-separated text input → the array the RPC expects."
		if back := tsToFormString(pf.TSType); back != "" {
			pf.PrefillExpr = fmt.Sprintf(`(item.%s ?? []).map((v) => %s).join(", ")`, n, back)
		} else {
			pf.PrefillExpr = fmt.Sprintf(`(item.%s ?? []).join(", ")`, n)
		}
	default:
		switch pf.TSType {
		case "bigint":
			// String in, String out: the digit text IS the integer, so
			// nothing rounds. Going through Number() here — which an
			// <input type="number"> would force — turns a stored
			// 9007199254740993 into 9007199254740992 on a save that
			// changed nothing.
			pf.SubmitExpr = fmt.Sprintf("BigInt(values.%s)", n)
			pf.PrefillExpr = fmt.Sprintf("String(item.%s ?? 0n)", n)
		case "number":
			pf.PrefillExpr = fmt.Sprintf("Number(item.%s ?? 0)", n)
		case "boolean":
			pf.PrefillExpr = fmt.Sprintf("Boolean(item.%s)", n)
		case "Uint8Array":
			pf.SubmitExpr = fmt.Sprintf("base64Decode(values.%s)", n)
			pf.PrefillExpr = fmt.Sprintf("base64Encode(item.%s ?? new Uint8Array())", n)
		default:
			pf.PrefillExpr = fmt.Sprintf(`String(item.%s ?? "")`, n)
		}
	}
}

// attachUnsetTSConversions is attachTSConversions for an EmptyIsUnset
// field. Its zod value is `T | undefined` — the preprocess in attachZodExpr
// has already turned an empty input into undefined — so the submit
// expression must carry undefined through its conversion (BigInt(undefined)
// throws), and the prefill must leave a stored "absent" absent instead of
// seeding the zero (`Number(item.year ?? 0)` is how an untouched Save wrote
// year 0 over NULL).
func attachUnsetTSConversions(pf *PageField) {
	n := pf.Name
	switch pf.TSType {
	case "bigint":
		pf.SubmitExpr = fmt.Sprintf("values.%s === undefined ? undefined : BigInt(values.%s)", n, n)
		pf.PrefillExpr = fmt.Sprintf(`String(item.%s ?? "")`, n)
	case "Uint8Array":
		pf.SubmitExpr = fmt.Sprintf("values.%s === undefined ? undefined : base64Decode(values.%s)", n, n)
		pf.PrefillExpr = fmt.Sprintf(`item.%s ? base64Encode(item.%s) : ""`, n, n)
	case "number":
		pf.PrefillExpr = fmt.Sprintf("item.%s", n)
	default:
		pf.PrefillExpr = fmt.Sprintf(`item.%s ?? ""`, n)
	}
}

// emptyAsUnsetZod wraps a field's zod expression so an empty input
// validates as ABSENT: "" becomes undefined before the inner schema runs,
// and `.optional()` accepts it. The inner rules still apply to a value that
// is present, so an optional email is checked when typed and skipped when
// left blank — the same thing protovalidate does on the wire for an unset
// optional field.
func emptyAsUnsetZod(expr string) string {
	return fmt.Sprintf(`z.preprocess((v) => (v === "" ? undefined : v), %s.optional())`, expr)
}

// attachZodExpr builds the field's whole zod schema expression.
//
// `.min(1, "Required")` is a LENGTH check, so it is appended only where
// the zod value is a string. A required number carries no such suffix —
// on z.coerce.number() the same call would mean "at least 1", which is a
// different claim about the domain.
func attachZodExpr(pf *PageField) {
	// The emptiness check a Required field needs, unless the projected
	// chain already opens with the rule's own `.min(N)`.
	nonEmpty := ""
	if pf.Required && !pf.chainRequiresValue {
		nonEmpty = `.min(1, "Required")`
	}

	var expr string
	switch {
	case pf.IsEnum:
		// The select's value coerces to a number that must be a declared
		// member, so the submitted value IS the protobuf-es TS enum and
		// mutate({...values}) type-checks without a cast. The zero is never
		// an option; refusing it here is the zod mirror of the born CHECK,
		// for a row a schema forge did not birth still holds it.
		//
		// The `: boolean` is load-bearing. Without it TypeScript (5.5+)
		// infers the arrow as a type PREDICATE, zod's refine overload
		// narrows the field to the non-zero members, and the edit form's
		// prefill — the entity's plain enum type — no longer type-checks.
		expr = fmt.Sprintf("z.coerce.number().pipe(z.nativeEnum(%s))", pf.EnumType)
		if pf.EnumZeroRef != "" {
			expr += fmt.Sprintf(`.refine((v): boolean => v !== %s, "Choose a value")`, pf.EnumZeroRef)
		}
	case pf.Type == "date", pf.IsRepeated:
		// A datetime-local value and a comma-separated list are both plain
		// text; the conversion happens in the submit handler.
		expr = "z.string()" + nonEmpty + pf.ZodChain
	default:
		base, allowChain := tsZodBase(pf.TSType)
		switch {
		case pf.TSType == "boolean":
			expr = base // a checkbox is never "required"; unchecked is false
		case pf.TSType == "number":
			expr = base + pf.ZodChain
		case !allowChain:
			// bigint and bytes: the zod value is a TEXT ENCODING of the
			// field, so only the emptiness check transfers.
			expr = base + nonEmpty
		default:
			expr = base + nonEmpty + pf.ZodChain
		}
	}
	if pf.EmptyIsUnset() {
		expr = emptyAsUnsetZod(expr)
	}
	pf.ZodExpr = expr
}

// HasCreateBytesFields / HasUpdateBytesFields report whether a form
// carries any `bytes`-typed field. A bytes column is edited as base64 —
// the encoding it already has on the wire — so those forms import
// protobuf-es's own base64 codec; every other form must not, or the
// pristine scaffold ships an unused import.
func (p PageTemplateData) HasCreateBytesFields() bool { return anyBytesField(p.CreateFields) }

// HasUpdateBytesFields is HasCreateBytesFields for the edit form.
func (p PageTemplateData) HasUpdateBytesFields() bool { return anyBytesField(p.UpdateFields) }

func anyBytesField(fields []PageField) bool {
	for _, f := range fields {
		if f.TSType == "Uint8Array" {
			return true
		}
	}
	return false
}

// isFieldMaskField reports whether a message field is an AIP-134
// google.protobuf.FieldMask (by referenced type, with a name fallback for
// older descriptors that don't carry MessageType).
func isFieldMaskField(f MessageFieldDef) bool {
	if f.MessageType == "google.protobuf.FieldMask" || strings.HasSuffix(f.MessageType, ".FieldMask") || f.MessageType == "FieldMask" {
		return true
	}
	return f.MessageType == "" && f.Name == "update_mask"
}

// localMessageName strips any package qualifier from a referenced message
// name ("services.tasks.v1.Task" → "Task") so it can key svc.Messages,
// which indexes by local name.
func localMessageName(messageType string) string {
	if idx := strings.LastIndex(messageType, "."); idx >= 0 {
		return messageType[idx+1:]
	}
	return messageType
}

// enumLikeNameFragments mirrors the isEnumLike heuristic in the emitted
// format-utils.ts: string fields whose names suggest a closed value set
// render as status badges.
var enumLikeNameFragments = []string{
	"status", "type", "kind", "role", "state", "category", "priority", "level",
}

// isEnumLikeFieldName reports whether a STRING field's name suggests a closed
// value set, so the cell renders as a badge rather than plain text.
//
// The match is on whole snake_case SEGMENTS, not a substring. `strings.Contains`
// badged `billing_state` — a US state, free text with fifty values — because it
// contains "state", and the emitted cell then handed a postal abbreviation to
// StatusBadge with no enum behind it. Segment matching keeps `job_state` and
// `payment_status` while dropping `billing_state`, `real_estate_id` and
// `statement_id`.
//
// A false positive here is worse than a miss: a badge asserts "this is one of a
// known set", and the page has to be hand-corrected to walk it back. A missed
// badge just renders as text, which is what the field already was.
func isEnumLikeFieldName(name string) bool {
	for _, seg := range strings.Split(strings.ToLower(name), "_") {
		for _, frag := range enumLikeNameFragments {
			if seg == frag {
				return true
			}
		}
	}
	return false
}

// listColumnIdentityFields are the identity/audit columns a LIST page omits by
// default. They stay on the DETAIL page, where "when was this created" is a
// real question a reader asks about one record — and so they are omitted only
// when there IS a detail page (EmitsDetailPage); otherwise they would be
// reachable nowhere in the UI.
//
// This is the same judgement the existing deleted_at skip already makes —
// machinery, not data — applied to the fields every measured run deleted by
// hand. In a table they are pure noise: the primary key is already the row's
// link target, and created/updated timestamps push the columns that identify
// the record off the visible width. A measured 15-entity app generated 12
// columns per list and every one of its 30 pages was rewritten; three of those
// twelve were these.
//
// It is a DEFAULT, not a rule. The born page is user-owned from birth, so
// adding the column back is one line in a file forge never overwrites.
var listColumnIdentityFields = map[string]bool{
	"id":          true,
	"created_at":  true,
	"create_time": true,
	"updated_at":  true,
	"update_time": true,
}

func listColumnExcluded(fieldName string) bool {
	return listColumnIdentityFields[strings.ToLower(fieldName)]
}

// GuardedPageField is one column the scaffolded edit page refuses to write
// because a custom RPC owns it (`// forge:guards <table>.<column>`), plus
// the name of that RPC.
//
// GuardedBy is the load-bearing half. The column could simply have been
// dropped — it is off the update_mask either way — but a field that is
// merely absent reproduces the discoverability failure the marker exists to
// close: the author who lowered an invoice's total below its collected
// amount and got a 500 learns nothing from a form control that is not
// there. A disabled row reading "managed by RecordPayment" is a pointer to
// the correct API at the one moment the user is looking for it, and it costs
// a row on a form that already shows every sibling column.
type GuardedPageField struct {
	// Name is the camelCase accessor the detail row reads off the entity.
	Name string
	// Label is the humanized column name ("Amount Paid Cents").
	Label string
	// ProtoName is the snake_case column — the update_mask path this field
	// is deliberately NOT contributing.
	ProtoName string
	// GuardedBy is the RPC that owns the column's writes ("RecordPayment").
	GuardedBy string
}

// guardedColumns indexes every `<table>.<column>` the service's RPCs declare
// with ProtoMarkerGuards, mapped to the RPC that declared it.
//
// The marker sits on a FIELD of a request message, but the thing it says is
// about the enclosing RPC — "this call owns that column" — so the walk is
// over methods, not over messages: a request message reached some other way
// would carry the marker with no rpc to name, and a row reading "managed by
// (unknown)" is worse than the raw write it replaced.
//
// Two RPCs guarding one column keeps the FIRST in method order. That is
// arbitrary but deterministic, and the alternative — naming neither — turns
// the strongest case (a column so protected that two calls mediate it) into
// the one that gets no guidance.
func guardedColumns(svc ServiceDef) map[string]string {
	guards := map[string]string{}
	for _, m := range svc.Methods {
		fq := m.InputTypeFQ
		if fq == "" {
			continue
		}
		for _, f := range svc.Schemas[fq] {
			for _, target := range f.Guards {
				if _, taken := guards[target]; !taken {
					guards[target] = m.Name
				}
			}
		}
	}
	return guards
}

// attachGuardedFields moves every guarded column off the edit form and onto
// the page's read-only guarded list.
//
// The removal is from UpdateFields specifically because UpdateFields IS the
// update_mask — both edit templates render `paths: [...]` straight off it —
// so a guarded field left in the slice is not a cosmetic problem but a
// guaranteed raw write that bypasses whatever the RPC enforces.
//
// Create is deliberately untouched. A guard is a rule about TRANSITIONS
// (RecordPayment refuses an overpayment, ScheduleJob refuses a double
// booking); the initial value is the author's to set, and stripping it from
// Create would leave a NOT NULL column with nothing to fill it.
//
// A guarded column that is also `forge:read-only` was never on the form, so
// there is nothing to remove — but it still gets its row. Read-only is the
// API refusing the write; the guard is the page saying who CAN make it. The
// two compose, and together they are the right shape for a lifecycle column.
func attachGuardedFields(page *PageTemplateData, tableName string, guards map[string]string) {
	if page == nil || tableName == "" || len(guards) == 0 {
		return
	}
	kept := page.UpdateFields[:0]
	for _, f := range page.UpdateFields {
		rpc, guarded := guards[tableName+"."+f.ProtoName]
		if !guarded {
			kept = append(kept, f)
			continue
		}
		page.GuardedFields = append(page.GuardedFields, GuardedPageField{
			Name: f.Name, Label: f.Label, ProtoName: f.ProtoName, GuardedBy: rpc,
		})
	}
	page.UpdateFields = kept
	for _, f := range page.readOnlyEditFields {
		if rpc, guarded := guards[tableName+"."+f.ProtoName]; guarded {
			page.GuardedFields = append(page.GuardedFields, GuardedPageField{
				Name: f.Name, Label: f.Label, ProtoName: f.ProtoName, GuardedBy: rpc,
			})
		}
	}
	page.HasGuardedFields = len(page.GuardedFields) > 0

	// Recompute what the edit form's IMPORT block is gated on. Both are
	// derived from the field list this function just shortened, and a
	// stale `true` here ships the pristine scaffold with an import nothing
	// uses — which the generated frontend's own lint fails on, turning a
	// correct guard into a build break.
	page.UpdateEnumImports = collectEnumImports(page.UpdateFields)
	page.HasDateUpdateFields = false
	for _, f := range page.UpdateFields {
		if f.Type == "date" {
			page.HasDateUpdateFields = true
		}
	}
}

// AttachEntityMeta enriches a PageTemplateData with typed field metadata
// from the matched proto entity definition. The page generator calls this
// after pairing a CRUD RPC group with its EntityDef — the same pairing
// that gates page emission — so templates can emit fully typed columns,
// search fields, and detail rows. svc supplies the deep type graph needed to
// resolve enum COLUMNS to their protobuf-es TS type (which the badge cell
// passes to StatusBadge); an unresolvable enum column degrades to the
// enum-like string path (String(item.field)).
func AttachEntityMeta(page *PageTemplateData, entity EntityDef, svc ServiceDef) {
	// Columns a custom RPC owns come off the edit form BEFORE the detail
	// metadata below is derived, so the guarded rows are already parked on
	// GuardedFields when the template asks for them.
	attachGuardedFields(page, entity.TableName, guardedColumns(svc))

	importSource := entity.ProtoFile
	if importSource == "" {
		// Entity declared in the service's proto file.
		page.EntityTypeImportPath = page.TypesImportPath
	} else {
		page.EntityTypeImportPath = "@/gen/" + ProtoFileToTSImportPath(importSource)
	}

	if key, ok := EntityWireKey(entity); ok {
		page.PkFieldCamel = fieldNameToCamel(key)
	} else {
		page.PkFieldCamel = fieldNameToCamel(entity.PkField)
	}
	if page.PkFieldCamel == "" {
		page.PkFieldCamel = "id"
	}

	// Sibling currency field (if any) — money columns render currency-aware
	// against it. Resolved once per entity, not per money column.
	currencyField := entityCurrencyField(entity)

	for _, f := range entity.Fields {
		switch f.Kind {
		case FieldKindMessage, FieldKindMap, FieldKindRepeatedMessage:
			// Nested structures don't render in a table cell / detail row.
			continue
		}
		// The soft-delete column is machinery, not data: generated reads
		// filter `deleted_at IS NULL`, so the cell is always empty — a
		// dead column that makes the UI look broken. A deleted_at declared
		// `forge:no-soft-delete` is data (an archive stamp on rows the
		// list does show), so it gets its column like any other.
		if (f.Name == "deleted_at" && entity.SoftDelete) || f.Name == "delete_time" {
			continue
		}

		camel := fieldNameToCamel(f.Name)
		// A badge shows ONE value: <StatusBadge value=…> takes a
		// `string | number`, so a repeated enum handed to it is a type
		// error on both the list and the detail page. Repeated columns
		// render through formatValue like any other array.
		repeated := isRepeatedEntityField(f)
		isBadge := (f.Kind == FieldKindEnum && !repeated) ||
			(f.ProtoType == "string" && !repeated && isEnumLikeFieldName(f.Name))
		if isBadge {
			page.HasBadgeColumns = true
		}
		isMoney := isMoneyFieldName(f.Name) && isNumericProtoScalar(f.ProtoType)
		if isMoney {
			page.HasMoneyColumns = true
		}
		col := EntityPageField{
			Name:    camel,
			Label:   fieldNameToLabel(f.Name),
			IsBadge: isBadge,
			IsMoney: isMoney,
		}
		// A money column renders currency-aware only when its entity has a
		// sibling currency field; otherwise the cell keeps the USD default.
		if isMoney {
			col.CurrencyField = currencyField
		}
		// Real protobuf enum column: resolve its TS type + import so the badge
		// cell can hand the enum object to StatusBadge. The FQ
		// enum name rides on EntityField.MessageType (schemaFieldToEntityField
		// records it). Unresolvable (cross-package / old descriptor) → leave
		// EnumType empty and fall back to the String() path.
		if f.Kind == FieldKindEnum && !repeated && f.MessageType != "" {
			if meta, ok := resolveFormEnum(svc, entity.Name, f.MessageType); ok {
				col.EnumType = meta.TSType
				col.EnumImportPath = meta.ImportPath
			}
		}
		page.Columns = append(page.Columns, col)
		// The identity/audit columns are dropped from the list only
		// because the detail page shows them. Without a detail page the
		// list is the record's only view, so they stay.
		if !page.EmitsDetailPage() || !listColumnExcluded(f.Name) {
			page.ListColumns = append(page.ListColumns, col)
		}

		if f.ProtoType == "string" {
			page.SearchFields = append(page.SearchFields, camel)
		}
	}

	page.DisplayField = deriveDisplayField(entity)

	// Enum-column import lines (detail page) + the merged set the list page
	// needs (filter <select>s + badge columns, deduped so a field that is both
	// a filter and a column imports its enum exactly once).
	page.ColumnEnumImports = collectColumnEnumImports(page.Columns)
	page.ListEnumImports = mergeEnumImports(page.ListFilterEnumImports, page.ColumnEnumImports)
}

// deriveDisplayField picks the string column that is this entity's HUMAN NAME
// — what a detail page is titled with, what a breadcrumb reads, and what the
// foreign-key picker labels its options with. "" when the entity has none, in
// which case every one of those falls back to the primary key.
//
// The fallback is the whole reason this is ranked rather than a two-name
// equality test. `name` and `title` alone meant an entity spelling it
// `full_name` — the single most common spelling for a person — got NOTHING,
// so its picker listed forty ULIDs and nobody could pick a row. Widening the
// rule fixes the picker, the detail title and the breadcrumb at once, because
// all three read this one field.
//
// The set is deliberately closed to spellings that name the WHOLE label:
//
//	name · title · display_name · full_name · <entity>_name · <entity>_title
//
// `first_name` is excluded on purpose. It is human-readable, which is exactly
// what makes it dangerous: a picker showing forty rows called "John" looks
// like it works and silently drops the surname, where a ULID at least
// announces that the generator could not tell. An entity outside this set
// keeps the id and the emitted call site carries the comment naming the prop
// to retarget — a visible gap the user closes in one line.
func deriveDisplayField(entity EntityDef) string {
	self := naming.ToSnakeCase(entity.Name)
	ranked := []string{
		"name",
		"title",
		"display_name",
		"full_name",
		self + "_name",
		self + "_title",
	}
	strs := map[string]bool{}
	for _, f := range entity.Fields {
		if f.ProtoType == "string" && f.Kind != FieldKindEnum {
			strs[strings.ToLower(f.Name)] = true
		}
	}
	for _, want := range ranked {
		if strs[want] {
			return fieldNameToCamel(want)
		}
	}
	return ""
}

// isMoneyFieldName reports whether a proto field name denotes an integer
// minor-unit (cents) money column — the "_cents" suffix convention. The
// camelCase form ("priceCents") derives from this snake_case name, so the
// snake suffix is the single source of truth.
func isMoneyFieldName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "_cents")
}

// entityCurrencyField returns the camelCase name of the entity's sibling
// ISO-4217 currency field ("currency" or "currency_code"), or "" when the
// entity has none. Money columns pass it to formatMoneyCents so an amount
// renders in its row's own currency instead of a hardcoded USD. The field
// must be a plain string scalar — an enum/message named "currency" isn't a
// currency-code string the Intl formatter accepts.
func entityCurrencyField(entity EntityDef) string {
	for _, f := range entity.Fields {
		if f.ProtoType != "string" {
			continue
		}
		switch strings.ToLower(f.Name) {
		case "currency", "currency_code":
			return fieldNameToCamel(f.Name)
		}
	}
	return ""
}

// collectColumnEnumImports aggregates the enum-typed badge columns' import
// needs into sorted, deduped import lines (mirrors collectEnumImports).
func collectColumnEnumImports(cols []EntityPageField) []PageEnumImport {
	fields := make([]PageField, 0, len(cols))
	for _, c := range cols {
		if c.EnumType == "" || c.EnumImportPath == "" {
			continue
		}
		fields = append(fields, PageField{IsEnum: true, EnumType: c.EnumType, EnumImportPath: c.EnumImportPath})
	}
	return collectEnumImports(fields)
}

// mergeEnumImports unions two enum-import lists, deduping by path+type so the
// same enum imported for both a filter and a column yields ONE import line.
func mergeEnumImports(a, b []PageEnumImport) []PageEnumImport {
	byPath := map[string]map[string]bool{}
	for _, imp := range append(append([]PageEnumImport{}, a...), b...) {
		if byPath[imp.Path] == nil {
			byPath[imp.Path] = map[string]bool{}
		}
		for _, t := range imp.Types {
			byPath[imp.Path][t] = true
		}
	}
	if len(byPath) == 0 {
		return nil
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]PageEnumImport, 0, len(paths))
	for _, p := range paths {
		types := make([]string, 0, len(byPath[p]))
		for t := range byPath[p] {
			types = append(types, t)
		}
		sort.Strings(types)
		out = append(out, PageEnumImport{Path: p, Types: types})
	}
	return out
}

// PascalToKebab converts PascalCase to kebab-case, respecting Go
// initialisms (LLM, API, URL, JSON, …) so that "LLMGateway" produces
// "llm-gateway" rather than "l-l-m-gateway".
//
// Thin wrapper around naming.ToKebabCase — kept here for backwards
// compatibility with existing callers (frontend_pages, frontend_mocks,
// related tests). New code should call naming.ToKebabCase directly.
func PascalToKebab(s string) string {
	return naming.ToKebabCase(s)
}

// EmitsDetailPage reports whether the detail page (`/<slug>/<id>`) is
// scaffolded. It is the ONE answer to "does a record have a page of its own":
// the page generator writes the detail route on it, and every other page that
// links to a record — the list's row click, the create page's landing — asks
// it too. A link built on any other condition is a 404 the moment the service
// has a List RPC but no Get RPC.
func (p PageTemplateData) EmitsDetailPage() bool { return p.HasGet }

// EmitsEditPage reports whether the edit page can be scaffolded. The page loads
// the record through the entity's Get hook before it renders the form, so an
// entity served by an Update RPC but no Get RPC has nothing to load from — the
// template would emit `use({ id })` with an empty hook name. No Get, no edit page.
func (p PageTemplateData) EmitsEditPage() bool { return p.HasUpdate && p.HasGet }
