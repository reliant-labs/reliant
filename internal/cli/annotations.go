// Package cli — `forge project annotations`: dump forge's authoritative
// entity-authoring annotation spec as structured data.
//
// The command answers "what annotations does forge understand, and what do
// they do?" from forge's OWN definitions rather than from prose that drifts:
//
//   - markers        the `// forge:*` comment markers the proto scanner reads
//     (forge:entity, forge:soft-delete, forge:append-only,
//     forge:read-only, forge:computed, forge:generated, forge:secret, …).
//   - field_types    the proto→column mapping an entity birth applies,
//     iterated from the real scaffold.ProtoSQLMappings (whose
//     scalar half is projected from the renderer's scalarSQL).
//   - validate_rules the projected protovalidate subset, DISCOVERED by
//     probing forge's own recognizer (codegen.ParseRawValidateOptions)
//     with every rule protovalidate itself declares, and whose db/zod
//     effects are COMPUTED by forge's own projection functions
//     (codegen.FieldConstraints.SQLChecks / .ZodChain).
//   - service_options / method_options
//     the (forge.v1.service) / (forge.v1.method) proto options,
//     enumerated from the compiled forgepb descriptors.
//
// Everything sourced from an iterable structure is derived, not transcribed,
// so the dump tracks the code. The genuinely descriptive fields (a marker's
// effect/example, an option's default) are thin literals whose source is
// cited at the definition site; a package test pins their names against the
// real recognizers/descriptors so a rename can never silently drift the dump.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	validatepb "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/forge/internal/closedset"
	"github.com/reliant-labs/forge/internal/codegen"
	entityscaffold "github.com/reliant-labs/forge/internal/scaffold"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// MarkerSpec describes one `// forge:*` comment marker. AppliesTo is the
// descriptor level it attaches to ("entity" for the message-level markers,
// "field" for the field-level ones).
type MarkerSpec struct {
	Name      string `json:"name"`
	AppliesTo string `json:"applies_to"`
	Effect    string `json:"effect"`
	Placement string `json:"placement"`
	Example   string `json:"example"`
}

// FieldTypeSpec is one row of the proto→column mapping an entity birth
// applies: the field as declared in the message, the column emitted for it,
// and the qualification where the column is not simply the scalar type.
type FieldTypeSpec struct {
	ProtoType string `json:"proto_type"`
	SQLType   string `json:"sql_type"`
	Notes     string `json:"notes"`
}

// ValidateRuleSpec is one projected protovalidate rule and its three
// enforcement points. Rule is the leaf rule name as protovalidate declares it
// (the name the author types: `min_len`, `len`, `gte`), AppliesTo the proto
// scalar kinds the projection actually fires on, and Example the exact option
// text that produced DBEffect/ZodEffect. Those two are SAMPLE projections
// computed by forge's own FieldConstraints methods (column named "value");
// the real literal follows the bound the author declared.
type ValidateRuleSpec struct {
	Rule       string   `json:"rule"`
	AppliesTo  []string `json:"applies_to"`
	Example    string   `json:"example"`
	DBEffect   string   `json:"db_effect"`
	ZodEffect  string   `json:"zod_effect"`
	WireEffect string   `json:"wire_effect"`
}

// OptionSpec is one proto option field on (forge.v1.service) or
// (forge.v1.method). Name/Type are read from the compiled descriptor.
type OptionSpec struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Effect  string `json:"effect"`
	Default string `json:"default"`
}

// AnnotationsSpec is the whole dump. Sections are omitempty so a `--kind`
// filter yields only the selected section(s).
type AnnotationsSpec struct {
	Markers        []MarkerSpec       `json:"markers,omitempty"`
	FieldTypes     []FieldTypeSpec    `json:"field_types,omitempty"`
	ValidateRules  []ValidateRuleSpec `json:"validate_rules,omitempty"`
	ServiceOptions []OptionSpec       `json:"service_options,omitempty"`
	MethodOptions  []OptionSpec       `json:"method_options,omitempty"`
}

// newAnnotationsCmd builds `forge project annotations`. The spec is forge's
// built-in vocabulary, not project state, so the command reads no project
// files and runs anywhere.
func newAnnotationsCmd() *cobra.Command {
	var (
		kind   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "annotations",
		Short: "Dump forge's authoritative entity-authoring annotation spec",
		Long: `Dump forge's authoritative annotation spec.

Emits the vocabulary forge itself understands, sourced from forge's own
definitions so it cannot drift from behavior:

  markers          the // forge:* comment markers, in proto AND Go source,
                   plus the forge:* COMMENT ON COLUMN/CONSTRAINT markers
  field_types      the proto->column mapping an entity birth applies
  validate_rules   the projected protovalidate subset and its db/zod/wire effects
  service_options  the (forge.v1.service) options
  method_options   the (forge.v1.method) options

Use --kind to limit the dump to one annotation level, and --json for a
machine-readable dump that tools can query instead of re-deriving the spec.
--kind column is the forge:* markers declared as a postgres catalog COMMENT
in a migration rather than a proto/Go comment (forge:immutable on a column,
forge:ref on a foreign-key constraint); --kind table is the same mechanism one
level up, on the table itself (forge:append-only). --kind go is the wiring/observability
/contract vocabulary forge reads out of .go files (forge:optional-dep,
forge:constructor, forge:no-observe, forge:exclude-contract, ...) — the
markers you meet in scaffolded code.

Examples:
  forge project annotations --json
  forge project annotations --kind entity --json
  forge project annotations --kind column
  forge project annotations --kind go`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnnotations(cmd.OutOrStdout(), kind, asJSON)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "limit to one annotation kind: entity, field, column, table, service, method, or go (default: all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the spec as JSON")
	return cmd
}

func runAnnotations(w io.Writer, kind string, asJSON bool) error {
	spec, err := buildAnnotationsSpec(kind)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(spec)
	}
	return writeAnnotationsText(w, spec)
}

// buildAnnotationsSpec assembles the full spec and applies the --kind filter.
// Empty kind ⇒ everything; an unknown kind is a user error.
func buildAnnotationsSpec(kind string) (AnnotationsSpec, error) {
	all := AnnotationsSpec{
		Markers:        markerSpecs(),
		FieldTypes:     fieldTypeSpecs(),
		ValidateRules:  validateRuleSpecs(),
		ServiceOptions: serviceOptionSpecs(),
		MethodOptions:  methodOptionSpecs(),
	}
	switch kind {
	case "":
		return all, nil
	case "entity":
		// Only the message-level markers attach to an entity.
		return AnnotationsSpec{Markers: filterMarkers(all.Markers, "entity")}, nil
	case "field":
		// Field-level markers plus the per-field authoring vocabulary.
		return AnnotationsSpec{
			Markers:       filterMarkers(all.Markers, "field"),
			FieldTypes:    all.FieldTypes,
			ValidateRules: all.ValidateRules,
		}, nil
	case "service":
		// Service options plus the one comment marker that attaches to a
		// service (forge:unmanaged).
		return AnnotationsSpec{
			Markers:        filterMarkers(all.Markers, "service"),
			ServiceOptions: all.ServiceOptions,
		}, nil
	case "method":
		// Method options plus the comment markers that attach to an rpc
		// (forge:mutation, forge:query-scope, forge:custom).
		return AnnotationsSpec{
			Markers:       filterMarkers(all.Markers, "method"),
			MethodOptions: all.MethodOptions,
		}, nil
	case "column":
		// Column-comment markers (forge:immutable, forge:ref) — declared as
		// postgres catalog COMMENTs, not proto comments, so they get their
		// own kind rather than polluting the entity/field views.
		return AnnotationsSpec{Markers: filterMarkers(all.Markers, "column")}, nil
	case "table":
		// Table-comment markers (forge:append-only) — the same catalog
		// mechanism as --kind column, declared one level up on the TABLE.
		return AnnotationsSpec{Markers: filterMarkers(all.Markers, "table")}, nil
	case "go":
		// The markers read out of .go source rather than out of proto —
		// the wiring/observability/contract vocabulary. These have no
		// proto counterpart, so they get their own kind rather than
		// polluting the entity/field/method views.
		return AnnotationsSpec{Markers: filterMarkers(all.Markers, goMarkerKinds...)}, nil
	default:
		return AnnotationsSpec{}, fmt.Errorf("unknown --kind %q (want entity, field, column, table, service, method, or go)", kind)
	}
}

