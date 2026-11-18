package generator

import (
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// reliantCfg is serviceCfg with `ci.provider: reliant`.
func reliantCfg() *config.ProjectConfig {
	cfg := serviceCfg()
	cfg.CI.Provider = config.CIProviderReliant
	return cfg
}

func ciDestList(files []CIWorkflowFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Dest)
	}
	return out
}

// ci.provider reliant gives the project the two Reliant workflows and no
// GitHub Actions file: the provider decides which files forge offers, in
// every writer (they all go through CIWorkflowsFor).
func TestCIWorkflowsFor_ReliantProviderIsTheReliantWorkflowsOnly(t *testing.T) {
	files := CIWorkflowsFor(t.TempDir(), reliantCfg(), CIInputs{HostedEnvs: []string{"prod"}})
	got := strings.Join(ciDestList(files), ",")
	if want := ReliantCIWorkflowPath + "," + ReliantReviewWorkflowPath; got != want {
		t.Fatalf("ci.provider reliant scaffolds %s, want %s", got, want)
	}
	for _, f := range files {
		out, err := f.Render()
		if err != nil {
			t.Fatalf("render %s: %v", f.Dest, err)
		}
		var doc struct {
			Name  string `yaml:"name"`
			Check struct {
				Name   string `yaml:"name"`
				Output string `yaml:"output"`
			} `yaml:"check"`
			Triggers []struct {
				Name string `yaml:"name"`
			} `yaml:"triggers"`
		}
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatalf("%s is not YAML: %v\n%s", f.Dest, err, out)
		}
		name := CIWorkflowName(f.Dest)
		if doc.Name != name {
			t.Errorf("%s: workflow name %q, want %q (the file name: Reliant addresses it by it)", f.Dest, doc.Name, name)
		}
		if want := "demo/" + name; doc.Check.Name != want {
			t.Errorf("%s: check.name %q, want %q — the name a branch ruleset requires", f.Dest, doc.Check.Name, want)
		}
		if !strings.Contains(doc.Check.Output, "nodes.?report.check") {
			t.Errorf("%s: check.output %q does not read the report node's check", f.Dest, doc.Check.Output)
		}
		if len(doc.Triggers) == 0 {
			t.Errorf("%s declares no triggers", f.Dest)
		}
		// The scaffold banner is what tells ForeignCIWorkflow the file
		// is forge's, however the user edits it afterwards.
		if !strings.Contains(string(out), "yours: scaffolded once") {
			t.Errorf("%s has no scaffold-once banner", f.Dest)
		}
		// A Go template action that leaked out of the raw-string blocks
		// would reach the engine as a broken CEL template.
		if strings.Contains(string(out), "{{.") || strings.Contains(string(out), "{{`") {
			t.Errorf("%s carries an unrendered Go template action", f.Dest)
		}
	}
}

// The GitHub set is unchanged by the new provider: the default provider and
// an explicit `github` both scaffold exactly what forge always did, and never
// a Reliant workflow (so rescaffold never offers one under github either).
func TestCIWorkflowsFor_GitHubProviderUnchanged(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := t.TempDir()
	def := serviceCfg()
	if def.CI.EffectiveProvider() != config.CIProviderGitHub {
		t.Fatalf("the default provider is %q, want github", def.CI.EffectiveProvider())
	}
	explicit := serviceCfg()
	explicit.CI.Provider = "GitHub"
	a := CIWorkflowsFor(root, def, CIInputs{})
	b := CIWorkflowsFor(root, explicit, CIInputs{})
	if !reflectDeepEqualFiles(a, b) {
		t.Fatalf("ci.provider: GitHub scaffolds %v, the default %v", ciDestList(b), ciDestList(a))
	}
	if !reflectDeepEqualFiles(a, ciWorkflowsAll(root, def, CIInputs{})) {
		t.Fatalf("the github provider no longer scaffolds the whole GitHub set: %v", ciDestList(a))
	}
	for _, f := range a {
		if CIProviderOfPath(f.Dest) != config.CIProviderGitHub {
			t.Errorf("the github provider scaffolds %s", f.Dest)
		}
		if _, err := f.Render(); err != nil {
			t.Errorf("render %s: %v", f.Dest, err)
		}
	}
}

