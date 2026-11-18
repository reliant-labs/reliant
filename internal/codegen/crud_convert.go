package codegen

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// proto<->entity conversion generation.
//
// Entity structs are projections of the APPLIED schema (time.Time,
// pointers for nullable columns, native slices); wire messages are the
// service-proto truth (timestamppb, wrappers, repeated fields). The
// CRUD ops file carries one generated conversion pair per entity —
// <entity>ToProto / <entity>FromProto — mapping the intersection of
// wire fields and columns by name. Wire-only fields never reach the
// database; column-only fields never leak onto the wire.

// EntityConvTemplateData renders one entity's conversion pair.
type EntityConvTemplateData struct {
	EntityName string // "Item" — the db.<Entity> struct
	// EntityPbType is the generated pb message type: EntityName in
	// protoc-gen-go's casing ("Oauth2token" → Oauth2Token).
	EntityPbType     string
	EntityLower      string   // "item"
	ToProtoAssigns   []string // statements: "m.Name = e.Name"
	FromProtoAssigns []string
}

// UnmappedField names one (wire field, column) pair the conversion
// generator cannot map in a direction that round-trips.
//
// Such a pair is DEAD over the API: the column exists, the wire field
// exists, and nothing carries a value between them — the field is created
// as the column DEFAULT forever and never read back. The generator used to
// emit an explanatory COMMENT in the generated body for exactly this case,
// which is a guard that cannot fire: the build stays green, the tests stay
// green, and the only way to learn about it is to read generated code
// nobody reads. (Measured: a `repeated OrderLineItem line_items` over a
// JSONB column — Create accepted line items and stored `[]`, List returned
// none, forever.) Every one of these now fails the generate instead; see
// UnmappedFieldsError.
type UnmappedField struct {
	Message  string    // wire message name ("Order")
	Field    string    // proto field name ("line_items")
	Kind     FieldKind // wire kind ("repeated_message")
	Table    string    // table name ("orders")
	Column   string    // column name ("line_items")
	DeclType string    // declared SQL type ("JSONB")
	Reason   string    // why the pairing has no conversion
}

// UnmappedFieldsError renders a set of unmappable pairings as the error
// `forge generate` fails with. nil for an empty set. Every pair is listed
// rather than just the first, so one run tells the author about every dead
// field instead of one per regenerate.
func UnmappedFieldsError(fields []UnmappedField) error {
	if len(fields) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d proto field(s) have a column but no conversion, so they would be silently dropped:\n", len(fields))
	for _, f := range fields {
		fmt.Fprintf(&b, "  - %s.%s (%s) <-> %s.%s %s: %s\n",
			f.Message, f.Field, f.Kind, f.Table, f.Column, f.DeclType, f.Reason)
	}
	b.WriteString("Change the column type or the proto field so the pairing is one forge maps, " +
		"or drop one side. A field forge cannot map is dead over the API.")
	return errors.New(b.String())
}

// entityConvLower is the prefix of an entity's generated conversion pair,
// `<lower>ToProto` / `<lower>FromProto` (EntityConvTemplateData.EntityLower).
// One definition, because crudOpsDemand looks those names up in user code.
func entityConvLower(entityName string) string {
	return strings.ToLower(entityName)
}

// BuildEntityConv builds the conversion data for one entity, plus every
// (wire field, column) pair it could not map.
func BuildEntityConv(svc ServiceDef, entity EntityDef) (EntityConvTemplateData, []UnmappedField) {
	conv := EntityConvTemplateData{
		EntityName:   entity.Name,
		EntityPbType: naming.GoCamelCase(entity.Name),
		EntityLower:  entityConvLower(entity.Name),
	}
	var unmapped []UnmappedField
	colByName := map[string]EntityColumn{}
	for _, c := range entity.Columns {
		colByName[c.Name] = c
	}
	enumRepeated := enumRepeatedWireFields(svc, entity.Name)
	for _, wf := range entity.Fields {
		wf = promoteOptionalScalar(wf)
		wf = promoteEnum(wf, svc, enumRepeated[wf.Name])
		col, ok := colByName[wf.Name]
		if !ok {
			conv.ToProtoAssigns = append(conv.ToProtoAssigns,
				fmt.Sprintf("// %s: wire-only field (no %q column in the applied schema)", wf.GoName, wf.Name))
			continue
		}
		// forge:secret — the column is real and stays WRITABLE (the
		// FromProto/create path below maps it), but the read path never packs
		// it: Get/List responses omit the value, and it is never logged via a
		// packed message. Sensitive columns (credentials, tokens) are stripped
		// from reads without the app author hand-writing the skip.
		if wf.Secret {
			from, why := assignToDB("e", "m", wf, col)
			if why != "" {
				unmapped = append(unmapped, unmappedField(entity, wf, col, why))
				continue
			}
			conv.ToProtoAssigns = append(conv.ToProtoAssigns,
				fmt.Sprintf("// %s: forge:secret — omitted from read responses (never packed onto the wire)", wf.GoName))
			conv.FromProtoAssigns = append(conv.FromProtoAssigns, from)
			continue
		}
		to, whyTo := assignToProto("m", "e", wf, col)
		from, whyFrom := assignToDB("e", "m", wf, col)
		if whyTo != "" || whyFrom != "" {
			why := whyTo
			if why == "" {
				why = whyFrom
			}
			unmapped = append(unmapped, unmappedField(entity, wf, col, why))
			continue
		}
		conv.ToProtoAssigns = append(conv.ToProtoAssigns, to)
		conv.FromProtoAssigns = append(conv.FromProtoAssigns, from)
	}
	return conv, unmapped
}

func unmappedField(entity EntityDef, wf EntityField, col EntityColumn, reason string) UnmappedField {
	return UnmappedField{
		Message:  entity.Name,
		Field:    wf.Name,
		Kind:     wf.Kind,
		Table:    entity.TableName,
		Column:   col.Name,
		DeclType: col.DeclType,
		Reason:   reason,
	}
}

// ConvNeedsTimestamppb reports whether any assignment uses timestamppb.
// Comment-only entries (the wire-only and forge:secret explanations) are
// ignored: a comment naming *timestamppb.Timestamp must not pull in an
// import no code uses.
func ConvNeedsTimestamppb(convs []EntityConvTemplateData) bool {
	for _, c := range convs {
		for _, a := range c.ToProtoAssigns {
			if strings.HasPrefix(a, "//") {
				continue
			}
			if strings.Contains(a, "timestamppb.") {
				return true
			}
		}
	}
	return false
}

// ConvNeedsFmt reports whether any conversion emits a guard that calls
// fmt.Errorf — the corrupt-enum read, or either half of a json/jsonb
// pairing. The ops file imports fmt only then; otherwise the import would
// be unused. (goimports would also correct this post-write, but gating it
// keeps the rendered file correct on its own.)
func ConvNeedsFmt(convs []EntityConvTemplateData) bool {
	return convsContain(convs, "fmt.Errorf")
}

