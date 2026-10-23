package generator

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// ciFile returns the data CIWorkflows planned for dest, or nil when the file
// is not part of the set.
func ciFile(files []CIWorkflowFile, dest string) any {
	for _, f := range files {
		if f.Dest == dest {
			return f.Data
		}
	}
	return nil
}

// serviceCfg is a service-kind config with derived defaults applied — the
// shape the loader hands both CI callers.
func serviceCfg() *config.ProjectConfig {
	cfg := &config.ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo", Kind: config.ProjectKindService}
	config.ApplyDerivedDefaults(cfg)
	return cfg
}

// The Docker Build job is part of every service project's ci.yml. It is the
// job houndersclub lost when `forge generate` re-scaffolded ci.yml: the
// generate-side mapper never set HasDocker.
func TestCIWorkflows_ServiceGetsDockerBuild(t *testing.T) {
	files := CIWorkflows(t.TempDir(), serviceCfg(), nil)
	ci, ok := ciFile(files, ".github/workflows/ci.yml").(templates.CIWorkflowData)
	if !ok {
		t.Fatalf("no ci.yml in %+v", files)
	}
	if !ci.HasDocker {
		t.Fatal("ci.yml data for a service project has HasDocker=false — the docker-build job would vanish")
	}
	out, err := templates.CITemplates("github").Render("ci.yml.tmpl", ci)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "  docker-build:\n") {
		t.Fatalf("rendered ci.yml has no docker-build job:\n%s", out)
	}
}

// A service project gets buf lint and proto-breaking.yml even before it
// declares its first service proto: `forge generate` runs `buf generate`
// over proto/config on that project, so a verify-generated job without buf
// could not reproduce the committed tree.
func TestCIWorkflows_ServiceWithoutServiceProtoStillGetsBuf(t *testing.T) {
	files := CIWorkflows(t.TempDir(), serviceCfg(), nil)
	ci := ciFile(files, ".github/workflows/ci.yml").(templates.CIWorkflowData)
	if !ci.HasServices || !ci.LintBuf {
		t.Fatalf("service project must lint protos with buf: HasServices=%v LintBuf=%v", ci.HasServices, ci.LintBuf)
	}
	if ciFile(files, ".github/workflows/proto-breaking.yml") == nil {
		t.Fatal("service project must get proto-breaking.yml")
	}
}

// CLI and library projects get only what they can run: no Docker image, no
// deploy, no proto-breaking, and no job that `needs:` one of those.
func TestCIWorkflows_CLIGetsTheBuildableSubset(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "tool", ModulePath: "github.com/example/tool", Kind: config.ProjectKindCLI}
	config.ApplyDerivedDefaults(cfg)
	files := CIWorkflows(t.TempDir(), cfg, []templates.FrontendCIConfig{{Name: "web", Path: "frontends/web"}})

	var dests []string
	for _, f := range files {
		dests = append(dests, f.Dest)
	}
	if got := strings.Join(dests, ","); got != ".github/workflows/ci.yml,.github/dependabot.yml" {
		t.Fatalf("CLI workflow set = %s", got)
	}
	ci := ciFile(files, ".github/workflows/ci.yml").(templates.CIWorkflowData)
	if ci.HasDocker || ci.HasServices || ci.HasFrontends || ci.LintBuf || ci.VulnDocker || ci.E2EEnabled {
		t.Fatalf("CLI ci.yml carries service-only jobs: %+v", ci)
	}
	if !ci.VerifyGenerated {
		t.Fatal("verify-generated applies to every kind: contract mocks drift in CLIs too")
	}
}

