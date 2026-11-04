package codegen

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestDeclaredWorkloads_ReadsBuildArgsAndServes pins the parser both
// `forge lint` and `forge scaffold` read workloads.k through: names, GoBuild
// cmd (inline, or bound to a top-level variable) and args per literal,
// `serves` with comments and brackets in it, prose ignored.
func TestDeclaredWorkloads_ReadsBuildArgsAndServes(t *testing.T) {
	src := `# example = fw.Workload {name = "nats"}
_demo_build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

migrate = fw.Workload {
    name = "migrate"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["db", "migrate", "up"]
    env = {A = "{not a brace}"}
}
admin_api = fw.Workload {
    name = "admin-api"
    build = _demo_build
    args = ["admin-api"]
    serves = [
        "billing-admin",  # [legacy] name, kept
        # "retired-admin",
        "user-admin"
    ]
}
nats = fw.Workload {
    name = "nats"
    image = "docker.io/library/nats:2.10"
}
`
	got := DeclaredWorkloads(src)
	if len(got) != 3 {
		t.Fatalf("parsed %d workloads, want 3: %+v", len(got), got)
	}
	if m := got["migrate"]; m.BuildCmd != "./cmd/demo" || strings.Join(m.Args, " ") != "db migrate up" || m.Serves != nil {
		t.Errorf("migrate = %+v", m)
	}
	a := got["admin-api"]
	if a.BuildCmd != "./cmd/demo" {
		t.Errorf("admin-api build = %q, want the bound _demo_build's cmd", a.BuildCmd)
	}
	if strings.Join(a.Serves, ",") != "billing-admin,user-admin" {
		t.Errorf("admin-api serves = %v, want [billing-admin user-admin] (the commented-out entry is not one)", a.Serves)
	}
	if n := got["nats"]; n.BuildCmd != "" || len(n.Args) != 0 {
		t.Errorf("nats = %+v, want no build and no args", n)
	}
}

// TestServingWorkload: a component is run by another workload when that
// workload's `serves` names it (in either spelling) or when the project binary
// runs its all-in-one `server` — but never a secondary binary, which is its
// own program, and never a `server` of some OTHER binary.
func TestServingWorkload(t *testing.T) {
	const src = `
admin_api = fw.Workload {
    name = "admin-api"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["admin-api"]
    serves = ["overview-admin"]
}
other = fw.Workload {
    name = "other"
    build = forge.GoBuild {cmd = "./cmd/other", output_name = "other"}
    args = ["server"]
}
`
	declared := DeclaredWorkloads(src)
	for _, tc := range []struct {
		comp   config.ComponentConfig
		wantBy string
	}{
		{config.ComponentConfig{Name: "overview-admin", Kind: config.ComponentKindServer}, "admin-api"},
		{config.ComponentConfig{Name: "overview_admin", Kind: config.ComponentKindServer}, "admin-api"},
		{config.ComponentConfig{Name: "billing", Kind: config.ComponentKindServer}, ""},
	} {
		by, ok := ServingWorkload(declared, "demo", tc.comp)
		if by != tc.wantBy || ok != (tc.wantBy != "") {
			t.Errorf("ServingWorkload(%s) = %q, %v; want %q", tc.comp.Name, by, ok, tc.wantBy)
		}
	}

	allInOne := DeclaredWorkloads(src + `
app = fw.Workload {
    name = "app"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["server"]
}
`)
	for _, kind := range []string{config.ComponentKindServer, config.ComponentKindWorker, config.ComponentKindOperator} {
		if by, _ := ServingWorkload(allInOne, "demo", config.ComponentConfig{Name: "reaper", Kind: kind}); by != "app" {
			t.Errorf("%s under an all-in-one server: served by %q, want app", kind, by)
		}
	}
	if by, ok := ServingWorkload(allInOne, "demo", config.ComponentConfig{Name: "tool", Kind: config.ComponentKindBinary}); ok {
		t.Errorf("a secondary binary is its own program, but was reported served by %q", by)
	}
}

