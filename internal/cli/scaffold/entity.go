package scaffold

import (
	"fmt"
	"strings"

	"github.com/jinzhu/inflection"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/naming"
)

// entityFieldType is the wire projection of one entity field: the proto3
// type its CRUD envelopes are rendered with. Fields reach this shape from
// the proto — either an authored message's SchemaFieldDefs
// (entityFieldsFromSchemaDefs) or an applied table's columns — never from
// a CLI type vocabulary; forge has no second spelling of a field's type.
type entityFieldType struct {
	Proto    string // proto3 field type ("repeated string" for arrays)
	Repeated bool
}

type entityField struct {
	Name string // snake_case
	Type entityFieldType
	// Decl is the filterable kind driving the List-request affordances:
	// "string" (spanned by free-text search), "bool", "enum", or "fk" (a
	// `<stem>_id` reference). Empty for a field that gets no affordance.
	Decl string
	// EnumType is the enum's proto type name, set only when Decl == "enum".
	// The born List facet is typed as the enum rather than a string, so an
	// invalid value is a compile error at the call site instead of a filter
	// that silently matches nothing.
	EnumType string
	// ReadOnly marks a `// forge:read-only` field: kept on the entity
	// wire message + Get/List responses, but OMITTED from the born
	// Create/Update request messages — a value the client must not write.
	ReadOnly bool
	// ValidateOptions is the field's `(buf.validate.field)` inline-options
	// block as authored on the entity message, or "" when it carries none.
	// It is re-emitted on the fields Create<Entity>Request FLATTENS: that
	// request is its own message, so protovalidate enforces nothing on it
	// unless the rules are declared there too (Update<Entity>Request nests
	// the entity instead, and the interceptor recurses into it — which is
	// why only the flattening shape needs this).
	ValidateOptions string
}

// protoFieldOptionSuffix renders a field's inline-options block as the
// suffix that follows the field number (` [(buf.validate.field)...]`), or
// "" when the field carries no options.
func protoFieldOptionSuffix(opts string) string {
	if opts == "" {
		return ""
	}
	return " " + opts
}

func newEntityCmd(_ *factory.Factory) *cobra.Command {
	// --service and --no-rpcs are deliberately absent. Both were
	// field-list-form flags: one picked which service proto received the
	// generated CRUD surface, the other suppressed that surface entirely.
	// A --from-proto birth names its service INSIDE the flag value and
	// never writes the entity's own message, so neither has anything left
	// to control — advertising a flag whose only behaviour is to error is
	// worse than not having it.
	var (
		tableFlag    string
		softDelete   bool
		noTimestamps bool
		fromProto    string
		dryRun       bool
	)
	cmd := &cobra.Command{
		Use:   "entity <name> --from-proto <svc>[.<Message>]",
		Short: "Birth a database entity from its already-authored proto message: the owned forward migration + the CRUD wire contract",
		Long: `Birth a database entity from the proto.

The proto is where an entity is declared. Author the message, mark it
with a leading ` + "`// forge:entity`" + ` comment, and run bare ` + "`forge scaffold`" + ` —
that births every marked message at once (migration + the missing
CRUD quintet) and generates. This command is the same birth, narrowed to
one named message.

The migration is yours at birth and is NEVER re-derived: evolution is a
new migration plus a proto edit, on independent clocks. Running
` + "`forge generate`" + ` projects the APPLIED schema into entity structs, the
ORM, CRUD wiring, and frontend pages.

  --from-proto <svc>[.<Message>]   Derive the create-table migration from
      the already-authored proto message (read from
      gen/forge_descriptor.json). With no <Message> and no <name>, sweeps
      every message sitting in entity position of a full CRUD quintet that
      has no applied table yet, PLUS every message carrying a leading
      ` + "`// forge:entity`" + ` marker (those also get their missing CRUD quintet
      injected, one-time). Positional message names birth an explicit
      list. --dry-run prints the plan and writes nothing.

Example:
  forge scaffold                                      # birth every marked message
  forge scaffold entity order --from-proto tasks
  forge scaffold entity --from-proto tasks.Order
  forge scaffold entity --from-proto tasks            # batch: quintets with no table
  forge scaffold entity --from-proto tasks --dry-run`,
		// Zero positionals are legal for the --from-proto batch form; a
		// named entity still needs the name. Cobra parses flags before
		// validating args, so the captured flag vars are readable here —
		// and a bare `scaffold entity` stays an ARGS error, which keeps
		// usage-printing intact (TestRootCmd_ArgErrorStillShowsUsage).
		Args: func(cmd *cobra.Command, args []string) error {
			// The removed-flag refusal has to win here, not in RunE: cobra
			// validates args first, so `--from-schema invoices` with no
			// entity name would otherwise die on arity and print usage —
			// the one output that says nothing about why the flag is gone.
			if err := removedFromSchemaRefusal(cmd); err != nil {
				return err
			}
			if fromProto != "" {
				return nil // the affordance validates its own positional shape in RunE
			}
			return cobra.MinimumNArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := entityOpts{
				Table:        tableFlag,
				SoftDelete:   softDelete,
				NoTimestamps: noTimestamps,
				DryRun:       dryRun,
			}
			switch {
			case dryRun && fromProto == "":
				return cliutil.UserErr("forge scaffold entity",
					"--dry-run applies to the --from-proto forms (they plan a birth sweep)", "",
					"pass --from-proto <svc> [...] with --dry-run, or drop the flag")
			case fromProto != "":
				return runEntityFromProto(args, fromProto, opts)
			}
			return entityWithoutProtoRefusal(args)
		},
	}
	cmd.Flags().StringVar(&tableFlag, "table", "", "table name override (default: pluralized snake_case of <name>)")
	cmd.Flags().BoolVar(&softDelete, "soft-delete", false, "add deleted_at TIMESTAMPTZ — deletes become UPDATEs, reads filter IS NULL")
	cmd.Flags().BoolVar(&noTimestamps, "no-timestamps", false, "skip the managed created_at/updated_at columns")
	cmd.Flags().StringVar(&fromProto, "from-proto", "", "derive the create-table migration from an already-authored proto message: <svc> or <svc>.<Message> (one-time; the migration is yours at birth)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the --from-proto birth plan (migrations + quintet completion) and write nothing")
	registerRemovedFromSchemaFlag(cmd)
	return cmd
}

