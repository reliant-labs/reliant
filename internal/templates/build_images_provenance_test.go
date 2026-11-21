package templates

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// privateProvenanceStep is the rendered build-images.yml's cosign provenance
// step for a private repository: its env and its run script.
func privateProvenanceStep(t *testing.T) (env map[string]string, run string) {
	t.Helper()
	out, err := CITemplates("github").Render("build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "myapp"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(out, &wf); err != nil {
		t.Fatalf("build-images.yml is not valid YAML: %v", err)
	}
	for _, job := range wf.Jobs {
		for _, s := range job.Steps {
			if s.Name == "Attest build provenance (private repositories, cosign)" {
				return s.Env, s.Run
			}
		}
	}
	t.Fatal("no private-repository provenance step")
	return nil, ""
}

// The private-repository provenance is built only from values the step
// itself sets from the github context (never the runner's ambient env), and
// running it yields a complete SLSA v1 predicate bound to the workflow
// invocation: source ref, workflow path, commit and run. A context value that
// resolves empty fails the step before cosign signs anything.
func TestBuildImagesPrivateProvenancePredicate(t *testing.T) {
	env, run := privateProvenanceStep(t)
	if strings.Contains(run, "env.") {
		t.Errorf("the predicate reads the ambient environment (jq env.*); every value must come from the step's env:\n%s", run)
	}
	for _, k := range []string{"SOURCE_REF", "SOURCE_SHA", "WORKFLOW_REF", "REPOSITORY", "SERVER_URL", "RUN_ID", "RUN_ATTEMPT", "EVENT_NAME"} {
		if !strings.HasPrefix(env[k], "${{ ") {
			t.Errorf("step env %s = %q, want a github context expression", k, env[k])
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("the step runs on ubuntu-latest; its bash script is not run on Windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH: cannot run the step's script")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH: cannot run the step's script")
	}
	ctx := map[string]string{
		"IMAGE_REF": "ghcr.io/acme/myapp@sha256:" + strings.Repeat("a", 64), "SERVER_URL": "https://github.com",
		"REPOSITORY": "acme/myapp", "REPOSITORY_ID": "42", "REPOSITORY_OWNER_ID": "7",
		"SOURCE_REF": "refs/heads/main", "SOURCE_SHA": strings.Repeat("b", 40),
		"WORKFLOW_REF": "acme/myapp/.github/workflows/build-images.yml@refs/heads/main",
		"EVENT_NAME":   "push", "RUNNER_ENV": "github-hosted", "RUN_ID": "1001", "RUN_ATTEMPT": "2",
	}
	runStep := func(t *testing.T, vars map[string]string) (string, []byte, error) {
		dir := t.TempDir()
		// cosign is stubbed: what matters is the predicate it would sign.
		cmd := exec.Command(bash, "-euo", "pipefail", "-c", "cosign() { :; }\n"+run)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		for k := range env {
			cmd.Env = append(cmd.Env, k+"="+vars[k])
		}
		out, err := cmd.CombinedOutput()
		return dir, out, err
	}

	dir, out, err := runStep(t, ctx)
	if err != nil {
		t.Fatalf("step failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		BuildDefinition struct {
			BuildType          string `json:"buildType"`
			ExternalParameters struct {
				Workflow struct{ Ref, Repository, Path string } `json:"workflow"`
			} `json:"externalParameters"`
			ResolvedDependencies []struct {
				URI    string            `json:"uri"`
				Digest map[string]string `json:"digest"`
			} `json:"resolvedDependencies"`
		} `json:"buildDefinition"`
		RunDetails struct {
			Builder  struct{ ID string } `json:"builder"`
			Metadata struct {
				InvocationID string `json:"invocationId"`
			} `json:"metadata"`
		} `json:"runDetails"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("provenance.json: %v\n%s", err, data)
	}
	w := p.BuildDefinition.ExternalParameters.Workflow
	if w.Ref != "refs/heads/main" || w.Repository != "https://github.com/acme/myapp" || w.Path != ".github/workflows/build-images.yml" {
		t.Errorf("externalParameters.workflow = %+v", w)
	}
	if d := p.BuildDefinition.ResolvedDependencies; len(d) != 1 || d[0].URI != "git+https://github.com/acme/myapp@refs/heads/main" || d[0].Digest["gitCommit"] != strings.Repeat("b", 40) {
		t.Errorf("resolvedDependencies = %+v", d)
	}
	if p.BuildDefinition.BuildType != "https://actions.github.io/buildtypes/workflow/v1" ||
		p.RunDetails.Builder.ID != "https://github.com/acme/myapp/.github/workflows/build-images.yml@refs/heads/main" ||
		p.RunDetails.Metadata.InvocationID != "https://github.com/acme/myapp/actions/runs/1001/attempts/2" {
		t.Errorf("buildType %q builder %q invocation %q", p.BuildDefinition.BuildType, p.RunDetails.Builder.ID, p.RunDetails.Metadata.InvocationID)
	}

	for _, missing := range []string{"SOURCE_REF", "WORKFLOW_REF", "SOURCE_SHA"} {
		vars := map[string]string{}
		for k, v := range ctx {
			vars[k] = v
		}
		vars[missing] = ""
		if _, out, err := runStep(t, vars); err == nil || !strings.Contains(string(out), "provenance predicate is incomplete") {
			t.Errorf("%s empty: step err %v, want it refused as incomplete\n%s", missing, err, out)
		}
	}
}
