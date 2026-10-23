// File: internal/cli/scaffold/entity_from_proto.go
//
// `forge scaffold entity ... --from-proto <svc>[.<Message>]` — the
// wire→schema birth affordance. The message is ALREADY authored in the
// service proto; this command reads its shape from the compiled
// descriptor (gen/forge_descriptor.json) and emits the create-table
// migration, once. The migration is user-owned at birth and never
// re-read: after this moment, forge never writes or modifies a migration
// from proto state ("the line is the filesystem" —
// docs/design/VERTICAL_SCAFFOLDING.md §6). Rendering lives in
// internal/scaffold (entityproto.go); this file owns resolution
// (descriptor service, message selection, batch sweep, refusals) and
// reporting.

package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/internal/migrationver"
	"github.com/reliant-labs/forge/internal/naming"
	entityscaffold "github.com/reliant-labs/forge/internal/scaffold"
	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/schemadef"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// crudQuintetOps is the full CRUD verb set `forge scaffold entity`
// scaffolds; the batch sweep selects only entities whose service carries
// ALL five (a partial set is someone's hand-rolled surface — sweep past
// it; the explicit single form still works for those).
var crudQuintetOps = []string{"create", "get", "update", "delete", "list"}

// listEnvelopeFields are the pagination/mask field names that mark a
// message as a request/response ENVELOPE, not an entity — refused even
// when explicitly named.
var listEnvelopeFields = map[string]bool{
	"page_size":   true,
	"page_token":  true,
	"update_mask": true,
}

func runEntityFromProto(args []string, fromProto string, opts entityOpts) error {
	ctxLabel := "forge scaffold entity --from-proto"

	// Flag hygiene: the birth derives everything from the message, so a
	// `name:type` argument is someone reaching for the removed field-list
	// grammar. Refuse rather than treat it as a message name.
	for _, a := range args {
		if strings.Contains(a, ":") {
			return cliutil.UserErr(ctxLabel,
				"the columns come from the proto message — `field:type` arguments were removed", "",
				"drop the field list (the message IS the field list); declare any missing field on the message and re-run")
		}
	}
	root, err := projectRoot()
	if err != nil {
		return err
	}

	svcArg, msgArg, _ := strings.Cut(fromProto, ".")
	if svcArg == "" {
		return cliutil.UserErr(ctxLabel, "empty --from-proto value", "",
			"pass --from-proto <svc> or --from-proto <svc>.<Message>")
	}
	if msgArg != "" && len(args) > 1 {
		return cliutil.UserErr(ctxLabel,
			"the dot form names exactly one message (an optional entity name may accompany it)", "",
			"birth several messages with positional names: `--from-proto <svc> <Msg> [<Msg> ...]`")
	}

	// An unmanaged service is absent from the managed set below, which would
	// read as "missing descriptor" or "no such service" — name the real
	// reason first.
	if err := refuseUnmanagedService(ctxLabel, root, svcArg); err != nil {
		return err
	}
	services, err := codegen.ParseServicesFromProtos("", root)
	if err != nil || len(services) == 0 {
		return cliutil.UserErr(ctxLabel,
			"gen/forge_descriptor.json is missing or empty — the affordance reads the message shape recorded there", "",
			"run `forge generate` first (it compiles the descriptor), then re-run")
	}
	sd, ok := descriptorServiceByName(services, svcArg)
	if !ok {
		known := make([]string, 0, len(services))
		for _, s := range services {
			known = append(known, descriptorServiceArg(s))
		}
		sort.Strings(known)
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("no service %q in the descriptor (known: %s)", svcArg, strings.Join(known, ", ")), "",
			"name the service as it appears under proto/services/ (e.g. --from-proto tasks.Order)")
	}

	migDir := filepath.Join(root, "db", "migrations")
	applied, existing := appliedSchema(migDir)
	fkReg := newFKRegistry(existing)

	// ── batch form: --from-proto <svc>, no message, no name ─────────
	if msgArg == "" && len(args) == 0 {
		if opts.Table != "" {
			return cliutil.UserErr(ctxLabel,
				"--table doesn't apply to the batch form (it sweeps several entities)", "",
				"name one entity (`forge scaffold entity <name> --from-proto <svc>`) to override its table")
		}
		return runEntityFromProtoBatch(ctxLabel, root, migDir, sd, applied, fkReg, opts)
	}

	// ── explicit form: one <svc>.<Message> (+ optional entity name),
	//    or N positional message names ─────────────────────────────────
	type listedEntity struct{ msgName, name string }
	var entries []listedEntity
	if msgArg != "" {
		name := naming.ToSnakeCase(msgArg)
		if len(args) == 1 {
			name = args[0] // the dot form still accepts a decoupled entity name
		}
		entries = []listedEntity{{msgName: msgArg, name: name}}
	} else {
		for _, a := range args {
			entries = append(entries, listedEntity{
				msgName: naming.ToPascalCase(naming.ToSnakeCase(a)),
				name:    naming.ToSnakeCase(a),
			})
		}
	}
	if opts.Table != "" && len(entries) > 1 {
		return cliutil.UserErr(ctxLabel,
			"--table names one table, but several entities were listed", "",
			"birth the --table entity in its own invocation")
	}
	bc := birthContext{CtxLabel: ctxLabel, Root: root, MigDir: migDir, Service: sd, Applied: applied, FKReg: fkReg}
	for _, e := range entries {
		if err := birthListedEntityFromProto(bc, e.msgName, e.name, opts); err != nil {
			return err
		}
	}
	if !opts.DryRun {
		printFromProtoNextSteps()
	}
	return nil
}