// goMarkerKinds are the AppliesTo values of the markers read out of .go
// source rather than proto or SQL: what `--kind go` lists.
var goMarkerKinds = []string{"package", "contract", "constructor", "deps-field", "function", "root", "declaration"}

func filterMarkers(in []MarkerSpec, appliesTo ...string) []MarkerSpec {
	want := make(map[string]bool, len(appliesTo))
	for _, a := range appliesTo {
		want[a] = true
	}
	var out []MarkerSpec
	for _, m := range in {
		if want[m.AppliesTo] {
			out = append(out, m)
		}
	}
	return out
}

// markerSpecs is the one thin literal projection in this file — the
// DESCRIPTIONS are literals; the marker NAMES are not.
//
// Proto marker names come from codegen.KnownProtoMarkers and column marker
// names from schemadef.KnownColumnMarkers, the same two registries the
// scanners spell their markers from and `forge lint`'s unknown-marker checks
// enforce, so this dump cannot advertise a vocabulary that differs from the
// one forge actually reads. The placement grammar behind each proto name is
// owned by the regexes those constants build (proto_rawscan.go's
// entityMarkerRE / softDeleteMarkerRE / appendOnlyMarkerRE /
// readOnlyFieldMarkerRE, forge_descriptor.go's secretFieldMarkerRE, and
// generate_frontend_hooks.go's mutationMarkerRE). The effects are the
// birth/read behaviors in internal/scaffold/entityproto.go and
// internal/codegen/crud_convert.go.
//
// Go-source marker names remain literals here: they have no registry,
// because each is read by a single recognizer in its own package rather than
// by a vocabulary shared across passes. TestAnnotations_MarkerNames pins
// every one of them against its real recognizer.
// the number of markers forge supports, and splitting it across helpers
// would scatter one inventory for no reduction in what a reader must hold.
//
//nolint:funlen // One flat table of marker specs, no branching: length is
func markerSpecs() []MarkerSpec {
	return []MarkerSpec{
		{
			Name:      codegen.ProtoMarkerEntity,
			AppliesTo: "entity",
			Effect:    "Marks a top-level proto message as a database entity: forge births the create-table migration and the CRUD quintet (Create/Get/Update/Delete/List) at scaffold time, and DECLARES the managed fields it doesn't already have (id, created_at, updated_at) on the message itself — appended at free field numbers, never renumbering yours.",
			Placement: "full-line leading comment immediately above `message X {`",
			Example:   "// forge:entity\nmessage Order { ... }",
		},
		{
			Name:      codegen.ProtoMarkerSoftDelete,
			AppliesTo: "entity",
			Effect:    "Entity is born with a nullable deleted_at column, declared on the message alongside the other managed fields; Delete becomes an UPDATE and reads filter `deleted_at IS NULL`. Also tablizes the message (implies forge:entity).",
			Placement: "full-line leading comment immediately above `message X {`",
			Example:   "// forge:soft-delete\nmessage Note { ... }",
		},
		{
			Name:      codegen.ProtoMarkerAppendOnly,
			AppliesTo: "entity",
			Effect:    "Entity is born WITHOUT Update/Delete RPCs and with a DB trigger that rejects every UPDATE/DELETE (an immutable ledger). Also tablizes the message (implies forge:entity).",
			Placement: "full-line leading comment immediately above `message X {`",
			Example:   "// forge:append-only\nmessage AuditEvent { ... }",
		},
		{
			Name:      codegen.ProtoMarkerReadOnly,
			AppliesTo: "field",
			Effect:    "Field is kept on the entity message and Get/List responses but is never CLIENT-writable — the write-side mirror of forge:secret. At birth it is OMITTED from the Create request. On every `forge generate` it also shapes the CRUD Update, whose AIP-134 request carries the whole entity: an update_mask naming the column is InvalidArgument (reason unknown_field), and a full replace leaves it as stored whatever the request carried (crud.Preserve), so a client can neither set it nor reset it to the column default by leaving it out. Your own code still writes it — Create, or db.Update<Entity>Masked naming it, which is what a custom RPC owning a state machine calls; a plain db.Update<Entity> from app code writes it too. Nothing assigns the value for you: give the column a DB DEFAULT (or set it in your handler), or an insert lands the zero value. If the value is DERIVED rather than defaulted, use forge:computed instead — it does the same thing and lint holds you to the derivation.",
			Placement: "leading full-line comment above the field, or a trailing comment after the field's terminating `;` — inline `[...]` options, including multi-line braced protovalidate values, sit between them harmlessly. A marker no field consumes REFUSES the birth, naming file and line.",
			Example:   "int64 unit_price_cents = 4 [(buf.validate.field).int64.gte = 0]; // forge:read-only",
		},
		{
			Name:      codegen.ProtoMarkerComputed,
			AppliesTo: "field",
			Effect:    "Everything forge:read-only does (kept on the entity + Get/List, OMITTED from the born Create request, and never written by a client Update — a mask naming it is refused, a full replace leaves it as stored), PLUS a declared obligation that your app derives the value. `forge lint --computed-fields` fails the field when no non-generated Go file assigns it. Because no CRUD Update writes it, derive it where its inputs change: a GENERATED column for a same-row value, db.Update<Entity>Masked naming it for one that depends on other rows. Use this instead of forge:read-only whenever the value is calculated rather than defaulted: a read-only field nothing computes takes the column DEFAULT, which for a money column is 0 — no constraint is violated, no test fails, and the only symptom is a screen showing $0.00.",
			Placement: "leading full-line comment above the field, or a trailing comment after the field's terminating `;` — same two positions as forge:read-only, and a marker no field consumes REFUSES the birth.",
			Example:   "// quantity_milli * unit_price_cents / 1000, maintained on write.\nint64 amount_cents = 7; // forge:computed",
		},
		{
			Name:      codegen.ProtoMarkerGenerated,
			AppliesTo: "field",
			Effect:    "Births the column as `<col> <type> NOT NULL GENERATED ALWAYS AS (<expr>) STORED`, the expression copied verbatim from the rest of the comment line — postgres computes the value on every INSERT and UPDATE and refuses any write to it. NOT NULL exactly when the field is not `optional` (a Timestamp is always nullable), the rule every born column follows, so an expression that yields NULL fails that write loudly; no DEFAULT; an enum keeps its CHECK and protovalidate rules still project to CHECKs. Implies forge:read-only — off the born Create request, and the generated CRUD Update refuses an update_mask naming it — which only moves to the API edge a refusal postgres would make anyway; the ORM already keeps the column out of every INSERT and SET (it projects with `forge:\"generated\"` on its struct tag) and reads it back after each write. Before writing anything, birth applies the rendered migration to the shadow database; an expression postgres rejects (unknown column, wrong type, a non-IMMUTABLE function, a reference to another generated column) REFUSES the birth, naming the marker's file and line and postgres's message. Use it for a value derived from the SAME row (line_total = quantity × unit price, balance = amount − amount_paid); a value derived from OTHER rows cannot be a generated column — use forge:computed. Single-valued scalar, enum and Timestamp fields only. Inert after birth: the applied schema's GENERATED column is the truth, evolved by a new migration.",
			Placement: "leading full-line comment above the field, or a trailing comment after the field's terminating `;` — same two positions as forge:read-only. The expression is the REST OF THE LINE after the marker and a space, so prose explaining it goes on its own comment line above.",
			Example:   "int64 line_total_cents = 9; // forge:generated round(quantity * unit_price_cents)::BIGINT",
		},
		{
			Name:      codegen.ProtoMarkerGuards,
			AppliesTo: "field",
			Effect:    "Declares that THIS rpc owns the named column's writes, so the scaffolded CRUD edit page drops that column from its update_mask and renders it as a disabled row naming this rpc instead. Written on a field of a CUSTOM rpc's request message; the field it sits on is only the attachment point — the subject is the `<table>.<column>` it names, which is frequently on another message entirely. Without it the page generator reasons from the entity's column list alone, knows nothing of what your rpcs enforce, and writes the column raw: lower an invoice's amount_cents below its amount_paid_cents through the scaffolded form and the raw CHECK surfaces as a 500, with the state machine RecordPayment implements bypassed entirely. The target is EXPLICIT because it cannot be inferred — RecordPaymentRequest guards invoices.amount_paid_cents, a column its request never names, while the field it does share a name with (amount_cents) is a freely editable column on a different table. Scaffolded pages are written once and never regenerated, so `forge lint --guarded-fields` reports pages that predate the marker.",
			Placement: "leading full-line comment above the request field, or a trailing comment on the field line. Repeat the marker, one target per line, for an rpc that guards several columns.",
			Example:   "int64 amount_cents = 2; // forge:guards invoices.amount_paid_cents",
		},
		{
			Name:      codegen.ProtoMarkerMutation,
			AppliesTo: "method",
			Effect:    "Forces the generated React Query hook for this rpc to be a useMutation. Classification is otherwise: an rpc declaring protobuf's `option idempotency_level = NO_SIDE_EFFECTS;` is a read whatever its name (Health, Version — declare it on every read whose name is not a read verb); without it, by leading verb — Get/List/Search/Find/Check/Has/Is/Count/Exists (whole-word) are reads, EVERYTHING else is a mutation (IDEMPOTENT does not make a read). So this marker is needed only for an imperative rpc that happens to open with a read word (FindAndHoldSeat, IssueRefund), and it wins over NO_SIDE_EFFECTS. A write generated as useQuery re-fires on every component remount. Unlike the entity/field markers this one is read on every `forge generate`, not just at birth.",
			Placement: "leading full-line comment above the `rpc` line, or a trailing comment on it",
			Example:   "// forge:mutation — holds inventory, so it must never be a cached read.\nrpc FindAndHoldSeat(FindAndHoldSeatRequest) returns (FindAndHoldSeatResponse);",
		},
		{
			Name:      codegen.ProtoMarkerQueryScope,
			AppliesTo: "method",
			Effect:    "Keys a custom READ's generated query under an entity scope: the key becomes [service, <entity>, method, req] and `<svc>Keys.<entity>` is declared, so every CRUD mutation of that entity on the service (which invalidates exactly that scope) refreshes the read. Without it only a CRUD read is entity-scoped (by its name); a custom read sits at service scope and only `<svc>Keys.all` reaches it, so a CreateRun leaves LastRunPerWorkflow stale. The entity is DECLARED, never inferred from a `<entity>_id` request field — that field names what the read is filtered by, not what it reads. Name the entity's proto message exactly (`Run`, not `run`): a CRUD entity of the service or a message it declares. Generate refuses the marker with no entity, on an rpc that generates a useMutation (a custom mutation already invalidates the whole service), on a CRUD rpc keyed under a different entity, and on a name that is no entity of the service. Read on every `forge generate`.",
			Placement: "leading full-line comment above the `rpc` line, or a trailing comment on it — same as forge:mutation",
			Example:   "// forge:query-scope Run — reads runs; a run write must refresh it.\nrpc LastRunPerWorkflow(LastRunPerWorkflowRequest) returns (LastRunPerWorkflowResponse) {\n  option idempotency_level = NO_SIDE_EFFECTS;\n}",
		},
		{
			Name:      codegen.ProtoMarkerCustom,
			AppliesTo: "method",
			Effect:    "Declares this rpc custom whatever its name. Without it a unary rpc named exactly Create<E>/Get<E>/Update<E>/Delete<E>/List<Es>, where <E> has a table and a wire message, is CRUD and gets generic CRUD wiring. Marked, it is custom on both sides of the wire: a pb-through stub in rpc_<name>.go plus a scaffold test row, no CRUD shim or op, no entity page, a hook that invalidates at service scope, and it is exempt from the list-filter lint. It makes no CRUD entity, so when EVERY CRUD-named rpc of a table carries it the table gets no CRUD wiring, no page and no seed root — but it keeps its store: the ORM projects every table whatever its rpcs (`forge:no-entity` on the table is the store opt-out). Use it instead of renaming an rpc to dodge detection. Marking an rpc that was CRUD stops forge generating its crud<Rpc>Op, so generate refuses while a shim still calls it — delete that method (or replace its body) first. A near-miss spelling, or the marker on a service or message, fails generate.",
			Placement: "full-line comment directly above the `rpc` line. Never trailing: protobuf hands a comment after an rpc (same line, or the line below followed by a blank line) to THAT rpc even when it was written for the next one, so generate refuses a trailing marker, and refuses a marker written above an rpc that the compiled descriptor did not attach to it.",
			Example:   "// forge:custom — creating a task also wires its dependencies and fires triggers.\nrpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);",
		},
		{
			Name:      codegen.ProtoMarkerUnmanaged,
			AppliesTo: "service",
			Effect:    "Takes the whole service out of forge's service model, for a project whose handlers for it live somewhere else — the incremental way onto forge. buf still generates its Go/TS code and it stays in gen/forge_descriptor/, but forge generates nothing service-scoped for it: no internal/handlers/<svc>/ or rpc stubs, no mocks, CRUD wiring or entities, no mount, compose entry or per-service subcommand, no frontend hooks or pages; `forge scaffold` skips it and refuses `scaffold service|rpc|entity --from-proto` on it; audit, shapes and the list-filter lint treat it as not forge's. Its rpcs' auth_required declarations still project into pkg/middleware/procedures_gen.go, because whether an rpc is public is part of the contract whoever serves it. `forge project audit` lists every unmanaged service (shape → unmanaged_services). Adopt one by deleting the line: the next `forge generate` scaffolds it like any new service. A near-miss spelling, or the marker on an rpc or message, fails generate.",
			Placement: "full-line comment above `service X {`, with only comment or blank lines between",
			Example:   "// forge:unmanaged — handlers still in internal/grpc/services; adopt one service per PR.\nservice ChatService { ... }",
		},
		{
			Name:      codegen.ProtoMarkerSecret,
			AppliesTo: "field",
			Effect:    "Column stays real schema truth but is stripped from read responses — the generated <entity>ToProto never packs it, so Get/List omit it and it is excluded from the list search span. Because a client therefore reads it back as \"\", the column is also tagged ,skipupdate: a maskless full-replace Update leaves the stored value alone rather than clobbering the credential with that \"\". Settable on Create, and on an explicit update_mask that names it.",
			Placement: "leading full-line comment above the field, or a trailing comment on the field line",
			Example:   "string api_token = 2; // forge:secret",
		},

		// ── Column-comment markers ──
		//
		// Declared as a postgres catalog COMMENT applied by a migration, not
		// as a `//` proto comment — these attach to the APPLIED schema, which
		// is forge's one source of truth for columns and constraints (see the
		// `db` skill). schemadef.Column.HasMarker / schemadef.KnownColumnMarkers
		// is the single registry both this dump and `forge lint`'s
		// unknown-column-marker check read, so the two cannot drift.
		{
			Name:      schemadef.ColumnMarkerImmutable,
			AppliesTo: "column",
			Effect:    "The column is omitted from EVERY full-replace UPDATE's SET clause — projects to Bun's `,skipupdate` tag on the generated struct, so app code's db.Update<Entity> skips it as well as the client's — while an explicit update_mask naming it still writes it, from a client too. Use for a value no full replace should rewrite (an owner id, an externally-issued identifier). To keep a value away from CLIENTS while your own code keeps full-replace writes, mark the wire field forge:read-only instead.",
			Placement: "COMMENT ON COLUMN <table>.<col> IS 'forge:immutable'; in a migration",
			Example:   "COMMENT ON COLUMN invoices.total_cents IS 'forge:immutable';",
		},
		{
			Name:      schemadef.ColumnMarkerRef,
			AppliesTo: "column",
			Effect:    "Declares which route is authoritative when a table reaches the same parent two ways (a direct FK and a longer FK chain) — `authoritative` (this edge is the truth), `derived-from=<column>` (seed this column from the named edge), or `independent` (unrelated facts). Consumed by `forge db seed`'s diamond resolution; undeclared diamonds are refused rather than silently seeded inconsistently. Full grammar and worked examples: the `db/seeding` skill.",
			Placement: "COMMENT ON CONSTRAINT <fk_name> ON <table> IS 'forge:ref ...'; in a migration — on the FOREIGN KEY constraint, NOT a column comment.",
			Example:   "COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref derived-from=prescription_id';",
		},
		{
			Name:      schemadef.ColumnMarkerClosedSet,
			AppliesTo: "column",
			Effect:    "Declares that a CHECK's value list mirrors a closed Go set. `forge lint` (the closed-sets lane, on by default; `--closed-sets` alone) reads the CHECK the migrations LAST defined for the constraint — `col <@ ARRAY[…]` for an array column, `col = ANY (ARRAY[…])` or `col IN (…)` for a scalar — and fails when its values differ from the set, naming the missing and extra values and printing the forward migration (`forge db migration new --closed-set <constraint>` writes it). The set is a forge-exported name (" + strings.Join(closedset.Names(), ", ") + ") or a project set's qualified Go name, `<import path>.<Var>`, whose initializer is a literal of constants; forge type-checks it and runs nothing. postgres drops a constraint's comment with the constraint, so a migration that replaces the CHECK must re-issue the COMMENT — the lane reports a declaration lost that way.",
			Placement: "COMMENT ON CONSTRAINT <check_name> ON <table> IS 'forge:closed-set <set>'; in a migration, after the CHECK exists — on the CHECK constraint, NOT the column.",
			Example:   "COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS 'forge:closed-set accesstoken.AllScopes';",
		},
		{
			Name:      schemadef.ColumnMarkerVersion,
			AppliesTo: "column",
			Effect:    "Opts the entity into optimistic concurrency control: Update/UpdateMasked add this column to the WHERE clause (matched against the caller's last-read value) and increment it on a successful write. A write that loses the race to a concurrent writer fails with Connect code Aborted instead of silently overwriting the other writer's change. REQUIRES a matching field on the entity's wire message (`int64 version = N;  // forge:read-only`) — the check compares the version the CALLER presents, so a column the caller cannot read back and return is always the zero value, and every update after a row's first one fails Aborted; `forge generate` refuses the column-only half rather than emit that. The repo owns the increment, so the client's proposed value is never trusted: forge:read-only keeps the field off the Create/Update request shapes while leaving it on the entity and the read responses, and an update_mask naming the column is an unknown_field error.",
			Placement: "BOTH halves, in the same change: ALTER TABLE <table> ADD COLUMN <col> BIGINT NOT NULL DEFAULT 0; plus COMMENT ON COLUMN <table>.<col> IS 'forge:version'; in a migration, AND `int64 <col> = N;  // forge:read-only` on the entity message in the proto",
			Example:   "COMMENT ON COLUMN crews.version IS 'forge:version';  (with `int64 version = 12;  // forge:read-only` on message Crew)",
		},
		{
			Name:      schemadef.ColumnMarkerFill,
			AppliesTo: "column",
			Effect:    "Declares WHO fills a forge:read-only column the generated Create/Update never carries a value for — `ulid` (forge generates one at Create, the same chokepoint that ULID-generates an empty string PK; non-PK columns only) or `handler` (pure acknowledgement — no codegen behavior changes, but the create shim scaffolds an op.Entity wrapper with a FORGE_SCAFFOLD reminder naming the column). Suppresses `forge lint`'s unsatisfiable-column check, which otherwise fails the build for a NOT NULL column with no DB DEFAULT and no forge:fill declaration — that combination cannot be inserted through the generated CRUD path at all.",
			Placement: "COMMENT ON COLUMN <table>.<col> IS 'forge:fill=ulid'; or 'forge:fill=handler'; in a migration",
			Example:   "COMMENT ON COLUMN customers.company_id IS 'forge:fill=handler';",
		},
		{
			Name:      schemadef.ColumnMarkerGoType,
			AppliesTo: "column",
			Effect:    "Declares the Go type the column projects to on the generated entity struct, in place of the one its SQL type maps to (TEXT → string, INTEGER → int32, SMALLINT → int16, BIGINT → int64, JSONB → json.RawMessage, …): a named enum type over an integer column, `map[string]any` or a struct over a json column, `time.Duration` over a bigint. The type must scan from and bind to the column by Bun's own rules. A nullable column gets a pointer unless the type is already a pointer, slice or map. A qualified type names its import path — `<path>.<Type>`, with `<path>;<package>.<Type>` when the package name is not the path's last element (the go_package convention). forge generates no wire conversion for the column: a CRUD rpc whose wire message carries it fails `forge generate` naming it — convert it in the owned shim, or keep it off the wire.",
			Placement: "COMMENT ON COLUMN <table>.<col> IS 'forge:go-type=<type>'; in a migration",
			Example:   "COMMENT ON COLUMN message_content_blocks.block_type IS 'forge:go-type=github.com/acme/app/gen/app/v1;appv1.ContentBlockType';",
		},
		{
			Name:      schemadef.ColumnMarkerNoSoftDelete,
			AppliesTo: "column",
			Effect:    "Declares the table's `deleted_at` an ordinary nullable timestamp instead of forge's soft-delete tombstone — for an ARCHIVE stamp, where an archived row must stay readable and writable and a delete must delete. Without it a time-typed deleted_at IS the soft-delete convention: Bun's `,soft_delete` tag, Get/List/Update skip rows where it is set, Delete stamps it, and ListAll<E> reads every row. Declared, the column gets no soft-delete tag, Get/List see every row, Update and an update_mask may write the column, Delete is a hard DELETE, no ListAll<E> is generated, and the generated list/detail pages show the column. Only meaningful on deleted_at; on any other column `forge generate` and `forge lint` reject it.",
			Placement: "COMMENT ON COLUMN <table>.deleted_at IS 'forge:no-soft-delete'; in a migration",
			Example:   "COMMENT ON COLUMN worktrees.deleted_at IS 'forge:no-soft-delete — the archive stamp; archived worktrees stay readable';",
		},
		{
			Name:      schemadef.ColumnMarkerSecret,
			AppliesTo: "column",
			Effect:    "Withholds the column from the database's read-only role (forge_ro, which `forge db query` and the control plane read through): Postgres refuses it by name, through `select *`, `upper(col)` and `row_to_json`. It is the SQL spelling of the proto `// forge:secret` field marker, and the only one a column with no wire field can use — most credential tables (sealed tokens, ciphertext, token hashes) have none. Either spelling withholds the column from the role; only the proto one also strips the field from read responses. A credential-shaped column (token, password, secret, key, ciphertext, sealed, hash …) with neither this nor forge:readable is UNDECIDED: the grants withhold it and `forge lint --db-grants` fails naming it.\n\nThe pattern list: `forge skill load db/query`.",
			Placement: "COMMENT ON COLUMN <table>.<col> IS 'forge:secret'; in a migration",
			Example:   "COMMENT ON COLUMN git_credentials.access_token IS 'forge:secret';",
		},
		{
			Name:      schemadef.ColumnMarkerReadable,
			AppliesTo: "column",
			Effect:    "Grants the read-only role a column whose NAME looks like a credential but is not one (`token_hash` of a prompt, a public `api_key_prefix`, a bytea attachment). The reason after the marker is mandatory — it is the review record of an exception to the role's fail-closed default — and `forge generate` rejects a bare `forge:readable`. forge:secret wins when a column declares both. Grants exactly that column; the rest of the table stays as decided.",
			Placement: "COMMENT ON COLUMN <table>.<col> IS 'forge:readable <why this is not a credential>'; in a migration",
			Example:   "COMMENT ON COLUMN usage_events.token_hash IS 'forge:readable — SHA-256 of the prompt, a dedupe key, not of a credential';",
		},
		// ── Table-comment markers ──
		//
		// The same catalog-comment mechanism as the column markers above,
		// one level up: COMMENT ON TABLE, not COMMENT ON COLUMN. Its own
		// AppliesTo because the placement genuinely differs, and a marker
		// documented at the wrong level is a marker the reader writes in the
		// wrong place — which, for a catalog comment, is completely silent.
		{
			Name:      schemadef.TableMarkerAppendOnly,
			AppliesTo: "table",
			Effect:    "Declares the TABLE an immutable ledger: BOTH generated routes to a write lose Update/UpdateMasked/Delete — the <Entity>Store interface and its adapter, and the package-level delegates in <entity>_orm_gen.go — so `db.DeleteX(ctx, tx, id)` does not resolve and a call that would rewrite or erase a row is a compile error rather than a runtime rejection. (Narrowing only the interface left the exported delegates reopening the same write, which decayed the guarantee back to a SQLSTATE P0001.) Reads, Create and WithTx are untouched. The generated test factory takes no overrides (applying one means UPDATEing the row it just inserted), and an Update/Delete RPC declared against the table fails `forge generate` by name, since there is no delegate left for the op to call. This is the STORAGE half of the `forge:append-only` proto marker and the half that every post-birth pass reads — the proto marker is a birth-time instruction (it writes the trigger and omits the Update/Delete RPCs) that nothing carries forward. `forge scaffold entity --from-proto` emits this declaration alongside the guard trigger; declare it by hand on a table a hand-written migration made append-only. A table forge made append-only BEFORE this declaration existed needs no migration: its `<table>_append_only` guard trigger is read as the same declaration, matched by that exact name (an updated_at stamper is also BEFORE UPDATE and means the opposite).",
			Placement: "COMMENT ON TABLE <table> IS 'forge:append-only'; in a migration — on the TABLE, not a column.",
			Example:   "COMMENT ON TABLE payments IS 'forge:append-only — rows are inserted and read, never rewritten or erased.';",
		},
		{
			Name:      schemadef.TableMarkerNoEntity,
			AppliesTo: "table",
			Effect:    "Keeps the TABLE out of the generated data layer: no <entity>_orm_gen.go struct, no <Entity>Store, no delegates, no factory — and no CRUD wiring, since a CRUD-named rpc over it has no store to call (it is custom, as if the table did not exist). Without it every applied BASE TABLE is projected, whether or not any service declares a CRUD rpc for it: the store follows the schema, not the wire, so `// forge:custom` rpcs do not take a table off the store. Declare it on a table hand-written SQL owns (another tool's bookkeeping, a partition parent), or on one whose projected name collides with a declaration of yours while you port onto the store. `//forge:no-orm` is the same opt-out for the whole data layer.",
			Placement: "COMMENT ON TABLE <table> IS 'forge:no-entity'; in a migration — on the TABLE, not a column.",
			Example:   "COMMENT ON TABLE goose_db_version IS 'forge:no-entity — goose owns it';",
		},
		{
			Name:      schemadef.TableMarkerEntityName,
			AppliesTo: "table",
			Effect:    "Names the entity the TABLE holds, for a table whose name derives to something else. Without it the entity is named from the table — the singular, camel-cased (`message_content_blocks` → MessageContentBlock, `daemon_attachment` → DaemonAttachment) — and a CRUD rpc's entity <E> finds its table by that name, by the plural of <E> (`bookmarks`) or by its singular (`daemon_attachment`). Declared, the store is named <Name> and a `Get<Name>`/`List<Names>`/… rpc over a <Name> message is CRUD over this table. A CRUD-named rpc whose entity has a wire message but matches no table, and that no handler implements by hand, fails `forge generate` by name rather than serving forge's Unimplemented stub — this declaration is one of its fixes.",
			Placement: "COMMENT ON TABLE <table> IS 'forge:entity-name=<Name>'; in a migration — on the TABLE, not a column.",
			Example:   "COMMENT ON TABLE tbl_widget_v2 IS 'forge:entity-name=Widget';",
		},
		{
			Name:      schemadef.ViewMarkerEntity,
			AppliesTo: "table", // on a VIEW — the table-level catalog comment of a relation that is not a base table
			Effect:    "Opts a VIEW (regular or materialized) into the generated data layer as a READ-ONLY entity: the row struct (named from the view: `chats_with_activity` → ChatsWithActivity), List<E>/Count<E>, and Get<E>ByID by its key column — `id` when the view has one, or the column `forge:entity key=<column>` names; a keyless view gets List/Count only. Nothing writes: no Create/Update/Delete delegate or store method is generated, so a write through a view is a compile error. The catalog cannot say whether a view column is nullable, so every column but the key projects as a pointer (the key must never be NULL). Views without the marker are not projected — most are reporting conveniences nothing in Go reads.",
			Placement: "COMMENT ON VIEW <view> IS 'forge:entity'; (or COMMENT ON MATERIALIZED VIEW) in a migration, optionally 'forge:entity key=<column>'.",
			Example:   "COMMENT ON VIEW chats_with_activity IS 'forge:entity — the sidebar row';",
		},
		{
			Name:      schemadef.ColumnMarkerOwner,
			AppliesTo: "column",
			Effect:    "Declares this column as the one naming WHO a row belongs to. Changes NO codegen — forge ships no ownership of its own and never injects a WHERE clause for you, because only your app knows how a caller's claims map onto this column's values. What it changes is what forge lets ship: it ARMS the `unscoped_auth` audit category for this table, so an authenticated RPC over this entity whose handler never resolves the caller stops being a warning and becomes an ERROR, failing `forge project audit`. Declare nothing and the category behaves exactly as before (warn, exit 0), which is why a fresh scaffold is unaffected. The gate arms PER TABLE — declaring an owner on customers does not fail an unscoped RPC over a global product catalog — and `// forge:auth-unscoped-ok: <reason>` above the handler suppresses the error for a legitimately global RPC (the reason is mandatory; a bare directive does not count). Pairs naturally with forge:immutable on the same column, since an owner column is exactly the kind a full-replace Update must not zero.",
			Placement: "COMMENT ON COLUMN <table>.<col> IS 'forge:owner'; in a migration",
			Example:   "COMMENT ON COLUMN customers.company_id IS 'forge:owner';",
		},

		// ── Go-source markers ──
		//
		// These are read out of the project's .go files, not its protos.
		// Both the spaced (`// forge:x`) and unspaced (`//forge:x`) forms are
		// accepted, and the marker must be the WHOLE comment line after
		// stripping comment syntax and whitespace — so prose that merely
		// mentions a directive is never mistaken for one.
		{
			Name:      "forge:optional-dep",
			AppliesTo: "deps-field",
			Effect:    "The Deps field may legitimately be nil at construction: validateDeps must not enforce it, and composition emits the typed zero silently rather than reporting an unresolved dependency. Guard each use (`if s.deps.X != nil`). Without the marker, a required non-scalar Deps field that resolves to nothing is a loud generate-time or compile-time error — forge never emits a silent typed-zero for a required field.",
			Placement: "leading full-line comment above the Deps struct field, or a trailing comment on it",
			Example:   "type Deps struct {\n\t// forge:optional-dep\n\tStripe StripeClient\n}",
		},
		{
			Name:      "forge:optional-checked",
			AppliesTo: "deps-field",
			Effect:    "Suppresses `forge lint --optional-deps-guard` on one dereference of a `forge:optional-dep` field that is provably safe by reasoning the analyzer cannot follow.",
			Placement: "on the dereferencing line, or the line above it",
			Example:   "return s.deps.Stripe.Charge(ctx, amt) // forge:optional-checked",
		},
		{
			Name:      "forge:constructor",
			AppliesTo: "constructor",
			Effect:    "Names this function as the package's component constructor when it is not the canonical `New`. (It no longer decides instrumentation: every package whose constructor returns its contract gets the generated observability decorator, middleware_gen.go, unless it carries `forge:no-observe`.)",
			Placement: "on the constructor's doc comment",
			Example:   "// NewClient builds the client.\n// forge:constructor\nfunc NewClient(d Deps) (Service, error)",
		},
		{
			Name:      "forge:no-observe",
			AppliesTo: "constructor",
			Effect:    "Opts out of the generated observability decorator, which every package whose constructor returns its contract gets by default. On the constructor it exempts the whole package (no middleware_gen.go, and compose.go constructs it unwrapped); on a single interface method it routes just that method around the chain.",
			Placement: "on the constructor's doc comment (whole package), or above one interface method",
			Example:   "type Service interface {\n\t// forge:no-observe\n\tHealthz() error\n}",
		},
		{
			Name:      "forge:service",
			AppliesTo: "contract",
			Effect:    "Declares this interface to be the package's contract when it is not named `Service`. `forge:contract` is an accepted synonym. Names are deliberately free — `Service` is a poor name for a mailer or a store — but the signature is not: the constructor must take Deps and return the contract, or there is nothing to wire.",
			Placement: "on the interface's doc comment",
			Example:   "// forge:service\ntype Mailer interface { Send(context.Context, Message) error }",
		},
		{
			Name:      "forge:exclude-contract",
			AppliesTo: "package",
			Effect:    "Opts the package out of contract codegen entirely — no canonical-shape check, no wiring, and NO generated mock. The per-package equivalent of listing it in forge.yaml `contracts.exclude`. REQUIRES a reason (`forge:exclude-contract: <why>`); a bare marker is a `forge lint` error. Refused outright on a package that does outbound I/O (make it an adapter) or owns a multi-implementation interface it offers (that interface is the contract — a seam the package itself consumes, as a field or parameter, does not count). Legitimate for pure functions, test-support, API/CRD types, a strategy registry.",
			Placement: "package doc comment, or a free-standing comment in any of the package's .go files",
			Example:   "//forge:exclude-contract: strategy registry — one constructor per algorithm\npackage strategyregistry",
		},
		{
			Name:      "forge:external-component",
			AppliesTo: "package",
			Effect:    "This component is hand-constructed in providers.go / OpenInfra rather than by the injector, so the injector skips it — but the package STILL gets its contract and mock codegen. For a package that is contract-shaped and wants its mock, but whose construction is bespoke (adapter wrapping, two-phase setters, a dialer nil'd on unset env) and so cannot be a plain New(Deps) node. `forge:provided` is an accepted synonym.",
			Placement: "package doc comment, or a free-standing comment in any of the package's .go files",
			Example:   "// forge:external-component\npackage natsbus",
		},
		{
			Name:      "forge:not-a-component",
			AppliesTo: "package",
			Effect:    "The server does not have this package: the CLI, a daemon or another binary constructs it. It keeps contract.go, its generated mock and (with `forge:constructor`) its decorator, but compose.go never constructs it, never resolves its Deps and gives it no Infra field. A server component that requires it by type fails `forge generate`, because the marker would then be false. Unlike `forge:external-component`, which says the server builds it by hand, this says the server does not build it at all. REQUIRES a reason naming who does (`forge:not-a-component: <why>`); a bare marker, or one on a handler/worker/operator or a package without contract.go, is a `forge lint` error.",
			Placement: "package doc comment, or a free-standing comment in any of an internal/<pkg> contract package's .go files",
			Example:   "//forge:not-a-component: CLI-only — built by `reliant login`\npackage cliauth",
		},
		{
			Name:      "forge:outbound-io",
			AppliesTo: "package",
			Effect:    "Asserts the package is an outbound boundary — it talks OUT to a third-party system and must not serve inbound RPCs. `forge lint` enforces that with forgeconv-outbound-io-no-rpc.",
			Placement: "package doc comment, or a free-standing comment in any of the package's .go files",
			Example:   "// forge:outbound-io\npackage stripeadapter",
		},
		{
			Name:      codegen.ResolvesCallerDirective,
			AppliesTo: "function",
			Effect:    "Declares that calling this function resolves the caller: it reads the authenticated principal the auth interceptor put on the request context. Forge's own seam (middleware.GetUser / ClaimsFromContext in the scaffolded pkg/middleware) needs no marker. A project with its own auth package marks its accessors, and the unscoped_auth audit then counts a handler that calls one — through the import that binds the declaring package in that file, or through a helper in the handler's own package — as resolving the caller, and names it in its hint. Package-level functions only: a marker on a method is reported as ignored, because a call site does not name its receiver's type. `forge project audit --json` lists the declarations it found under unscoped_auth's caller_resolvers.",
			Placement: "on the function's doc comment, spaced or unspaced, as the whole comment line",
			Example:   "// GetUserIDFromContext returns the signed-in user's id.\n//\n//forge:resolves-caller\nfunc GetUserIDFromContext(ctx context.Context) (string, bool)",
		},
		{
			Name:      "forge:contract",
			AppliesTo: "contract",
			Effect:    "Synonym of `forge:service`: declares this interface the package's contract when it is not named `Service`. `contract` reads better on a non-service package.",
			Placement: "on the interface's doc comment",
			Example:   "// forge:contract\ntype Store interface { Get(ctx context.Context, id string) (Row, error) }",
		},
		{
			Name:      "forge:provided",
			AppliesTo: "package",
			Effect:    "Synonym of `forge:external-component`: the component is hand-constructed in providers.go, and the injector skips it.",
			Placement: "package doc comment, or a free-standing comment in any of the package's .go files",
			Example:   "// forge:provided\npackage natsbus",
		},
		{
			Name:      "forge:no-orm",
			AppliesTo: "package",
			Effect:    "Switches the generated data layer off for the project: no <entity>_orm_gen.go structs, stores or factories under internal/db. The opt-out sits in the package it exempts; the reason is not required, but write one.",
			Placement: "package doc comment of internal/db (spaced or unspaced); a reason follows a colon or a space",
			Example:   "//forge:no-orm: the repository is hand-written; entities are not projected\npackage db",
		},
		{
			Name:      "forge:placeholder",
			AppliesTo: "deps-field",
			Effect:    "Records the type a Deps field typed `any` is waiting for — a placeholder left until the dependency it names exists. While the field is still `any`, the wire-coverage lint errors on it as unresolved: the marker is a TODO forge can count, not a way to wire `any`.",
			Placement: "leading or trailing comment on the Deps field; the type follows a colon",
			Example:   "type Deps struct {\n\tUserRepo any // forge:placeholder: user.Repository\n}",
		},
		{
			Name:      "forge:root",
			AppliesTo: "root",
			Effect:    "Declares a struct in internal/app as a process ROOT: its fields are the nodes that process runs, and forge generates its command into roots_gen.go. With no root declared, the generated default `server` root runs every component. Options: command=<word>[,<word>…] (the command path that runs it) and migrate=apply|wait|none (what its process does about the schema); an unknown option fails generate.",
			Placement: "on the struct's doc comment, unspaced",
			Example:   "//forge:root api command=serve migrate=apply\ntype API struct { ... }",
		},
		{
			Name:      "forge:group",
			AppliesTo: "root",
			Effect:    "Declares the help of a GROUP command — one roots_gen.go writes only to hold roots' commands (`server` above `server api`). The comment's text is the help by the root doc's rule: the first paragraph the Short, the rest the Long. Undeclared, a group's help is generated. A path no root's command runs beneath, or a root's own command, fails generate.",
			Placement: "a comment of its own in an owned internal/app file (not a //forge:root's doc), unspaced, last; the path's words comma-separated",
			Example:   "// Run cloud server components\n//\n// Each subcommand starts one server role.\n//\n//forge:group server",
		},
		{
			Name:      "forge:root-only",
			AppliesTo: "function",
			Effect:    "Scopes a provider to the named roots: a root-specific variant of a component (reliant's worker-only daemon router, which may act on a trigger's delegated token). Resolution fills only a Deps field of the provider's very type with it, never an interface it satisfies, and generate fails when a root it does not name reaches it — however compose.go draws the edge.",
			Placement: "on the provider func's doc comment, unspaced",
			Example:   "//forge:root-only worker\nfunc NewWorkerDaemonRouter(d WorkerDaemonRouterDeps) (*WorkerDaemonRouter, error)",
		},
		{
			Name:      "forge:standby",
			AppliesTo: "function",
			Effect:    "Keeps a provider ready for components to come: `forge lint --graph` does not require a root to reach it.",
			Placement: "on the provider func's doc comment, unspaced",
			Example:   "//forge:standby\nfunc NewMailer(d MailerDeps) (Mailer, error)",
		},
		{
			Name:      codegen.TxOpenerMarker,
			AppliesTo: "function",
			Effect:    "Declares a project's own transaction opener — a func or method that takes an orm.TxPolicy and runs the transaction itself (resolving it with orm.ResolveTxPolicy) — so forgeconv-tx-lock-under-serializable judges its calls as it judges pkg/orm's RunTx: a lock under SERIALIZABLE in its body is reported. level-arg is the 0-based index of the call argument naming the policy (ctx is 0; a receiver is not counted); body-arg, the func argument that is the transaction's body, defaults to the last func-typed parameter, and an opener with none is detached (the rest of the opening function is its body). Calls are matched by name and arity, project wide. A marker that does not parse, or is not on a func declaration, is a lint error.",
			Placement: "on the func's doc comment, spaced or unspaced",
			Example:   "//forge:tx-opener level-arg=1\nfunc (r *Repo) RunTx(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error",
		},
		{
			Name:      "forge:audience",
			AppliesTo: "function",
			Effect:    "Names the trust domain an authenticator provider authenticates; `forge lint --graph` compares it with the audience of every service a listener serves through a chain over it.",
			Placement: "on the provider func's doc comment, unspaced, with exactly one lower-case word",
			Example:   "//forge:audience daemon\nfunc NewDaemonAuthenticator(d AuthDeps) (Authenticator, error)",
		},
		{
			Name:      "forge:auth-unscoped-ok",
			AppliesTo: "function",
			Effect:    "Acknowledges that an authenticated rpc is intentionally global, so the unscoped_auth audit lists it as acknowledged rather than unscoped. The reason is required: the marker alone is the unfalsifiable comment the audit exists to replace.",
			Placement: "on the handler method's doc comment; the reason follows a colon",
			Example:   "// forge:auth-unscoped-ok: the plan catalog is the same for every caller\nfunc (s *Service) ListPlans(...)",
		},
		{
			Name:      "forge:allow",
			AppliesTo: "declaration",
			Effect:    "The user-owned (Tier-3) marker on a declaration in a scaffold-once file: forge's banner lint reads it as \"this is yours\". Scaffolds stamp it; it changes no codegen.",
			Placement: "on the declaration's doc comment, unspaced",
			Example:   "// yours: scaffolded once, never touched again\n//\n//forge:allow\ntype Infra struct { ... }",
		},
	}
}

