package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type protoBreakingStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func renderProtoBreakingSteps(t *testing.T) []protoBreakingStep {
	t.Helper()
	content, err := CITemplates("github").Render("proto-breaking.yml.tmpl", CIWorkflowData{PermContents: "read"})
	if err != nil {
		t.Fatalf("render proto-breaking.yml: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []protoBreakingStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, content)
	}
	return wf.Jobs["buf-breaking"].Steps
}

// proto-breaking.yml checks breaking changes and nothing else.
//
// bufbuild/buf-action@v1 defaults to running lint, format, breaking, push and
// archive, and to posting a PR comment. Under this workflow's `contents: read`
// the comment failed every PR ("Resource not accessible by integration"), and
// the format step duplicated pre-commit's `buf format` with a second, noisier
// failure (houndersclub PR #1). Input names are buf-action's own, from its
// action.yml.
func TestProtoBreakingWorkflow_BreakingOnly(t *testing.T) {
	var buf *protoBreakingStep
	steps := renderProtoBreakingSteps(t)
	for i := range steps {
		if strings.HasPrefix(steps[i].Uses, "bufbuild/buf-action@") {
			buf = &steps[i]
		}
	}
	if buf == nil {
		t.Fatal("no bufbuild/buf-action step")
	}
	for input, want := range map[string]string{
		"breaking":   "true",
		"lint":       "false",
		"format":     "false",
		"push":       "false",
		"archive":    "false",
		"pr_comment": "false",
	} {
		if got := buf.With[input]; got != want {
			t.Errorf("buf-action input %s = %q, want %q", input, got, want)
		}
	}
	if !strings.Contains(buf.With["breaking_against"], "github.base_ref") {
		t.Errorf("breaking_against = %q, want it to compare against the PR's base branch", buf.With["breaking_against"])
	}
	if buf.If == "" {
		t.Error("buf-action runs unconditionally, so the first PR that introduces protos fails with `had no .proto files`")
	}
}

// The first PR that introduces protos has no base to break. buf fails that
// comparison with `Module "path: "proto"" had no .proto files`, which is not
// a breaking change — so the check must pass with a notice, and must still
// run on every PR after it.
func TestProtoBreakingWorkflow_SkipsWhenBaseHasNoProtos(t *testing.T) {
	if testing.Short() {
		t.Skip("builds git repositories")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	var probe *protoBreakingStep
	steps := renderProtoBreakingSteps(t)
	for i := range steps {
		if steps[i].ID == "base" {
			probe = &steps[i]
		}
	}
	if probe == nil {
		t.Fatal("no step with id `base` deciding whether the base branch has protos")
	}

	for _, c := range []struct {
		name       string
		baseProtos bool
		want       string
	}{
		{"first proto PR: base has none", false, "has_protos=false"},
		{"later PR: base has protos", true, "has_protos=true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// origin: the base branch. clone: what actions/checkout leaves.
			origin := t.TempDir()
			git := func(dir string, args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			git(origin, "init", "-q", "-b", "main")
			if err := os.WriteFile(filepath.Join(origin, "README"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.baseProtos {
				if err := os.MkdirAll(filepath.Join(origin, "proto", "a", "v1"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(origin, "proto", "a", "v1", "a.proto"), []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			git(origin, "add", "-A")
			git(origin, "commit", "-qm", "base")
			clone := filepath.Join(t.TempDir(), "clone")
			git(filepath.Dir(clone), "clone", "-q", origin, clone)

			outFile := filepath.Join(t.TempDir(), "github_output")
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", probe.Run)
			cmd.Dir = clone
			cmd.Env = append(os.Environ(), "BASE_REF=main", "GITHUB_OUTPUT="+outFile)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("probe step failed: %v\n%s", err, out)
			}
			got, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), c.want) {
				t.Errorf("probe wrote %q, want %q", got, c.want)
			}
		})
	}
}
