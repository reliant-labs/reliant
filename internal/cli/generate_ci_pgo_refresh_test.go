// Not t.Parallel: the birth ledger's in-memory cache and the CI discovery
// cache are process-global.
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// PGO turns on by itself for an EXISTING hosted project (owner, 2026-11-20:
// "turn them on if there are profiles available"): the project was scaffolded
// before pgo-refresh.yml existed, and the next `forge generate` after the pin
// bump writes it beside the user's own CI files, which it leaves alone. A
// deleted one stays deleted, and `ci.workflows: {pgo-refresh: false}` declines
// it.
func TestGenerateCIWorkflows_ExistingHostedProjectGetsPGORefresh(t *testing.T) {
	const pgoRefresh = ".github/workflows/pgo-refresh.yml"
	setup := func(t *testing.T) (string, *config.ProjectConfig) {
		t.Helper()
		root := t.TempDir()
		checksums.ResetScaffoldLedgerCache()
		t.Cleanup(checksums.ResetScaffoldLedgerCache)
		cfg := &config.ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo", Kind: config.ProjectKindService}
		config.ApplyDerivedDefaults(cfg)
		// Born by an older forge and edited since: the user's.
		const release = ".github/workflows/release.yml"
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, release), []byte("# yours: scaffolded once\nname: Release\n# edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		checksums.RecordScaffold(root, release)
		// prod is declared, and hosted (the render-backed discovery,
		// answered up front).
		if err := os.MkdirAll(filepath.Join(root, "deploy", "kcl", "prod"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "deploy", "kcl", "prod", "main.k"), []byte("# never evaluated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ciHostedMu.Lock()
		ciHostedCache[root] = ciHostedTopology{Hosted: []string{"prod"}, PGO: []string{"prod"}}
		ciHostedMu.Unlock()
		// CI also discovers frontends. This fixture has none: answer that
		// discovery too, rather than invoking KCL on the placeholder main.k.
		ciFrontendsMu.Lock()
		ciFrontendsCache[root] = nil
		ciFrontendsMu.Unlock()
		t.Cleanup(func() { forgetCIDiscovery(root) })
		return root, cfg
	}
	generate := func(t *testing.T, root string, cfg *config.ProjectConfig) {
		t.Helper()
		captureStdout(t, func() {
			if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
				t.Fatalf("generateCIWorkflows: %v", err)
			}
		})
	}

	t.Run("written on the next generate", func(t *testing.T) {
		root, cfg := setup(t)
		generate(t, root, cfg)
		b, err := os.ReadFile(filepath.Join(root, pgoRefresh))
		if err != nil {
			t.Fatalf("an existing hosted project did not get %s: %v", pgoRefresh, err)
		}
		if !strings.Contains(string(b), "forge pgo pull --env prod --json") {
			t.Errorf("%s does not pull prod:\n%s", pgoRefresh, b)
		}
		if got, _ := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml")); !strings.Contains(string(got), "# edited") {
			t.Error("generate rewrote the user's release.yml")
		}
	})

	t.Run("a deleted one stays deleted", func(t *testing.T) {
		root, cfg := setup(t)
		generate(t, root, cfg)
		if err := os.Remove(filepath.Join(root, pgoRefresh)); err != nil {
			t.Fatal(err)
		}
		generate(t, root, cfg)
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Errorf("generate re-created %s after the user deleted it", pgoRefresh)
		}
	})

	t.Run("its birth is recorded in the committed ledger", func(t *testing.T) {
		root, cfg := setup(t)
		generate(t, root, cfg)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(checksums.ScaffoldEntryPath(pgoRefresh)))); err != nil {
			t.Fatalf("generate wrote %s without its birth record %s: %v", pgoRefresh, checksums.ScaffoldEntryPath(pgoRefresh), err)
		}
	})

	// reliant and control-plane deleted the file and committed its birth
	// record. A fresh clone (nothing cached) has the record and no file: no
	// generate — the upgrade's generate phase is `forge generate` — writes it,
	// and the upgrade's report has no row for it.
	t.Run("a committed birth record keeps a deleted one out of a fresh clone", func(t *testing.T) {
		root, cfg := setup(t)
		checksums.RecordScaffold(root, pgoRefresh)
		checksums.ResetScaffoldLedgerCache()
		out := captureStdout(t, func() {
			if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
				t.Fatalf("generateCIWorkflows: %v", err)
			}
		})
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Fatalf("generate re-created %s over its birth record", pgoRefresh)
		}
		if strings.Contains(out, "Generated "+pgoRefresh) {
			t.Errorf("generate claims to have written %s:\n%s", pgoRefresh, out)
		}
		for _, row := range generator.CIWorkflowAdvisoryFiles(root, cfg, ciInputs(root, cfg)) {
			if filepath.ToSlash(row.Path) == pgoRefresh {
				t.Errorf("the upgrade report offers %s, which the project deleted", pgoRefresh)
			}
		}
		if projectMayGetPGORefresh(root) {
			t.Error("the v1.11.0 pgo-default-on playbook still applies to a project that deleted the workflow")
		}
	})

	// reliant's case: its one hosted env serves a static site.
	t.Run("a hosted env with no Go binary gets none", func(t *testing.T) {
		root, cfg := setup(t)
		ciHostedMu.Lock()
		ciHostedCache[root] = ciHostedTopology{Hosted: []string{"prod"}}
		ciHostedMu.Unlock()
		out := captureStdout(t, func() {
			if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
				t.Fatalf("generateCIWorkflows: %v", err)
			}
		})
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Errorf("generate wrote %s for a hosted env with no Go binary to profile", pgoRefresh)
		}
		if strings.Contains(out, "pgo-refresh") {
			t.Errorf("nothing to say about a workflow the project's shape does not include, got:\n%s", out)
		}
	})

	// control-plane's case: a Reliant workflow refreshes the profiles.
	t.Run("a Reliant pgo-refresh workflow stands in for it", func(t *testing.T) {
		root, cfg := setup(t)
		if err := os.MkdirAll(filepath.Join(root, ".reliant", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".reliant", "workflows", "pgo-refresh.yaml"), []byte("name: pgo-refresh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := captureStdout(t, func() {
			if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
				t.Fatalf("generateCIWorkflows: %v", err)
			}
		})
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Errorf("generate wrote %s beside .reliant/workflows/pgo-refresh.yaml", pgoRefresh)
		}
		if !strings.Contains(out, ".reliant/workflows/pgo-refresh.yaml") || !strings.Contains(out, "ci.workflows: {pgo-refresh: false}") {
			t.Errorf("want one line naming the Reliant workflow and the opt-out, got:\n%s", out)
		}
		cfg.CI.Workflows = map[string]bool{"pgo-refresh": false}
		if out := captureStdout(t, func() { _ = generateCIWorkflows(root, cfg, nil, false) }); strings.Contains(out, "pgo-refresh") {
			t.Errorf("declined in forge.yaml, still noted:\n%s", out)
		}
	})

	t.Run("declined in forge.yaml", func(t *testing.T) {
		root, cfg := setup(t)
		cfg.CI.Workflows = map[string]bool{"pgo-refresh": false}
		generate(t, root, cfg)
		if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
			t.Errorf("generate wrote %s although ci.workflows declines it", pgoRefresh)
		}
	})
}