// birthContext is the per-invocation state every birth in one
// `forge scaffold entity --from-proto` run shares: where the project is, which
// service descriptor is being read, and the applied-schema views the
// one-time-birth guard and FK back-fill consult. It travels as one value
// because each entity in a listing is born against the SAME context — only
// the message/entity name pair varies per birth.
type birthContext struct {
	CtxLabel string
	Root     string
	MigDir   string
	Service  codegen.ServiceDef
	Applied  map[string]bool
	FKReg    *fkRegistry
}

// birthListedEntityFromProto births ONE explicitly-named entity from the
// descriptor: migration + CRUD-quintet completion. Refusals are
// hard errors — an explicit listing gets loud refusal, not a silent skip.
func birthListedEntityFromProto(bc birthContext, msgName, name string, opts entityOpts) error {
	ctxLabel, root, migDir := bc.CtxLabel, bc.Root, bc.MigDir
	sd, applied, fkReg := bc.Service, bc.Applied, bc.FKReg
	if err := validateIdentifier(name); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid entity name", "",
			"use a name starting with a letter, containing letters/digits/_ (e.g. bookmark)", err)
	}

	fq := sd.Package + "." + msgName
	fields, ok := sd.Schemas[fq]
	if !ok {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("message %s is not in the descriptor", fq), "",
			"check the message name (PascalCase, as declared in the proto) and run `forge generate` if the proto changed since the last generate")
	}
	if reason := entityMessageRefusal(msgName, fields); reason != "" {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("message %s is not entity-shaped: %s", fq, reason), "",
			"point --from-proto at the entity message itself (e.g. tasks.Order), never at a request/response envelope")
	}

	table := opts.Table
	if table == "" {
		table = naming.Pluralize(naming.ToSnakeCase(name))
	}
	if applied[table] {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("table %q already exists in the applied schema (db/migrations)", table), migDir,
			"the birth affordance is one-time; evolve the entity by writing a new ALTER TABLE migration instead")
	}

	// Raw scan of the service's proto dir: the quintet-completion target,
	// the behavior-marker source (append-only / soft-delete live in the
	// leading comments the descriptor can't see), and the dry-run source.
	scan := rawScanForService(root, sd)
	markers := markersFromScan(scan, msgName)

	if opts.DryRun {
		missRPCs, missMsgs := predictQuintetCompletion(scan, msgName, markers.AppendOnly)
		fmt.Printf("📋 would birth %s → table %q (migration)\n", fq, table)
		printQuintetPlan(scan, msgName, missRPCs, missMsgs)
		return nil
	}

	if err := readOnlyMarkerRefusal(scanMessageOrEmpty(scan, msgName)); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "read-only marker", "",
			"move the marker onto the field it names", err)
	}
	if err := retiredMarkerRefusal(scanMessageOrEmpty(scan, msgName)); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "retired marker", "",
			"rewrite the retired marker as its replacement", err)
	}

	known := fkKnownTables(applied, sd, scan)
	emit := entityMigrationEmit{migDir, table, sd, msgName, fields, known, opts, markers, fkReg}
	if err := generatedPreflight(migDir, emit.spec(), scanMessageOrEmpty(scan, msgName)); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "forge:generated marker", "",
			"fix the marker at the line named above and re-run", err)
	}
	softDelete := opts.SoftDelete || markers.SoftDelete || messageHasField(fields, "deleted_at")
	added, merr := birthManagedFields(scan, msgName, !opts.NoTimestamps, softDelete)
	reportManagedFields(msgName, added, merr)
	if err := emitEntityFromProtoMigration(emit); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "write migration", migDir, "verify db/migrations is writable", err)
	}
	reportQuintetCompletion(completeQuintetForEntity(root, scan, msgName, sd.Package, fields, markers.AppendOnly))
	return nil
}

// readOnlyMarkerRefusal refuses a birth whose entity message carries a
// `// forge:read-only` marker (either spelling) the raw scanner could not
// attach to a field. The marker's whole job is to keep a field OFF the born
// Create/Update request; a dropped one ships that field to clients while the
// author believes it is protected — the worst possible failure mode, and the
// one this refusal makes impossible. Nothing has been written when it fires:
// fix the marker, re-run, get the entity.
func readOnlyMarkerRefusal(m codegen.RawProtoMessage) error {
	if len(m.UnappliedReadOnlyMarkers) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "message %s carries a field marker (%s) that attaches to no field:",
		m.Name, strings.Join(codegen.ReadOnlyProtoMarkers, " / "))
	for _, site := range m.UnappliedReadOnlyMarkers {
		fmt.Fprintf(&b, "\n    %s", site)
	}
	b.WriteString("\n  put the marker on the field's own line (trailing) or on the line directly above it, and re-run")
	return errors.New(b.String())
}

