package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// dumpJSON runs the command body in JSON mode for the given kind and decodes
// the result, failing the test on any error (invalid JSON included).
func dumpJSON(t *testing.T, kind string) AnnotationsSpec {
	t.Helper()
	var buf bytes.Buffer
	if err := runAnnotations(&buf, kind, true); err != nil {
		t.Fatalf("runAnnotations(kind=%q): %v", kind, err)
	}
	var spec AnnotationsSpec
	if err := json.Unmarshal(buf.Bytes(), &spec); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	return spec
}

func TestAnnotations_JSONValidAndComplete(t *testing.T) {
	spec := dumpJSON(t, "")

	// All five markers.
	markerNames := map[string]bool{}
	for _, m := range spec.Markers {
		markerNames[m.Name] = true
	}
	for _, want := range []string{
		"forge:entity", "forge:soft-delete", "forge:append-only",
		"forge:read-only", "forge:secret", "forge:mutation", "forge:custom", "forge:unmanaged",
	} {
		if !markerNames[want] {
			t.Errorf("markers missing %q", want)
		}
	}
	// The full dump carries every marker in the catalog, no more and no
	// fewer. The authoritative per-marker pin is
	// TestAnnotations_MarkerNamesMatchRecognizers and its Go-source twin;
	// this only guards the dump against silent loss. Derived rather than
	// counted, so two branches that each add a marker do not collide here.
	catalog := markerSpecs()
	if len(spec.Markers) != len(catalog) {
		t.Errorf("the dump has %d markers, the catalog %d", len(spec.Markers), len(catalog))
	}
	for _, m := range catalog {
		if !markerNames[m.Name] {
			t.Errorf("the dump lost catalog marker %q", m.Name)
		}
	}

	// The proto→column mapping a birth applies: every proto3 scalar kind
	// the renderer maps, plus the shape-driven rows.
	fieldTypes := map[string]FieldTypeSpec{}
	for _, ft := range spec.FieldTypes {
		if ft.SQLType == "" || ft.ProtoType == "" {
			t.Errorf("field type entry is incomplete: %+v", ft)
		}
		fieldTypes[ft.ProtoType] = ft
	}
	for _, want := range []string{
		// scalars, projected from the renderer's own scalarSQL
		"string", "bool", "bytes", "double", "float",
		"int32", "int64", "uint32", "uint64",
		"sint32", "sint64", "fixed32", "fixed64", "sfixed32", "sfixed64",
		// the shape-driven rows
		"optional <scalar>", "repeated <scalar>", "string <x>_id",
		"enum E (same package)", "repeated enum E",
		"google.protobuf.Timestamp", "nested message (same package)",
		"map<K, scalar>",
	} {
		if _, ok := fieldTypes[want]; !ok {
			t.Errorf("field types missing %q", want)
		}
	}
	// Spot-check that the SQL is the real emitted column, not a label:
	// these are the exact strings the birth migration writes.
	for proto, wantSQL := range map[string]string{
		"string": "TEXT NOT NULL DEFAULT ''",
		"int64":  "BIGINT NOT NULL DEFAULT 0",
		"bool":   "BOOLEAN NOT NULL DEFAULT FALSE",
		"bytes":  `BYTEA NOT NULL DEFAULT '\x'`,
	} {
		if got := fieldTypes[proto].SQLType; got != wantSQL {
			t.Errorf("field type %q sql_type = %q, want the renderer's %q", proto, got, wantSQL)
		}
	}

	// Validate rules present with computed effects.
	if len(spec.ValidateRules) == 0 {
		t.Fatal("no validate rules emitted")
	}
	byRule := map[string]ValidateRuleSpec{}
	for _, r := range spec.ValidateRules {
		byRule[r.Rule] = r
	}
	if got := byRule["gte"].DBEffect; got != "CHECK (value >= 1)" {
		t.Errorf("gte db_effect = %q, want the SQLChecks projection", got)
	}
	if got := byRule["gte"].ZodEffect; got != ".gte(1)" {
		t.Errorf("gte zod_effect = %q, want the ZodChain projection", got)
	}
	if byRule["email"].DBEffect == "" || byRule["email"].ZodEffect == "" {
		t.Errorf("email rule has empty effects: %+v", byRule["email"])
	}
	for _, r := range spec.ValidateRules {
		if r.WireEffect == "" {
			t.Errorf("rule %q has empty wire_effect", r.Rule)
		}
		if r.Example == "" || len(r.AppliesTo) == 0 {
			t.Errorf("rule %q is missing its example/applies_to: %+v", r.Rule, r)
		}
	}

	// Method options include auth_required (and all five), each documented.
	methodNames := map[string]OptionSpec{}
	for _, o := range spec.MethodOptions {
		methodNames[o.Name] = o
	}
	if _, ok := methodNames["auth_required"]; !ok {
		t.Error("method_options missing auth_required")
	}
	for _, want := range []string{"auth_required", "idempotent", "timeout", "idempotency_key", "errors"} {
		o, ok := methodNames[want]
		if !ok {
			t.Errorf("method_options missing %q", want)
			continue
		}
		if o.Effect == "" || o.Type == "" {
			t.Errorf("method option %q incomplete: %+v", want, o)
		}
	}
	if got := methodNames["auth_required"].Type; got != "optional bool" {
		t.Errorf("auth_required type = %q, want %q", got, "optional bool")
	}
	if got := methodNames["errors"].Type; got != "repeated string" {
		t.Errorf("errors type = %q, want %q", got, "repeated string")
	}

	// Service options present and documented.
	if len(spec.ServiceOptions) == 0 {
		t.Error("service_options empty")
	}
	svcNames := map[string]bool{}
	for _, o := range spec.ServiceOptions {
		svcNames[o.Name] = true
	}
	for _, want := range []string{"name", "version", "description", "visibility"} {
		if !svcNames[want] {
			t.Errorf("service_options missing %q", want)
		}
	}
}

