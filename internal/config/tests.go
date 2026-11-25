package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// TestsConfig is the `tests:` block: how a project's test runs are captured,
// what coverage they measure and what forge does about flaky tests
// (docs/proposals/tests-coverage-flakes.md §1.8). Every key is optional and
// the whole block may be absent:
//
//	tests:
//	  coverage: true                # or a mapping, below
//	  upload: ci                    # ci | always | never
//	  flakes:
//	    quarantine: suggest         # suggest | pr
//	    issues: false
//	  retention:
//	    default_branch_reports: 50
//
//	tests:
//	  coverage:
//	    enabled: true               # the mapping form; absent is true
//	    packages: package           # package | module
//	    exclude: ["internal/legacy/**"]
//	    include_generated: false
//
// A new scaffold writes `coverage: true`; a project without the block has
// coverage off until it opts in (the `tests` upgrade playbook adds it), so a
// forge bump alone never instruments anything.
type TestsConfig struct {
	// Coverage says whether coverage is measured, and how. Read through
	// [ProjectConfig.TestsCoverage], which folds in the legacy
	// `ci.test.coverage`.
	Coverage TestsCoverageConfig `yaml:"coverage,omitempty"`
	// Upload is when a run's report is sent to the control plane: `ci`
	// (default: from CI only), `always` (also local clean-tree runs) or
	// `never`. Read by `forge test report --upload` (F3).
	Upload string `yaml:"upload,omitempty"`
	// Flakes is what forge does about a test that flakes. Read by
	// `forge test flakes` (F4).
	Flakes TestsFlakesConfig `yaml:"flakes,omitempty"`
	// Retention bounds what the control plane keeps for this project. Sent
	// with the uploaded report (F3); the control plane applies it.
	Retention TestsRetentionConfig `yaml:"retention,omitempty"`
}

// TestsCoverageConfig is `tests.coverage`: a bool, or a mapping whose
// `enabled` defaults to true.
type TestsCoverageConfig struct {
	// Enabled is the bool form, or the mapping's `enabled`. nil when the key
	// is absent. Read through [ProjectConfig.TestsCoverage]. It drives the
	// computed default of KCL `forge.Tests.coverage` (instrumented builds
	// for local, ephemeral and preview envs) and the scaffolded lanes'
	// coverage flags.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Packages is what a test lane's coverage measures: `package` (default:
	// each test binary its own package) or `module` (`-coverpkg=<module>/...`,
	// deduplicated at capture). Read by `forge test capture` and the
	// scaffolded lanes (F2, F7).
	Packages string `yaml:"packages,omitempty"`
	// Exclude are path globs left out of every coverage percentage. Read by
	// `forge test report` / `coverage` (F3).
	Exclude []string `yaml:"exclude,omitempty"`
	// IncludeGenerated counts generated code (gen/, *_gen.go, *.pb.go, …) in
	// the percentages instead of on a line of its own. Read by F3.
	IncludeGenerated bool `yaml:"include_generated,omitempty"`
}

// The values tests.* enumerates.
const (
	TestsCoveragePackage = "package"
	TestsCoverageModule  = "module"

	TestsUploadCI     = "ci"
	TestsUploadAlways = "always"
	TestsUploadNever  = "never"

	TestsQuarantineSuggest = "suggest"
	TestsQuarantinePR      = "pr"
)

// UnmarshalYAML accepts `coverage: <bool>` and the mapping form.
func (c *TestsCoverageConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var b bool
		if err := n.Decode(&b); err != nil {
			return fmt.Errorf("tests.coverage must be true, false or a mapping: %w", err)
		}
		*c = TestsCoverageConfig{Enabled: &b}
		return nil
	}
	type plain TestsCoverageConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	if p.Enabled == nil {
		on := true
		p.Enabled = &on
	}
	*c = TestsCoverageConfig(p)
	return nil
}

// MarshalYAML writes the bool form when nothing but `enabled` is set.
func (c TestsCoverageConfig) MarshalYAML() (any, error) {
	if c.Packages == "" && len(c.Exclude) == 0 && !c.IncludeGenerated {
		if c.Enabled == nil {
			return nil, nil
		}
		return *c.Enabled, nil
	}
	type plain TestsCoverageConfig
	return plain(c), nil
}