// TestDeclaresServes: the project's own statement that it groups components
// — any `serves`, even an empty one — and nothing else counts as one.
func TestDeclaresServes(t *testing.T) {
	plain := "billing = fw.Workload {\n    name = \"billing\"\n    args = [\"billing\"]\n}\n"
	if DeclaresServes(DeclaredWorkloads(plain)) {
		t.Error("a file with no serves reported as grouping")
	}
	if DeclaresServes(DeclaredWorkloads("# serves = [\"x\"]\n" + plain)) {
		t.Error("a serves in a comment reported as grouping")
	}
	grouped := "api = fw.Workload {\n    name = \"api\"\n    args = [\"api\"]\n    serves = []\n}\n"
	if !DeclaresServes(DeclaredWorkloads(plain + grouped)) {
		t.Error("an explicit serves = [] not reported as grouping")
	}
}

// controlPlaneShape is the shape of control-plane's deploy/kcl/workloads.k:
// one binary, its services mounted into two processes it composes
// (`public-api`, `admin-api`), its workers into a third (`workers`), one
// worker and one operator run through their own subcommands under process
// names, a job on a hand-written subcommand, and infrastructure it does not
// build.
const controlPlaneShape = `
_cp_build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

admin_server = fw.Workload {
    name = "admin-server"
    kind = "service"
    build = _cp_build
    args = ["public-api"]
    ports = [fw.Port {name = "http", port = 8090, expose = True}]
}
admin_api = fw.Workload {
    name = "admin-api"
    kind = "service"
    build = _cp_build
    args = ["admin-api"]
}
workers = fw.Workload {
    name = "demo-workers"
    kind = "worker"
    build = _cp_build
    args = ["workers"]
}
identity_reconciler = fw.Workload {
    name = "identity-reconciler"
    kind = "worker"
    build = _cp_build
    args = ["identity_reconcile"]
}
workspace_controller = fw.Workload {
    name = "workspace-controller"
    kind = "operator"
    build = _cp_build
    args = ["workspace"]
}
backup_prune = fw.Workload {
    name = "backup-prune"
    kind = "job"
    build = _cp_build
    args = ["cloudsql", "prune-backups"]
}
nats = fw.Workload {
    name = "nats"
    kind = "service"
    image = "docker.io/library/nats:2.10"
}
`

func controlPlaneComponents() []config.ComponentConfig {
	return []config.ComponentConfig{
		{Name: "billing", Kind: config.ComponentKindServer},
		{Name: "billing_admin", Kind: config.ComponentKindServer},
		{Name: "alerting", Kind: config.ComponentKindServer},
		{Name: "reaper", Kind: config.ComponentKindWorker},
		{Name: "spend_snapshot", Kind: config.ComponentKindWorker},
		{Name: "identity_reconcile", Kind: config.ComponentKindWorker},
		{Name: "workspace", Kind: config.ComponentKindOperator},
		{Name: "pricing-export", Kind: config.ComponentKindBinary},
	}
}

// TestDeclaredWorkloads_ReadsKind: the literal kind is read; a kind that is
// not a string literal is left empty rather than guessed.
func TestDeclaredWorkloads_ReadsKind(t *testing.T) {
	got := DeclaredWorkloads(controlPlaneShape + "\nodd = fw.Workload {\n    name = \"odd\"\n    kind = _kind\n}\n")
	if got["admin-api"].Kind != WorkloadKindService || got["demo-workers"].Kind != WorkloadKindWorker || got["backup-prune"].Kind != WorkloadKindJob {
		t.Errorf("kinds = %+v", got)
	}
	if got["odd"].Kind != "" {
		t.Errorf("a non-literal kind read as %q", got["odd"].Kind)
	}
}

// TestServingWorkload_OwnSubcommandUnderAnotherName: a workload running a
// component's own subcommand is that component's declaration, whatever it
// is called — and only for that component.
func TestServingWorkload_OwnSubcommandUnderAnotherName(t *testing.T) {
	declared := DeclaredWorkloads(controlPlaneShape)
	for _, tc := range []struct {
		comp   config.ComponentConfig
		wantBy string
	}{
		{config.ComponentConfig{Name: "workspace", Kind: config.ComponentKindOperator}, "workspace-controller"},
		{config.ComponentConfig{Name: "identity_reconcile", Kind: config.ComponentKindWorker}, "identity-reconciler"},
		{config.ComponentConfig{Name: "reaper", Kind: config.ComponentKindWorker}, ""},
		{config.ComponentConfig{Name: "billing", Kind: config.ComponentKindServer}, ""},
	} {
		by, ok := ServingWorkload(declared, "demo", tc.comp)
		if by != tc.wantBy || ok != (tc.wantBy != "") {
			t.Errorf("ServingWorkload(%s) = %q, %v; want %q", tc.comp.Name, by, ok, tc.wantBy)
		}
	}
	if by, ok := ServingWorkload(declared, "other", config.ComponentConfig{Name: "workspace", Kind: config.ComponentKindOperator}); ok {
		t.Errorf("another binary's workload read as serving the operator: %q", by)
	}
}