// retiredMarkerRefusal refuses a birth whose entity message carries a
// `forge:*` spelling forge deliberately RETIRED. Unlike the read-only
// refusal above — which catches a marker forge recognizes but could not
// place — this catches a marker forge no longer recognizes at all, and so
// silently does nothing: the comment reads as prose, the field stays
// client-writable on Create/Update, and the birth exits zero.
//
// This is a HARD refusal rather than the lint check's warning because a
// retired spelling is not ambiguous the way an unknown one is. An
// unrecognized marker might be a future forge's, or prose; a retired
// spelling has a KNOWN prior meaning, a KNOWN replacement, and an author
// who demonstrably believes it is in force. Warning about it would leave
// the exact silent failure the refusal exists to prevent, at exactly the
// moment (birth) when the wrong write surface gets baked into the born
// Create/Update requests. Nothing has been written when it fires: fix the
// marker, re-run, get the entity.
func retiredMarkerRefusal(m codegen.RawProtoMessage) error {
	if len(m.RetiredMarkers) == 0 {
		return nil
	}
	var b strings.Builder
	first := m.RetiredMarkers[0]
	fmt.Fprintf(&b, "message %s carries `%s`, which forge no longer recognizes — it was renamed to `%s`:",
		m.Name, first.Marker, first.ReplacedBy)
	for _, site := range m.RetiredMarkers {
		fmt.Fprintf(&b, "\n    %s", site)
	}
	fmt.Fprintf(&b, "\n  as written this comment does NOTHING: the field stays client-writable on the born Create/Update requests.")
	fmt.Fprintf(&b, "\n  rewrite each marker as `%s` and re-run", first.ReplacedBy)
	return errors.New(b.String())
}

// scanMessageOrEmpty returns the scanned message, or the zero value when
// the scan never saw it (a marker check then finds nothing to refuse).
func scanMessageOrEmpty(scan *codegen.RawProtoScan, msgName string) codegen.RawProtoMessage {
	if scan == nil {
		return codegen.RawProtoMessage{}
	}
	m, _ := scan.MessageByName(msgName)
	return m
}

// birthManagedFields injects the managed fields into the entity's own
// message, so the proto declares the shape everything downstream projects.
// A message the raw scan never saw (declared outside the service's proto
// dir) is skipped — same degradation as quintet completion.
func birthManagedFields(scan *codegen.RawProtoScan, msgName string, timestamps, softDelete bool) ([]string, error) {
	declFile := entityDeclFileFor(scan, msgName)
	if declFile == "" {
		return nil, nil
	}
	return injectManagedEntityFields(declFile, msgName, timestamps, softDelete)
}

// reportManagedFields prints one line per birth naming the fields forge
// wrote into the author's message — a silent proto edit is its own defect.
func reportManagedFields(msgName string, added []string, err error) {
	switch {
	case err != nil:
		fmt.Printf("  ⚠️  managed fields not declared on %s: %v (add id/created_at/updated_at by hand)\n",
			msgName, err)
	case len(added) > 0:
		fmt.Printf("  ✅ managed fields declared on %s: %s\n", msgName, strings.Join(added, ", "))
	}
}

// runEntityFromProtoBatch sweeps the service for birthable entities:
// messages in entity position of a full CRUD quintet whose table isn't
// applied yet (descriptor-driven, the historical selection) UNION
// messages carrying the `// forge:entity` marker with no applied table
// (raw-proto-driven — a marked message may be brand new and thus
// invisible to the descriptor). Quintet-selected entities already have
// their wire surface; marked ones get quintet completion + migration.
func runEntityFromProtoBatch(ctxLabel, root, migDir string, sd codegen.ServiceDef, applied map[string]bool, fkReg *fkRegistry, opts entityOpts) error {
	selected, skipped := selectFromProtoBatch(sd, applied)

	scan := rawScanForService(root, sd)
	marked, markedSkips := selectMarkedFromRawScan(scan, applied, selected)
	skipped = append(skipped, markerSkipNotes(markedSkips)...)

	for _, s := range skipped {
		fmt.Printf("  ⏭️  %s\n", s)
	}
	if len(selected)+len(marked) == 0 {
		fmt.Println("✅ Nothing to birth: no CRUD-quintet or `// forge:entity`-marked message is missing its applied table.")
		return nil
	}

	if opts.DryRun {
		for _, msgName := range selected {
			table := naming.Pluralize(naming.ToSnakeCase(msgName))
			fmt.Printf("📋 would birth %s → table %q (migration; quintet already present)\n", sd.Package+"."+msgName, table)
		}
		for _, m := range marked {
			table := naming.Pluralize(naming.ToSnakeCase(m.Name))
			fmt.Printf("📋 would birth %s → table %q (migration) [// forge:entity]\n", m.Package+"."+m.Name, table)
			missRPCs, missMsgs := predictQuintetCompletion(scan, m.Name, m.AppendOnly)
			printQuintetPlan(scan, m.Name, missRPCs, missMsgs)
		}
		fmt.Println("\n(dry run — nothing written)")
		return nil
	}

	// One registry for the whole batch: the applied schema plus every table
	// this run births (descriptor quintets + marked messages) — so an entity
	// that references a sibling being born in the same sweep resolves.
	known := fkKnownTables(applied, sd, scan)
	for _, msgName := range selected {
		table := naming.Pluralize(naming.ToSnakeCase(msgName))
		fmt.Printf("\n🔧 Birthing %s → %s ...\n", sd.Package+"."+msgName, table)
		fields := sd.Schemas[sd.Package+"."+msgName]
		if err := readOnlyMarkerRefusal(scanMessageOrEmpty(scan, msgName)); err != nil {
			return cliutil.WrapUserErr(ctxLabel, "read-only marker", "",
				"move the marker onto the field it names", err)
		}
		if err := retiredMarkerRefusal(scanMessageOrEmpty(scan, msgName)); err != nil {
			return cliutil.WrapUserErr(ctxLabel, "retired marker", "",
				"rewrite the retired marker as its replacement", err)
		}
		emit := entityMigrationEmit{migDir, table, sd, msgName, fields, known, opts, entityBirthMarkers{}, fkReg}
		if err := generatedPreflight(migDir, emit.spec(), scanMessageOrEmpty(scan, msgName)); err != nil {
			return cliutil.WrapUserErr(ctxLabel, "forge:generated marker", "",
				"fix the marker at the line named above and re-run", err)
		}
		added, merr := birthManagedFields(scan, msgName,
			!opts.NoTimestamps, opts.SoftDelete || messageHasField(fields, "deleted_at"))
		reportManagedFields(msgName, added, merr)
		if err := emitEntityFromProtoMigration(emit); err != nil {
			return cliutil.WrapUserErr(ctxLabel, "write migration", migDir, "verify db/migrations is writable", err)
		}
	}
	for _, m := range marked {
		fmt.Printf("\n🔧 Birthing %s → %s (// forge:entity) ...\n", m.Package+"."+m.Name, naming.Pluralize(naming.ToSnakeCase(m.Name)))
		rep, err := birthMarkedEntity(migDir, root, scan, m, known, opts, fkReg)
		if err != nil {
			return cliutil.WrapUserErr(ctxLabel, "birth marked entity "+m.Name, migDir,
				"fix the underlying error and re-run", err)
		}
		printMarkedBirthReport(rep)
	}
	printFromProtoNextSteps()
	return nil
}