// fieldTypeSpecs iterates the authoritative entity-birth mapping —
// scaffold.ProtoSQLMappings, whose scalar half is projected straight from the
// renderer's own scalarSQL — so the dump tracks what a birth actually emits.
func fieldTypeSpecs() []FieldTypeSpec {
	var out []FieldTypeSpec
	for _, m := range entityscaffold.ProtoSQLMappings() {
		out = append(out, FieldTypeSpec{
			ProtoType: m.Proto,
			SQLType:   m.SQL,
			Notes:     m.Notes,
		})
	}
	return out
}

// ─── validate rules: discovered, never transcribed ──────────────────────

// validateRuleWireEffect is the one sentence that is the same for every rule.
const validateRuleWireEffect = "Enforced at the wire by the protovalidate interceptor, which reads the same (buf.validate.field) option off the compiled descriptor."

// validateSampleColumn is the column name the sample DB projection uses.
const validateSampleColumn = "value"

// validateRuleSpecs DISCOVERS the protovalidate rules forge projects instead
// of listing them. Two derived halves, no hand-maintained rule names:
//
//   - The candidate universe is every rule protovalidate itself declares:
//     the top-level scalar fields of buf.validate.FieldRules (`required`)
//     plus every field of every per-type rules message reachable through
//     FieldRules' `type` oneof (StringRules.len, Int64Rules.gte, …). Truth
//     source: the compiled buf.validate descriptors.
//   - The verdict is forge's OWN recognizer: each candidate is spelled as
//     the author would write it, run through codegen.ParseRawValidateOptions,
//     and projected with codegen.FieldConstraints.SQLChecks / .ZodChain. A
//     candidate appears in the dump only when that projection is non-empty.
//
// So a rule forge starts (or stops) projecting changes this dump with no edit
// here — which is what lets the skills call the dump authoritative. `len` went
// missing from the old transcribed list for exactly this reason.
//
// Rules protovalidate supports but forge does not PROJECT (const, uuid,
// in/not_in, CEL, …) are absent by construction; they are still enforced at
// the wire, which writeAnnotationsText and the JSON wire_effect both say.
func validateRuleSpecs() []ValidateRuleSpec {
	var (
		out   []ValidateRuleSpec
		index = map[string]int{} // leaf rule name → position in out
	)
	for _, cand := range validateRuleCandidates() {
		kinds, dbEffect, zodEffect := probeValidateRule(cand)
		if len(kinds) == 0 {
			continue
		}
		if at, seen := index[cand.rule]; seen {
			out[at].AppliesTo = append(out[at].AppliesTo, kinds...)
			continue
		}
		index[cand.rule] = len(out)
		out = append(out, ValidateRuleSpec{
			Rule:       cand.rule,
			AppliesTo:  kinds,
			Example:    cand.option(),
			DBEffect:   dbEffect,
			ZodEffect:  zodEffect,
			WireEffect: validateRuleWireEffect,
		})
	}
	return out
}

