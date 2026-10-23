package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// The runtime checks probe the port `forge env status` RESOLVED, not a
// default.
//
// `forge doctor`'s App Health looked up "app:8080" and, in a project serving
// on :3091, rendered a gray dash that was indistinguishable from "not
// applicable". The fix is not a better default: it is that the check's
// address comes from the rows the status table was built from — the rendered
// KCL plus the live-port overlay plus an actual probe. There is exactly one
// resolver, and this is the seam onto it.
func TestRuntimeTargetComesFromTheResolvedRows(t *testing.T) {
	rows := []upServiceRow{
		// Not listening: a down service must not become the probe target,
		// or every check downstream reports a connection error instead of
		// the honest "nothing is up".
		{Name: "admin-server", Kind: "host", Port: 8090, Listening: false},
		{Name: "reliant-api-server", Kind: "host", Port: 3091, Listening: true},
		{Name: "reliant-web", Kind: "frontend", Port: 3000, Listening: true},
	}
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		hostWL("reliant-api-server", withEnv("PPROF_ADDR", ":6061")),
	}}

	got := runtimeTargetFor(entities, rows)
	if got.Service != "reliant-api-server" {
		t.Errorf("Service = %q, want the first LISTENING host service", got.Service)
	}
	if got.HTTP != "localhost:3091" {
		t.Errorf("HTTP = %q, want localhost:3091 (the resolved port), not a guessed default", got.HTTP)
	}
	// pprof is a SEPARATE serverkit listener; deriving it from the HTTP port
	// would be the same class of guess.
	if got.Pprof != "localhost:6061" {
		t.Errorf("Pprof = %q, want localhost:6061 from the service's declared PPROF_ADDR", got.Pprof)
	}
}

// Nothing listening means no target — and no target means the dependent
// check reports UNDETERMINED. Inventing one would produce a confident
// failure about a port nothing ever bound.
func TestRuntimeTargetIsEmptyWhenNothingIsListening(t *testing.T) {
	rows := []upServiceRow{
		{Name: "admin-server", Kind: "host", Port: 8090, Listening: false},
		{Name: "admin-web", Kind: "frontend", Port: 3000, Listening: true},
	}
	got := runtimeTargetFor(nil, rows)
	if got.HTTP != "" || got.Service != "" {
		t.Errorf("target = %+v, want empty: no host service is up, and a frontend is not the app", got)
	}
}

// RemoteOnly is decided by what the env DECLARES runs on this machine. A
// cluster-only env with a dev-servable frontend is still remote-only: the
// frontend preview is not the stack the machine-local checks probe.
func TestRuntimeTargetRemoteOnlyFollowsTheDeclaration(t *testing.T) {
	cases := map[string]struct {
		services []WorkloadEntity
		infra    []HostInfraEntity
		want     bool
	}{
		"remote cluster + hosted only": {services: []WorkloadEntity{
			clusterWL("api", "gke_acme_us-central1_prod", "prod"),
			hostedWL("sb"),
		}, want: true},
		"a local k3d cluster service": {services: []WorkloadEntity{clusterWL("api", "k3d-acme", "dev")}, want: false},
		"a host service":              {services: []WorkloadEntity{hostWL("api")}, want: false},
		"a compose unit":              {services: []WorkloadEntity{composeWL("pg", "docker-compose.yml")}, want: false},
		"a host-infra one":            {infra: []HostInfraEntity{{Name: "bao"}}, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &KCLEntities{Workloads: tc.services, Infra: tc.infra, Frontends: []FrontendEntity{{Name: "web", Port: 3100}}}
			if got := runtimeTargetFor(e, nil).RemoteOnly; got != tc.want {
				t.Errorf("RemoteOnly = %v, want %v", got, tc.want)
			}
		})
	}
	if runtimeTargetFor(nil, nil).RemoteOnly {
		t.Error("RemoteOnly with no render must be false: an unknown env is not proven remote")
	}
}