// ConvNeedsTime reports whether any conversion reaches the stdlib `time`
// package — today only the legacy-TEXT timestamp pairing, which formats and
// parses time.RFC3339Nano. Gated for the same reason as ConvNeedsFmt: an
// entity with no such column must not pull in the import.
func ConvNeedsTime(convs []EntityConvTemplateData) bool {
	return convsContain(convs, timeImportToken)
}

// timeImportToken is the substring that marks a generated assignment as
// needing the `time` import. Both halves of the legacy-TEXT timestamp
// pairing spell it, and no other conversion does — timestamppb.New is a
// different package and does not match.
const timeImportToken = "time.RFC3339Nano"

// ConvNeedsORM reports whether any conversion calls into pkg/orm — today
// the json/jsonb encoders. Gated for the same reason as ConvNeedsFmt: an
// entity with no json column must not pull in the import.
func ConvNeedsORM(convs []EntityConvTemplateData) bool {
	return convsContain(convs, "orm.")
}

// convsContain reports whether any generated assignment, in either
// direction, contains tok. Comment-only entries (the wire-only and
// forge:secret explanations) are ignored: a comment naming a package must
// not pull in an import no code uses.
func convsContain(convs []EntityConvTemplateData, tok string) bool {
	for _, c := range convs {
		for _, a := range append(append([]string{}, c.ToProtoAssigns...), c.FromProtoAssigns...) {
			if strings.HasPrefix(a, "//") {
				continue
			}
			if strings.Contains(a, tok) {
				return true
			}
		}
	}
	return false
}

// AssignsContain reports whether any of the create-path assignments
// contains tok. The create closure is built per METHOD, not per entity, so
// its import needs are gated separately from the conversion pair's.
func AssignsContain(assigns []string, tok string) bool {
	for _, a := range assigns {
		if strings.HasPrefix(a, "//") {
			continue
		}
		if strings.Contains(a, tok) {
			return true
		}
	}
	return false
}

// promoteOptionalScalar rewrites an explicit-presence (`optional`)
// proto3 scalar wire field to its pointer shape: protobuf-go generates
// a *T struct field for optional scalars, so conversions must pair
// them exactly like wrapper fields (nil-safe pointer handling).
// Treating them as plain scalars emitted `&v` of an already-pointer
// (**T) toward the DB and a bare value into a *T wire field toward the
// proto — neither compiles. Non-scalar kinds (timestamp, message,
// repeated) pass through untouched: their Go shape is already a
// pointer/slice and their conversion paths handle presence.
func promoteOptionalScalar(f EntityField) EntityField {
	if f.Optional && f.Kind == FieldKindScalar {
		f.GoType = "*" + f.GoType
		f.Kind = FieldKindWrapper
	}
	return f
}

// promoteEnum rewrites an enum wire field's GoType to its concrete
// protobuf-go shape, qualified through the generated pb package the ops
// file already imports: `pb.<Enum>` plain, `*pb.<Enum>` for explicit
// presence (`optional`), `[]pb.<Enum>` for `repeated`. Entity birth
// stores enum columns as TEXT holding the proto enum VALUE NAMES (the
// CHECK (col IN (...)) vocabulary), so the conversion pairing is
// name<->number and needs the concrete Go type to spell the
// pb.<Enum>_value lookup. Without this promotion the raw EntityField
// carries only the bare proto short name ("OrderStatus") and every enum
// field fell to the unmapped default — the field was dead over the API
// (created as UNSPECIFIED forever, updates writing "" into the CHECK).
//
// Cross-package enums (whose columns birth TODO-skips — they cannot be
// referenced through the single pb import) and legacy descriptors that
// carry no enum type name stay untouched: their GoType keeps no "pb."
// marker, so the assign functions report the pairing as unmappable. With
// no column — the normal case, since birth TODO-skips them — the field is
// simply wire-only; with a hand-written column it fails the generate,
// because a field forge cannot map is dead over the API.
func promoteEnum(f EntityField, svc ServiceDef, repeated bool) EntityField {
	if f.Kind != FieldKindEnum {
		return f
	}
	name, ok := enumInServicePackage(svc, f.MessageType)
	if !ok {
		return f
	}
	switch {
	case repeated:
		f.GoType = "[]pb." + name
	case f.Optional:
		f.GoType = "*pb." + name
	default:
		f.GoType = "pb." + name
	}
	return f
}

// enumInServicePackage is the Go identifier of an enum reachable through the
// service's own `pb` import, ok=false for any enum generated into a different
// Go package. The deciding fact is the enum's go_package (EnumGoRefs), not its
// proto package: two files of one proto package routinely generate into two Go
// packages, and a constant spelled `pb.X` for the second does not compile.
// Descriptors without EnumGoRefs fall back to the proto-package rule.
func enumInServicePackage(svc ServiceDef, fq string) (string, bool) {
	if ref, known := svc.EnumGoRefs[fq]; known {
		if ref.ImportPath != svc.GoPackage {
			return "", false
		}
		return ref.GoName, true
	}
	return enumWireGoName(svc.Package, fq)
}

// enumWireGoName maps a fully-qualified SAME-PACKAGE enum name to its
// protoc-gen-go identifier (naming.GoTypeName): top-level
// "orders.v1.OrderStatus" → "OrderStatus"; nested "orders.v1.Order.Status"
// → "Order_Status"; "orders.v1.Oauth2provider" → "Oauth2Provider".
// ok=false for cross-package or unnamed enums — those cannot be
// referenced through the pb import and stay unmapped.
func enumWireGoName(protoPkg, fq string) (string, bool) {
	if protoPkg == "" || !strings.HasPrefix(fq, protoPkg+".") {
		return "", false
	}
	return naming.GoTypeName(protoPkg, fq), true
}

// enumWireShape decodes the concrete wire shape promoteEnum stamped onto
// an enum field's GoType. ok=false when the enum was never resolved to a
// same-package pb type (cross-package reference, or a legacy descriptor
// without the deep schema) — such fields have no conversion.
func enumWireShape(wf EntityField) (pbType string, repeated, optional, ok bool) {
	t := wf.GoType
	switch {
	case strings.HasPrefix(t, "[]pb."):
		return strings.TrimPrefix(t, "[]"), true, false, true
	case strings.HasPrefix(t, "*pb."):
		return strings.TrimPrefix(t, "*"), false, true, true
	case strings.HasPrefix(t, "pb."):
		return t, false, false, true
	}
	return "", false, false, false
}