// validateRuleCandidate is one rule spelling to probe: the dotted option path
// the author writes, a sample value of the rule's declared proto type, and the
// proto scalar kinds the rule could apply to (one for a per-type rule, all of
// them for a type-independent FieldRules rule like `required`).
type validateRuleCandidate struct {
	rule   string   // leaf rule name, e.g. "min_len"
	path   string   // dotted option path, e.g. "string.min_len" or "required"
	sample string   // value literal, already quoted when the rule takes a string
	kinds  []string // proto scalar kinds to project against
}

func (c validateRuleCandidate) option() string {
	return fmt.Sprintf("[(buf.validate.field).%s = %s]", c.path, c.sample)
}

// validateRuleCandidates walks the buf.validate descriptors and returns every
// rule spelling worth probing, in declaration order (so the dump's order is
// protovalidate's own, not an author's opinion).
func validateRuleCandidates() []validateRuleCandidate {
	fieldRules := (&validatepb.FieldRules{}).ProtoReflect().Descriptor()

	// Pass 1: the `type` oneof — one rules message per proto scalar kind.
	// The oneof field's NAME is the proto type token the author writes
	// (`string` in `(buf.validate.field).string.min_len`).
	var (
		typed     []validateRuleCandidate
		allKinds  []string
		typeOneof = fieldRules.Oneofs().ByName("type")
	)
	if typeOneof != nil {
		for i := 0; i < typeOneof.Fields().Len(); i++ {
			fd := typeOneof.Fields().Get(i)
			rules := fd.Message()
			if rules == nil {
				continue
			}
			token := string(fd.Name())
			allKinds = append(allKinds, token)
			for j := 0; j < rules.Fields().Len(); j++ {
				rf := rules.Fields().Get(j)
				sample, ok := validateSampleValue(rf)
				if !ok {
					continue
				}
				typed = append(typed, validateRuleCandidate{
					rule:   string(rf.Name()),
					path:   token + "." + string(rf.Name()),
					sample: sample,
					kinds:  []string{token},
				})
			}
		}
	}

	// Pass 2: the type-independent rules that sit directly on FieldRules
	// (`required`). They carry no type token and could fire on any kind.
	var plain []validateRuleCandidate
	for i := 0; i < fieldRules.Fields().Len(); i++ {
		fd := fieldRules.Fields().Get(i)
		if fd.ContainingOneof() != nil {
			continue // handled above
		}
		sample, ok := validateSampleValue(fd)
		if !ok {
			continue
		}
		plain = append(plain, validateRuleCandidate{
			rule:   string(fd.Name()),
			path:   string(fd.Name()),
			sample: sample,
			kinds:  allKinds,
		})
	}
	return append(plain, typed...)
}