// TestComposedProcesses: a project that runs a kind of component in processes
// it composes itself — and not one workload each — names those processes for
// a new component of the kind; every other shape names none, so the new
// component is declared as before.
func TestComposedProcesses(t *testing.T) {
	declared := DeclaredWorkloads(controlPlaneShape)
	components := controlPlaneComponents()
	newOf := func(name, kind string) config.ComponentConfig { return config.ComponentConfig{Name: name, Kind: kind} }

	for _, tc := range []struct {
		name string
		comp config.ComponentConfig
		want string
	}{
		// Every service runs in public-api or admin-api.
		{"service", newOf("autopilot_receiver_internal", config.ComponentKindServer), "admin-api,admin-server"},
		// One worker has a process of its own; the other two share `workers`.
		{"worker", newOf("sweeper", config.ComponentKindWorker), "demo-workers"},
		// The only operator runs through its own subcommand: per-component.
		{"operator", newOf("staticsite", config.ComponentKindOperator), ""},
		// A cron is a worker (its scheduler lives in the process)...
		{"cron", newOf("nightly", config.ComponentKindCron), "demo-workers"},
		// ...a job runs to completion on its own, never in a shared process.
		{"job", newOf("backfill", config.ComponentKindJob), ""},
		{"binary", newOf("stripe-catalog", config.ComponentKindBinary), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(ComposedProcesses(declared, "demo", components, tc.comp), ",")
			if got != tc.want {
				t.Errorf("ComposedProcesses = %q, want %q", got, tc.want)
			}
		})
	}

	// A fresh project's shape: each service has its own workload. A custom
	// process beside them does not make the next service a member of it.
	perComponent := DeclaredWorkloads(controlPlaneShape + `
billing = fw.Workload {
    name = "billing"
    kind = "service"
    build = _cp_build
    args = ["billing"]
}
billing_admin = fw.Workload {
    name = "billing-admin"
    kind = "service"
    build = _cp_build
    args = ["billing-admin"]
}
`)
	if got := ComposedProcesses(perComponent, "demo", components, newOf("ledger", config.ComponentKindServer)); got != nil {
		t.Errorf("per-component project: ComposedProcesses = %v, want none", got)
	}
	// No other service to read a convention from: declare.
	if got := ComposedProcesses(declared, "demo", nil, newOf("ledger", config.ComponentKindServer)); got != nil {
		t.Errorf("first service: ComposedProcesses = %v, want none", got)
	}
	// Another binary's processes are not this project's.
	if got := ComposedProcesses(declared, "other", components, newOf("ledger", config.ComponentKindServer)); got != nil {
		t.Errorf("another binary: ComposedProcesses = %v, want none", got)
	}
	// A process that states its components in `serves` is read through
	// ServingWorkload instead; what it leaves out is left out.
	stated := DeclaredWorkloads(strings.Replace(controlPlaneShape, `args = ["admin-api"]`, "args = [\"admin-api\"]\n    serves = [\"billing_admin\"]", 1))
	if got := strings.Join(ComposedProcesses(stated, "demo", components, newOf("ledger", config.ComponentKindServer)), ","); got != "admin-server" {
		t.Errorf("with serves on admin-api: ComposedProcesses = %q, want admin-server", got)
	}
}

// TestComposedProcessHint names the processes, the serves alternative, and
// the stanza for a workload of its own.
func TestComposedProcessHint(t *testing.T) {
	c := config.ComponentConfig{Name: "ledger", Kind: config.ComponentKindServer}
	got := ComposedProcessHint("github.com/acme/demo", "demo", c, []string{"admin-api", "admin-server"})
	for _, want := range []string{"admin-api, admin-server", `"ledger"`, "`serves`", "ledger = fw.Workload {"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint lacks %q:\n%s", want, got)
		}
	}
}