// verify-generated reruns `forge generate`, which runs `buf generate` whenever
// the codegen feature is on, and codegen derives from .proto files existing,
// not from the project being a service. So the job needs buf on every project
// with protos. It used to install buf only for services, so a CLI with protos
// got a job that died on `exec: "buf": executable file not found`.
func TestCIWorkflows_VerifyGeneratedInstallsBufWhereGenerateRunsIt(t *testing.T) {
	cases := []struct {
		name     string
		tree     map[string]string
		wantKind string
		wantBuf  bool
	}{
		{"cli with protos", map[string]string{
			"cmd/tool/main.go":         "package main\n\nfunc main() {}\n",
			"proto/tool/v1/tool.proto": "syntax = \"proto3\";\n",
		}, config.ProjectKindCLI, true},
		{"cli without protos", map[string]string{
			"cmd/tool/main.go": "package main\n\nfunc main() {}\n",
		}, config.ProjectKindCLI, false},
		{"service", map[string]string{
			"internal/handlers/.keep":           "",
			"proto/services/demo/v1/demo.proto": "syntax = \"proto3\";\n",
		}, config.ProjectKindService, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.tree["forge.yaml"] = "name: demo\nmodule_path: github.com/example/demo\n"
			for rel, body := range tc.tree {
				p := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.LoadProjectDir(root)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.Kind != tc.wantKind {
				t.Fatalf("tree derived kind %q, want %q", cfg.Kind, tc.wantKind)
			}

			installs, version := verifyGeneratedBuf(t, CIWorkflows(root, cfg, nil))
			switch {
			case installs != tc.wantBuf:
				t.Errorf("verify-generated installs buf = %v, want %v (features.codegen = %v)",
					installs, tc.wantBuf, cfg.Features.CodegenEnabled())
			case installs && version != "1.50.0":
				t.Errorf("verify-generated installs buf %q, want the pinned \"1.50.0\": it regenerates "+
					"the tree and demands identical bytes, so buf must not move under it", version)
			}
		})
	}
}

// verifyGeneratedBuf renders ci.yml from files and reports whether the job that
// runs verify-generated (`checks`) installs buf, and the version it pins.
func verifyGeneratedBuf(t *testing.T, files []CIWorkflowFile) (installs bool, version string) {
	t.Helper()
	data, ok := ciFile(files, ".github/workflows/ci.yml").(templates.CIWorkflowData)
	if !ok {
		t.Fatalf("no ci.yml in %+v", files)
	}
	out, err := templates.CITemplates("github").Render("ci.yml.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(out, &workflow); err != nil {
		t.Fatalf("rendered ci.yml is not YAML: %v\n%s", err, out)
	}
	// verify-generated is a row of the `checks` job (`task ci:checks`), so the
	// buf it regenerates with is installed there.
	job, ok := workflow.Jobs["checks"]
	if !ok {
		t.Fatalf("rendered ci.yml has no checks job:\n%s", out)
	}
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "bufbuild/buf-setup-action@") {
			v, _ := step.With["version"].(string)
			return true, v
		}
	}
	return false, ""
}

// A CLI never gets a deploy workflow: there is no image and nothing deployed.
func TestCIWorkflows_CLIIgnoresReconcileFlag(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "tool", Kind: config.ProjectKindCLI}
	config.ApplyDerivedDefaults(cfg)
	for _, f := range CIWorkflows(t.TempDir(), cfg, nil) {
		switch f.Dest {
		case ".github/workflows/build-images.yml", ".github/workflows/deploy.yml":
			t.Errorf("%s planned for a CLI project", f.Dest)
		}
	}
}