func TestAnnotations_KindFilters(t *testing.T) {
	// entity → only entity-level markers.
	entity := dumpJSON(t, "entity")
	if len(entity.FieldTypes) != 0 || len(entity.ValidateRules) != 0 ||
		len(entity.MethodOptions) != 0 || len(entity.ServiceOptions) != 0 {
		t.Errorf("--kind entity leaked non-marker sections: %+v", entity)
	}
	if len(entity.Markers) == 0 {
		t.Fatal("--kind entity returned no markers")
	}
	for _, m := range entity.Markers {
		if m.AppliesTo != "entity" {
			t.Errorf("--kind entity returned a %s marker: %s", m.AppliesTo, m.Name)
		}
	}

	// field → field markers + field_types + validate_rules, no options.
	field := dumpJSON(t, "field")
	if len(field.FieldTypes) == 0 || len(field.ValidateRules) == 0 {
		t.Errorf("--kind field missing field_types/validate_rules: %+v", field)
	}
	if len(field.MethodOptions) != 0 || len(field.ServiceOptions) != 0 {
		t.Errorf("--kind field leaked options")
	}
	for _, m := range field.Markers {
		if m.AppliesTo != "field" {
			t.Errorf("--kind field returned a %s marker: %s", m.AppliesTo, m.Name)
		}
	}

	// service → service_options plus the service-level comment marker.
	service := dumpJSON(t, "service")
	if len(service.ServiceOptions) == 0 {
		t.Error("--kind service returned no service_options")
	}
	if len(service.Markers) != 1 || service.Markers[0].Name != "forge:unmanaged" {
		t.Errorf("--kind service must carry the service-level marker: %+v", service.Markers)
	}
	if len(service.FieldTypes) != 0 ||
		len(service.ValidateRules) != 0 || len(service.MethodOptions) != 0 {
		t.Errorf("--kind service leaked other sections: %+v", service)
	}

	// method → method_options plus the rpc-level comment markers.
	method := dumpJSON(t, "method")
	if len(method.MethodOptions) == 0 {
		t.Error("--kind method returned no method_options")
	}
	methodMarkers := map[string]bool{}
	for _, m := range method.Markers {
		methodMarkers[m.Name] = true
	}
	if len(method.Markers) != 2 || !methodMarkers["forge:mutation"] || !methodMarkers["forge:custom"] {
		t.Errorf("--kind method must carry the rpc-level markers forge:mutation and forge:custom: %+v", method.Markers)
	}
	if len(method.FieldTypes) != 0 ||
		len(method.ValidateRules) != 0 || len(method.ServiceOptions) != 0 {
		t.Errorf("--kind method leaked other sections: %+v", method)
	}

	// column → only the column-comment markers (forge:immutable, forge:ref,
	// forge:version), no other section.
	column := dumpJSON(t, "column")
	if len(column.FieldTypes) != 0 || len(column.ValidateRules) != 0 ||
		len(column.MethodOptions) != 0 || len(column.ServiceOptions) != 0 {
		t.Errorf("--kind column leaked non-marker sections: %+v", column)
	}
	columnNames := map[string]bool{}
	for _, m := range column.Markers {
		columnNames[m.Name] = true
		if m.AppliesTo != "column" {
			t.Errorf("--kind column returned a %s marker: %s", m.AppliesTo, m.Name)
		}
	}
	for _, want := range []string{"forge:immutable", "forge:ref", "forge:version", "forge:fill", "forge:owner"} {
		if !columnNames[want] {
			t.Errorf("--kind column missing %q", want)
		}
	}
	// Counted from the registry rather than a literal: the whole point of
	// schemadef.KnownColumnMarkers is that the dump and the vocabulary
	// forge actually reads cannot drift, and a hardcoded number here
	// would make adding a marker fail this test for no reason.
	if len(column.Markers) != len(schemadef.KnownColumnMarkers) {
		t.Errorf("--kind column returned %d markers, want %d (schemadef.KnownColumnMarkers): %+v",
			len(column.Markers), len(schemadef.KnownColumnMarkers), column.Markers)
	}

	// table → the catalog-comment markers declared one level up, on the
	// TABLE rather than a column. Counted from the registry for the same
	// reason --kind column is.
	table := dumpJSON(t, "table")
	if len(table.FieldTypes) != 0 || len(table.ValidateRules) != 0 ||
		len(table.MethodOptions) != 0 || len(table.ServiceOptions) != 0 {
		t.Errorf("--kind table leaked non-marker sections: %+v", table)
	}
	for _, m := range table.Markers {
		if m.AppliesTo != "table" {
			t.Errorf("--kind table returned a %s marker: %s", m.AppliesTo, m.Name)
		}
	}
	if len(table.Markers) != len(schemadef.KnownTableMarkers) {
		t.Errorf("--kind table returned %d markers, want %d (schemadef.KnownTableMarkers)",
			len(table.Markers), len(schemadef.KnownTableMarkers))
	}

	// bogus → error.
	if err := runAnnotations(&bytes.Buffer{}, "bogus", true); err == nil {
		t.Error("--kind bogus should error")
	}
}

