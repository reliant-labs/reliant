package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The test and integration-test jobs each end with a step that summarises
// the run `task test` recorded and uploads it to the control plane. The
// step must run after a failed test step (that is when a report matters),
// carry the token from the repository secret, and never fail the job.
func TestCIWorkflowTemplate_ReportsTestResults(t *testing.T) {
	for name, data := range map[string]CIWorkflowData{
		"minimal": {ProjectName: "minimal", TestRace: true, PermContents: "read"},
		"all features": {
			ProjectName: "myapp", HasFrontends: true, HasServices: true, TestRace: true, TestCoverage: true,
			E2EEnabled: true, E2ERuntime: "docker-compose", PermContents: "read", HasDocker: true,
			Frontends: []FrontendCIConfig{{Name: "web", Path: "frontends/web"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			content, err := CITemplates("github").Render("ci.yml.tmpl", data)
			if err != nil {
				t.Fatalf("render error: %v", err)
			}
			var wf struct {
				Jobs map[string]struct {
					Steps []struct {
						Name string            `yaml:"name"`
						If   string            `yaml:"if"`
						Env  map[string]string `yaml:"env"`
						Run  string            `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(content, &wf); err != nil {
				t.Fatalf("invalid YAML: %v", err)
			}
			for _, jobName := range []string{"test", "integration-test"} {
				job, ok := wf.Jobs[jobName]
				if !ok {
					t.Fatalf("no %q job", jobName)
				}
				var found int
				for _, s := range job.Steps {
					if s.Name != "Report test results" {
						continue
					}
					found++
					if s.If != "${{ !cancelled() }}" {
						t.Errorf("%s: report step if = %q, want ${{ !cancelled() }} so it runs after a failed test step", jobName, s.If)
					}
					if got := s.Env["FORGE_CONTROL_PLANE_TOKEN"]; got != "${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}" {
						t.Errorf("%s: report step FORGE_CONTROL_PLANE_TOKEN = %q, want the repository secret", jobName, got)
					}
					if !strings.HasPrefix(s.Run, "task test:report -- --upload") || !strings.Contains(s.Run, "||") {
						t.Errorf("%s: report step run = %q, want `task test:report -- --upload` that cannot fail the job", jobName, s.Run)
					}
				}
				if found != 1 {
					t.Errorf("%s: want exactly one \"Report test results\" step, found %d", jobName, found)
				}
			}
		})
	}
}
