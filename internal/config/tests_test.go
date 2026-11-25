package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const testsHead = "name: demo\nmodule_path: github.com/example/demo\n"

func TestTestsConfig_CoverageForms(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       bool
	}{
		{"absent is off", "", false},
		{"bool true", "tests:\n  coverage: true\n", true},
		{"bool false", "tests:\n  coverage: false\n", false},
		{"mapping defaults enabled", "tests:\n  coverage:\n    packages: module\n", true},
		{"mapping disabled", "tests:\n  coverage:\n    enabled: false\n    packages: module\n", false},
		{"legacy ci.test.coverage implies it", "ci:\n  provider: github\n  test:\n    coverage: true\n", true},
		{"explicit tests.coverage wins over legacy", "ci:\n  provider: github\n  test:\n    coverage: true\ntests:\n  coverage: false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadProject([]byte(testsHead+tc.yaml), "forge.yaml")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := cfg.TestsCoverage(); got != tc.want {
				t.Errorf("TestsCoverage() = %v, want %v", got, tc.want)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(testsHead+tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := ReadTestsCoverage(dir); got != tc.want {
				t.Errorf("ReadTestsCoverage() = %v, want %v (must agree with the full loader)", got, tc.want)
			}
		})
	}
}

func TestTestsConfig_FullShapeLoads(t *testing.T) {
	in := testsHead + `tests:
  coverage:
    packages: module
    exclude: ["internal/legacy/**"]
    include_generated: true
  upload: always
  flakes:
    quarantine: pr
    issues: true
  retention:
    default_branch_reports: 20
`
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tc := cfg.Tests
	if tc.Coverage.Packages != TestsCoverageModule || tc.Coverage.Exclude[0] != "internal/legacy/**" || !tc.Coverage.IncludeGenerated ||
		tc.Upload != TestsUploadAlways || tc.Flakes.Quarantine != TestsQuarantinePR || !tc.Flakes.Issues ||
		tc.Retention.DefaultBranchReports != 20 {
		t.Errorf("tests = %+v", tc)
	}
}

func TestTestsConfig_RejectsBadValues(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{"tests:\n  upload: sometimes\n", "tests.upload"},
		{"tests:\n  coverage:\n    packages: everything\n", "tests.coverage.packages"},
		{"tests:\n  flakes:\n    quarantine: delete\n", "tests.flakes.quarantine"},
		{"tests:\n  retention:\n    default_branch_reports: -1\n", "tests.retention.default_branch_reports"},
		{"tests:\n  coverage:\n    exclude: [\"\"]\n", "empty glob"},
		{"tests:\n  coverge: true\n", "did you mean \"coverage\""},
		{"tests:\n  coverage:\n    pakages: module\n", "did you mean \"packages\""},
		{"tests:\n  coverage: maybe\n", "tests.coverage must be"},
	} {
		_, err := LoadProject([]byte(testsHead+tc.yaml), "forge.yaml")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want one naming %q", tc.yaml, err, tc.want)
		}
	}
}

func TestTestsCoverageConfig_MarshalRoundTrip(t *testing.T) {
	on := true
	for _, c := range []TestsConfig{
		{Coverage: TestsCoverageConfig{Enabled: &on}},
		{Coverage: TestsCoverageConfig{Enabled: &on, Packages: TestsCoverageModule}},
		{Upload: TestsUploadNever},
	} {
		out, err := yaml.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var back TestsConfig
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		if testsCoverage(back, CITestConfig{}) != testsCoverage(c, CITestConfig{}) || back.Coverage.Packages != c.Coverage.Packages || back.Upload != c.Upload {
			t.Errorf("round trip of %+v gave %+v via\n%s", c, back, out)
		}
	}
	if out, _ := yaml.Marshal(TestsConfig{Coverage: TestsCoverageConfig{Enabled: &on}}); string(out) != "coverage: true\n" {
		t.Errorf("bool form marshals as %q, want \"coverage: true\\n\"", out)
	}
}