// validateSampleValue renders a probe value for a rule field from its DECLARED
// proto type, so no rule name is special-cased. Rules whose value is a message
// or a list (duration bounds, in/not_in, CEL) are not probeable through the
// scalar option grammar and are skipped — forge projects none of them, and a
// skipped candidate can only ever under-report, never invent.
func validateSampleValue(fd protoreflect.FieldDescriptor) (string, bool) {
	if fd.IsList() || fd.IsMap() {
		return "", false
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return "true", true
	case protoreflect.StringKind, protoreflect.BytesKind:
		return `"abc"`, true
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind,
		protoreflect.Uint64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind, protoreflect.Sfixed32Kind,
		protoreflect.Sfixed64Kind, protoreflect.FloatKind, protoreflect.DoubleKind:
		return "1", true
	default:
		return "", false
	}
}

// probeValidateRule runs one candidate through forge's real recognizer and
// projections. It returns the proto scalar kinds the projection actually fires
// on plus the sample db/zod effects for the first such kind. Empty kinds means
// forge does not project this rule.
//
// SQLChecks is the kind-aware half (it switches on the proto scalar kind);
// ZodChain only knows "number input vs string input", so it would happily
// answer .min(1) for a bool or bytes field forge never puts a length check on.
// A kind therefore counts as projected when the MIGRATION projection fires,
// and the zod chain is reported alongside it.
func probeValidateRule(c validateRuleCandidate) (kinds []string, dbEffect, zodEffect string) {
	fc := codegen.ParseRawValidateOptions(c.option())
	if fc == nil {
		return nil, "", ""
	}
	for _, kind := range c.kinds {
		db := strings.Join(fc.SQLChecks(validateSampleColumn, kind), " ")
		if db == "" {
			continue
		}
		if len(kinds) == 0 {
			dbEffect, zodEffect = db, fc.ZodChain(zodFormFor(kind))
		}
		kinds = append(kinds, kind)
	}
	return kinds, dbEffect, zodEffect
}