// e2e.yml runs `task test:e2e` over ./e2e/..., so it is emitted only where a
// suite exists: the generated harness is on disk, or ci.e2e.enabled opts in.
// Otherwise the job fails on `lstat ./e2e/`.
func TestCIWorkflows_E2EWorkflowNeedsASuite(t *testing.T) {
	root := t.TempDir()
	if ciFile(CIWorkflows(root, serviceCfg(), nil), ".github/workflows/e2e.yml") != nil {
		t.Error("e2e.yml emitted for a project with no e2e/ harness and ci.e2e off")
	}

	if err := os.MkdirAll(filepath.Join(root, "e2e", "item"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ciFile(CIWorkflows(root, serviceCfg(), nil), ".github/workflows/e2e.yml") == nil {
		t.Error("e2e.yml missing for a project that carries the generated e2e/ harness")
	}

	optedIn := serviceCfg()
	optedIn.CI.E2E.Enabled = true
	if ciFile(CIWorkflows(t.TempDir(), optedIn, nil), ".github/workflows/e2e.yml") == nil {
		t.Error("e2e.yml missing with ci.e2e.enabled")
	}
}

// The deploy workflow assigns meaning by POSITION: envs[0] auto-deploys
// on every successful image build on main, envs[len-1] carries the
// environment-protection gate and is where a `v*` release tag ships.
// Filesystem discovery returns envs alphabetically, so an unsorted list
// put PROD first: prod auto-deployed on every merge to main with no
// protection, and the release tag shipped to staging forever.
func TestSortByPromotionOrder_ProdIsLast(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"default scaffold, alphabetical", []string{"prod", "staging"}, []string{"staging", "prod"}},
		{"three stages", []string{"preprod", "prod", "staging"}, []string{"staging", "preprod", "prod"}},
		{"bespoke env sorts mid-pipeline", []string{"prod", "sandbox", "staging"}, []string{"staging", "sandbox", "prod"}},
		{"production spelled out", []string{"production", "staging"}, []string{"staging", "production"}},
		{"single env", []string{"prod"}, []string{"prod"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envs := make([]templates.DeployEnv, 0, len(tc.in))
			for _, n := range tc.in {
				envs = append(envs, templates.DeployEnv{Name: n})
			}
			sortByPromotionOrder(envs)
			got := make([]string, 0, len(envs))
			for _, e := range envs {
				got = append(got, e.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("promotion order = %v, want %v", got, tc.want)
			}
		})
	}
}

// hostedRoot is a service project declaring staging and prod (dev excluded
// from CI by ciDeployEnvs).
func hostedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeEnvMain(t, root, "dev", "staging", "prod")
	return root
}

func ciDests(files []CIWorkflowFile) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		out[f.Dest] = true
	}
	return out
}

