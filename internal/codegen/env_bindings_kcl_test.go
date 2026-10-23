package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// There is no env-level runtime, so a workload `forge scaffold` declares runs
// nowhere until each env binds it. AppendEnvBinding adds that one line, the
// way the env binds its other workloads, and never rewrites anything.
func TestAppendEnvBinding(t *testing.T) {
	dir := t.TempDir()
	write := func(env, body string) string {
		p := filepath.Join(dir, "deploy", "kcl", env, "main.k")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	list := "_workloads = [\n    _on_host_job(wl.migrate)\n    _on_host(wl.item)\n]\n\noutput = 1\n"
	dev := write("dev", list)
	// A deployed env as scaffolded today: hosted on the control plane.
	write("prod", strings.ReplaceAll(strings.ReplaceAll(list, "_on_host_job(", "_hosted("), "_on_host(", "_hosted("))
	// A deployed env scaffolded before hosting was the default: every
	// workload on a cluster the project operates.
	write("legacy", strings.ReplaceAll(strings.ReplaceAll(list, "_on_host_job(", "_on_cluster("), "_on_host(", "_on_cluster("))

	for _, tc := range []struct {
		env, kind, name, wantBinder string
	}{
		{"dev", WorkloadKindWorker, "mailer", "_on_host"},
		{"dev", WorkloadKindOperator, "reaper", "_on_k3d"},
		{"prod", WorkloadKindWorker, "mailer", "_hosted"},
		{"prod", WorkloadKindJob, "backfill", "_hosted"},
		// The platform refuses these: a cluster you operate is the one
		// binding that can run them.
		{"prod", WorkloadKindCron, "sweeper", "_on_cluster"},
		{"prod", WorkloadKindOperator, "reaper", "_on_cluster"},
		{"prod", WorkloadKindTool, "ctl", "_build_only"},
		// A pre-hosting env keeps its shape: a new service lands beside its
		// siblings on the cluster, never on a control plane it may not have.
		{"legacy", WorkloadKindService, "billing", "_on_cluster"},
	} {
		res, err := AppendEnvBinding(dir, "shop", tc.env, tc.kind, tc.name)
		if err != nil || !res.Applied {
			t.Fatalf("%s %s: applied=%v err=%v", tc.env, tc.name, res.Applied, err)
		}
		if res.Binder != tc.wantBinder || res.Bound != "wl."+tc.name {
			t.Errorf("%s %s (%s): bound %s with %s, want wl.%s with %s", tc.env, tc.name, tc.kind, res.Bound, res.Binder, tc.name, tc.wantBinder)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "deploy", "kcl", tc.env, "main.k"))
		if want := "    " + tc.wantBinder + "(wl." + tc.name + ")\n]"; !strings.Contains(string(b), want) {
			t.Errorf("%s: want %q at the end of the list:\n%s", tc.env, want, b)
		}
		if !strings.HasSuffix(string(b), "]\n\noutput = 1\n") {
			t.Errorf("%s: content after the list was disturbed:\n%s", tc.env, b)
		}
	}

	// Already bound (by an earlier run, or by hand, under any binder): no-op.
	before, _ := os.ReadFile(dev)
	if res, err := AppendEnvBinding(dir, "shop", "dev", WorkloadKindService, "item"); res.Applied || err != nil {
		t.Errorf("rebinding an already-bound workload: applied=%v err=%v", res.Applied, err)
	}
	if after, _ := os.ReadFile(dev); string(after) != string(before) {
		t.Errorf("a no-op append wrote the file")
	}

	// Restructured past recognition: no write, the caller prints the line.
	write("staging", "_workloads = [w for w in wl.ALL]\n")
	if res, err := AppendEnvBinding(dir, "shop", "staging", WorkloadKindService, "item"); res.Applied || err != nil {
		t.Errorf("no recognisable list: applied=%v err=%v", res.Applied, err)
	}
}