// The opt-out end to end, from forge.yaml text as a project writes it (both
// YAML spellings): it loads, and then generate, the upgrade report and the
// v1.11.0 playbook all leave pgo-refresh.yml alone.
func TestPGORefreshOptOut_FromForgeYAML(t *testing.T) {
	const pgoRefresh = ".github/workflows/pgo-refresh.yml"
	for name, yaml := range map[string]string{
		"block": "name: demo\nmodule_path: github.com/example/demo\nkind: service\nci:\n  workflows:\n    pgo-refresh: false\n",
		"flow":  "name: demo\nmodule_path: github.com/example/demo\nkind: service\nci: {workflows: {pgo-refresh: false}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			checksums.ResetScaffoldLedgerCache()
			t.Cleanup(checksums.ResetScaffoldLedgerCache)
			for rel, body := range map[string]string{
				"forge.yaml":             yaml,
				"deploy/kcl/prod/main.k": "app = forge.OnHosted {}\n",
				"cmd/demo/main.go":       "package main\n\nfunc main() {}\n",
			} {
				p := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.LoadProjectDir(root)
			if err != nil {
				t.Fatalf("forge.yaml with the opt-out does not load: %v", err)
			}
			if cfg.CI.WorkflowEnabled("pgo-refresh") {
				t.Fatal("ci.workflows.pgo-refresh: false loaded as enabled")
			}
			ciHostedMu.Lock()
			ciHostedCache[root] = ciHostedTopology{Hosted: []string{"prod"}, PGO: []string{"prod"}}
			ciHostedMu.Unlock()
			t.Cleanup(func() { forgetCIDiscovery(root) })

			out := captureStdout(t, func() {
				if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
					t.Fatalf("generateCIWorkflows: %v", err)
				}
			})
			if _, err := os.Stat(filepath.Join(root, pgoRefresh)); err == nil {
				t.Errorf("generate wrote %s although forge.yaml declines it", pgoRefresh)
			}
			if strings.Contains(out, "pgo-refresh") {
				t.Errorf("generate talks about a declined workflow:\n%s", out)
			}
			if checksums.ScaffoldRecorded(root, pgoRefresh) {
				t.Error("a declined workflow got a birth record")
			}
			if !strings.Contains(generator.CIWorkflowAbsenceReasonFor(cfg, pgoRefresh), "ci.workflows: {pgo-refresh: false}") {
				t.Error("rescaffold's absence reason does not name the opt-out")
			}
			if projectMayGetPGORefresh(root) {
				t.Error("the v1.11.0 pgo-default-on playbook nags a project that declined the workflow")
			}
		})
	}
}