// enumRepeatedWireFields reads the entity wire message's deep schema for
// the `repeated` cardinality of its enum fields. EntityField collapses a
// repeated enum to the bare enum kind (GoType keeps no slice marker), but
// the conversion shape differs entirely: TEXT[] of value names versus a
// single name. Nil on legacy descriptors without the deep Schemas map —
// enum fields then stay unmapped (safe: no code that could miscompile).
func enumRepeatedWireFields(svc ServiceDef, entityName string) map[string]bool {
	defs, ok := svc.Schemas[svc.Package+"."+entityName]
	if !ok {
		return nil
	}
	var out map[string]bool
	for _, d := range defs {
		if d.Kind == "enum" && d.Repeated {
			if out == nil {
				out = map[string]bool{}
			}
			out[d.Name] = true
		}
	}
	return out
}

// dbBaseGoType is the NOT NULL Go type of a column on the entity struct —
// the SAME projection internal/generator declares the struct field with, so
// a pairing this file blesses is one the compiler can typecheck.
func dbBaseGoType(col EntityColumn) string {
	return ColumnGoType(col.Type, col.DeclType, col.IsArray)
}

// dbNullable reports whether the column maps to a pointer struct field: a
// nullable column, or a NOT NULL one whose DEFAULT a nil leaves to the
// database (ColumnInsertDefault).
func dbNullable(col EntityColumn) bool {
	if col.IsArray || col.Type == "bytes" || col.Type == "json" {
		return false
	}
	return (!col.NotNull && !col.IsPK) || columnInsertDefault(col) == InsertDefaultPointer
}

// columnInsertDefault is ColumnInsertDefault for an EntityColumn.
func columnInsertDefault(col EntityColumn) InsertDefault {
	return ColumnInsertDefault(InsertDefaultColumn{
		Canonical: col.Type, IsArray: col.IsArray, NotNull: col.NotNull, IsPK: col.IsPK,
		Generated: col.IsGenerated, Version: col.Version, Default: col.Default,
		ZeroRejected: col.ZeroRejected,
	})
}

// jsonVarName spells the local the write path binds the marshalled
// document to, before assigning it onto the entity struct: "LineItems" ->
// "lineItemsJSON". Wire field names are unique within a message, so the
// locals never collide, and the JSON suffix keeps them clear of the
// single-letter names the surrounding conversions use (e, m, v, t, sv).
func jsonVarName(goName string) string {
	if goName == "" {
		return "valueJSON"
	}
	return strings.ToLower(goName[:1]) + goName[1:] + "JSON"
}

// jsonbPair names the pkg/orm encoder pair for a wire kind stored in a
// json/jsonb column, or "" when the kind has no round-tripping pairing.
//
// The pairings are the JSON shapes a proto field can hold: a repeated
// message is an array of objects, a singular message is an object, a
// repeated scalar is an array of scalars, and a map of scalars is an
// object of scalars. Everything else falls through and fails the generate.
//
// A plain `string` wire field is NOT here: over a json column it is the
// document itself, and the scalar branch already assigns it straight
// across. That is the passthrough an app uses when it owns the JSON.
func jsonbPairable(wf EntityField) bool {
	m, _ := jsonbPair(wf)
	return m != ""
}

func jsonbPair(wf EntityField) (marshal, unmarshal string) {
	switch wf.Kind {
	case FieldKindRepeatedMessage:
		return "orm.MarshalJSONBList", "orm.UnmarshalJSONBList"
	case FieldKindMessage:
		return "orm.MarshalJSONBMessage", "orm.UnmarshalJSONBMessage"
	case FieldKindRepeatedScalar:
		return "orm.MarshalJSONBScalars", "orm.UnmarshalJSONBScalars"
	case FieldKindMap:
		// Only a map of SCALARS. A map of messages would store protobuf
		// struct internals through encoding/json, and a map of enums would
		// store wire NUMBERS where every other enum column in a forge app
		// stores value names — a renumber would then silently reinterpret
		// stored rows. Entity birth TODO-skips both rather than creating a
		// column for them, so this refusal and that one are the same rule.
		if isScalarProtoKind(wf.MapValueKind) {
			return "orm.MarshalJSONBMap", "orm.UnmarshalJSONBMap"
		}
	}
	return "", ""
}

// isScalarProtoKind reports the proto kinds whose JSON encoding Go's own
// encoding/json produces correctly, with no proto-specific rules to apply.
//
// That is every scalar kind and only those, so it reads the closed table
// rather than restating its fifteen names: the set this asks about and the
// set the projections answer for are the same set, by construction.
func isScalarProtoKind(kind string) bool {
	return IsProtoScalarKind(kind)
}

// isJSONColumn reports a scalar json/jsonb column — the shape that holds a
// JSON document in a single value. A json ARRAY column (jsonb[]) is not one
// of these: its Go projection is []string, and a wire field over it has no
// pairing forge maps.
func isJSONColumn(col EntityColumn) bool { return col.Type == "json" && !col.IsArray }

// pbFieldRef / dbFieldRef spell one field of a conversion on each side.
// The pb message's field is protoc-gen-go's name (EntityField.GoName); the
// db struct's is forge's ORM name (naming.ColumnGoName). They agree for
// most names and differ exactly where a digit or an underscore before one
// sits ("base64item": m.Base64Item, e.Base64item), so a conversion that
// spells both sides one way references a field one of the structs lacks.
func pbFieldRef(v string, wf EntityField) string { return v + "." + wf.GoName }

func dbFieldRef(v string, col EntityColumn) string { return v + "." + naming.ColumnGoName(col.Name) }

// goTypeOverrideUnmapped is the reason a column declaring `forge:go-type`
// has no generated wire conversion, "" for a column without one. The
// declared type is the author's (an enum, a map, a domain type) and forge
// cannot see its shape at generate time, so pairing it with a wire field is
// refused by name rather than guessed at.
func goTypeOverrideUnmapped(col EntityColumn) string {
	if col.GoType == "" {
		return ""
	}
	return fmt.Sprintf("column %s declares forge:go-type=%s, which forge does not convert to or from the wire — "+
		"convert it in the owned shim (handlers_crud.go), or leave the field off the wire message", col.Name, col.GoType)
}

// assignJSONBToDB emits the write half of a json/jsonb pairing: marshal the
// wire value, then store the document (the entity field is a
// json.RawMessage, assigned from the encoder's text as bytes). A NULLABLE
// column stores SQL NULL for an absent value rather than a fabricated empty
// document, so "no value" and "empty value" stay distinguishable in the
// table.
func assignJSONBToDB(dst, src string, wf EntityField, col EntityColumn) string {
	d, s := dbFieldRef(dst, col), pbFieldRef(src, wf)
	marshal, _ := jsonbPair(wf)
	v := jsonVarName(wf.GoName)
	fail := fmt.Sprintf("return nil, fmt.Errorf(%q, err)", wf.Name+": %w")

	if !col.NotNull {
		// An absent message / empty list is SQL NULL. `%s != nil` reads the
		// same for a pointer message and for a nil slice.
		return fmt.Sprintf(
			"if %s != nil {\n\t\t%s, err := %s(%s)\n\t\tif err != nil {\n\t\t\t%s\n\t\t}\n\t\t%s = []byte(%s)\n\t}",
			s, v, marshal, s, fail, d, v)
	}
	return fmt.Sprintf(
		"%s, err := %s(%s)\n\tif err != nil {\n\t\t%s\n\t}\n\t%s = []byte(%s)",
		v, marshal, s, fail, d, v)
}