// markerSkip is one `// forge:entity` marker that produced no birth, and
// WHY. The two reasons are not the same news and must never be summed into
// one "skipped: N": Inert means the entity is already born and the sweep had
// nothing to do (the steady state of every re-run), while a refusal means
// forge looked at a marker the author wrote on purpose and declined it —
// something the project asked for that is still missing.
type markerSkip struct {
	// Note is the human-readable line, already prefixed with the message
	// name.
	Note string
	// Inert is true for "the table already exists"; false for a refusal.
	Inert bool
}

// markerSkipNotes projects the notes out of a skip list, for callers that
// only render text.
func markerSkipNotes(skips []markerSkip) []string {
	out := make([]string, 0, len(skips))
	for _, s := range skips {
		out = append(out, s.Note)
	}
	return out
}

// selectMarkedFromRawScan picks the `// forge:entity`-marked messages
// eligible for birth: not already selected by the quintet sweep, not
// envelope-shaped (the marker does NOT override the guard — refused
// loudly), and with no applied table (marker inert then). Returns the
// eligible messages plus a classified skip note per marker that produced
// nothing.
func selectMarkedFromRawScan(scan *codegen.RawProtoScan, applied map[string]bool, alreadySelected []string) (marked []codegen.RawProtoMessage, skipped []markerSkip) {
	if scan == nil {
		return nil, nil
	}
	dup := make(map[string]bool, len(alreadySelected))
	for _, s := range alreadySelected {
		dup[s] = true
	}
	for _, m := range scan.Messages {
		if !m.Marked || dup[m.Name] {
			continue
		}
		if reason := entityMessageRefusal(m.Name, m.Fields); reason != "" {
			skipped = append(skipped, markerSkip{
				Note: fmt.Sprintf("%s: marked `// forge:entity`, but %s — the marker does not override the envelope guard", m.Name, reason),
			})
			continue
		}
		if table := naming.Pluralize(naming.ToSnakeCase(m.Name)); applied[table] {
			skipped = append(skipped, markerSkip{
				Note:  fmt.Sprintf("%s: table %q exists — marker inert; evolution is a new migration", m.Name, table),
				Inert: true,
			})
			continue
		}
		marked = append(marked, m)
	}
	return marked, skipped
}

// rawScanForService raw-scans the service's own proto directory
// (proto/services/<leaf>/). Best-effort: scan failures degrade to nil,
// and every consumer treats nil as "raw truth unavailable".
func rawScanForService(root string, sd codegen.ServiceDef) *codegen.RawProtoScan {
	dir := filepath.Join(root, "proto", "services", descriptorServiceArg(sd))
	scan, err := codegen.ScanRawProtoDir(dir)
	if err != nil {
		return nil
	}
	return scan
}

// predictQuintetCompletion computes the CRUD pieces the raw proto lacks
// for entity — the dry-run prediction, derived from the same piece
// builders the real injection uses.
func predictQuintetCompletion(scan *codegen.RawProtoScan, entity string, appendOnly bool) (missingRPCs, missingMsgs []string) {
	if scan == nil {
		return nil, nil
	}
	rpcNames := scan.RPCNames()
	for _, p := range appendOnlyFilter(buildEntityCRUDRPCPieces(entity), entity, appendOnly) {
		if !rpcNames[p.name] {
			missingRPCs = append(missingRPCs, p.name)
		}
	}
	msgNames := make(map[string]bool, len(scan.Messages))
	for _, m := range scan.Messages {
		msgNames[m.Name] = true
	}
	for _, p := range appendOnlyFilter(buildEntityCRUDMessagePieces(entity, nil), entity, appendOnly) {
		if !msgNames[p.name] {
			missingMsgs = append(missingMsgs, p.name)
		}
	}
	return missingRPCs, missingMsgs
}