// A provider forge has no templates for scaffolds no CI at all, as before.
func TestCIWorkflowsFor_UnknownProviderScaffoldsNothing(t *testing.T) {
	cfg := serviceCfg()
	cfg.CI.Provider = "gitlab"
	if files := CIWorkflowsFor(t.TempDir(), cfg, CIInputs{}); len(files) != 0 {
		t.Fatalf("ci.provider gitlab scaffolds %v", ciDestList(files))
	}
}

// `ci.workflows` switches the Reliant workflows by name like the GitHub ones.
func TestCIWorkflowsFor_ReliantWorkflowsSwitchOff(t *testing.T) {
	cfg := reliantCfg()
	cfg.CI.Workflows = map[string]bool{"review": false}
	if got := strings.Join(ciDestList(CIWorkflowsFor(t.TempDir(), cfg, CIInputs{})), ","); got != ReliantCIWorkflowPath {
		t.Fatalf("ci.workflows {review: false} scaffolds %s, want only %s", got, ReliantCIWorkflowPath)
	}
	if reason := CIWorkflowAbsenceReasonFor(cfg, ReliantReviewWorkflowPath); !strings.Contains(reason, "review: false") {
		t.Errorf("absence reason %q does not name the switch", reason)
	}
}

func TestCIProviderOfPath(t *testing.T) {
	for path, want := range map[string]string{
		ReliantCIWorkflowPath:                  config.CIProviderReliant,
		ReliantReviewWorkflowPath:              config.CIProviderReliant,
		".github/workflows/ci.yml":             config.CIProviderGitHub,
		".github/dependabot.yml":               config.CIProviderGitHub,
		ForgeDeployActionPath:                  config.CIProviderGitHub,
		".reliant/workflows/mine.yaml":         "", // the user's own workflow
		".github/workflows/my-own.yml":         "",
		"internal/handlers/demo/service.go":    "",
		".reliant/workflows/ci/scenarios.yaml": "",
	} {
		if got := CIProviderOfPath(path); got != want {
			t.Errorf("CIProviderOfPath(%q) = %q, want %q", path, got, want)
		}
		if got := IsCIMapperPath(path); got != (want != "") {
			t.Errorf("IsCIMapperPath(%q) = %v, want %v", path, got, want != "")
		}
	}
	if got := CIWorkflowName(ReliantCIWorkflowPath); got != "ci" {
		t.Errorf("CIWorkflowName(%q) = %q, want ci", ReliantCIWorkflowPath, got)
	}
}

// Rescaffold asks the mapper, and the mapper answers for the provider
// forge.yaml names: the other provider's file is absent, and the reason
// says which setting gives it.
func TestCIWorkflowFileFor_FollowsTheProvider(t *testing.T) {
	root := t.TempDir()
	if _, ok, err := CIWorkflowFileFor(root, reliantCfg(), CIInputs{}, ReliantCIWorkflowPath); err != nil || !ok {
		t.Fatalf("ci.provider reliant has no %s (ok=%v, err=%v)", ReliantCIWorkflowPath, ok, err)
	}
	if _, ok, _ := CIWorkflowFileFor(root, reliantCfg(), CIInputs{}, ".github/workflows/ci.yml"); ok {
		t.Fatal("ci.provider reliant still offers .github/workflows/ci.yml")
	}
	if reason := CIWorkflowAbsenceReasonFor(serviceCfg(), ReliantCIWorkflowPath); !strings.Contains(reason, "ci.provider: reliant") {
		t.Errorf("absence reason %q does not say ci.provider: reliant gives it", reason)
	}
}

func TestReliantCIGuidanceNamesTheCheckAndTheSteps(t *testing.T) {
	g := ReliantCIGuidance(reliantCfg(), "forge", []string{ReliantCIWorkflowPath})
	for _, want := range []string{"demo/ci", "reliant trigger activate --workflow ci", "ruleset", ".github/workflows/", "forge skill load ci/reliant"} {
		if !strings.Contains(g, want) {
			t.Errorf("guidance lacks %q:\n%s", want, g)
		}
	}
}
