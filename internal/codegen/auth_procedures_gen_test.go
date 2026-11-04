package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

// `auth_required` used to gate nothing at runtime: the interceptor took its
// allow-list from a hand-written map in the project's pkg/middleware, so a
// proto could declare an RPC authenticated, have `forge project graph`
// report it as authenticated, and still serve anonymous
// callers. One measured app shipped 17 of 20 CRUD RPCs that way.
//
// The declaration is now the only input to the runtime set. These assertions
// pin the projection: what is public is what an rpc DECLARED public, the
// health probes are always reachable, and silence never publishes anything.

// authProceduresFixture is two services whose RPCs mix the declaration, in
// two different connect packages, so import handling is exercised too.
func authProceduresFixture() []ServiceDef {
	return []ServiceDef{
		{
			Name:      "PatientsService",
			Package:   "demo.v1",
			GoPackage: "example.com/app/gen/services/patients/v1",
			PkgName:   "patientsv1",
			Methods: []Method{
				{Name: "CreatePatient", AuthRequired: true},
				{Name: "GetPatient", AuthRequired: true},
				{Name: "GetPatientStatus", AuthRequired: false},
			},
		},
		{
			Name:      "PublicService",
			Package:   "demo.v1",
			GoPackage: "example.com/app/gen/services/public/v1",
			PkgName:   "publicv1",
			Methods: []Method{
				{Name: "GetVersion", AuthRequired: false},
			},
		},
	}
}

// TestOpenProcedures_OnlyWhatTheProtoDeclaredPublic is the assertion the
// measured defect needed: the runtime set is the auth_required projection and
// nothing else.
func TestOpenProcedures_OnlyWhatTheProtoDeclaredPublic(t *testing.T) {
	data := BuildOpenProcedures(authProceduresFixture(), "example.com/app")

	got := map[string]bool{}
	for _, e := range data.Open {
		got[e.Const] = true
	}
	if len(got) == 0 {
		t.Fatal("no procedures projected — every assertion below would pass vacuously")
	}

	want := []string{
		"patientsv1connect.PatientsServiceGetPatientStatusProcedure",
		"publicv1connect.PublicServiceGetVersionProcedure",
	}
	for _, c := range want {
		if !got[c] {
			t.Errorf("%s declares auth_required: false but is not in the open set — the declaration gates nothing", c)
		}
	}
	for _, c := range []string{
		"patientsv1connect.PatientsServiceCreatePatientProcedure",
		"patientsv1connect.PatientsServiceGetPatientProcedure",
	} {
		if got[c] {
			t.Errorf("%s declares auth_required: true but is in the OPEN set — the projection is inverted", c)
		}
	}
	if len(got) != len(want) {
		t.Errorf("open set has %d entries, want %d: %v", len(got), len(want), got)
	}
}

// TestOpenProcedures_ImportsAreOnePerConnectPackage keeps the generated file
// compilable: one import per module, aliased as connect names it.
func TestOpenProcedures_ImportsAreOnePerConnectPackage(t *testing.T) {
	svcs := authProceduresFixture()
	// A second service in the SAME connect package must not import it twice.
	svcs = append(svcs, ServiceDef{
		Name:      "SiblingService",
		Package:   "demo.v1",
		GoPackage: "example.com/app/gen/services/public/v1",
		PkgName:   "publicv1",
		Methods:   []Method{{Name: "GetHealthNote", AuthRequired: false}},
	})

	data := BuildOpenProcedures(svcs, "example.com/app")
	seen := map[string]int{}
	for _, imp := range data.Imports {
		seen[imp.Path]++
	}
	if len(seen) != 2 {
		t.Fatalf("imported %d connect packages, want 2: %v", len(seen), seen)
	}
	for path, n := range seen {
		if n != 1 {
			t.Errorf("connect package %s imported %d times — the generated file will not compile", path, n)
		}
	}
}

// TestOpenProcedures_SilenceDoesNotPublish pins the fail-closed default: an
// RPC nobody annotated is not public. The descriptor defaults AuthRequired to
// true for exactly this reason, so an unannotated method here stands in for
// "the author said nothing".
func TestOpenProcedures_SilenceDoesNotPublish(t *testing.T) {
	data := BuildOpenProcedures([]ServiceDef{{
		Name: "QuietService", Package: "demo.v1",
		GoPackage: "example.com/app/gen/services/quiet/v1", PkgName: "quietv1",
		Methods: []Method{{Name: "DoThing", AuthRequired: true}},
	}}, "example.com/app")
	if len(data.Open) != 0 {
		t.Errorf("an RPC that declared nothing public ended up in the open set: %v", data.Open)
	}
	if len(data.Imports) != 0 {
		t.Errorf("no open procedures, but the generated file imports %v — an unused import does not compile", data.Imports)
	}
}