// TestAnnotations_MarkerNamesMatchRecognizers pins every marker name in the
// dump against the REAL recognizers (the raw proto scanner for the entity /
// field markers, the secret field regex for forge:secret), so renaming a
// marker in forge breaks this test rather than silently drifting the dump.
func TestAnnotations_MarkerNamesMatchRecognizers(t *testing.T) {
	dir := t.TempDir()
	proto := `syntax = "proto3";
package services.demo.v1;

// forge:entity
message WidgetEntity {
  string id = 1;
  string status = 2; // forge:read-only
  string api_token = 3; // forge:secret
  int64 doubled = 4; // forge:generated status_len * 2
}

// forge:soft-delete
message NoteEntity {
  string id = 1;
}

// forge:append-only
message LedgerEntity {
  string id = 1;
}
`
	if err := os.WriteFile(filepath.Join(dir, "demo.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := codegen.ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}

	if m, ok := scan.MessageByName("WidgetEntity"); !ok || !m.Marked {
		t.Error("forge:entity not recognized by the raw scanner")
	}
	if m, ok := scan.MessageByName("NoteEntity"); !ok || !m.SoftDeleteMarked {
		t.Error("forge:soft-delete not recognized by the raw scanner")
	}
	if m, ok := scan.MessageByName("LedgerEntity"); !ok || !m.AppendOnly {
		t.Error("forge:append-only not recognized by the raw scanner")
	}
	widget, _ := scan.MessageByName("WidgetEntity")
	readOnlySeen := false
	for _, f := range widget.Fields {
		if f.Name == "status" && f.ReadOnly {
			readOnlySeen = true
		}
	}
	if !readOnlySeen {
		t.Error("forge:read-only not recognized on the field")
	}
	generatedSeen := false
	for _, f := range widget.Fields {
		if f.Name == "doubled" && f.ReadOnly && f.Generated == "status_len * 2" {
			generatedSeen = true
		}
	}
	if !generatedSeen {
		t.Error("forge:generated not recognized on the field (want read-only with its expression)")
	}
	// buf strips the `//` before protogen sees the comment, so the secret
	// regex matches the comment text WITHOUT slashes — mirror that form here.
	if !secretFieldMarkerRE.MatchString(" forge:secret\n") {
		t.Error("forge:secret regex no longer matches the canonical marker")
	}

	// The dump lists exactly the recognized names — the six proto markers,
	// the column-comment markers, plus the Go-source markers, each
	// pinned against its real recognizer below so the catalog cannot
	// advertise a marker nothing reads.
	got := map[string]bool{}
	for _, m := range markerSpecs() {
		got[m.Name] = true
	}
	for _, want := range []string{
		"forge:entity", "forge:soft-delete", "forge:append-only",
		"forge:read-only", "forge:generated", "forge:secret", "forge:guards", "forge:mutation",
		"forge:custom", "forge:unmanaged",
		"forge:optional-dep", "forge:optional-checked", "forge:constructor",
		"forge:no-observe", "forge:service", "forge:exclude-contract",
		"forge:external-component", "forge:not-a-component", "forge:outbound-io",
		"forge:resolves-caller",
	} {
		if !got[want] {
			t.Errorf("markerSpecs missing recognized marker %q", want)
		}
	}
	// "Exactly" without a count: no marker is catalogued twice for the
	// same kind (one name may serve two — forge:append-only is both a proto
	// entity marker and a table comment), and every marker is of a kind
	// some check pins against its recognizer — proto markers against
	// codegen.KnownProtoMarkers, column and table markers against
	// schemadef's registries (all below), Go markers against
	// TestAnnotations_GoMarkerNamesMatchRecognizers. A marker of any other
	// kind would be advertised with nothing checking that it is read.
	entries := map[string]bool{}
	for _, m := range markerSpecs() {
		key := m.AppliesTo + " " + m.Name
		if entries[key] {
			t.Errorf("marker %q is catalogued twice for %q", m.Name, m.AppliesTo)
		}
		entries[key] = true
	}
	pinnedKinds := map[string]bool{"entity": true, "field": true, "method": true, "service": true, "column": true, "table": true}
	for _, k := range goMarkerKinds {
		pinnedKinds[k] = true
	}
	for _, m := range markerSpecs() {
		if !pinnedKinds[m.AppliesTo] {
			t.Errorf("marker %q applies to %q, a kind no recognizer check covers", m.Name, m.AppliesTo)
		}
	}

	// Proto markers are pinned against codegen.KnownProtoMarkers — the same
	// registry the scanners spell their markers from and the
	// unknown-proto-marker lint check enforces — so the dump, the recognizers
	// and the lint rule cannot silently diverge. The column-marker pin below
	// is the identical guarantee one layer down.
	protoMarkers := map[string]bool{}
	for _, m := range markerSpecs() {
		if m.AppliesTo == "entity" || m.AppliesTo == "field" || m.AppliesTo == "method" || m.AppliesTo == "service" {
			protoMarkers[m.Name] = true
		}
	}
	if len(protoMarkers) != len(codegen.KnownProtoMarkers) {
		t.Errorf("markerSpecs has %d proto markers, codegen.KnownProtoMarkers has %d — these must match exactly",
			len(protoMarkers), len(codegen.KnownProtoMarkers))
	}
	for _, name := range codegen.KnownProtoMarkers {
		if !protoMarkers[name] {
			t.Errorf("markerSpecs missing proto marker %q from codegen.KnownProtoMarkers", name)
		}
	}

	// Column markers are pinned against schemadef.KnownColumnMarkers — the
	// same registry the unknown-column-marker lint check reads — so the
	// dump and the lint rule cannot silently diverge on marker names.
	columnMarkers := filterMarkers(markerSpecs(), "column")
	if len(columnMarkers) != len(schemadef.KnownColumnMarkers) {
		t.Errorf("markerSpecs has %d column markers, schemadef.KnownColumnMarkers has %d — these must match exactly",
			len(columnMarkers), len(schemadef.KnownColumnMarkers))
	}
	for _, name := range schemadef.KnownColumnMarkers {
		if !got[name] {
			t.Errorf("markerSpecs missing column marker %q from schemadef.KnownColumnMarkers", name)
		}
	}

	// Table markers get the same treatment against their own registry —
	// KnownTableMarkers is what schemadef.Table.AppendOnly reads, so a
	// marker documented here and absent there is a marker that does
	// nothing where it is written.
	tableMarkers := filterMarkers(markerSpecs(), "table")
	if len(tableMarkers) != len(schemadef.KnownTableMarkers) {
		t.Errorf("markerSpecs has %d table markers, schemadef.KnownTableMarkers has %d — these must match exactly",
			len(tableMarkers), len(schemadef.KnownTableMarkers))
	}
	for _, name := range schemadef.KnownTableMarkers {
		if !got[name] {
			t.Errorf("markerSpecs missing table marker %q from schemadef.KnownTableMarkers", name)
		}
	}

	// forge:mutation is recognized by the hook generator, not the entity
	// scanner — check it against ITS recognizer, or the catalog could
	// advertise a marker nothing reads.
	if !mutationMarkerRE.MatchString("// forge:mutation") {
		t.Error("forge:mutation regex no longer matches the canonical marker")
	}
	if marked := mutationMarkedMethods(filepath.Join(dir, "hooks.proto")); marked["FindAndHoldSeat"] {
		t.Error("mutationMarkedMethods invented a marker for a file that does not exist")
	}
	hooksProto := `syntax = "proto3";
package services.demo.v1;
service DemoService {
  // forge:mutation
  rpc FindAndHoldSeat(Req) returns (Resp);
  rpc FindSeat(Req) returns (Resp);
}
`
	if err := os.WriteFile(filepath.Join(dir, "hooks.proto"), []byte(hooksProto), 0o644); err != nil {
		t.Fatal(err)
	}
	marked := mutationMarkedMethods(filepath.Join(dir, "hooks.proto"))
	if !marked["FindAndHoldSeat"] {
		t.Error("forge:mutation not recognized on the rpc it labels")
	}
	if marked["FindSeat"] {
		t.Error("forge:mutation leaked onto an unmarked rpc")
	}

	// forge:unmanaged is read by BOTH the raw scanner and the descriptor
	// path — pin each against the catalog name.
	unmanagedProto := "syntax = \"proto3\";\npackage services.demo.v1;\n// forge:unmanaged\nservice LegacyService {\n  rpc Ping(Req) returns (Resp);\n}\n"
	udir := t.TempDir()
	if err := os.WriteFile(filepath.Join(udir, "legacy.proto"), []byte(unmanagedProto), 0o644); err != nil {
		t.Fatal(err)
	}
	uscan, err := codegen.ScanRawProtoDir(udir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	if !uscan.ServiceUnmanaged("LegacyService") {
		t.Error("forge:unmanaged not recognized by the raw scanner")
	}
	if !codegen.CommentMarksUnmanaged(" forge:unmanaged\n") {
		t.Error("forge:unmanaged not recognized by the descriptor-path recognizer")
	}
}

// TestAnnotations_GoMarkerNamesMatchRecognizers is the Go-source half of the
// pin above. Every marker the catalog advertises for .go files is fed to the
// REAL recognizer codegen uses, so renaming one in forge breaks this test
// rather than leaving `forge project annotations --kind go` describing a
// marker nothing reads.
func TestAnnotations_GoMarkerNamesMatchRecognizers(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	// forge:constructor + forge:optional-dep, and the absence of the
	// package-level opt-out.
	dir := write(t, map[string]string{
		"contract.go": "package x\n\ntype Service interface{ Do() error }\n\n" +
			"type Deps struct {\n\t// forge:optional-dep\n\tStripe Service\n}\n",
		"service.go": "package x\n\n// New builds it.\n// forge:constructor\n" +
			"func New(d Deps) (Service, error) { return nil, nil }\n",
	})
	if !codegen.HasConstructorMarker(dir) {
		t.Error("forge:constructor not recognized on the constructor doc")
	}
	if codegen.HasPackageNoObserveDirective(dir) {
		t.Error("forge:no-observe reported for a package that does not carry it")
	}
	deps, err := codegen.ParseServiceDeps(dir)
	if err != nil {
		t.Fatalf("ParseServiceDeps: %v", err)
	}
	optionalSeen := false
	for _, f := range deps {
		if f.Name == "Stripe" && f.Optional {
			optionalSeen = true
		}
	}
	if !optionalSeen {
		t.Error("forge:optional-dep not recognized on the Deps field")
	}

	// The package-scoped directives, each in its own package so one
	// marker cannot mask another.
	for _, tc := range []struct {
		marker string
		body   string
		got    func(string) bool
	}{
		{"forge:no-observe", "// forge:no-observe\npackage x\n", codegen.HasPackageNoObserveDirective},
		{"forge:exclude-contract", "// forge:exclude-contract\npackage x\n", codegen.HasExcludeContractDirective},
		{"forge:external-component", "// forge:external-component\npackage x\n", codegen.HasExternalComponentDirective},
		{"forge:not-a-component", "// forge:not-a-component: CLI-only\npackage x\n", codegen.HasNotAComponentDirective},
		{"forge:outbound-io", "// forge:outbound-io\npackage x\n", codegen.HasOutboundIODirective},
	} {
		if !tc.got(write(t, map[string]string{"doc.go": tc.body})) {
			t.Errorf("%s not recognized by its own recognizer", tc.marker)
		}
	}

	// forge:service names a contract that isn't called Service.
	named := write(t, map[string]string{
		"contract.go": "package x\n\n// forge:service\ntype Mailer interface{ Send() error }\n",
	})
	if got := codegen.DetectServiceInterfaceName(named); got != "Mailer" {
		t.Errorf("forge:service should name Mailer as the contract, got %q", got)
	}

	// forge:resolves-caller declares a package-level function.
	resolverDir := write(t, map[string]string{
		"go.mod":  "module example.com/x\n",
		"auth.go": "package x\n\nimport \"context\"\n\n//forge:resolves-caller\nfunc UserID(ctx context.Context) string { return \"\" }\n",
	})
	if got, _ := codegen.DeclaredCallerResolvers(resolverDir); len(got) != 1 || got[0].Func != "UserID" {
		t.Errorf("forge:resolves-caller not recognized on a function doc, got %+v", got)
	}

	// The synonyms read through the same recognizers.
	if got := codegen.DetectServiceInterfaceName(write(t, map[string]string{
		"contract.go": "package x\n\n// forge:contract\ntype Store interface{ Get() error }\n",
	})); got != "Store" {
		t.Errorf("forge:contract should name Store as the contract, got %q", got)
	}
	if !codegen.HasExternalComponentDirective(write(t, map[string]string{"doc.go": "// forge:provided\npackage x\n"})) {
		t.Error("forge:provided not recognized as external-component")
	}
	if got := codegen.ExtractPlaceholderType("forge:placeholder: user.Repository"); got != "user.Repository" {
		t.Errorf("forge:placeholder not recognized, got %q", got)
	}

	// Every Go marker in the catalog is covered above. If someone adds one
	// without a recognizer check, this catches it.
	covered := map[string]bool{
		"forge:optional-dep": true, "forge:constructor": true,
		"forge:no-observe": true, "forge:service": true,
		"forge:exclude-contract": true, "forge:external-component": true,
		"forge:not-a-component": true, "forge:outbound-io": true,
		"forge:resolves-caller": true, "forge:contract": true,
		"forge:provided": true, "forge:placeholder": true,
		// Lint-suppression directive: read by the analyzer, not by codegen,
		// so it has no codegen recognizer to pin here.
		"forge:optional-checked": true,
		// Read by unexported scanners outside codegen's exported surface —
		// the composition graph's directive scan (root, group, standby,
		// audience, root-only; pinned in codegen's go_markers_test.go and
		// composition-graph tests), internal/config's no-orm scan
		// (derive_test.go), the unscoped_auth audit, and the banner lint.
		"forge:root": true, "forge:standby": true, "forge:audience": true, "forge:group": true, "forge:root-only": true,
		"forge:no-orm": true, "forge:auth-unscoped-ok": true, "forge:allow": true,
		// Read by forgeconv-tx-lock-under-serializable, which parses it
		// and reports a malformed one (forgeconv's
		// TestLintTxLockUnderSerializable_ProjectDeclaredOpeners).
		"forge:tx-opener": true,
	}
	for _, m := range filterMarkers(markerSpecs(), goMarkerKinds...) {
		if !covered[m.Name] {
			t.Errorf("Go marker %q is advertised but has no recognizer check in this test", m.Name)
		}
	}
}

// TestAnnotations_GoMarkersMatchTheLintRegistry pins the catalog to the
// vocabulary `forge lint --go-markers` enforces (codegen.KnownGoMarkers): a
// marker the catalog advertises must be one the lint accepts, spelled in its
// example the way the lint accepts it, and every marker an author writes by
// hand must be advertised — the lint's fix hint sends the reader to
// `forge project annotations --kind go`, which must then list it.
func TestAnnotations_GoMarkersMatchTheLintRegistry(t *testing.T) {
	advertised := map[string]bool{}
	for _, m := range filterMarkers(markerSpecs(), goMarkerKinds...) {
		advertised[m.Name] = true
		if _, ok := codegen.LookupGoMarker(m.Name); !ok {
			t.Errorf("annotations advertise %s, which the go-markers lint does not know", m.Name)
			continue
		}
		found := false
		for _, line := range strings.Split(m.Example, "\n") {
			line = strings.TrimSpace(line)
			if i := strings.Index(line, "//"); i >= 0 {
				use := codegen.ClassifyGoMarkerComment(line[i:])
				if use.Name == m.Name {
					found = true
					if use.Status != codegen.GoMarkerOK {
						t.Errorf("%s: the example %q is a spelling the lint rejects", m.Name, line)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s: the example does not show the marker", m.Name)
		}
	}
	// Written by forge itself, never by an author: not advertised.
	forgeWritten := map[string]bool{
		"forge:hash": true, "forge:gen": true, "forge:deps": true, "forge:custom-read-shape": true,
		"forge:lint-disable": true, "forge:lint-disable-file": true,
		"forge:lint-disable-next-line": true, "forge:lint-enable": true, // documented with the suppression mechanism
	}
	for _, name := range codegen.KnownGoMarkers() {
		if !advertised[name] && !forgeWritten[name] {
			t.Errorf("%s is a Go-source marker the lint accepts but `forge project annotations --kind go` does not list", name)
		}
	}
}

// TestAnnotations_ValidateRulesAreDiscoveredNotTranscribed pins the property
// the `db` skill's "it cannot drift" claim rests on: the dump's rule set is
// whatever forge's own recognizer accepts, not a list someone maintains.
//
// The regression it guards: `string.len = 3` really does project to
// `CHECK (char_length(col) = 3)`, but the hand-written table omitted it, so
// the "authoritative" dump told agents a working rule did not exist.
func TestAnnotations_ValidateRulesAreDiscoveredNotTranscribed(t *testing.T) {
	// The candidate universe has to reach protovalidate's whole rule surface;
	// a walk that silently found nothing would make the dump empty-but-green.
	cands := validateRuleCandidates()
	if len(cands) < 100 {
		t.Fatalf("only %d rule candidates discovered from the buf.validate descriptors — the walk has broken", len(cands))
	}
	byPath := map[string]validateRuleCandidate{}
	for _, c := range cands {
		byPath[c.path] = c
	}
	for _, want := range []string{"string.len", "string.min_len", "int64.gte", "string.const", "string.uuid", "required"} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("candidate universe is missing %q — probing cannot classify what it never sees", want)
		}
	}

	rules := map[string]ValidateRuleSpec{}
	for _, r := range validateRuleSpecs() {
		rules[r.Rule] = r
	}

	// Rules forge really projects must be listed — verified against the real
	// projection functions rather than against a copy of this expectation.
	for _, want := range []string{"gte", "gt", "lte", "lt", "min_len", "max_len", "len", "pattern", "email", "required"} {
		spec, ok := rules[want]
		if !ok {
			t.Errorf("validate rules omit %q, which codegen.ParseRawValidateOptions accepts", want)
			continue
		}
		fc := codegen.ParseRawValidateOptions(spec.Example)
		if fc == nil {
			t.Errorf("rule %q advertises example %q, which forge's recognizer rejects", want, spec.Example)
			continue
		}
		if got := strings.Join(fc.SQLChecks(validateSampleColumn, spec.AppliesTo[0]), " "); got != spec.DBEffect {
			t.Errorf("rule %q db_effect = %q, but re-projecting its own example gives %q", want, spec.DBEffect, got)
		}
	}
	if got := rules["len"].DBEffect; got != "CHECK (char_length(value) = 1)" {
		t.Errorf("string.len db_effect = %q, want the exact-length CHECK", got)
	}
	if got := rules["min_len"].AppliesTo; len(got) != 1 || got[0] != "string" {
		t.Errorf("min_len applies_to = %v, want just [string]", got)
	}
	if got := rules["gte"].AppliesTo; len(got) < 8 || !slices.Contains(got, "int64") {
		t.Errorf("gte applies_to = %v, want every numeric proto kind", got)
	}

	// Rules protovalidate declares but forge does NOT project must stay out:
	// listing one would be the same class of lie in the other direction.
	for _, unprojected := range []string{"const", "uuid", "prefix", "in", "not_in", "defined_only"} {
		if spec, ok := rules[unprojected]; ok {
			t.Errorf("validate rules advertise %q (db %q), which forge projects nowhere", unprojected, spec.DBEffect)
		}
	}
	for _, r := range validateRuleSpecs() {
		if r.DBEffect == "" {
			t.Errorf("rule %q is listed with no db projection", r.Rule)
		}
	}
}

// TestAnnotations_OptionsFullyDocumented asserts every live (forge.v1.method)
// and (forge.v1.service) descriptor field carries effect + type prose, so a
// new proto option cannot ship undocumented in the dump.
func TestAnnotations_OptionsFullyDocumented(t *testing.T) {
	check := func(kind string, specs []OptionSpec) {
		if len(specs) == 0 {
			t.Fatalf("%s: no options emitted", kind)
		}
		for _, o := range specs {
			if o.Effect == "" {
				t.Errorf("%s option %q has no effect prose", kind, o.Name)
			}
			if o.Type == "" {
				t.Errorf("%s option %q has no type", kind, o.Name)
			}
		}
	}
	check("method", methodOptionSpecs())
	check("service", serviceOptionSpecs())
}
