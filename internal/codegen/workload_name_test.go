package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// A component is named like a Go package (`engine_shadow`), and the workload
// scaffold declared for it carried that name verbatim — which fw.Workload
// rejects, because the name becomes every object's name: "Workload.name
// 'engine_shadow' must be an RFC-1123 label". Every env then failed to
// render, and `forge generate` reported it only as a failed k3d-ports step.

// workloadNameCheck is kcl/workload.k's check on Workload.name, verbatim.
var workloadNameCheck = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestWorkloadName_IsADNSLabelThatMatchesItsComponent(t *testing.T) {
	for in, want := range map[string]string{
		"engine_shadow":        "engine-shadow",
		"settlement_processor": "settlement-processor",
		"admin-server":         "admin-server",
		"AdminServer":          "admin-server",
		"billing":              "billing",
		"book_snapshotter2":    "book-snapshotter2",
	} {
		got := WorkloadName(in)
		if got != want {
			t.Errorf("WorkloadName(%q) = %q, want %q", in, got, want)
		}
		if !workloadNameCheck.MatchString(got) {
			t.Errorf("WorkloadName(%q) = %q fails fw.Workload's name check", in, got)
		}
		if !SameWorkload(got, in) {
			t.Errorf("SameWorkload(%q, %q) = false; a workload must match its component", got, in)
		}
	}
	if SameWorkload("engine-shadow", "engine") {
		t.Error("distinct names must not match")
	}
}

func TestWorkloadStanza_NamesAMultiWordComponentAsALabel(t *testing.T) {
	stanza := WorkloadStanza("github.com/acme/demo", "demo",
		config.ComponentConfig{Name: "engine_shadow", Kind: config.ComponentKindWorker})
	for _, want := range []string{
		"engine_shadow = fw.Workload {\n", // the KCL binding an env refines
		`    name = "engine-shadow"` + "\n",
		`    args = ["engine_shadow"]` + "\n", // the worker's cobra subcommand
	} {
		if !strings.Contains(stanza, want) {
			t.Errorf("stanza lacks %q:\n%s", want, stanza)
		}
	}
	if !WorkloadDeclared(stanza, "engine_shadow") || !WorkloadDeclared(stanza, "engine-shadow") {
		t.Errorf("the stanza must read as declaring engine_shadow in either spelling:\n%s", stanza)
	}
}

// The field half of WorkloadDeclared matches any spelling on its own, so a
// user who renamed the binding still has the workload recognised.
func TestWorkloadDeclared_MatchesTheFieldInAnySpelling(t *testing.T) {
	content := "shadow = fw.Workload {\n    name = \"engine-shadow\"\n}\n"
	if !WorkloadDeclared(content, "engine_shadow") {
		t.Errorf("name = \"engine-shadow\" must declare engine_shadow")
	}
	if WorkloadDeclared(content, "engine") {
		t.Errorf("a different component must not match")
	}
}

// `forge scaffold crd --operator fleet_sync` finds the operator's workload,
// declared `name = "fleet-sync"`.
func TestAppendOperatorCRD_FindsAMultiWordOperator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, WorkloadsKCLRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stanza := WorkloadStanza("github.com/acme/demo", "demo",
		config.ComponentConfig{Name: "fleet_sync", Kind: config.ComponentKindOperator, Group: "demo.io", Version: "v1"})
	if err := os.WriteFile(path, []byte(stanza), 0o644); err != nil {
		t.Fatal(err)
	}
	if edit, err := AppendOperatorCRD(dir, "fleet_sync", "Fleet"); err != nil || edit != CRDListAdded {
		t.Fatalf("AppendOperatorCRD: %v, %v — want added", edit, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `crds = ["Fleet"]`) {
		t.Errorf("CRD not listed:\n%s", b)
	}
}

// The dev env's `_port_of` lambda compares the workload's declared name, so a
// multi-word service that claims the API port key is written as its label.
func TestAppendEnvBinding_AMultiWordServiceClaimsTheKeyByItsWorkloadName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deploy", "kcl", "dev", "main.k")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	main := `_port_of = lambda name: str -> int {
    plugin.resolve_port("demo-dev-api" if name == "demo" else "demo-dev-" + name, 8085)
}
_workloads = [
    _on_host_job(wl.migrate)
]
`
	if err := os.WriteFile(p, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := AppendEnvBinding(dir, "demo", "dev", WorkloadKindService, "admin_server"); !res.Applied || err != nil {
		t.Fatalf("bind admin_server: applied=%v err=%v", res.Applied, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `if name == "admin-server" else`) {
		t.Errorf("the API port key must name the workload as declared (admin-server):\n%s", b)
	}
	if !strings.Contains(string(b), "wl.admin_server") {
		t.Errorf("the binding must use the KCL identifier:\n%s", b)
	}
}
