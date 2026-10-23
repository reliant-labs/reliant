//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// composedProcessWorkloadsK is control-plane's deploy shape in miniature: the
// project binary mounts its services into processes it composes itself
// (`public-api`, `admin-api`), so no service has a workload of its own.
const composedProcessWorkloadsK = `import forge
import forge.workloads as fw

_build = forge.GoBuild {cmd = "./cmd/multiapp", output_name = "multiapp"}

migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    build = _build
    args = ["db", "migrate", "up"]
    config_secrets = ["DATABASE_URL"]
}

public_api = fw.Workload {
    name = "public-api"
    kind = "service"
    build = _build
    args = ["public-api"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    config_secrets = ["DATABASE_URL"]
}

admin_api = fw.Workload {
    name = "admin-api"
    kind = "service"
    build = _build
    args = ["admin-api"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    config_secrets = ["DATABASE_URL"]
}

ALL: [fw.Workload] = [
    migrate,
    public_api,
    admin_api,
]
`

// TestE2EScaffoldServiceIntoComposedProcess is the control-plane #1106
// defect, end to end. `forge scaffold service` in a project whose services
// all run in processes it composes appended a standalone workload for the new
// service to deploy/kcl/workloads.k and asked for it to be bound in every
// env. It must declare and bind nothing — the new service belongs in one of
// those processes, which only the project's Go knows — and say so, with the
// stanza to paste if it does want a workload of its own.
func TestE2EScaffoldServiceIntoComposedProcess(t *testing.T) {
	t.Parallel()

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin,
		"project", "new", "multiapp",
		"--mod", "example.com/multiapp",
		"--service", "order",
		"--service", "intake",
	)
	projectDir := filepath.Join(dir, "multiapp")
	addCorpusForgePkgReplace(t, projectDir)

	workloadsPath := filepath.Join(projectDir, "deploy", "kcl", "workloads.k")
	writeFileE2E(t, workloadsPath, composedProcessWorkloadsK)
	envs := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(projectDir, "deploy", "kcl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		main := filepath.Join(projectDir, "deploy", "kcl", e.Name(), "main.k")
		if raw, err := os.ReadFile(main); e.IsDir() && err == nil {
			envs[main] = string(raw)
		}
	}
	if len(envs) == 0 {
		t.Fatal("the scaffolded project has no env main.k to check")
	}

	out := runCmdOutput(t, projectDir, forgeBin, "scaffold", "service", "billing")

	if got := readFileE2E(t, workloadsPath); got != composedProcessWorkloadsK {
		t.Fatalf("workloads.k changed in a project that composes its service processes:\n%s", got)
	}
	for path, before := range envs {
		if got := readFileE2E(t, path); got != before {
			t.Errorf("%s changed: a service that has no workload must not be bound:\n%s", path, got)
		}
	}
	for _, want := range []string{
		"processes it composes itself (admin-api, public-api)",
		"`serves`",
		"billing = fw.Workload {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scaffold output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `Bind workload "billing"`) {
		t.Errorf("scaffold still asked for billing to be bound:\n%s", out)
	}
	// The scaffold itself still ran: the service exists.
	if _, err := os.Stat(filepath.Join(projectDir, "internal", "handlers", "billing")); err != nil {
		t.Errorf("billing handler package missing: %v", err)
	}
}