// A project with no hosted env gets exactly the cluster pipeline it got
// before F6: no release.yml, no vendored action, and a deploy.yml over every
// declared env — the same data the hosted-unaware entry point plans.
func TestCIWorkflows_ClusterOnlyProjectIsUnchanged(t *testing.T) {
	root := hostedRoot(t)
	files := CIWorkflowsFor(root, serviceCfg(), CIInputs{})
	dests := ciDests(files)
	if dests[".github/workflows/release.yml"] || dests[ForgeDeployActionPath] {
		t.Fatalf("a cluster-only project got the hosted release pipeline: %v", dests)
	}
	deploy, ok := ciFile(files, ".github/workflows/deploy.yml").(templates.DeployWorkflowData)
	if !ok {
		t.Fatal("a cluster-only project lost deploy.yml")
	}
	var names []string
	for _, e := range deploy.Environments {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "staging,prod" {
		t.Errorf("deploy.yml envs = %v, want staging,prod", names)
	}
	if !reflectDeepEqualFiles(files, CIWorkflows(root, serviceCfg(), nil)) {
		t.Error("CIWorkflowsFor with no hosted input must plan exactly what CIWorkflows does")
	}
	images := ciFile(files, ".github/workflows/build-images.yml").(templates.BuildImagesWorkflowData)
	if images.BuildEnv != "staging" {
		t.Errorf("build-images builds for %q, want staging", images.BuildEnv)
	}
}

// Every env hosted: release.yml + the action carry the whole deploy path,
// deploy.yml is NOT emitted (it would render no trigger and no job, which
// GitHub rejects), and the per-commit image build stays — its signing and
// scanning still run on every push — against the first hosted env.
func TestCIWorkflows_HostedEnvsGetReleaseWorkflow(t *testing.T) {
	root := hostedRoot(t)
	files := CIWorkflowsFor(root, serviceCfg(), CIInputs{HostedEnvs: []string{"prod", "staging"}})
	dests := ciDests(files)
	if !dests[".github/workflows/release.yml"] || !dests[ForgeDeployActionPath] {
		t.Fatalf("a hosted project must get release.yml and the forge-deploy action: %v", dests)
	}
	if dests[".github/workflows/deploy.yml"] {
		t.Error("deploy.yml planned with no non-hosted env to deploy")
	}
	rel := ciFile(files, ".github/workflows/release.yml").(templates.ReleaseWorkflowData)
	if rel.BuildEnv != "staging" || len(rel.Stages) != 2 || rel.Stages[0].Env.Name != "staging" || rel.Stages[1].Env.Name != "prod" {
		t.Errorf("release stages must follow promotion order from staging: %+v", rel)
	}
	if rel.Stages[1].Prev != "staging" || rel.Stages[0].Next != "prod" {
		t.Errorf("stages must chain: %+v", rel.Stages)
	}
	images, ok := ciFile(files, ".github/workflows/build-images.yml").(templates.BuildImagesWorkflowData)
	if !ok || images.BuildEnv != "staging" {
		t.Errorf("build-images must still build per commit, for the first hosted env: %+v", images)
	}
}

// Some envs hosted: deploy.yml keeps the others, release.yml takes the
// hosted ones, and the per-commit build follows the env deploy.yml deploys.
func TestCIWorkflows_PartlyHostedSplitsByEnv(t *testing.T) {
	root := hostedRoot(t)
	files := CIWorkflowsFor(root, serviceCfg(), CIInputs{HostedEnvs: []string{"prod"}})
	deploy := ciFile(files, ".github/workflows/deploy.yml").(templates.DeployWorkflowData)
	if len(deploy.Environments) != 1 || deploy.Environments[0].Name != "staging" {
		t.Errorf("deploy.yml must keep only the cluster env: %+v", deploy.Environments)
	}
	rel := ciFile(files, ".github/workflows/release.yml").(templates.ReleaseWorkflowData)
	if len(rel.Stages) != 1 || rel.Stages[0].Env.Name != "prod" || rel.Stages[0].Prev != "" {
		t.Errorf("release.yml must hold only prod, promoted by version: %+v", rel.Stages)
	}
	images := ciFile(files, ".github/workflows/build-images.yml").(templates.BuildImagesWorkflowData)
	if images.BuildEnv != "staging" {
		t.Errorf("build-images must build for deploy.yml's env, got %q", images.BuildEnv)
	}
}

// A MIXED env leaves deploy.yml WHOLE. Its ledger is the control plane, so a
// promoted env's `forge env deploy` pins both halves to the release — a
// deploy.yml rebuild of the cluster half would ship the OLD digests while
// reporting success. Its release.yml stage is marked Mixed (kubeconfig +
// always deploy).
func TestCIWorkflows_MixedEnvIsWhollyReleaseYml(t *testing.T) {
	root := hostedRoot(t)
	files := CIWorkflowsFor(root, serviceCfg(), CIInputs{HostedEnvs: []string{"staging", "prod"}, MixedEnvs: []string{"prod"}})
	if ciFile(files, ".github/workflows/deploy.yml") != nil {
		t.Error("a mixed env must not keep a deploy.yml job: a promoted env deploys its release, not the rebuild")
	}
	rel := ciFile(files, ".github/workflows/release.yml").(templates.ReleaseWorkflowData)
	if rel.Stages[0].Mixed || !rel.Stages[1].Mixed {
		t.Errorf("only prod is mixed: %+v", rel.Stages)
	}
}

// `forge project rescaffold` reaches the new files through the CI mapper: the
// vendored action is a mapper path although it is not under workflows/, and
// its template's name under workflows/ is NOT one. A hosted project re-emits
// both; a cluster-only project is told why it has neither.
func TestRescaffold_HostedReleaseFilesAreMapperPaths(t *testing.T) {
	for path, want := range map[string]bool{
		ForgeDeployActionPath:                       true,
		".github/workflows/release.yml":             true,
		".github/workflows/forge-deploy-action.yml": false,
	} {
		if got := IsCIMapperPath(path); got != want {
			t.Errorf("IsCIMapperPath(%q) = %v, want %v", path, got, want)
		}
	}

	root := hostedRoot(t)
	hosted := CIInputs{HostedEnvs: []string{"staging", "prod"}}
	for _, p := range []string{ForgeDeployActionPath, ".github/workflows/release.yml"} {
		content, ok, err := CIWorkflowFileFor(root, serviceCfg(), hosted, p)
		if err != nil || !ok || len(content) == 0 {
			t.Errorf("a hosted project must re-emit %s (ok=%v err=%v)", p, ok, err)
		}
		if _, ok, _ := CIWorkflowFileFor(root, serviceCfg(), CIInputs{}, p); ok {
			t.Errorf("a cluster-only project must not get %s", p)
		}
		if reason := CIWorkflowAbsenceReason(p); !strings.Contains(reason, "HOSTED") {
			t.Errorf("absence reason for %s should name hosted envs: %q", p, reason)
		}
	}
}

func reflectDeepEqualFiles(a, b []CIWorkflowFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Dest != b[i].Dest || a[i].Template != b[i].Template || !reflect.DeepEqual(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

// forge.yaml's deploy.environments, when declared, is used verbatim —
// including its auto/protection choices.
func TestCIDeployEnvs_ConfigWins(t *testing.T) {
	cfg := serviceCfg()
	cfg.Deploy.Environments = []config.DeployEnvConfig{{Name: "prod", Protection: true, URL: "https://example.com"}}
	envs := ciDeployEnvs(cfg, []string{"dev", "staging", "prod"})
	if len(envs) != 1 || envs[0].Name != "prod" || !envs[0].Protection || envs[0].URL != "https://example.com" {
		t.Fatalf("declared deploy.environments must be used verbatim, got %+v", envs)
	}
}

// TestCIWorkflows_BuildImagesHostedFollowsItsOwnEnv is ADR-0003 F3's
// generator half: whether the per-commit build job supplies a registry
// credential is decided by the env IT builds for, not by whether the project
// has a hosted env anywhere.
//
// The partly-hosted case is the one that matters. Such a project builds its
// per-commit image for the CLUSTER env (deploy.yml's first), whose registry is
// the author's own and still needs the flags — while its releases promote
// through a hosted env that needs none. A flag read off "the project is
// hosted" would drop the login from a job that requires it, and the push would
// fail with a 401 the workflow gives no way to fix.
func TestCIWorkflows_BuildImagesHostedFollowsItsOwnEnv(t *testing.T) {
	root := hostedRoot(t)
	for name, tc := range map[string]struct {
		hosted []string
		want   bool
	}{
		"every env hosted":                          {[]string{"staging", "prod"}, true},
		"partly hosted, builds for the cluster env": {[]string{"prod"}, false},
		"no hosted env":                             {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			files := CIWorkflowsFor(root, serviceCfg(), CIInputs{HostedEnvs: tc.hosted})
			images, ok := ciFile(files, ".github/workflows/build-images.yml").(templates.BuildImagesWorkflowData)
			if !ok {
				t.Fatal("no build-images workflow planned")
			}
			if images.Hosted != tc.want {
				t.Errorf("build-images for env %q: Hosted = %v, want %v — "+
					"a hosted env's push authenticates itself from the control-plane credential; "+
					"a foreign registry still needs the login step",
					images.BuildEnv, images.Hosted, tc.want)
			}
		})
	}
}