// assignJSONBToProto emits the read half: parse the stored document back
// onto the wire field. The destination is passed by ADDRESS so pkg/orm
// allocates the message (or the slice) only when the column holds one.
func assignJSONBToProto(dst, src string, wf EntityField, col EntityColumn) string {
	d, s := pbFieldRef(dst, wf), dbFieldRef(src, col)
	_, unmarshal := jsonbPair(wf)
	fail := fmt.Sprintf("return nil, fmt.Errorf(%q, err)", wf.Name+": %w")

	if !col.NotNull {
		return fmt.Sprintf(
			"if %s != nil {\n\t\tif err := %s(string(%s), &%s); err != nil {\n\t\t\t%s\n\t\t}\n\t}",
			s, unmarshal, s, d, fail)
	}
	return fmt.Sprintf(
		"if err := %s(string(%s), &%s); err != nil {\n\t\t%s\n\t}",
		unmarshal, s, d, fail)
}

// jsonTextPairing reports a plain string wire field (or its wrapper) over a
// json column: the app owns the document and passes its text straight
// through, now converted between the wire's string and the entity's
// json.RawMessage bytes.
func jsonTextPairing(wf EntityField, col EntityColumn) bool {
	return isJSONColumn(col) && ((wf.Kind == FieldKindScalar && wf.GoType == "string") ||
		(wf.Kind == FieldKindWrapper && wf.GoType == "*string"))
}

func assignJSONTextToDB(d, s string, wf EntityField, col EntityColumn) string {
	if wf.Kind == FieldKindWrapper {
		return fmt.Sprintf("if %s != nil {\n\t\t%s = []byte(*%s)\n\t}", s, d, s)
	}
	if !col.NotNull {
		// Unset is SQL NULL, not an empty (invalid) document.
		return fmt.Sprintf("if %s != \"\" {\n\t\t%s = []byte(%s)\n\t}", s, d, s)
	}
	return fmt.Sprintf("%s = []byte(%s)", d, s)
}

func assignJSONTextToProto(d, s string, wf EntityField) string {
	if wf.Kind == FieldKindWrapper {
		return fmt.Sprintf("if %s != nil {\n\t\tv := string(%s)\n\t\t%s = &v\n\t}", s, s, d)
	}
	return fmt.Sprintf("%s = string(%s)", d, s)
}

// legacyTextTimestamp reports the pairing of a google.protobuf.Timestamp
// wire field with a TEXT (canonical "string") column.
//
// This is NOT a shape entity birth emits — birth writes TIMESTAMPTZ for
// every Timestamp field. It is a shape forge ADOPTS: the applied schema is
// the storage truth (db/migrations, whoever wrote them), and a pre-forge
// table storing its timestamps as text is the case forge already decided to
// support everywhere else in the pipeline:
//
//   - schemadef.DetectConventions counts created_at/updated_at as MANAGED
//     when they are `time` OR `string` columns, explicitly so the generator
//     can stamp both;
//   - pkg/crud's writeStamp stamps a string column as time.RFC3339Nano;
//   - internal/generator projects the column to a string struct field and
//     records Timestamps: true on the repo Spec.
//
// Without this branch those three halves say "supported" while the
// conversion generator says "no conversion" — and after the pairing became
// a hard gate, a schema forge stamps at runtime could not be generated at
// all. RFC3339Nano is not a choice made here; it is the format pkg/crud
// writes, so the pairing has to read and write the same one.
func legacyTextTimestamp(wf EntityField, col EntityColumn) bool {
	return wf.Kind == FieldKindTimestamp && col.Type == "string" && !col.IsArray
}

// legacyTextTimestampToProto builds the read half of the legacy-TEXT
// timestamp pairing. d is the wire field ("m.CreatedAt"), s the entity
// field ("e.CreatedAt"), col the column name (for the error message).
//
// The three outcomes are DISTINCT, mirroring the enum read path:
//
//   - empty text          → the field stays absent (a NULL/unstamped row,
//     not corruption — an adopted TEXT timestamp column typically defaults
//     to the empty string, which is what an unstamped row holds);
//   - RFC3339Nano text    → the instant;
//   - non-empty garbage   → a loud error the ToProto helper returns and the
//     op turns into CodeInternal.
//
// Parsing silently to the zero time is what the comment this replaced did
// in effect: the field read back as unset and no one learned the column
// held something else.
func legacyTextTimestampToProto(d, s, col string, nullable bool) string {
	// value is the entity field dereferenced to its string; guard is the
	// "there is something stored here" test. A nullable column projects to
	// *string, so nil and "" are both absent.
	value, guard := s, fmt.Sprintf("%s != \"\"", s)
	if nullable {
		value = "*" + s
		guard = fmt.Sprintf("%s != nil && *%s != \"\"", s, s)
	}
	// %q on the format string emits it as a Go literal, so the verbs inside
	// reach the generated code instead of this Sprintf (same construction as
	// assignJSONBToDB's failure line).
	fail := fmt.Sprintf("return nil, fmt.Errorf(%q, %s, err)",
		"unparseable timestamp %q for column "+col+": %w", value)
	return fmt.Sprintf(
		"if %s {\n\t\tt, err := time.Parse(time.RFC3339Nano, %s)\n\t\tif err != nil {\n\t\t\t%s\n\t\t}\n\t\t%s = timestamppb.New(t)\n\t}",
		guard, value, fail, d)
}