// IsZero lets `omitempty` drop an absent coverage key.
func (c TestsCoverageConfig) IsZero() bool {
	return c.Enabled == nil && c.Packages == "" && len(c.Exclude) == 0 && !c.IncludeGenerated
}

// TestsFlakesConfig is `tests.flakes`.
type TestsFlakesConfig struct {
	// Quarantine is what `forge test flakes` does with a confirmed flake:
	// `suggest` (default: print the quarantine entry) or `pr` (open a pull
	// request adding it). Read by F4.
	Quarantine string `yaml:"quarantine,omitempty"`
	// Issues files and updates `flake:suspected` / `flake:removed` issues.
	// Read by F4.
	Issues bool `yaml:"issues,omitempty"`
}

// TestsRetentionConfig is `tests.retention`.
type TestsRetentionConfig struct {
	// DefaultBranchReports is how many full reports of the default branch
	// the control plane keeps (newest first; 0 is its default, 50). Older
	// ones keep a daily summary. Sent with the upload (F3).
	DefaultBranchReports int `yaml:"default_branch_reports,omitempty"`
}

// TestsCoverage reports whether the project measures coverage: an explicit
// `tests.coverage` wins, and otherwise the legacy `ci.test.coverage: true`
// means true. Absent both, it is false: coverage is opt-in for a project
// that predates the `tests:` block.
func (c *ProjectConfig) TestsCoverage() bool {
	if c == nil {
		return false
	}
	return testsCoverage(c.Tests, c.CI.Test)
}

func testsCoverage(t TestsConfig, legacy CITestConfig) bool {
	if t.Coverage.Enabled != nil {
		return *t.Coverage.Enabled
	}
	return legacy.Coverage
}

// ReadTestsCoverage is [ProjectConfig.TestsCoverage] for the forge.yaml in
// projectDir, reading only the two keys it needs, so a render does not load
// the whole project. A missing or unreadable file is false; an invalid one is
// left to the full loader to report.
func ReadTestsCoverage(projectDir string) bool {
	data, err := os.ReadFile(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		return false
	}
	var doc struct {
		Tests TestsConfig `yaml:"tests"`
		CI    struct {
			Test CITestConfig `yaml:"test"`
		} `yaml:"ci"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	return testsCoverage(doc.Tests, doc.CI.Test)
}

func validateTests(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	enum := func(path []string, v string, allowed ...string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, a := range allowed {
			if v == a {
				return
			}
		}
		line, col := findNodePos(root, path)
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("%s %q is invalid", strings.Join(path, "."), v),
			fix: fmt.Sprintf("use one of: %s (absent is %s).", strings.Join(allowed, ", "), allowed[0])})
	}
	t := cfg.Tests
	enum([]string{"tests", "coverage", "packages"}, t.Coverage.Packages, TestsCoveragePackage, TestsCoverageModule)
	enum([]string{"tests", "upload"}, t.Upload, TestsUploadCI, TestsUploadAlways, TestsUploadNever)
	enum([]string{"tests", "flakes", "quarantine"}, t.Flakes.Quarantine, TestsQuarantineSuggest, TestsQuarantinePR)
	for i, g := range t.Coverage.Exclude {
		if strings.TrimSpace(g) == "" {
			line, col := findNodePos(root, []string{"tests", "coverage", "exclude", fmt.Sprintf("[%d]", i)})
			out = append(out, validationIssue{line: line, column: col,
				msg: "tests.coverage.exclude has an empty glob", fix: "remove it, or name the paths to leave out (\"internal/legacy/**\")."})
		}
	}
	if t.Retention.DefaultBranchReports < 0 {
		line, col := findNodePos(root, []string{"tests", "retention", "default_branch_reports"})
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("tests.retention.default_branch_reports value %d is invalid", t.Retention.DefaultBranchReports),
			fix: "use a positive count of reports (absent keeps the control plane's default, 50)."})
	}
	return out
}