// printQuintetPlan prints the dry-run line for one entity's quintet
// state.
func printQuintetPlan(scan *codegen.RawProtoScan, entity string, missingRPCs, missingMsgs []string) {
	switch {
	case scan == nil || scan.ServiceFile == "":
		fmt.Printf("     quintet: service proto not found — completion would be skipped with a note\n")
	case len(missingRPCs) == 0 && len(missingMsgs) == 0:
		fmt.Printf("     quintet: already complete — no proto injection\n")
	default:
		fmt.Printf("     quintet: would inject %d rpc(s) [%s] + %d message(s)\n",
			len(missingRPCs), strings.Join(missingRPCs, " "), len(missingMsgs))
	}
}

// completeQuintetForEntity runs quintet completion for one entity whose
// message fields are known (descriptor or raw scan). Returns a result
// for reporting plus the field-mapping notes; a nil result means the
// service proto could not be located (reported, never fatal — the
// migration half already landed, and re-running the birth once the
// service exists completes the quintet).
func completeQuintetForEntity(root string, scan *codegen.RawProtoScan, entity, protoPkg string, fields []codegen.SchemaFieldDef, appendOnly bool) (*quintetCompletionResult, []string, error) {
	if scan == nil || scan.ServiceFile == "" {
		return nil, nil, nil
	}
	efields, notes := entityFieldsFromSchemaDefs(protoPkg, withAuthoredValidateOptions(scan, entity, fields))
	entityDeclFile := ""
	if m, ok := scan.MessageByName(entity); ok {
		entityDeclFile = m.File
	}
	res, err := completeEntityCRUDProto(root, scan.ServiceFile, entityDeclFile, entity, efields, appendOnly)
	if err != nil {
		return nil, notes, err
	}
	return res, notes, nil
}

// withAuthoredValidateOptions fills in each field's authored
// `(buf.validate.field)` option TEXT from the raw scan — the proto file the
// injection is about to edit, and the only place the spelling survives.
//
// Both field sources reach quintet completion: the raw scan itself (a
// brand-new `// forge:entity` message, whose defs already carry the text)
// and the compiled descriptor (`--from-proto <svc>.<Message>`), which parses
// rules into FieldConstraints but keeps no source text. Sourcing the text
// from one place keeps a single spelling for both, so the Create request a
// project is born with never depends on which selector found the entity.
// A field the scan doesn't know (entity declared outside the service's proto
// dir) keeps whatever it arrived with.
func withAuthoredValidateOptions(scan *codegen.RawProtoScan, entity string, fields []codegen.SchemaFieldDef) []codegen.SchemaFieldDef {
	m, ok := scan.MessageByName(entity)
	if !ok {
		return fields
	}
	authored := make(map[string]string, len(m.Fields))
	for _, f := range m.Fields {
		if f.ValidateOptions != "" {
			authored[f.Name] = f.ValidateOptions
		}
	}
	if len(authored) == 0 {
		return fields
	}
	out := make([]codegen.SchemaFieldDef, len(fields))
	copy(out, fields)
	for i := range out {
		if opts, found := authored[out[i].Name]; found {
			out[i].ValidateOptions = opts
		}
	}
	return out
}

// reportQuintetCompletion prints the outcome of one quintet completion.
func reportQuintetCompletion(res *quintetCompletionResult, notes []string, err error) {
	switch {
	case err != nil:
		fmt.Printf("  ⚠️  quintet completion failed: %v (the migration landed; add the CRUD surface by hand)\n", err)
	case res == nil:
		fmt.Printf("  ℹ️  service proto not found — quintet completion skipped (scaffold the service, then re-run `forge scaffold`: the migration already landed, so only the CRUD surface is missing)\n")
	case res.Complete():
		fmt.Printf("  ✅ CRUD quintet already complete in %s\n", filepath.Base(res.ProtoPath))
	default:
		fmt.Printf("  ✅ Completed CRUD quintet in %s (+%d rpc, +%d message)\n",
			filepath.Base(res.ProtoPath), len(res.AddedRPCs), len(res.AddedMessages))
	}
	for _, n := range notes {
		fmt.Printf("  ℹ️  %s\n", n)
	}
}

// markedBirthReport is the per-item outcome of one marked-message birth,
// consumed by the batch printer and `forge scaffold`'s summary.
type markedBirthReport struct {
	Message string // fully-qualified message name
	Table   string
	UpPath  string
	Quintet *quintetCompletionResult
	// QuintetErr records a completion failure (the migration half still
	// landed; never fatal to the batch).
	QuintetErr error
	// Notes carries migration mapping notes + envelope field notes.
	Notes []string
	// TodoFields counts fields carried as TODO comments in the migration.
	TodoFields int
	// ManagedFields names the id/created_at/updated_at/deleted_at fields
	// birth declared on the author's entity message (empty when it already
	// declared them all).
	ManagedFields []string
	// ManagedFieldsErr records a failed managed-field injection; the birth
	// still proceeds (the migration and quintet are the load-bearing half).
	ManagedFieldsErr error
}