// assignToDB emits "dst.<X> = ..." converting a wire field onto the
// entity struct. src is the wire message variable. The second return is
// the REASON the pairing has no conversion, or "" when it mapped — an
// unmappable pairing fails the generate (see UnmappedField); it never
// ships as a comment.
func assignToDB(dst, src string, wf EntityField, col EntityColumn) (string, string) {
	if why := goTypeOverrideUnmapped(col); why != "" {
		return "", why
	}
	d, s := dbFieldRef(dst, col), pbFieldRef(src, wf)
	base := dbBaseGoType(col)
	nullable := dbNullable(col)

	switch {
	// json/jsonb column: the entity field is the document TEXT, so a
	// structured wire field pairs through pkg/orm's protojson encoders.
	// Checked before the kind branches below because a repeated field over
	// a json column is a JSON array, not a postgres ARRAY.
	case isJSONColumn(col) && jsonbPairable(wf):
		return assignJSONBToDB(dst, src, wf, col), ""
	case jsonTextPairing(wf, col):
		return assignJSONTextToDB(d, s, wf, col), ""

	case wf.Kind == FieldKindTimestamp && col.Type == "time":
		if nullable {
			return fmt.Sprintf("if %s != nil {\n\t\tt := %s.AsTime()\n\t\t%s = &t\n\t}", s, s, d), ""
		}
		return fmt.Sprintf("if %s != nil {\n\t\t%s = %s.AsTime()\n\t}", s, d, s), ""

	// Legacy TEXT timestamp column — see legacyTextTimestamp. The entity
	// field is the RFC3339Nano TEXT itself, so the write path formats.
	case legacyTextTimestamp(wf, col):
		v := fmt.Sprintf("%s.AsTime().Format(time.RFC3339Nano)", s)
		if nullable {
			return fmt.Sprintf("if %s != nil {\n\t\tv := %s\n\t\t%s = &v\n\t}", s, v, d), ""
		}
		return fmt.Sprintf("if %s != nil {\n\t\t%s = %s\n\t}", s, d, v), ""

	case wf.Kind == FieldKindRepeatedScalar && col.IsArray:
		wireElem, colElem := elemType(wf.GoType), elemType(base)
		if wireElem == colElem {
			return fmt.Sprintf("%s = append(%s(nil), %s...)", d, base, s), ""
		}
		if !numericPairs(wireElem, colElem) {
			return "", fmt.Sprintf("no conversion from wire %s to column %s", wf.GoType, col.DeclType)
		}
		guard := numericGuardStmt("v", wireElem, colElem, wf.Name, col, true)
		return fmt.Sprintf("for _, v := range %s {\n\t\t%s%s = append(%s, %s(v))\n\t}",
			s, indentBlock(guard), d, d, colElem), ""

	case wf.Kind == FieldKindWrapper && !col.IsArray:
		elem := strings.TrimPrefix(wf.GoType, "*")
		if elem == base {
			if nullable {
				return fmt.Sprintf("if %s != nil {\n\t\tv := *%s\n\t\t%s = &v\n\t}", s, s, d), ""
			}
			return fmt.Sprintf("if %s != nil {\n\t\t%s = *%s\n\t}", s, d, s), ""
		}
		if !numericPairs(elem, base) {
			return "", fmt.Sprintf("no conversion from wire %s to column %s", wf.GoType, col.DeclType)
		}
		guard := numericGuardStmt("*"+s, elem, base, wf.Name, col, true)
		c := base + "(*" + s + ")"
		if nullable {
			return fmt.Sprintf("if %s != nil {\n\t\t%sv := %s\n\t\t%s = &v\n\t}", s, indentBlock(guard), c, d), ""
		}
		return fmt.Sprintf("if %s != nil {\n\t\t%s%s = %s\n\t}", s, indentBlock(guard), d, c), ""

	case wf.Kind == FieldKindScalar && !col.IsArray:
		if wf.GoType == base {
			if nullable {
				return fmt.Sprintf("{\n\t\tv := %s\n\t\t%s = &v\n\t}", s, d), ""
			}
			return fmt.Sprintf("%s = %s", d, s), ""
		}
		if !numericPairs(wf.GoType, base) {
			return "", fmt.Sprintf("no conversion from wire %s to column %s", wf.GoType, col.DeclType)
		}
		guard := stmtPrefix(numericGuardStmt(s, wf.GoType, base, wf.Name, col, true))
		expr := base + "(" + s + ")"
		if nullable {
			return fmt.Sprintf("%s{\n\t\tv := %s\n\t\t%s = &v\n\t}", guard, expr, d), ""
		}
		return fmt.Sprintf("%s%s = %s", guard, d, expr), ""

	case wf.Kind == FieldKindEnum:
		_, repeated, optional, resolved := enumWireShape(wf)
		if !resolved || repeated != col.IsArray {
			break
		}
		switch col.Type {
		case "string":
			// Enum columns forge BIRTHS store the proto enum VALUE NAMES
			// as TEXT (the CHECK (col IN (...)) vocabulary birth emitted),
			// so the entity side takes String() — the declared name. An
			// out-of-range wire number renders numerically and is rejected
			// by the CHECK: loud, never silently stored as ''.
			if repeated {
				return fmt.Sprintf("for _, v := range %s {\n\t\t%s = append(%s, v.String())\n\t}", s, d, d), ""
			}
			return enumSingularAssign(d, s, optional, nullable, col), ""
		case "int64":
			// An integer column (any width) stores the WIRE NUMBER — the
			// schema the author declared, e.g. a project that kept its
			// enums as integers. The wire number is an int32: an int32 or
			// int64 column holds every one, a smallint column is guarded.
			if repeated {
				elem := elemType(base)
				guard := numericGuardStmt("v", "int32", elem, wf.Name, col, true)
				return fmt.Sprintf("for _, v := range %s {\n\t\t%s%s = append(%s, %s(v))\n\t}", s, indentBlock(guard), d, d, elem), ""
			}
			return enumNumberAssign(d, s, optional, nullable, col), ""
		}
	}
	return "", fmt.Sprintf("no conversion between wire kind %s and column type %s", wf.Kind, col.DeclType)
}

// enumSingularAssign emits the write path for a non-repeated enum field.
//
// proto3 gives a plain enum field NO presence: a field left unset and a
// field set to the zero are byte-identical on the wire, so the zero can
// only mean "the caller did not say". A row, unlike a request, is always
// in some state — which is why the born CHECK does not admit the zero
// sentinel (scaffold.bornEnumVocabulary), the seeder will not draw it, the
// generated fixtures will not use it, and the scaffolded create form will
// not submit it.
//
// Writing `e.Status = req.Status.String()` unconditionally contradicted
// all of that, and it did something worse than store a non-state: because
// every INSERT then NAMES the column, the `DEFAULT 'ORDER_STATUS_OPEN'`
// birth wrote beside the CHECK could never fire. The schema declared what
// an unsaid value means and the app made the declaration unreachable.
//
// So the zero leaves the field unset, and the schema says what unset means:
//
//   - NOT NULL column with a DEFAULT that is not the zero → the entity
//     field is left unset (ColumnInsertDefault: a nil pointer, or "" on the
//     ,nullzero field a born enum column is, its CHECK refusing ""), so the
//     INSERT omits the column and the DEFAULT fires — and a full-replace
//     Update keeps the stored value rather than resetting it.
//   - nullable column → left nil, so it stores NULL, which is that
//     column's own spelling for unset.
//   - NOT NULL column with NO default (or one equal to the zero) →
//     unchanged. The schema declined to say what unset means, so forge
//     invents nothing: the value goes as sent and the CHECK rejects the
//     sentinel loudly.
//
// An `optional` wire field has presence, so it adds one more way to say
// unset (absent) — both it and an explicit zero take the same route.
func enumSingularAssign(d, s string, optional, nullable bool, col EntityColumn) string {
	set := s + " != 0"
	if optional {
		set = s + " != nil && *" + s + " != 0"
	}
	if nullable {
		return fmt.Sprintf("if %s {\n\t\tv := %s.String()\n\t\t%s = &v\n\t}", set, s, d)
	}
	// A born enum column (a DEFAULT, and a CHECK vocabulary without '') is
	// a ,nullzero field: its "" is unset, so the INSERT omits it.
	if optional || columnInsertDefault(col) == InsertDefaultNullZero {
		return fmt.Sprintf("if %s {\n\t\t%s = %s.String()\n\t}", set, d, s)
	}
	return fmt.Sprintf("%s = %s.String()", d, s)
}