// registerRemovedFromSchemaFlag keeps `--from-schema` PARSEABLE so it can be
// refused with an explanation. Dropping the flag outright would answer it with
// cobra's `unknown flag: --from-schema` — accurate and useless, since the
// caller's real question is what replaced it, and the answer ("nothing; forge
// does not adopt a pre-existing database") is not one they can guess.
//
// Hidden, so it is absent from --help: the flag exists to be refused, not to
// be discovered. Delete it once the spelling stops showing up in the wild.
func registerRemovedFromSchemaFlag(cmd *cobra.Command) {
	var fromSchema string
	cmd.Flags().StringVar(&fromSchema, "from-schema", "", "removed")
	_ = cmd.Flags().MarkHidden("from-schema")
}

// removedFromSchemaRefusal returns the refusal when `--from-schema` was
// passed, or nil. It reads the flag off the command rather than closing over
// the variable so the Args validator — which runs before RunE — can call it.
func removedFromSchemaRefusal(cmd *cobra.Command) error {
	table, err := cmd.Flags().GetString("from-schema")
	if err != nil || table == "" {
		return nil
	}
	// An Args error normally keeps the usage block, which is right for a
	// wrong arg count. This is not that: the flag list says nothing about
	// why --from-schema is gone, and 15 lines of it between the user and
	// the explanation is the dump this refusal exists to replace.
	cmd.SilenceUsage = true
	entity := naming.ToPascalCase(naming.ToSnakeCase(inflection.Singular(table)))
	return cliutil.UserErr("forge scaffold entity --from-schema",
		"--from-schema was removed — it existed to adopt a database forge did not create, "+
			"and forge births entities rather than importing them", "",
		"declare the entity in the service proto and run `forge scaffold`:\n"+
			"    // forge:entity\n"+
			"    message "+entity+" { ... }\n"+
			"  the birth writes its own migration, so an ALREADY-APPLIED table will collide —\n"+
			"  keep the table by hand-writing the matching proto message and running\n"+
			"  `forge generate`, which projects the ORM and CRUD wiring from the applied schema")
}