// birthMarkedEntity births one `// forge:entity`-marked message: the
// owned forward migration rendered from the RAW proto fields (the marker
// and the message live in the same file — one truth, one read; a brand
// new message need not be in the descriptor), then CRUD-quintet
// completion into the service proto.
func birthMarkedEntity(migDir, root string, scan *codegen.RawProtoScan, m codegen.RawProtoMessage, known map[string]bool, opts entityOpts, fkReg *fkRegistry) (*markedBirthReport, error) {
	if err := readOnlyMarkerRefusal(m); err != nil {
		return nil, err
	}
	if err := retiredMarkerRefusal(m); err != nil {
		return nil, err
	}

	table := naming.Pluralize(naming.ToSnakeCase(m.Name))
	rep := &markedBirthReport{Message: m.Package + "." + m.Name, Table: table}

	timestamps := !opts.NoTimestamps
	softDelete := opts.SoftDelete || m.SoftDeleteMarked || messageHasField(m.Fields, "deleted_at")

	spec := entityscaffold.EntityFromProtoSpec{
		Table:          table,
		MessageFQ:      m.Package + "." + m.Name,
		ProtoPkg:       m.Package,
		Fields:         m.Fields,
		Enums:          scan.Enums,
		SoftDelete:     softDelete,
		Timestamps:     timestamps,
		AppendOnly:     m.AppendOnly,
		KnownTables:    known,
		ExistingTables: fkReg.snapshot(),
	}
	// Refused before the managed-field injection below, so a bad expression
	// leaves the author's proto exactly as they wrote it.
	if err := generatedPreflight(migDir, spec, m); err != nil {
		return nil, err
	}

	// The author's message declares its own managed fields from here on —
	// injected before the migration so a failure costs no migration.
	rep.ManagedFields, rep.ManagedFieldsErr = injectManagedEntityFields(m.File, m.Name, timestamps, softDelete)

	mig := entityscaffold.RenderEntityMigrationFromProto(spec)
	upPath, err := writeBirthMigration(migDir, table, mig.UpSQL)
	if err != nil {
		return nil, err
	}
	fkReg.born(table, mig)
	rep.UpPath = upPath
	rep.Notes = append(rep.Notes, mig.Notes...)
	for _, n := range mig.Notes {
		if strings.Contains(n, "TODO") {
			rep.TodoFields++
		}
	}

	res, notes, qerr := completeQuintetForEntity(root, scan, m.Name, m.Package, m.Fields, m.AppendOnly)
	rep.Quintet = res
	rep.QuintetErr = qerr
	rep.Notes = append(rep.Notes, notes...)
	return rep, nil
}

// printMarkedBirthReport prints one marked birth's outcome (batch form).
func printMarkedBirthReport(rep *markedBirthReport) {
	fmt.Printf("✅ Created %s\n", rep.UpPath)
	reportManagedFields(rep.Message, rep.ManagedFields, rep.ManagedFieldsErr)
	reportQuintetCompletion(rep.Quintet, nil, rep.QuintetErr)
	for _, n := range rep.Notes {
		fmt.Printf("  ℹ️  %s\n", n)
	}
}

// emitEntityFromProtoMigration renders and writes one birth migration,
// printing the per-field notes. markers carries the `// forge:append-only` /
// `// forge:soft-delete` behavior resolved from the raw scan (zero value for
// the descriptor-selected batch path, which never sees a marked message).
// entityMigrationEmit carries the inputs for one migration-pair emission.
// A struct rather than nine positionals: the call sites differ only in
// `markers`, and a positional list that long hides which argument moved.
// (The old signature also carried a `root` that the body never read.)
type entityMigrationEmit struct {
	migDir  string
	table   string
	sd      codegen.ServiceDef
	msgName string
	fields  []codegen.SchemaFieldDef
	known   map[string]bool
	opts    entityOpts
	markers entityBirthMarkers
	// fkReg is the running view of what already exists, so this migration
	// emits only constraints the database can accept. nil for callers with
	// no sweep context (nothing exists ⇒ no forward constraints).
	fkReg *fkRegistry
}

// spec is the renderer input this emission writes — computed separately so
// the birth's pre-flight (generatedPreflight) checks the very migration the
// emission will write, before anything is written.
func (e entityMigrationEmit) spec() entityscaffold.EntityFromProtoSpec {
	return entityscaffold.EntityFromProtoSpec{
		Table:          e.table,
		MessageFQ:      e.sd.Package + "." + e.msgName,
		ProtoPkg:       e.sd.Package,
		Fields:         e.fields,
		Enums:          e.sd.Enums,
		SoftDelete:     e.opts.SoftDelete || e.markers.SoftDelete || messageHasField(e.fields, "deleted_at"),
		Timestamps:     !e.opts.NoTimestamps,
		AppendOnly:     e.markers.AppendOnly,
		KnownTables:    e.known,
		ExistingTables: e.fkReg.snapshot(),
	}
}

func emitEntityFromProtoMigration(e entityMigrationEmit) error {
	migDir, table := e.migDir, e.table
	mig := entityscaffold.RenderEntityMigrationFromProto(e.spec())
	upPath, err := writeBirthMigration(migDir, table, mig.UpSQL)
	if err != nil {
		return err
	}
	e.fkReg.born(table, mig)
	fmt.Printf("✅ Created %s\n", upPath)
	for _, n := range mig.Notes {
		fmt.Printf("  ℹ️  %s\n", n)
	}
	return nil
}

// printFromProtoNextSteps states the evolution contract plainly: birth
// is the only moment forge derives one truth from the other.
func printFromProtoNextSteps() {
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Review the migration — it is yours; this was a one-time birth")
	fmt.Println("     convenience, and forge never re-reads the proto to change it.")
	fmt.Println("  2. Run `forge generate` — the entity struct, ORM, CRUD wiring and")
	fmt.Println("     frontend pages are projected from the APPLIED schema.")
	fmt.Println("  3. Evolution is a new migration plus a proto edit — forge never")
	fmt.Println("     re-derives either truth from the other.")
}

