package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/release"
)

// composedFixture lays out, under one root:
//
//	app/         the deploying project: its own service, and a client mirror
//	             of the upstream's ProjectService
//	upstream/    the sibling checkout app's ShellBuild runs in; it has moved
//	             past the pin and serves a service the pin does not have
//	upstream-at-pin/  the same module at the version app pins (a directory
//	             replace stands in for the module cache, as the go command
//	             would read it)
//
// and returns app's directory and root.
func composedFixture(t *testing.T, goModExtra string) (app, root string) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	root = t.TempDir()
	app = filepath.Join(root, "app")
	writeFiles(t, app, map[string]string{
		"forge.yaml": "name: app\n",
		"go.mod":     "module example.com/app\n\ngo 1.22\n\n" + goModExtra,
	})
	writeAlertDefaultsAt(t, app, obsdash.AlertInput{Project: "app", Services: []obsdash.AlertService{
		{Name: "billing", RPCService: "billing.v1.BillingService"},
		{Name: "project", RPCService: "up.v1.ProjectService"},
	}})
	upstream := func(dir string, svcs ...obsdash.AlertService) {
		writeFiles(t, dir, map[string]string{
			"forge.yaml": "name: upstream\n",
			"go.mod":     "module example.com/upstream\n\ngo 1.22\n",
		})
		writeAlertDefaultsAt(t, dir, obsdash.AlertInput{Project: "upstream", Services: svcs})
	}
	upstream(filepath.Join(root, "upstream"),
		obsdash.AlertService{Name: "chat", RPCService: "up.v1.ChatService"},
		obsdash.AlertService{Name: "draft", RPCService: "up.v1.DraftService"},
		obsdash.AlertService{Name: "project", RPCService: "up.v1.ProjectService"})
	upstream(filepath.Join(root, "upstream-at-pin"),
		obsdash.AlertService{Name: "chat", RPCService: "up.v1.ChatService"},
		obsdash.AlertService{Name: "project", RPCService: "up.v1.ProjectService"})
	return app, root
}

// shellWorkload is a workload built by a ShellBuild in cwd.
func shellWorkload(name, kind, cwd string) WorkloadEntity {
	return WorkloadEntity{Name: name, Kind: kind, Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "true", Cwd: cwd}}}
}

// watched is the rpc services the env's ServiceErrorRateHigh rule names.
func watched(t *testing.T, alerts []release.ShapeAlert) []string {
	t.Helper()
	var expr string
	for _, a := range alerts {
		if a.Alert == "ServiceErrorRateHigh" {
			expr = a.Expr
		}
	}
	if expr == "" {
		t.Fatalf("no ServiceErrorRateHigh in %v", alerts)
	}
	var out []string
	for _, s := range []string{"billing.v1.BillingService", "up.v1.ChatService", "up.v1.DraftService", "up.v1.ProjectService"} {
		if n := strings.Count(expr, "'"+s+"'"); n > 0 {
			if n > 1 {
				t.Errorf("%s is listed %d times", s, n)
			}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func TestResolveShapeAlerts_ComposesTheProjectsTheEnvBuildsFrom(t *testing.T) {
	app, _ := composedFixture(t, "require example.com/upstream v1.2.3\n\nreplace example.com/upstream => ../upstream-at-pin\n")
	e := obsEntities(nil)
	e.Workloads = []WorkloadEntity{
		shellWorkload("upstream-api", "service", "../upstream"),
		shellWorkload("upstream-worker", "worker", "../upstream"),
		shellWorkload("own-tool", "service", "."),
	}

	got, _, err := resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	// The pin's services, not the checkout's (DraftService is only in the
	// checkout), and ProjectService once although both projects declare it.
	want := []string{"billing.v1.BillingService", "up.v1.ChatService", "up.v1.ProjectService"}
	if w := watched(t, got); strings.Join(w, ",") != strings.Join(want, ",") {
		t.Fatalf("watched %v, want %v", w, want)
	}
	for _, id := range []string{"ServiceLatencyP95High", "ServiceSilent"} {
		found := false
		for _, a := range got {
			if a.Alert == id {
				found = strings.Contains(a.Expr, "'up.v1.ChatService'")
			}
		}
		if !found {
			t.Errorf("%s does not watch the composed ChatService", id)
		}
	}

	// The deploying env's tuning applies to the composed services too: one
	// rule per default watches every service.
	e.Observability.Alerts = &AlertsEntity{Defaults: map[string]AlertDefaultEntity{
		"error-rate": {Enabled: true, Params: map[string]float64{"threshold": 0.2}},
	}}
	got, _, err = resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a.Alert == "ServiceErrorRateHigh" && (!strings.Contains(a.Expr, "value > 0.2") || !strings.Contains(a.Expr, "'up.v1.ChatService'")) {
			t.Errorf("tuned rule = %s", a.Expr)
		}
	}
}

func TestResolveShapeAlerts_AnUnpinnedSiblingIsReadAsCheckedOut(t *testing.T) {
	app, _ := composedFixture(t, "")
	e := obsEntities(nil)
	e.Workloads = []WorkloadEntity{shellWorkload("upstream-api", "service", "../upstream")}

	got, _, err := resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"billing.v1.BillingService", "up.v1.ChatService", "up.v1.DraftService", "up.v1.ProjectService"}
	if w := watched(t, got); strings.Join(w, ",") != strings.Join(want, ",") {
		t.Fatalf("watched %v, want %v", w, want)
	}
}

func TestResolveShapeAlerts_ComposesNothingTheEnvDoesNotRun(t *testing.T) {
	app, root := composedFixture(t, "require example.com/upstream v1.2.3\n\nreplace example.com/upstream => ../upstream-at-pin\n")
	own := []string{"billing.v1.BillingService", "up.v1.ProjectService"}

	for name, ws := range map[string][]WorkloadEntity{
		"no workloads": nil,
		// Built from the sibling, but never run: it serves nothing.
		"a build-only tool": {shellWorkload("image", "tool", "../upstream")},
		// Inside the project: its own inventory already covers it.
		"a build inside the project": {shellWorkload("api", "service", "."), shellWorkload("sub", "service", "cmd/sub")},
		// A sibling that is not a forge project has no inventory.
		"a non-forge sibling": {shellWorkload("misc", "service", filepath.Join(root, "plain"))},
	} {
		t.Run(name, func(t *testing.T) {
			writeFiles(t, filepath.Join(root, "plain"), map[string]string{"Makefile": "all:\n"})
			e := obsEntities(nil)
			e.Workloads = ws
			got, _, err := resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
			if err != nil {
				t.Fatal(err)
			}
			if w := watched(t, got); strings.Join(w, ",") != strings.Join(own, ",") {
				t.Fatalf("watched %v, want only the project's own %v", w, own)
			}
		})
	}
}

func TestResolveShapeAlerts_AMissingSiblingIsReportedNotFatal(t *testing.T) {
	app, _ := composedFixture(t, "")
	e := obsEntities(nil)
	e.Workloads = []WorkloadEntity{shellWorkload("gone-api", "service", "../not-checked-out")}

	var warn bytes.Buffer
	got, _, err := resolveShapeAlerts(context.Background(), &warn, app, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	if w := watched(t, got); strings.Join(w, ",") != "billing.v1.BillingService,up.v1.ProjectService" {
		t.Fatalf("watched %v", w)
	}
	if !strings.Contains(warn.String(), "../not-checked-out") || !strings.Contains(warn.String(), "gone-api") {
		t.Fatalf("warning = %q, want it to name the directory and the workload", warn.String())
	}
}

func TestResolveShapeAlerts_ADockerBuildContextComposesItsProject(t *testing.T) {
	app, _ := composedFixture(t, "")
	e := obsEntities(nil)
	e.Workloads = []WorkloadEntity{{Name: "api", Kind: "service", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{
		Dockerfile: "Dockerfile", BuildContexts: map[string]config.BuildContext{"upstream": {Path: "../upstream"}},
	}}}}
	got, _, err := resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	if w := watched(t, got); len(w) != 4 {
		t.Fatalf("watched %v, want the project's own and the build context's", w)
	}
}

// dockerWorkload is a workload docker-built from the primary context ctx with
// the given named build contexts.
func dockerWorkload(name, ctx string, named map[string]string) WorkloadEntity {
	bc := map[string]config.BuildContext{}
	for k, p := range named {
		bc[k] = config.BuildContext{Path: p}
	}
	return WorkloadEntity{Name: name, Kind: "service", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{
		Dockerfile: "Dockerfile", Context: ctx, BuildContexts: bc,
	}}}
}