// numericValidateKinds is the set of proto type tokens whose rules take
// numeric bounds, derived from the buf.validate descriptors: a token is
// numeric when its own rules message declares a numeric `const`. That is a
// fact about the proto type system, not a list of forge's rule names, so it
// cannot drift the way a transcribed rule table can.
var numericValidateKinds = func() map[string]bool {
	out := map[string]bool{}
	fieldRules := (&validatepb.FieldRules{}).ProtoReflect().Descriptor()
	typeOneof := fieldRules.Oneofs().ByName("type")
	if typeOneof == nil {
		return out
	}
	for i := 0; i < typeOneof.Fields().Len(); i++ {
		fd := typeOneof.Fields().Get(i)
		rules := fd.Message()
		if rules == nil {
			continue
		}
		constFD := rules.Fields().ByName("const")
		if constFD == nil || constFD.IsList() {
			continue
		}
		switch constFD.Kind() {
		case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind,
			protoreflect.Uint64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
			protoreflect.Fixed32Kind, protoreflect.Fixed64Kind, protoreflect.Sfixed32Kind,
			protoreflect.Sfixed64Kind, protoreflect.FloatKind, protoreflect.DoubleKind:
			out[string(fd.Name())] = true
		}
	}
	return out
}()

// zodFormFor maps a proto scalar kind to the zod base builder the frontend
// emitter uses, which is the argument ZodChain expects.
func zodFormFor(kind string) string {
	if numericValidateKinds[kind] {
		return "number"
	}
	return "string"
}

