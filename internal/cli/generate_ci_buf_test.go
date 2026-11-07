package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// The scaffolded verify-generated job reruns `forge generate` and demands
// identical bytes, so it needs buf on PATH exactly when generate runs
// `buf generate`. This test pins the two decisions to each other. It reads
// generate's decision from the real step table's gate, not a restatement of
// it, so a change to either side that the other does not follow fails here.
//
// They drifted once. The CI mapper installed buf only for service projects,
// while the step runs whenever features.codegen is on, and codegen derives
// from .proto files existing. A CLI with protos got a verify-generated job
// that died on `exec: "buf": executable file not found`.
func TestCIVerifyGeneratedInstallsBufExactlyWhenGenerateRunsIt(t *testing.T) {
	const bufStep = "buf generate (Go stubs)"
	var gate func(*pipelineContext) bool
	for _, step := range generateSteps() {
		if step.Name == bufStep {
			gate = step.Gate
		}
	}
	if gate == nil {
		t.Fatalf("no %q step in generateSteps(); point this test at the step that runs buf", bufStep)
	}

	// Features are derived from the tree, never configured, so the shapes
	// are trees: codegen is on exactly where a .proto exists.
	cmd := map[string]string{"cmd/tool/main.go": "package main\n\nfunc main() {}\n"}
	proto := map[string]string{"proto/tool/v1/tool.proto": "syntax = \"proto3\";\n"}
	service := map[string]string{
		"internal/handlers/.keep":           "",
		"proto/services/demo/v1/demo.proto": "syntax = \"proto3\";\n",
	}
	cases := []struct {
		name string
		tree []map[string]string
	}{
		{"service", []map[string]string{service}},
		{"service, no protos", []map[string]string{{"internal/handlers/.keep": ""}}},
		{"cli with protos", []map[string]string{cmd, proto}},
		{"cli without protos", []map[string]string{cmd}},
		{"library with protos", []map[string]string{proto}},
		{"library without protos", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"forge.yaml": "name: demo\nmodule_path: github.com/example/demo\n"}
			for _, part := range tc.tree {
				for rel, body := range part {
					files[rel] = body
				}
			}
			for rel, body := range files {
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

			runsBuf := gate(&pipelineContext{ProjectDir: root, AbsPath: root, Cfg: cfg})
			installsBuf := verifyGeneratedInstallsBuf(t, generator.CIWorkflowsFor(root, cfg, generator.CIInputs{}))
			if runsBuf != installsBuf {
				t.Errorf("%s project: forge generate runs buf = %v, but the scaffolded verify-generated "+
					"job installs buf = %v. The job reruns generate, so the two must agree.",
					cfg.Kind, runsBuf, installsBuf)
			}
		})
	}
}

// verifyGeneratedInstallsBuf renders ci.yml from files and reports whether
// its verify-generated job installs buf.
func verifyGeneratedInstallsBuf(t *testing.T, files []generator.CIWorkflowFile) bool {
	t.Helper()
	var data any
	for _, f := range files {
		if f.Dest == ".github/workflows/ci.yml" {
			data = f.Data
		}
	}
	if data == nil {
		t.Fatalf("no ci.yml planned in %+v", files)
	}
	out, err := templates.CITemplates("github").Render("ci.yml.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(out, &workflow); err != nil {
		t.Fatalf("rendered ci.yml is not YAML: %v\n%s", err, out)
	}
	// verify-generated is a row of the `checks` job (`task ci:checks`).
	job, ok := workflow.Jobs["checks"]
	if !ok {
		t.Fatalf("rendered ci.yml has no checks job:\n%s", out)
	}
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "bufbuild/buf-setup-action@") {
			return true
		}
	}
	return false
}