// A hosted env serves its API as ONE workload, `_api` — the binary's
// `server`, which mounts every service and supervises every worker — so the
// browser reaches every service at one origin. A service or worker scaffolded
// later is already run by it: binding it on a line of its own would host a
// second workload the browser cannot reach (or run a worker twice). What
// `server` does not run still binds as before: a job, an operator, a tool.
func TestAppendEnvBinding_HostedAPIRunsServicesAndWorkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(bindings string) {
		t.Helper()
		body := "# An example, not a declaration:\n#     _example = fw.Workload {name = \"x\", args = [\"server\"], build = forge.GoBuild {cmd = \"./cmd/shop\"}}\n" +
			APIWorkloadStanza("github.com/acme/shop", "shop") + "\n\n_workloads = [\n" + bindings + "]\n\noutput = 1\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	if ident, ok := EnvServerWorkload(APIWorkloadStanza("github.com/acme/shop", "shop"), "shop"); !ok || ident != APIWorkloadIdent {
		t.Fatalf("EnvServerWorkload(the scaffolded _api) = %q, %v", ident, ok)
	}
	// Another project's binary, or one subcommand of this one, is not the API.
	for _, other := range []string{
		strings.Replace(APIWorkloadStanza("github.com/acme/shop", "shop"), `"./cmd/shop"`, `"./cmd/admin"`, 1),
		strings.Replace(APIWorkloadStanza("github.com/acme/shop", "shop"), `["server"]`, `["orders"]`, 1),
	} {
		if ident, ok := EnvServerWorkload(other, "shop"); ok {
			t.Errorf("EnvServerWorkload found %q in a workload that does not run shop's `server`:\n%s", ident, other)
		}
	}

	// Bound already: services and workers are served, nothing is written.
	write("    _hosted(wl.migrate)\n    _hosted(_api)\n")
	before := read()
	for _, kind := range []string{WorkloadKindService, WorkloadKindWorker} {
		res, err := AppendEnvBinding(dir, "shop", "prod", kind, "billing")
		if err != nil || res.Applied || res.ServedBy != APIWorkloadIdent {
			t.Errorf("%s in an env binding _api: %+v, %v — want served by _api, nothing written", kind, res, err)
		}
	}
	if read() != before {
		t.Errorf("a served workload wrote the file:\n%s", read())
	}
	// What `server` does not run binds on its own line, as before.
	for kind, want := range map[string]string{WorkloadKindJob: "_hosted", WorkloadKindOperator: "_on_cluster", WorkloadKindTool: "_build_only"} {
		res, err := AppendEnvBinding(dir, "shop", "prod", kind, "x-"+kind)
		if err != nil || !res.Applied || res.Binder != want || res.ServedBy != "" {
			t.Errorf("%s: %+v, %v — want its own %s line", kind, res, err, want)
		}
	}

	// Born with no service, so `_api` is declared but not bound: the first
	// service binds the API itself, once, the way the env binds migrate.
	write("    _hosted(wl.migrate)\n")
	res, err := AppendEnvBinding(dir, "shop", "prod", WorkloadKindService, "orders")
	if err != nil || !res.Applied || res.Bound != APIWorkloadIdent || res.Binder != "_hosted" {
		t.Fatalf("first service in an env with an unbound _api: %+v, %v — want _api bound _hosted", res, err)
	}
	if got := read(); !strings.Contains(got, "    _hosted(wl.migrate)\n    _hosted(_api)\n]") || strings.Contains(got, "wl.orders") {
		t.Errorf("want `_hosted(_api)` appended and no per-service line:\n%s", got)
	}
	if res, _ := AppendEnvBinding(dir, "shop", "prod", WorkloadKindWorker, "mailer"); res.Applied || res.ServedBy != APIWorkloadIdent {
		t.Errorf("a worker after _api is bound: %+v — want served", res)
	}
}

// A hosted-admissible workload follows the env's migrate job, because it
// reads the database migrate migrates. A hosted env that put one cron on its
// own cluster still binds a new service hosted; a cluster env that rebound
// one service to the control plane still binds the next one on its cluster.
func TestEnvBinderIn_FollowsMigrate(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"hosted env with a cron on a cluster", "    _hosted(wl.migrate)\n    _on_cluster(wl.sweeper)\n    _on_cluster(wl.reaper)\n", "_hosted"},
		{"cluster env with one hosted service", "    _on_cluster(wl.migrate)\n    _hosted(wl.api)\n", "_on_cluster"},
		{"no migrate, a hosted binding", "    _on_cluster(wl.sweeper)\n    _hosted(wl.api)\n", "_hosted"},
		{"no migrate, cluster only", "    _on_cluster(wl.api)\n", "_on_cluster"},
		{"empty list", "", "_hosted"},
		{"migrate bound by a custom binder", "    _on_gpu(wl.migrate)\n    _on_cluster(wl.api)\n", "_on_cluster"},
	} {
		if got := envBinderIn(tc.body, "prod", WorkloadKindService); got != tc.want {
			t.Errorf("%s: binder = %s, want %s", tc.name, got, tc.want)
		}
	}
	// A refused kind never follows migrate onto the control plane.
	if got := envBinderIn("    _hosted(wl.migrate)\n", "prod", WorkloadKindCron); got != "_on_cluster" {
		t.Errorf("cron in a hosted env: binder = %s, want _on_cluster", got)
	}
}