// serviceOptionSpecs / methodOptionSpecs enumerate the (forge.v1.service) /
// (forge.v1.method) option fields straight off the compiled forgepb
// descriptors — names and proto types cannot drift from the .proto. Reserved
// field numbers are absent from the descriptor, so they are excluded for free.
func serviceOptionSpecs() []OptionSpec {
	return optionSpecsFromDescriptor(
		(&forgev1.ServiceOptions{}).ProtoReflect().Descriptor().Fields(), serviceOptionMeta)
}

func methodOptionSpecs() []OptionSpec {
	return optionSpecsFromDescriptor(
		(&forgev1.MethodOptions{}).ProtoReflect().Descriptor().Fields(), methodOptionMeta)
}

// optionMeta is the descriptive half of an option — effect + default — that
// has no iterable source; a package test asserts every live descriptor field
// has an entry so a new option cannot ship undocumented.
type optionMeta struct{ effect, deflt string }

// Source: internal/assets/proto/forge/v1/forge.proto (MethodOptions) +
// internal/cli/forge_descriptor.go (applyMethodOptions defaults).
var methodOptionMeta = map[string]optionMeta{
	"auth_required":   {"Whether the method requires authentication; overrides the service AuthConfig default. Live metadata in forge map/graph.", "true (fail-closed: auth required when unset)"},
	"idempotent":      {"Marks the method safe to retry without side effects.", "false"},
	"timeout":         {"Server-side deadline for the rpc, projected into pkg/middleware/procedures_gen.go (ProcedureDeadlines) and enforced by the chain's Deadline slot, which runs ahead of auth so the credential lookup is bounded too. Negative is no server deadline.", "unset (serverkit.DefaultUnaryDeadline, 30s, for a unary rpc; none for a stream)"},
	"idempotency_key": {"Convention marker advising callers to supply an idempotency key; forge does not enforce or inspect it.", "false"},
	"errors":          {"Declared Connect/gRPC error codes the method may return (e.g. NotFound, InvalidArgument); informational, surfaced to handler authors.", "[] (no declared errors)"},
	"activity":        {"The typed activity event a successful call records: verb, target {kind, id, label} (field paths into the request, or the response with a response. prefix), env, scope, detail, writer; or none: \"<why>\" for a mutation that records nothing. forge/pkg/activity's interceptor writes it. CRUD-shaped Create/Update/Delete rpcs imply theirs.", "unset (CRUD implied, or nothing recorded)"},
}