// enumNumberAssign is enumSingularAssign for an INTEGER column, which stores
// the wire number rather than the value name. The unset (zero) routing is the
// same and for the same reasons: a pointer column (a DEFAULT that is not the
// zero, or nullable) is left nil, and a column that declares neither gets the
// value as sent — with no CHECK vocabulary to refuse the zero, it stores 0,
// which is what the schema allowed.
func enumNumberAssign(d, s string, optional, nullable bool, col EntityColumn) string {
	goInt := dbBaseGoType(col)
	set, val := s+" != 0", goInt+"("+s+")"
	if optional {
		set, val = s+" != nil && *"+s+" != 0", goInt+"(*"+s+")"
	}
	if nullable {
		return fmt.Sprintf("if %s {\n\t\tv := %s\n\t\t%s = &v\n\t}", set, val, d)
	}
	if optional || columnInsertDefault(col) == InsertDefaultNullZero {
		return fmt.Sprintf("if %s {\n\t\t%s = %s\n\t}", set, d, val)
	}
	return fmt.Sprintf("%s = %s", d, val)
}

// enumFromNumberAssign is enumFromDBAssign for an INTEGER column: the stored
// number IS the wire value, so the read is a cast, not a lookup. A number the
// enum does not declare is not corruption here — proto3 enums are open, and
// the wire carries an unknown number through unchanged, exactly as a client
// sending one would — but a number outside int32, which no wire value can
// be, is refused with numericGuardStmt's error (a row written by a
// migration, an import or another service).
func enumFromNumberAssign(d, s, pbType, field string, col EntityColumn, repeated, optional, nullable bool) string {
	guard := func(expr string) string {
		return numericGuardStmt(expr, elemType(dbBaseGoType(col)), "int32", field, col, false)
	}
	switch {
	case repeated:
		return fmt.Sprintf("for _, v := range %s {\n\t\t%s%s = append(%s, %s(v))\n\t}",
			s, indentBlock(guard("v")), d, d, pbType)
	case optional && nullable:
		return fmt.Sprintf("if %s != nil {\n\t\t%sev := %s(*%s)\n\t\t%s = &ev\n\t}",
			s, indentBlock(guard("*"+s)), pbType, s, d)
	case optional:
		return fmt.Sprintf("%s{\n\t\tev := %s(%s)\n\t\t%s = &ev\n\t}", stmtPrefix(guard(s)), pbType, s, d)
	case nullable:
		return fmt.Sprintf("if %s != nil {\n\t\t%s%s = %s(*%s)\n\t}", s, indentBlock(guard("*"+s)), d, pbType, s)
	default:
		return fmt.Sprintf("%s%s = %s(%s)", stmtPrefix(guard(s)), d, pbType, s)
	}
}

// assignToProto emits "dst.<X> = ..." converting an entity struct field
// onto the wire message. src is the entity variable. The second return is
// the REASON the pairing has no conversion, or "" when it mapped.
func assignToProto(dst, src string, wf EntityField, col EntityColumn) (string, string) {
	if why := goTypeOverrideUnmapped(col); why != "" {
		return "", why
	}
	d, s := pbFieldRef(dst, wf), dbFieldRef(src, col)
	base := dbBaseGoType(col)
	nullable := dbNullable(col)

	switch {
	// See the matching branch in assignToDB.
	case isJSONColumn(col) && jsonbPairable(wf):
		return assignJSONBToProto(dst, src, wf, col), ""
	case jsonTextPairing(wf, col):
		return assignJSONTextToProto(d, s, wf), ""

	case wf.Kind == FieldKindTimestamp && col.Type == "time":
		if nullable {
			return fmt.Sprintf("if %s != nil {\n\t\t%s = timestamppb.New(*%s)\n\t}", s, d, s), ""
		}
		return fmt.Sprintf("if !%s.IsZero() {\n\t\t%s = timestamppb.New(%s)\n\t}", s, d, s), ""

	// Legacy TEXT timestamp column — see legacyTextTimestamp.
	case legacyTextTimestamp(wf, col):
		return legacyTextTimestampToProto(d, s, col.Name, nullable), ""

	case wf.Kind == FieldKindRepeatedScalar && col.IsArray:
		wireElem, colElem := elemType(wf.GoType), elemType(base)
		if wireElem == colElem {
			return fmt.Sprintf("%s = append(%s(nil), %s...)", d, wf.GoType, s), ""
		}
		if !numericPairs(colElem, wireElem) {
			return "", fmt.Sprintf("no conversion from column %s to wire %s", col.DeclType, wf.GoType)
		}
		guard := numericGuardStmt("v", colElem, wireElem, wf.Name, col, false)
		return fmt.Sprintf("for _, v := range %s {\n\t\t%s%s = append(%s, %s(v))\n\t}",
			s, indentBlock(guard), d, d, wireElem), ""

	case wf.Kind == FieldKindWrapper && !col.IsArray:
		elem := strings.TrimPrefix(wf.GoType, "*")
		if elem != base && !numericPairs(base, elem) {
			return "", fmt.Sprintf("no conversion from column %s to wire %s", col.DeclType, wf.GoType)
		}
		src, guard := s, ""
		if nullable {
			src = "*" + s
		}
		val := src
		if elem != base {
			guard = numericGuardStmt(src, base, elem, wf.Name, col, false)
			val = elem + "(" + src + ")"
		}
		if nullable {
			return fmt.Sprintf("if %s != nil {\n\t\t%sv := %s\n\t\t%s = &v\n\t}", s, indentBlock(guard), val, d), ""
		}
		return fmt.Sprintf("%s{\n\t\tv := %s\n\t\t%s = &v\n\t}", stmtPrefix(guard), val, d), ""

	case wf.Kind == FieldKindScalar && !col.IsArray:
		if wf.GoType != base && !numericPairs(base, wf.GoType) {
			return "", fmt.Sprintf("no conversion from column %s to wire %s", col.DeclType, wf.GoType)
		}
		if nullable {
			if wf.GoType == base {
				return fmt.Sprintf("if %s != nil {\n\t\t%s = *%s\n\t}", s, d, s), ""
			}
			guard := numericGuardStmt("*"+s, base, wf.GoType, wf.Name, col, false)
			return fmt.Sprintf("if %s != nil {\n\t\t%s%s = %s(*%s)\n\t}",
				s, indentBlock(guard), d, wf.GoType, s), ""
		}
		if wf.GoType == base {
			return fmt.Sprintf("%s = %s", d, s), ""
		}
		guard := stmtPrefix(numericGuardStmt(s, base, wf.GoType, wf.Name, col, false))
		return fmt.Sprintf("%s%s = %s(%s)", guard, d, wf.GoType, s), ""

	case wf.Kind == FieldKindEnum:
		// The column stores enum VALUE NAMES; pb.<Enum>_value maps a name
		// back to its wire number. The lookup is comma-ok checked so the
		// three outcomes are DISTINCT rather than collapsed:
		//
		//   - a declared name (including <ENUM>_UNSPECIFIED)  → its number;
		//   - an empty string                                 → the zero
		//     value (a NULL/absent enum, not corruption);
		//   - a NON-EMPTY name absent from the map            → a loud
		//     "corrupt enum value %q for column" error the packer returns
		//     and the op turns into CodeInternal.
		//
		// The pre-fix `pb.X(pb.X_value[name])` form silently yielded 0
		// (UNSPECIFIED) for an absent name, so an enum rename or a corrupt
		// row read back as UNSPECIFIED and was mishandled by state logic
		// with no error and no log. The read path therefore returns an
		// error (the ToProto helper's signature is (*pb.<Entity>, error)).
		pbType, repeated, optional, resolved := enumWireShape(wf)
		if !resolved || repeated != col.IsArray {
			break
		}
		switch col.Type {
		case "string":
			return enumFromDBAssign(d, s, pbType, col.Name, repeated, optional, nullable), ""
		case "int64": // an integer column stores the wire number — see enumFromNumberAssign
			return enumFromNumberAssign(d, s, pbType, wf.Name, col, repeated, optional, nullable), ""
		}
	}
	return "", fmt.Sprintf("no conversion between wire kind %s and column type %s", wf.Kind, col.DeclType)
}