// Which hosted envs pgo-refresh.yml may pull: those whose render builds a Go
// binary `forge pgo pull` writes a profile for (ResolvePGOBinaries), PGO on,
// with a main package in this checkout.
func TestCIEnvBuildsPGOBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "shop", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Directories that exist but build no Go binary (forge#2686 B1), and one
	// whose main package another platform compiles.
	for rel, files := range map[string]map[string]string{
		"cmd/empty":   {},
		"cmd/assets":  {"README.md": "not Go\n", "index.js": "console.log(1)\n"},
		"cmd/lib":     {"lib.go": "package lib\n"},
		"cmd/winonly": {"main.go": "//go:build " + otherGOOS() + "\n\npackage main\n"},
	} {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, body := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	goBin := func(pkg string) *KCLEntities {
		return &KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoGo(pkg, nil))}}
	}
	for name, tc := range map[string]struct {
		e    *KCLEntities
		want bool
	}{
		"a static site: no workloads":                            {&KCLEntities{Project: "shop", Env: "prod"}, false},
		"a GoBuild of ./cmd/shop":                                {&KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoGo("./cmd/shop", nil))}}, true},
		"the project Dockerfile's primary binary":                {&KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoDocker(DockerBuild{}))}}, true},
		"PGO turned off":                                         {&KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoGo("./cmd/shop", pgoOff()))}}, false},
		"a package not in this checkout":                         {&KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoGo("./cmd/gone", nil))}}, false},
		"a sibling checkout's binary":                            {&KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoGo("../other/cmd/x", nil))}}, true},
		"an adopted non-Go image (./cmd/<project> is not there)": {&KCLEntities{Project: "nodeapp", Env: "prod", Workloads: []WorkloadEntity{clusterWL("api", "x", "ns", pgoDocker(DockerBuild{}))}}, false},
		"an empty directory":                                     {goBin("./cmd/empty"), false},
		"a directory with no Go source":                          {goBin("./cmd/assets"), false},
		"a Go package that is not main":                          {goBin("./cmd/lib"), false},
		"a main package another platform builds":                 {goBin("./cmd/winonly"), true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ciEnvBuildsPGOBinary(root, tc.e); got != tc.want {
				t.Errorf("ciEnvBuildsPGOBinary = %v, want %v", got, tc.want)
			}
		})
	}
}

// otherGOOS is a GOOS the host is not, for a file the default build context
// excludes by its //go:build constraint.
func otherGOOS() string {
	if runtime.GOOS == "windows" {
		return "linux"
	}
	return "windows"
}