// selectFromProtoBatch picks the batch-sweep set: every message sitting
// in entity position of a FULL CRUD quintet (Create/Get/Update/Delete/
// List) whose message exists in the descriptor, passes the entity-shape
// guard, and has no applied table. Returns the selected message names
// (sorted) and human-readable skip notes for everything CRUD-shaped that
// didn't qualify.
func selectFromProtoBatch(sd codegen.ServiceDef, applied map[string]bool) (selected []string, skipped []string) {
	ops := map[string]map[string]bool{} // entity message name → op set
	for _, m := range sd.Methods {
		// A `// forge:custom` rpc claims no entity, so a quintet of them
		// births no table.
		op, entity := codegen.CRUDShape(m)
		if op == "" || entity == "" {
			continue
		}
		if op == "list" {
			entity = inflection.Singular(entity)
		}
		if ops[entity] == nil {
			ops[entity] = map[string]bool{}
		}
		ops[entity][op] = true
	}

	names := make([]string, 0, len(ops))
	for entity := range ops {
		names = append(names, entity)
	}
	sort.Strings(names)

	for _, entity := range names {
		missing := []string{}
		for _, op := range crudQuintetOps {
			if !ops[entity][op] {
				missing = append(missing, op)
			}
		}
		if len(missing) > 0 {
			skipped = append(skipped, fmt.Sprintf("%s: not a full CRUD quintet (missing %s) — birth it explicitly with `--from-proto <svc>.%s` if intended", entity, strings.Join(missing, "/"), entity))
			continue
		}
		fields, ok := sd.Schemas[sd.Package+"."+entity]
		if !ok {
			skipped = append(skipped, fmt.Sprintf("%s: CRUD quintet present but message %s.%s is not in the descriptor — run `forge generate`", entity, sd.Package, entity))
			continue
		}
		if reason := entityMessageRefusal(entity, fields); reason != "" {
			skipped = append(skipped, fmt.Sprintf("%s: %s", entity, reason))
			continue
		}
		if table := naming.Pluralize(naming.ToSnakeCase(entity)); applied[table] {
			skipped = append(skipped, fmt.Sprintf("%s: table %q already applied — nothing to birth", entity, table))
			continue
		}
		selected = append(selected, entity)
	}
	return selected, skipped
}

// entityMessageRefusal returns a non-empty reason when a message must
// not be treated as an entity, even when explicitly listed: request/
// response envelope names, and messages carrying pagination/mask fields.
func entityMessageRefusal(msgName string, fields []codegen.SchemaFieldDef) string {
	if strings.HasSuffix(msgName, "Request") || strings.HasSuffix(msgName, "Response") {
		return fmt.Sprintf("%q names a request/response envelope, not an entity", msgName)
	}
	for _, f := range fields {
		if listEnvelopeFields[f.Name] {
			return fmt.Sprintf("it carries the envelope field %q (pagination/mask fields mark a request/response shape)", f.Name)
		}
	}
	return ""
}