// entityWithoutProtoRefusal answers `forge scaffold entity <name> ...` with
// no --from-proto. Both shapes that used to be legal here — the `field:type`
// flag grammar and a bare name — birthed an entity from something OTHER than
// the proto, and forge no longer has a second authoring path to route them
// to. The refusal spells the proto-first equivalent literally, using the
// user's own entity name, so the fix is a paste rather than a lookup.
//
// The field list is echoed as proto fields when one was given: an author who
// typed `bookmark url:string done:bool` gets the message they meant, already
// written out, rather than a pointer at documentation.
func entityWithoutProtoRefusal(args []string) error {
	name := args[0]
	pascal := naming.ToPascalCase(naming.ToSnakeCase(name))

	var b strings.Builder
	b.WriteString("declare the entity in the service proto and run `forge scaffold`:\n")
	fmt.Fprintf(&b, "    // forge:entity\n")
	fmt.Fprintf(&b, "    message %s {\n", pascal)
	for i, f := range protoFieldsForRefusal(args[1:]) {
		fmt.Fprintf(&b, "      %s = %d;\n", f, i+2)
	}
	b.WriteString("    }\n")
	b.WriteString("  then run `forge scaffold` (or `forge scaffold entity ")
	b.WriteString(name)
	b.WriteString(" --from-proto <svc>` for just this one)")

	what := "the proto is the only place an entity is declared"
	if len(args) > 1 {
		what = "the `field:type` flag grammar was removed — the proto is the only place an entity is declared"
	}
	return cliutil.UserErr("forge scaffold entity "+name, what, "", b.String())
}

// protoFieldsForRefusal renders the `field:type` arguments a user typed as
// the proto3 field declarations that replace them, so the refusal above can
// show the message they were reaching for. Unparseable arguments and types
// the removed vocabulary spelled with no single proto equivalent fall back
// to `string`, which is a starting point the author edits — the refusal is a
// signpost, not a translator, and it is followed by `forge scaffold`
// reading the real proto.
func protoFieldsForRefusal(fieldArgs []string) []string {
	if len(fieldArgs) == 0 {
		return []string{"string name"}
	}
	protoOf := map[string]string{
		"string": "string", "int": "int64", "int64": "int64",
		"float": "double", "bool": "bool", "time": "google.protobuf.Timestamp",
		"[]string": "repeated string", "[]int": "repeated int64", "json": "string",
	}
	out := make([]string, 0, len(fieldArgs))
	for _, a := range fieldArgs {
		fname, typeStr, ok := strings.Cut(a, ":")
		if !ok {
			fname, typeStr = a, "string"
		}
		proto, known := protoOf[typeStr]
		if !known {
			proto = "string"
		}
		out = append(out, proto+" "+naming.ToSnakeCase(fname))
	}
	return out
}

// entityOpts is the entity-shaping half of a birth: the table it lands in
// and the lifecycle columns it gets. Everything about the entity's FIELDS
// comes from the proto message instead, which is why there is nothing here
// about them.
type entityOpts struct {
	// Table overrides the default pluralized snake_case table name.
	Table string
	// SoftDelete adds the nullable deleted_at column. A message carrying
	// a deleted_at field, or a `// forge:soft-delete` marker, does the
	// same thing — this is the flag spelling of that decision.
	SoftDelete bool
	// NoTimestamps skips the managed created_at/updated_at columns.
	NoTimestamps bool
	// DryRun prints the birth plan and writes nothing (--from-proto
	// forms only).
	DryRun bool
}

// ensureProtoImport adds `import "<path>";` after the last existing
// import (or after the package line) when missing.
func ensureProtoImport(content, path string) string {
	if strings.Contains(content, `import "`+path+`"`) {
		return content
	}
	lines := strings.Split(content, "\n")
	insertAt := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "import ") {
			insertAt = i
		}
	}
	if insertAt == -1 {
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "package ") {
				insertAt = i
				break
			}
		}
	}
	if insertAt == -1 {
		return content
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt+1]...)
	out = append(out, `import "`+path+`";`)
	out = append(out, lines[insertAt+1:]...)
	return strings.Join(out, "\n")
}

// insertIntoServiceBlock inserts text before the closing brace of the
// first `service <Name> { ... }` block.
func insertIntoServiceBlock(content, text string) (string, error) {
	idx := strings.Index(content, "\nservice ")
	if idx < 0 {
		if strings.HasPrefix(content, "service ") {
			idx = 0
		} else {
			return "", fmt.Errorf("no service block found")
		}
	}
	open := strings.Index(content[idx:], "{")
	if open < 0 {
		return "", fmt.Errorf("malformed service block")
	}
	depth := 0
	for i := idx + open; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[:i] + text + content[i:], nil
			}
		}
	}
	return "", fmt.Errorf("unbalanced braces in service block")
}

