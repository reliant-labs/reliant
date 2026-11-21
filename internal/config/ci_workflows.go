package config

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The CI providers forge scaffolds for (`ci.provider`).
const (
	// CIProviderGitHub writes GitHub Actions workflows under .github/. The
	// default.
	CIProviderGitHub = "github"
	// CIProviderReliant writes Reliant workflows under .reliant/workflows/:
	// they run on GitHub events on a Reliant machine and post GitHub check
	// runs as the Reliant App, so CI needs no Actions minutes or runners.
	CIProviderReliant = "reliant"
)

// EffectiveProvider is the CI provider forge scaffolds for: `ci.provider`
// lowercased, with unset meaning github. A value forge has no templates for
// (gitlab, none) is returned as written, and forge scaffolds no CI for it.
func (c CIConfig) EffectiveProvider() string {
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	if p == "" {
		return CIProviderGitHub
	}
	return p
}

// ciWorkflowNames is the closed set of files forge scaffolds for CI, by the
// name `ci.workflows` switches them with: the workflow's file name without
// its extension. Under ci.provider github these are .github/workflows/*.yml
// (release also covers its vendored forge-deploy action, and dependabot is
// .github/dependabot.yml); under ci.provider reliant, ci and review are
// .reliant/workflows/ci.yaml and review.yaml.
var ciWorkflowNames = []string{"build-images", "ci", "dependabot", "deploy", "e2e", "proto-breaking", "release", "review"}

// CIWorkflowNames returns the names `ci.workflows` accepts, sorted.
func CIWorkflowNames() []string { return append([]string(nil), ciWorkflowNames...) }

// WorkflowEnabled reports whether forge scaffolds the named workflow. Every
// workflow is on unless `ci.workflows` sets it false:
//
//	ci:
//	  workflows:
//	    deploy: false        # the repo deploys with its own workflow
//	    build-images: false
//
// It decides only whether forge WRITES the file (`forge generate`, `forge
// project new`, `forge project rescaffold`, and the upgrade advisory). A
// workflow already on disk is the project's either way: CI workflows are
// scaffold-once. `features.ci: false` (or a `ci.provider` forge has no
// templates for) still turns all of them off at once.
func (c CIConfig) WorkflowEnabled(name string) bool {
	on, set := c.Workflows[name]
	return !set || on
}

// validateCIWorkflows rejects a `ci.workflows` key forge has no workflow for,
// so a typo (`deploys: false`) is a load error rather than a switch that
// switches nothing.
func validateCIWorkflows(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var unknown []string
	for name := range cfg.CI.Workflows {
		known := false
		for _, n := range ciWorkflowNames {
			known = known || n == name
		}
		if !known {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	var out []validationIssue
	for _, name := range unknown {
		line, col := findNodePos(root, []string{"ci", "workflows", name})
		if line == 0 {
			line, col = findNodePos(root, []string{"ci", "workflows"})
		}
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("ci.workflows: forge scaffolds no workflow named %q", name),
			fix: "use one of: " + strings.Join(ciWorkflowNames, ", ") + " (the workflow's file name without its extension)."})
	}
	return out
}