func TestPprofPortFromAddr(t *testing.T) {
	cases := map[string]string{
		":6060":            "6060",
		"0.0.0.0:6060":     "6060",
		"localhost:16060":  "16060",
		"":                 "",
		"not-an-addr":      "",
		"localhost:notnum": "",
		":0":               "",
	}
	for in, want := range cases {
		if got := pprofPortFromAddr(in); got != want {
			t.Errorf("pprofPortFromAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// The scaffold's DEFAULT PPROF_ADDR must be a value this resolver can read.
//
// This is the other end of the wire from internal/codegen's
// TestScaffoldDefaultsPprofOn. The failure it guards is not a crash — it is
// the check quietly reporting "? pprof — could not resolve a pprof address"
// about a service that is, in fact, serving profiles. That is exactly the
// state the scaffold shipped in: a PPROF_ADDR projected onto every workload,
// declared on one side of the wire and read by neither.
func TestScaffoldPprofDefaultResolvesThroughEnvStatus(t *testing.T) {
	var def string
	for _, m := range codegen.DefaultConfigMessages() {
		for _, f := range m.Fields {
			if f.EnvVar == "PPROF_ADDR" {
				def = f.DefaultValue
			}
		}
	}
	if def == "" {
		t.Fatal("the scaffold ships no default PPROF_ADDR — a scaffolded binary starts no pprof listener")
	}

	rows := []upServiceRow{{Name: "gateway", Kind: "host", Port: 8080, Listening: true}}
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		// Exactly what appConfigEnvMap projects onto a workload when the
		// env pins nothing — the scaffold default, verbatim.
		hostWL("gateway", withEnv("PPROF_ADDR", def)),
	}}

	got := runtimeTargetFor(entities, rows)
	if got.Pprof == "" {
		t.Fatalf("the scaffold's default PPROF_ADDR (%q) resolves to no pprof address — "+
			"`forge env status` would report UNDETERMINED against a service that is serving profiles", def)
	}
	// And it must be a DIFFERENT address than the app's: pprof rides its own
	// serverkit listener, never the public port.
	if got.Pprof == got.HTTP {
		t.Errorf("pprof resolved to the app address %q — pprof must never share the public listener", got.HTTP)
	}
}

// `forge env status <env> --json` must emit a document that PARSES.
//
// activateDevStack printed `[devstack] worktree="" branch="main"` to STDOUT
// before the render, so on any checkout with a branch — i.e. all of them —
// the JSON stream was prefixed with a log line and `json.Unmarshal` failed.
// This is the discovery call agents and scripts make (it carries the API
// port and the DATABASE_URL), so the contract being unparseable is the whole
// value of the flag.
func TestEnvStatusJSONIsParseable(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	writeForgeYAML(t, dir, `name: demo
module_path: github.com/example/demo
`)
	fixture := fmt.Sprintf(`{
  "output": {
    "workloads": [
      {
        "name": "admin-server",
        "kind": "service",
        "runtime": {
          "type": "host",
          "runner": "go-run"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "ADMIN_SERVER_PORT",
              "value": "%d"
            }
          ]
        }
      }
    ]
  }
}`, port)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	t.Chdir(dir)

	out := captureStdout(t, func() {
		if err := runUpServices(t.Context(), "dev", true, "", false); err != nil {
			t.Fatalf("runUpServices: %v", err)
		}
	})

	var rep upServicesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("`env status --json` did not emit parseable JSON (%v); stdout was:\n%s", err, out)
	}
	if rep.Env != "dev" {
		t.Errorf("env = %q, want dev", rep.Env)
	}
	if len(rep.Checks) == 0 {
		t.Error("the JSON envelope carries no runtime checks — they moved here from `forge doctor`")
	}
	// And no diagnostic text leaked into the document.
	if strings.Contains(out, "[devstack]") {
		t.Errorf("the devstack diagnostic is on stdout, inside the JSON stream:\n%s", out)
	}
	// `services` is the HOST-process list, and for a long time it was the
	// only structured thing in this document — so `forge env status prod
	// --json` answered "what is running?" with two local dev servers while
	// fourteen deployments ran in the cluster, reachable only by parsing a
	// check's prose evidence. `workloads` is the cluster half, and it must
	// be present even when there is nothing to report, carrying the REASON
	// in its status rather than being absent.
	if rep.Workloads == nil {
		t.Fatalf("the JSON envelope has no `workloads` key — the cluster's state would again "+
			"be reachable only by parsing evidence prose:\n%s", out)
	}
	if rep.Workloads.Status == "" {
		t.Error("`workloads.status` is empty — it is what distinguishes an env that deploys " +
			"nothing from a cluster forge could not read")
	}
	if rep.Workloads.Workloads == nil {
		t.Errorf("`workloads.workloads` serialised as null rather than []:\n%s", out)
	}
}