// crudProtoPiece is one injectable unit of the CRUD wire surface: a
// single rpc block or a single message declaration. The one-time
// injection (injectEntityCRUDProto) assembles all of them; quintet
// COMPLETION (completeEntityCRUDProto, add_entity_quintet.go) injects
// only the missing ones — both paths render through the same builders so
// the emitted shape can never fork.
type crudProtoPiece struct {
	name string // "CreateOrder" (rpc) / "CreateOrderRequest" (message)
	text string // the full block text
}

// buildEntityCRUDRPCPieces renders the five CRUD rpc blocks, one piece
// per verb.
func buildEntityCRUDRPCPieces(entity string) []crudProtoPiece {
	plural := naming.Pluralize(entity)
	var pieces []crudProtoPiece
	for _, op := range []struct{ verb, name, opts string }{
		{"Create", "Create" + entity, "idempotency_key: true"},
		{"Get", "Get" + entity, ""},
		{"Update", "Update" + entity, "idempotency_key: true"},
		{"Delete", "Delete" + entity, "idempotency_key: true"},
		{"List", "List" + plural, ""},
	} {
		var b strings.Builder
		fmt.Fprintf(&b, "  rpc %s(%sRequest) returns (%sResponse) {\n", op.name, op.name, op.name)
		b.WriteString("    option (forge.v1.method) = {\n")
		b.WriteString("      auth_required: true\n")
		if op.opts != "" {
			fmt.Fprintf(&b, "      %s\n", op.opts)
		}
		b.WriteString("    };\n")
		b.WriteString("  }\n")
		pieces = append(pieces, crudProtoPiece{name: op.name, text: b.String()})
	}
	return pieces
}