// Docker sends the primary context as well as every named one, so a project
// that is only the primary context is composed — with or without named
// contexts beside it (review B1 on #2057).
func TestResolveShapeAlerts_ADockerPrimaryContextComposesItsProject(t *testing.T) {
	all := "billing.v1.BillingService,up.v1.ChatService,up.v1.DraftService,up.v1.ProjectService"
	for name, named := range map[string]map[string]string{
		"no named contexts": nil,
		// The named context is a base project with no services of its own.
		"beside a named base context": {"base": "../base"},
		// And beside one that is a plain directory, not a forge project.
		"beside a named non-forge context": {"tools": "../plain"},
	} {
		t.Run(name, func(t *testing.T) {
			app, root := composedFixture(t, "")
			writeFiles(t, filepath.Join(root, "base"), map[string]string{"forge.yaml": "name: base\n", "go.mod": "module example.com/base\n\ngo 1.22\n"})
			writeFiles(t, filepath.Join(root, "plain"), map[string]string{"Makefile": "all:\n"})
			e := obsEntities(nil)
			e.Workloads = []WorkloadEntity{dockerWorkload("api", "../upstream", named)}
			got, _, err := resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
			if err != nil {
				t.Fatal(err)
			}
			if w := strings.Join(watched(t, got), ","); w != all {
				t.Fatalf("watched %s, want the primary context's project composed: %s", w, all)
			}
		})
	}
}

// A named context and the primary one are both read: each composes its own
// project.
func TestResolveShapeAlerts_ADockerBuildComposesPrimaryAndNamedContexts(t *testing.T) {
	app, root := composedFixture(t, "")
	other := filepath.Join(root, "other")
	writeFiles(t, other, map[string]string{"forge.yaml": "name: other\n", "go.mod": "module example.com/other\n\ngo 1.22\n"})
	writeAlertDefaultsAt(t, other, obsdash.AlertInput{Project: "other", Services: []obsdash.AlertService{{Name: "ledger", RPCService: "other.v1.LedgerService"}}})
	e := obsEntities(nil)
	e.Workloads = []WorkloadEntity{dockerWorkload("api", "../upstream", map[string]string{"other": "../other"})}
	got, _, err := resolveShapeAlerts(context.Background(), io.Discard, app, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	var expr string
	for _, a := range got {
		if a.Alert == "ServiceErrorRateHigh" {
			expr = a.Expr
		}
	}
	for _, s := range []string{"'up.v1.ChatService'", "'other.v1.LedgerService'", "'billing.v1.BillingService'"} {
		if !strings.Contains(expr, s) {
			t.Errorf("ServiceErrorRateHigh does not watch %s", s)
		}
	}
}