// TestRenderOpenProcedures_IsValidGoAndAlwaysAllowsTheProbes renders the real
// file. The health probes are unconditional: they run before anything in the
// process can authenticate, and they are not declared in the project's protos,
// so nothing else could put them there.
func TestRenderOpenProcedures_IsValidGoAndAlwaysAllowsTheProbes(t *testing.T) {
	for _, tc := range []struct {
		name string
		svcs []ServiceDef
	}{
		{"with services", authProceduresFixture()},
		{"a project with no services at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := RenderOpenProcedures(tc.svcs, "example.com/app")
			if err != nil {
				t.Fatalf("RenderOpenProcedures: %v", err)
			}
			src := string(content)
			if _, err := parser.ParseFile(token.NewFileSet(), "procedures_gen.go", src, parser.SkipObjectResolution); err != nil {
				t.Fatalf("generated file is not valid Go: %v\n----\n%s", err, src)
			}
			for _, probe := range []string{"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"} {
				if !strings.Contains(src, probe) {
					t.Errorf("generated file does not allow %s — readiness probes cannot present a token:\n%s", probe, src)
				}
			}
			if !strings.Contains(src, "var UnauthenticatedProcedures = map[string]struct{}{") {
				t.Errorf("generated file does not declare the symbol pkg/middleware wires into the interceptor:\n%s", src)
			}
		})
	}
}

// `(forge.v1.method).timeout` is the declaration of an rpc's server-side
// deadline, and ProcedureDeadlines is its projection: only an rpc that
// declared one is there, a service with nothing public but a timed rpc still
// imports its connect package, and the budget is spelled as Go a reader would
// write.
func TestOpenProcedures_ProjectsDeclaredTimeouts(t *testing.T) {
	svcs := []ServiceDef{{
		Name: "ExportService", Package: "demo.v1",
		GoPackage: "example.com/app/gen/services/export/v1", PkgName: "exportv1",
		Methods: []Method{
			{Name: "Export", AuthRequired: true, Timeout: 2 * time.Minute},
			{Name: "StartOAuth", AuthRequired: true, Timeout: -time.Second},
			{Name: "Quick", AuthRequired: true, Timeout: 1500 * time.Millisecond},
			{Name: "Get", AuthRequired: true},
		},
	}}
	data := BuildOpenProcedures(svcs, "example.com/app")
	if len(data.Imports) != 1 || data.Imports[0].Alias != "exportv1connect" {
		t.Fatalf("imports = %v, want the one connect package the timed rpcs live in", data.Imports)
	}
	got := map[string]string{}
	for _, d := range data.Deadlines {
		got[d.Method] = d.Budget
	}
	want := map[string]string{
		"Export":     "2 * time.Minute",
		"StartOAuth": "-1 * time.Second",
		"Quick":      "1500 * time.Millisecond",
	}
	if len(got) != len(want) {
		t.Fatalf("deadlines = %v, want %v (an rpc with no timeout gets the slot's default, not an entry)", got, want)
	}
	for m, w := range want {
		if got[m] != w {
			t.Errorf("%s budget = %q, want %q", m, got[m], w)
		}
	}

	content, err := RenderOpenProcedures(svcs, "example.com/app")
	if err != nil {
		t.Fatal(err)
	}
	src := string(content)
	if _, err := parser.ParseFile(token.NewFileSet(), "procedures_gen.go", src, parser.SkipObjectResolution); err != nil {
		t.Fatalf("generated file is not valid Go: %v\n----\n%s", err, src)
	}
	for _, line := range []string{
		"var ProcedureDeadlines = map[string]time.Duration{",
		"exportv1connect.ExportServiceExportProcedure: 2 * time.Minute, // demo.v1.ExportService/Export — timeout: 2m0s",
	} {
		if !strings.Contains(src, line) {
			t.Errorf("generated file lacks %q:\n%s", line, src)
		}
	}
}

// TestRenderOpenProcedures_IsDeterministic keeps `forge generate` from
// producing a diff on a project nobody changed.
func TestRenderOpenProcedures_IsDeterministic(t *testing.T) {
	first, err := RenderOpenProcedures(authProceduresFixture(), "example.com/app")
	if err != nil {
		t.Fatal(err)
	}
	// Reverse the service order: the same project, described in a different
	// order, must render the same bytes.
	svcs := authProceduresFixture()
	svcs[0], svcs[1] = svcs[1], svcs[0]
	second, err := RenderOpenProcedures(svcs, "example.com/app")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("render is order-dependent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}