// enumFromDBAssign builds the read-path (DB→wire) conversion for one enum
// column, comma-ok checked so an unknown non-empty stored name returns a
// "corrupt enum value" error instead of silently reading back as the zero
// value (UNSPECIFIED). d is the wire field (e.g. "m.Status"), s the entity
// field (e.g. "e.Status"), pbType the concrete pb enum type (e.g.
// "pb.OrderStatus"), col the column name (for the error message). An empty
// stored string is a legitimately absent enum (NULL / unset) and maps to
// the zero value, never an error — only a NON-EMPTY name absent from the
// pb.<Enum>_value map is corruption. The block lives inside the
// <entity>ToProto helper, whose signature is (*pb.<Entity>, error); the
// `return nil, ...` on corruption is what makes the op surface CodeInternal.
func enumFromDBAssign(d, s, pbType, col string, repeated, optional, nullable bool) string {
	errf := func(nameExpr string) string {
		return fmt.Sprintf("return nil, fmt.Errorf(\"corrupt enum value %%q for column %s\", %s)", col, nameExpr)
	}
	switch {
	case repeated:
		// s is []string, d is []pb.<Enum>. An empty element is skipped.
		return fmt.Sprintf(
			"for _, sv := range %s {\n\t\tif v, ok := %s_value[sv]; ok {\n\t\t\t%s = append(%s, %s(v))\n\t\t} else if sv != \"\" {\n\t\t\t%s\n\t\t}\n\t}",
			s, pbType, d, d, pbType, errf("sv"))
	case optional && nullable:
		// s is *string, d is *pb.<Enum>.
		return fmt.Sprintf(
			"if %s != nil {\n\t\tif v, ok := %s_value[*%s]; ok {\n\t\t\tev := %s(v)\n\t\t\t%s = &ev\n\t\t} else if *%s != \"\" {\n\t\t\t%s\n\t\t}\n\t}",
			s, pbType, s, pbType, d, s, errf("*"+s))
	case optional:
		// s is string, d is *pb.<Enum>.
		return fmt.Sprintf(
			"if v, ok := %s_value[%s]; ok {\n\t\tev := %s(v)\n\t\t%s = &ev\n\t} else if %s != \"\" {\n\t\t%s\n\t}",
			pbType, s, pbType, d, s, errf(s))
	case nullable:
		// s is *string, d is pb.<Enum>.
		return fmt.Sprintf(
			"if %s != nil {\n\t\tif v, ok := %s_value[*%s]; ok {\n\t\t\t%s = %s(v)\n\t\t} else if *%s != \"\" {\n\t\t\t%s\n\t\t}\n\t}",
			s, pbType, s, d, pbType, s, errf("*"+s))
	default:
		// s is string, d is pb.<Enum>.
		return fmt.Sprintf(
			"if v, ok := %s_value[%s]; ok {\n\t\t%s = %s(v)\n\t} else if %s != \"\" {\n\t\t%s\n\t}",
			pbType, s, d, pbType, s, errf(s))
	}
}

// ── numeric pairings ────────────────────────────────────────────────────
//
// A wire field and its column routinely carry DIFFERENT numeric types:
// entity birth stores every proto integer kind in a BIGINT and both proto
// float kinds in a DOUBLE PRECISION, so `uint32 hits` sits over an int64
// column and `float ratio` over a float64 one. The conversion must cast,
// and the cast is where values disappear.
//
// The rule that shipped blessed every numeric cast unconditionally — if
// both sides were numeric, the cast was emitted — which accepted three
// different silent losses:
//
//   - SIGN. Postgres BIGINT is signed 64-bit, so `int64(m.Size)` on a
//     uint64 above 2^63 wraps to a negative and stores it without a word.
//     (Postgres itself REJECTS that value on the array path — `value
//     "18446744073709551615" is out of range for type bigint` — so the Go
//     cast is precisely what turned a loud database error into a quiet
//     one.)
//   - WIDTH. A BIGINT column holding 3000000000, read into an `int32` wire
//     field, is -1294967296.
//   - FAMILY. `int64(m.Ratio)` over a double drops the fraction of every
//     value ever written; `float64(m.Count)` over a bigint loses exactness
//     above 2^53.
//
// Family loss has no honest cast in either direction, so those pairings are
// REFUSED. Sign and width loss do have one: cast, but range-check first and
// fail loudly with the field, the column and the offending value — the same
// treatment the enum read path already gives a stored name it does not
// recognize.

// goNumeric classifies a Go numeric type for the pairing rules.
type goNumeric struct {
	unsigned bool
	isFloat  bool
	bits     int
}

var goNumericTypes = map[string]goNumeric{
	"int16":   {bits: 16},
	"int32":   {bits: 32},
	"int64":   {bits: 64},
	"uint32":  {unsigned: true, bits: 32},
	"uint64":  {unsigned: true, bits: 64},
	"float32": {isFloat: true, bits: 32},
	"float64": {isFloat: true, bits: 64},
}