// buildEntityCRUDMessagePieces renders the ten CRUD envelope messages
// (Create/Get/Update/Delete/List × Request/Response) — one piece each,
// WITHOUT the entity wire message itself (injectEntityCRUDProto renders
// that separately; quintet completion never re-renders it — the entity
// message already existing is that path's precondition).
func buildEntityCRUDMessagePieces(entity string, fields []entityField) []crudProtoPiece {
	plural := naming.Pluralize(entity)
	var pieces []crudProtoPiece
	add := func(name, text string) {
		pieces = append(pieces, crudProtoPiece{name: name, text: text})
	}

	// Create — `// forge:read-only` fields are OMITTED (the client must not
	// write them). They stay on the entity wire message + Get/List
	// responses, so reads are unaffected. Field numbers stay contiguous
	// across the omission (a running index, not the slice index).
	//
	// Each carried field ALSO repeats the entity field's
	// `(buf.validate.field)` options. This request FLATTENS the entity
	// rather than nesting it, so protovalidate — which validates the
	// message it is handed and only recurses into nested messages — would
	// otherwise enforce nothing here: an over-length name would pass the
	// wire, reach the DB, trip the CHECK, and surface as Internal. The
	// declaration and its projections stay in lockstep because both the
	// field and its rules are dropped together: a read-only field's rules
	// leave with the field, and message-level rules (the only shape that
	// can name a sibling field) are never lifted at all.
	var create strings.Builder
	fmt.Fprintf(&create, "message Create%sRequest {\n", entity)
	n := 0
	for _, f := range fields {
		if f.ReadOnly {
			continue
		}
		n++
		fmt.Fprintf(&create, "  %s %s = %d%s;\n", f.Type.Proto, f.Name, n, protoFieldOptionSuffix(f.ValidateOptions))
	}
	create.WriteString("}\n")
	add("Create"+entity+"Request", create.String())
	// The entity-carrying response/request field name is derived through
	// naming.EntityFieldName / naming.EntityListFieldName — the SAME helpers
	// the CRUD shape detector (validateCRUDShape) matches against and the ops
	// emitter's Go field derives from. Keep these three sites on the helper so
	// a multi-word entity ("module_config") can never drift back to a
	// concatenated-lowercase form that breaks generated CRUD.
	add("Create"+entity+"Response",
		fmt.Sprintf("message Create%sResponse {\n  %s %s = 1;\n}\n", entity, entity, naming.EntityFieldName(entity)))

	// Get
	add("Get"+entity+"Request", fmt.Sprintf("message Get%sRequest {\n  string id = 1;\n}\n", entity))
	add("Get"+entity+"Response",
		fmt.Sprintf("message Get%sResponse {\n  %s %s = 1;\n}\n", entity, entity, naming.EntityFieldName(entity)))

	// Update (AIP-134) — WRAPS the whole entity + an update_mask, so it never
	// enumerates fields: a `// forge:read-only` field is therefore already
	// absent from the Update request message text (it lives only on the
	// wrapped entity, which the marker keeps). Client write-exclusion for it
	// is enforced downstream — the born edit form and masked-update test skip
	// read-only fields, so the mask never names one and the value is never
	// clobbered from the client.
	var upd strings.Builder
	fmt.Fprintf(&upd, "message Update%sRequest {\n", entity)
	fmt.Fprintf(&upd, "  %s %s = 1;\n", entity, naming.EntityFieldName(entity))
	upd.WriteString("  google.protobuf.FieldMask update_mask = 2;\n")
	upd.WriteString("}\n")
	add("Update"+entity+"Request", upd.String())
	add("Update"+entity+"Response",
		fmt.Sprintf("message Update%sResponse {\n  %s %s = 1;\n}\n", entity, entity, naming.EntityFieldName(entity)))

	// Delete
	add("Delete"+entity+"Request",
		fmt.Sprintf("message Delete%sRequest {\n  string id = 1;\n  bool hard_delete = 2;\n}\n", entity))
	add("Delete"+entity+"Response",
		fmt.Sprintf("message Delete%sResponse {\n  string id = 1;\n  google.protobuf.Timestamp deleted_at = 2;\n}\n", entity))

	// List — the other request that flattens, but it flattens FILTERS, not
	// values, so it deliberately carries NO entity field rules: `search` is
	// a free-text query (never the constrained `name` itself), and an
	// `optional bool <field>` filter that inherited the entity field's rule
	// would gate the QUERY on it (a `bool.const` would pin the filter, a
	// `required` would forbid the unfiltered call). Rules travel with the
	// value, not with the predicate over it.
	var list strings.Builder
	// Header comment: the request's own fields ARE the filter surface, so a UI
	// that needs to filter/scope by something new adds a field here rather than
	// over-fetching and filtering in the client.
	list.WriteString("// List filters ARE this request's fields: the query filters on the same fields\n")
	list.WriteString("// declared here. Birth scaffolds a facet for every field it can filter\n")
	list.WriteString("// mechanically — free-text search, each bool, each enum, and each <stem>_id\n")
	list.WriteString("// reference. To filter by something else, add that field HERE and forge wires\n")
	list.WriteString("// it end to end — never fetch a page and filter client-side, which silently\n")
	list.WriteString("// truncates past the page cap. Delete a facet you do not want.\n")
	fmt.Fprintf(&list, "message List%sRequest {\n", plural)
	list.WriteString("  int32 page_size = 1;\n")
	list.WriteString("  string page_token = 2;\n")
	fn := 3
	hasText := false
	for _, f := range fields {
		if f.Decl == "string" {
			hasText = true
		}
	}
	if hasText {
		fmt.Fprintf(&list, "  optional string search = %d;\n", fn)
		fn++
	}
	for _, f := range fields {
		switch f.Decl {
		case "bool":
			fmt.Fprintf(&list, "  optional bool %s = %d;\n", f.Name, fn)
			fn++
		case "enum":
			// Typed as the enum, not a string: an invalid value is then a
			// compile error at the call site rather than a filter that
			// matches nothing. Status is the column real callers filter on
			// more than any other, so a born list that could not express it
			// was the single biggest gap in the generated API.
			fmt.Fprintf(&list, "  optional %s %s = %d;\n", f.EnumType, f.Name, fn)
			fn++
		case "fk":
			// Scoping a list to its parent — a customer's jobs, a job's line
			// items — is the other filter every caller needs, and the one an
			// unfiltered list makes impossible to do correctly.
			fmt.Fprintf(&list, "  optional string %s = %d;\n", f.Name, fn)
			fn++
		}
	}
	fmt.Fprintf(&list, "  string order_by = %d;\n", fn)
	fn++
	fmt.Fprintf(&list, "  bool descending = %d;\n", fn)
	list.WriteString("}\n")
	add("List"+plural+"Request", list.String())

	var listResp strings.Builder
	fmt.Fprintf(&listResp, "message List%sResponse {\n", plural)
	fmt.Fprintf(&listResp, "  repeated %s %s = 1;\n", entity, naming.EntityListFieldName(entity))
	listResp.WriteString("  string next_page_token = 2;\n")
	listResp.WriteString("  int32 total_count = 3;\n")
	listResp.WriteString("}\n")
	add("List"+plural+"Response", listResp.String())

	return pieces
}