// Source: internal/assets/proto/forge/v1/forge.proto (ServiceOptions) +
// internal/generator/service_gen.go (the scaffolded option block).
var serviceOptionMeta = map[string]optionMeta{
	"name":         {"Human-readable service name used in codegen and logging; scaffolded as <Handler>Service at birth.", ""},
	"version":      {"API version string (e.g. \"1.0.0\"); scaffolded at birth.", ""},
	"description":  {"Brief description of the service's purpose; scaffolded at birth.", ""},
	"visibility":   {"How the service is exposed: SERVICE_VISIBILITY_API (Connect/HTTP gateway) or SERVICE_VISIBILITY_INTERNAL (inter-service only).", "SERVICE_VISIBILITY_UNSPECIFIED (defaults to API)"},
	"dependencies": {"Other services this service depends on.", "[]"},
	"auth":         {"Service-wide authentication defaults (AuthConfig.auth_required / auth_provider); individual methods override via (forge.v1.method).auth_required.", ""},
	"audience":     {"The trust domain whose callers the service serves (\"user\", or a project-chosen name such as \"daemon\" or \"operator\"). A serverkit.Listener refuses to start when it mounts a service whose audience differs from its chain's Authenticator.Audience().", "\"user\""},
}

func optionSpecsFromDescriptor(fields protoreflect.FieldDescriptors, meta map[string]optionMeta) []OptionSpec {
	out := make([]OptionSpec, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := string(fd.Name())
		m := meta[name]
		out = append(out, OptionSpec{
			Name:    name,
			Type:    protoOptionType(fd),
			Effect:  m.effect,
			Default: m.deflt,
		})
	}
	return out
}

// protoOptionType renders a descriptor field's proto type readably: message
// fields resolve to the referenced message's full name (Duration/AuthConfig),
// list fields carry a "repeated " prefix, proto3-optional a "optional " one.
func protoOptionType(fd protoreflect.FieldDescriptor) string {
	base := fd.Kind().String()
	if m := fd.Message(); m != nil {
		base = string(m.FullName())
	}
	switch {
	case fd.IsList():
		return "repeated " + base
	case fd.HasOptionalKeyword():
		return "optional " + base
	default:
		return base
	}
}

// writeAnnotationsText renders the non-JSON view — a readable summary of the
// same spec, so the command is useful at a terminal as well as to tooling.
func writeAnnotationsText(w io.Writer, spec AnnotationsSpec) error {
	p := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}
	if len(spec.Markers) > 0 {
		if err := p("MARKERS\n"); err != nil {
			return err
		}
		for _, m := range spec.Markers {
			if err := p("  %-18s (%s) %s\n", m.Name, m.AppliesTo, m.Effect); err != nil {
				return err
			}
			// Placement is where the marker goes — for the entity markers this
			// is load-bearing: `// forge:entity` is only detected as a full-line
			// leading comment ABOVE `message X {`, and the raw scanner silently
			// ignores it anywhere else (inside the body, trailing a field), so a
			// misplaced marker births nothing with no error. Render it here so
			// the terminal view carries the same rule the JSON already does,
			// rather than leaving it discoverable only by reading the scanner.
			if m.Placement != "" {
				if err := p("  %-18s   ↳ placement: %s\n", "", m.Placement); err != nil {
					return err
				}
			}
		}
	}
	if len(spec.FieldTypes) > 0 {
		if err := p("\nFIELD TYPES — how a proto field is born as a column\n" +
			"  Applied once, at birth, by `forge scaffold` / `forge scaffold entity\n" +
			"  --from-proto`. The migration is yours from that moment: adjust any\n" +
			"  column below freely, and evolve with a new migration.\n"); err != nil {
			return err
		}
		for _, ft := range spec.FieldTypes {
			if err := p("  %-30s %-46s %s\n", ft.ProtoType, ft.SQLType, ft.Notes); err != nil {
				return err
			}
		}
	}
	if len(spec.ValidateRules) > 0 {
		if err := p("\nVALIDATE RULES — the (buf.validate.field) rules forge PROJECTS to db + zod\n" +
			"  Every other protovalidate rule (const, uuid, in/not_in, CEL, …) is still\n" +
			"  enforced on the wire by the protovalidate interceptor; it is simply not\n" +
			"  projected onto the migration or the generated form.\n" +
			"  `required` and `min_len >= 1` are what make a generated form field\n" +
			"  required; a NOT NULL column alone does not.\n"); err != nil {
			return err
		}
		for _, r := range spec.ValidateRules {
			if err := p("\n  %s   (%s)\n", r.Rule, strings.Join(r.AppliesTo, " ")); err != nil {
				return err
			}
			if err := p("    e.g. %s\n    db   %s\n    zod  %s\n", r.Example, r.DBEffect, r.ZodEffect); err != nil {
				return err
			}
		}
	}
	if err := writeOptionsText(w, "SERVICE OPTIONS (forge.v1.service)", spec.ServiceOptions); err != nil {
		return err
	}
	return writeOptionsText(w, "METHOD OPTIONS (forge.v1.method)", spec.MethodOptions)
}

func writeOptionsText(w io.Writer, heading string, opts []OptionSpec) error {
	if len(opts) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "\n%s\n", heading); err != nil {
		return err
	}
	for _, o := range opts {
		if _, err := fmt.Fprintf(w, "  %-16s %-28s %s\n", o.Name, o.Type, o.Effect); err != nil {
			return err
		}
	}
	return nil
}