// numericPairs reports whether a value of Go type `from` has a conversion
// to `to` at all. Float and integer are different families: whichever way
// the value crosses, the round trip does not return what it was given.
func numericPairs(from, to string) bool {
	f, okF := goNumericTypes[from]
	t, okT := goNumericTypes[to]
	return okF && okT && f.isFloat == t.isFloat
}

// numericGuardCond renders the condition under which converting expr (of Go
// type `from`) to `to` loses the value, or "" when the conversion cannot
// lose anything.
//
// float32 <-> float64 is deliberately unguarded. Widening is exact, and
// narrowing rounds back to precisely what widening produced, so every value
// THIS seam wrote round-trips. A double written by something else carrying
// more precision than a float32 holds loses the extra bits — which is what
// declaring the column REAL would have done anyway, and is a rounding, not
// a wraparound.
func numericGuardCond(expr, from, to string) string {
	f, t := goNumericTypes[from], goNumericTypes[to]
	if f.isFloat || t.isFloat {
		return ""
	}
	var conds []string
	if !f.unsigned && (t.unsigned || f.bits > t.bits) {
		if t.unsigned {
			conds = append(conds, expr+" < 0")
		} else {
			conds = append(conds, fmt.Sprintf("%s < %d", expr, goIntMin(t)))
		}
	}
	if goIntMax(f) > goIntMax(t) {
		conds = append(conds, fmt.Sprintf("%s > %d", expr, goIntMax(t)))
	}
	return strings.Join(conds, " || ")
}

func goIntMax(n goNumeric) uint64 {
	switch {
	case n.unsigned && n.bits == 64:
		return math.MaxUint64
	case n.unsigned:
		return math.MaxUint32
	case n.bits == 64:
		return math.MaxInt64
	default:
		return math.MaxInt32
	}
}

func goIntMin(n goNumeric) int64 {
	if n.bits == 64 {
		return math.MinInt64
	}
	return math.MinInt32
}

// numericGuardStmt renders the range check a lossy cast needs: the
// condition, and a failure naming the field, the column and the value that
// did not fit. "" when the cast cannot lose anything, so a lossless pairing
// costs nothing in the generated file.
//
// toDB selects the direction, which changes what the message can honestly
// say: writing, the WIRE supplied a value the column cannot hold; reading,
// the COLUMN holds a value the wire field cannot represent (a row written
// by a migration, an import, or another service).
func numericGuardStmt(expr, from, to, field string, col EntityColumn, toDB bool) string {
	cond := numericGuardCond(expr, from, to)
	if cond == "" {
		return ""
	}
	msg := fmt.Sprintf("%s: column %s holds %%d, out of range for the %s wire field",
		field, colLabel(col), to)
	if toDB {
		msg = fmt.Sprintf("%s: wire value %%d does not fit column %s", field, colLabel(col))
	}
	// %q emits the message as a Go literal, so the %d verb reaches the
	// generated code instead of this Sprintf.
	return fmt.Sprintf("if %s {\n\t\treturn nil, fmt.Errorf(%q, %s)\n\t}", cond, msg, expr)
}

// colLabel names a column in a runtime error message, with its declared
// SQL type when the descriptor carries one. The type is what makes the
// message actionable ("column size (BIGINT)" says why 2^63 did not fit),
// but a descriptor without it must not render an empty "()".
func colLabel(col EntityColumn) string {
	if col.DeclType == "" {
		return col.Name
	}
	return col.Name + " (" + col.DeclType + ")"
}

// stmtPrefix joins a guard statement to the assignment that follows it, at
// the indentation the surrounding emissions use. Empty guard, empty prefix.
func stmtPrefix(guard string) string {
	if guard == "" {
		return ""
	}
	return guard + "\n\t"
}

// indentBlock re-indents a guard by one level, for the guards that land
// inside a nil-check block rather than at statement top level.
func indentBlock(s string) string {
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "\n\t", "\n\t\t") + "\n\t\t"
}

// elemType strips one slice level: the ELEMENT type of a repeated field or
// an array column. `[][]byte` yields `[]byte`, which is why `bytes` must
// never be classified as repeated on its singular shape.
func elemType(sliceType string) string {
	return strings.TrimPrefix(sliceType, "[]")
}

// buildCreateAssigns maps the create-request fields onto the entity
// struct. Request fields are matched to columns by name; unmatched
// fields are dropped with a comment so the generated code says why.
// Columns the request does NOT carry — `// forge:read-only` fields, and
// anything in the table the wire message never had — are left unset, and
// a NOT NULL column's DEFAULT then applies: the entity field of one whose
// DEFAULT is not its Go zero is nil (or the json/array zero) until set,
// and the INSERT omits it (ColumnInsertDefault).
func buildCreateAssigns(svc ServiceDef, m Method, entity EntityDef) ([]string, []UnmappedField) {
	wireFields := requestWireFields(svc, m)
	colByName := map[string]EntityColumn{}
	for _, c := range entity.Columns {
		colByName[c.Name] = c
	}
	var out []string
	var unmapped []UnmappedField
	assigned := make(map[string]bool, len(wireFields))
	for _, wf := range wireFields {
		col, ok := colByName[wf.Name]
		if !ok {
			out = append(out, fmt.Sprintf("// %s: request-only field (no %q column in the applied schema)", wf.GoName, wf.Name))
			continue
		}
		a, why := assignToDB("e", "req", wf, col)
		if why != "" {
			// A create-request field with a column and no conversion is the
			// sharpest form of the dead field: the caller SUPPLIED a value
			// and the row got the column DEFAULT. Fail the generate.
			unmapped = append(unmapped, unmappedField(entity, wf, col, why))
			continue
		}
		out = append(out, a)
		assigned[wf.Name] = true
	}
	return out, unmapped
}

// requestWireFields extracts a request message's fields, preferring the
// deep Schemas map (which knows about repeated/optional) over the
// shallow Messages map.
func requestWireFields(svc ServiceDef, m Method) []EntityField {
	if m.InputTypeFQ != "" {
		if defs, ok := svc.Schemas[m.InputTypeFQ]; ok {
			fields := make([]EntityField, 0, len(defs))
			for _, d := range defs {
				// proto3 optional scalars surface as wrapper-like
				// pointers on the Go struct; enums resolve to their
				// concrete pb.<Enum> shape (see promoteEnum).
				f := promoteOptionalScalar(schemaFieldToEntityField(d))
				f = promoteEnum(f, svc, d.Repeated)
				fields = append(fields, f)
			}
			return withMessageGoNames(fields)
		}
	}
	defs, ok := svc.Messages[m.InputType]
	if !ok {
		return nil
	}
	fields := make([]EntityField, 0, len(defs))
	for _, d := range defs {
		// The shallow Messages map records no enum type name, so
		// promoteEnum never fires here — enum fields stay unmapped with
		// the honest comment (legacy-descriptor degradation).
		fields = append(fields, promoteOptionalScalar(messageFieldToEntityField(d)))
	}
	return withMessageGoNames(fields)
}