// A fresh scaffold's deployed envs are hosted: every kind the Restricted
// profile admits binds `_hosted`, and only the kinds it refuses bind to a
// cluster you operate. The refusal list must agree with the profile the
// deploy skill documents (cron, operator).
func TestEnvBinder_HostedByDefault(t *testing.T) {
	for kind, want := range map[string]string{
		WorkloadKindService:  "_hosted",
		WorkloadKindWorker:   "_hosted",
		WorkloadKindJob:      "_hosted",
		WorkloadKindCron:     "_on_cluster",
		WorkloadKindOperator: "_on_cluster",
		WorkloadKindTool:     "_build_only",
	} {
		for _, env := range []string{"staging", "prod", "preview"} {
			if got := EnvBinder(env, kind); got != want {
				t.Errorf("EnvBinder(%s, %s) = %s, want %s", env, kind, got, want)
			}
		}
		if (HostedRefusal(kind) != "") != (want == "_on_cluster") {
			t.Errorf("HostedRefusal(%s) = %q disagrees with its binder %s", kind, HostedRefusal(kind), want)
		}
	}
}

func TestEnvDeclaresNoCluster(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]bool{
		"# example:\n#     _cluster = forge.ClusterTarget {...}\n_cluster = None\n": true,
		"_cluster = forge.ClusterTarget {cluster = \"gke\", namespace = \"x\"}\n":   false,
		"# _cluster = None\n_workloads = []\n":                                      false,
	} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := EnvDeclaresNoCluster(dir, "prod"); got != want {
			t.Errorf("EnvDeclaresNoCluster(%q) = %v, want %v", body, got, want)
		}
	}
	if EnvDeclaresNoCluster(dir, "missing") {
		t.Error("a missing env declares nothing")
	}
}

// The frontend's dev config resolves the API at the `<project>-dev-api` port
// key; `_port_of` gives that key to one workload. A project created with no
// service names ITSELF — a workload that does not exist — so the first
// `forge scaffold service task` bound task under its own key, which stepped
// to the next free port, and config.js pointed at a port nothing listened
// on. The first service bound must take the key over; a bound owner keeps it.
func TestAppendEnvBinding_FirstServiceClaimsTheAPIPortKey(t *testing.T) {
	const portOf = `_port_of = lambda name: str -> int {
    plugin.resolve_port("demo-dev-api" if name == "demo" else "demo-dev-" + name, 8085)
}
`
	dir := t.TempDir()
	p := filepath.Join(dir, "deploy", "kcl", "dev", "main.k")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	unowned := portOf + "_workloads = [\n    _on_host_job(wl.migrate)\n]\n"
	if err := os.WriteFile(p, []byte(unowned), 0o644); err != nil {
		t.Fatal(err)
	}

	if res, err := AppendEnvBinding(dir, "demo", "dev", WorkloadKindService, "task"); !res.Applied || err != nil {
		t.Fatalf("bind task: applied=%v err=%v", res.Applied, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `resolve_port("demo-dev-api" if name == "task" else`) {
		t.Errorf("the first bound service must own the API port key:\n%s", b)
	}

	// A second service must not take it from the first, which is bound.
	if res, err := AppendEnvBinding(dir, "demo", "dev", WorkloadKindService, "billing"); !res.Applied || err != nil {
		t.Fatalf("bind billing: applied=%v err=%v", res.Applied, err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), `if name == "task" else`) {
		t.Errorf("a bound owner must keep the API port key:\n%s", b)
	}

	// A worker is not an API: it never claims the key.
	if err := os.WriteFile(p, []byte(unowned), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := AppendEnvBinding(dir, "demo", "dev", WorkloadKindWorker, "mailer"); !res.Applied || err != nil {
		t.Fatalf("bind mailer: applied=%v err=%v", res.Applied, err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), `if name == "demo" else`) {
		t.Errorf("a worker must not claim the API port key:\n%s", b)
	}
}