func messageHasField(fields []codegen.SchemaFieldDef, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// entityBirthMarkers carries the per-entity behavior markers resolved from
// the raw proto scan (the descriptor cannot see the leading-comment markers).
type entityBirthMarkers struct {
	AppendOnly bool
	SoftDelete bool
}

// markersFromScan resolves the `// forge:append-only` / `// forge:soft-delete`
// markers for one message off the raw scan. Zero value when the scan is nil
// or the message isn't found (the markers are inert then).
func markersFromScan(scan *codegen.RawProtoScan, msgName string) entityBirthMarkers {
	if scan == nil {
		return entityBirthMarkers{}
	}
	if m, ok := scan.MessageByName(msgName); ok {
		return entityBirthMarkers{AppendOnly: m.AppendOnly, SoftDelete: m.SoftDeleteMarked}
	}
	return entityBirthMarkers{}
}

// appliedSchema returns both views of the applied schema a birth needs:
// the table-name set (the one-time-birth guard, the FK referent registry)
// and the per-table reference columns still missing a FOREIGN KEY — the
// columns a newly born table may be able to constrain from its own
// migration, once it creates the table they point at.
//
// The reference-column view needs real introspection; the regex fallback
// recovers table names only. When it is all we have the back-fill is
// simply empty, which loses a constraint but never emits a broken one.
func appliedSchema(migDir string) (map[string]bool, []entityscaffold.ExistingTable) {
	out := map[string]bool{}
	projectDir := filepath.Dir(filepath.Dir(migDir))
	tables, err := shadowdb.Tables(projectDir, migDir)
	if err != nil {
		if parsed, perr := database.ParseMigrationsForSchema(migDir); perr == nil {
			for _, t := range parsed {
				out[t.Name] = true
			}
		}
		return out, nil
	}
	existing := make([]entityscaffold.ExistingTable, 0, len(tables))
	for _, t := range tables {
		out[t.Name] = true
		existing = append(existing, entityscaffold.ExistingTable{
			Name:                    t.Name,
			UnconstrainedRefColumns: unconstrainedRefColumns(t),
		})
	}
	return out, existing
}

// unconstrainedRefColumns lists a table's TEXT `<x>_id` columns that carry
// no declared FOREIGN KEY. `id` itself is the primary key, never a
// reference.
func unconstrainedRefColumns(t schemadef.Table) []string {
	constrained := make(map[string]bool, len(t.ForeignKeys))
	for _, fk := range t.ForeignKeys {
		constrained[fk.Column] = true
	}
	var out []string
	for _, c := range t.Columns {
		if c.IsArray || c.Type != schemadef.TypeString || c.Name == "id" {
			continue
		}
		if !strings.HasSuffix(c.Name, "_id") || constrained[c.Name] {
			continue
		}
		out = append(out, c.Name)
	}
	return out
}

// fkRegistry is the running "what exists now" view a birth sweep threads
// through its migrations. Every birth reads it to decide which constraints
// this migration can apply, then records its own table — including the
// reference columns it could NOT constrain, so a later sibling's birth
// back-fills them.
type fkRegistry struct {
	tables []entityscaffold.ExistingTable
}

func newFKRegistry(existing []entityscaffold.ExistingTable) *fkRegistry {
	return &fkRegistry{tables: append([]entityscaffold.ExistingTable(nil), existing...)}
}

// snapshot is the view a migration being rendered right now should see.
func (r *fkRegistry) snapshot() []entityscaffold.ExistingTable {
	if r == nil {
		return nil
	}
	return r.tables
}

// born records a table this sweep just created, from the migration the
// renderer actually produced: `mig.PendingRefColumns` are the new table's
// still-unconstrained references, and `mig.BackfilledRefColumns` names the
// earlier-table columns this migration constrained — struck off so a third
// birth cannot propose the same constraint twice. Reading the renderer's
// own report keeps the registry from re-deriving the resolution rule and
// disagreeing with it.
func (r *fkRegistry) born(table string, mig entityscaffold.EntityFromProtoMigration) {
	if r == nil {
		return
	}
	for i := range r.tables {
		done := mig.BackfilledRefColumns[r.tables[i].Name]
		if len(done) == 0 {
			continue
		}
		r.tables[i].UnconstrainedRefColumns = without(r.tables[i].UnconstrainedRefColumns, done)
	}
	r.tables = append(r.tables, entityscaffold.ExistingTable{
		Name:                    table,
		UnconstrainedRefColumns: mig.PendingRefColumns,
	})
}

// without returns cols minus every entry in drop.
func without(cols, drop []string) []string {
	if len(cols) == 0 || len(drop) == 0 {
		return cols
	}
	dropped := make(map[string]bool, len(drop))
	for _, d := range drop {
		dropped[d] = true
	}
	out := cols[:0:0]
	for _, c := range cols {
		if !dropped[c] {
			out = append(out, c)
		}
	}
	return out
}

// fkKnownTables builds the table-name set a birth's `<x>_id` FOREIGN KEYs
// resolve against (entityscaffold.EntityFromProtoSpec.KnownTables): the
// applied schema UNION every table forge recognizes as entity-backed —
// descriptor CRUD-quintet entities (sd) and `// forge:entity`-marked
// messages across the given scans. A `<x>_id` column whose stem names
// none of these is polymorphic/auth (user_id with no User entity) and
// gets NO foreign key. sd may be the zero ServiceDef for a marked-only
// birth (no descriptor in hand); any scan may be nil.
func fkKnownTables(applied map[string]bool, sd codegen.ServiceDef, scans ...*codegen.RawProtoScan) map[string]bool {
	known := make(map[string]bool, len(applied)+8)
	for t := range applied {
		known[t] = true
	}
	addEntity := func(name string) {
		if name != "" {
			known[naming.Pluralize(naming.ToSnakeCase(name))] = true
		}
	}
	for _, m := range sd.Methods {
		op, entity := codegen.CRUDShape(m) // unary, not forge:custom
		if op == "" || entity == "" {
			continue
		}
		if op == "list" { // ListOrders → Orders → singular for the table stem
			entity = inflection.Singular(entity)
		}
		addEntity(entity)
	}
	for _, s := range scans {
		if s == nil {
			continue
		}
		for _, m := range s.Messages {
			if m.Marked {
				addEntity(m.Name)
			}
		}
	}
	return known
}

// writeBirthMigration writes the next-numbered NNNNN_create_<table>.up.sql
// with pre-rendered contents — internal/scaffold's renderer owns the SQL,
// and it is the only writer of a birth migration forge has. Forward only:
// forge writes no down migrations (a birth is undone, if ever, by a new
// forward migration).
func writeBirthMigration(migDir, table, upSQL string) (string, error) {
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		return "", err
	}
	version, err := migrationver.Next(migDir)
	if err != nil {
		return "", err
	}
	upPath := filepath.Join(migDir, fmt.Sprintf("%s_create_%s.up.sql", version, table))
	if err := atomicfile.WriteFile(upPath, []byte(upSQL), 0o644); err != nil {
		return "", err
	}
	return upPath, nil
}

// descriptorServiceByName resolves a descriptor service from the leaf
// name users type on the CLI ("tasks"), accepting the descriptor's own
// service name ("TasksService") too.
func descriptorServiceByName(services []codegen.ServiceDef, svcArg string) (codegen.ServiceDef, bool) {
	want := naming.ServicePackage(svcArg)
	for _, sd := range services {
		if sd.Name == svcArg || descriptorServiceArg(sd) == want {
			return sd, true
		}
	}
	return codegen.ServiceDef{}, false
}

// descriptorServiceArg is the CLI spelling of a descriptor service — the
// same leaf `forge scaffold rpc <svc>` uses.
func descriptorServiceArg(sd codegen.ServiceDef) string {
	return naming.ServicePackage(strings.TrimSuffix(sd.Name, "Service"))
}
