// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func publishWorkflow(args string) string {
	return fmt.Sprintf(`
name: pv
inputs:
  issue: {type: integer, default: 1}
  pr: {type: integer, default: 7}
entry: [p]
nodes:
  - id: p
    type: git_publish
    args:
%s
`, args)
}

const publishBase = `      worktree: "/work/x"
      repo: "acme/app"
      message: "Fix"
`

func TestGitPublishValidation_ValidNodesAreClean(t *testing.T) {
	for name, extra := range map[string]string{
		"a templated branch": `      branch: "autopilot/issue-{{ inputs.issue }}"` + "\n",
		"a literal branch":   `      branch: "autopilot/fixed"` + "\n",
		"a pull request":     `      pr_head_of: "{{ inputs.pr }}"` + "\n",
		"an allow list":      `      branch: "autopilot/x"` + "\n" + `      allow: ["autopilot/*"]` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, computeFindings(t, publishWorkflow(publishBase+extra)))
		})
	}
}

func TestGitPublishValidation_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ args, finding string }{
		"main":                  {publishBase + "      branch: main\n", "never published to"},
		"master":                {publishBase + "      branch: master\n", "never published to"},
		"a release branch":      {publishBase + "      branch: release/v2\n", "never published to"},
		"a refs/ branch":        {publishBase + "      branch: refs/heads/x\n", "plain branch"},
		"a tag":                 {publishBase + "      branch: refs/tags/v1\n", "plain branch"},
		"a branch with a space": {publishBase + `      branch: "a b"` + "\n", "plain branch"},
		"no target":             {publishBase, "branch or a pr_head_of"},
		"both targets":          {publishBase + "      branch: x\n      pr_head_of: 3\n", "not both"},
		"no message":            {"      worktree: /w\n      repo: acme/app\n      branch: x\n", "message"},
		"no repo":               {"      worktree: /w\n      message: m\n      branch: x\n", "repo"},
		"no worktree":           {"      repo: acme/app\n      message: m\n      branch: x\n", "worktree"},
	} {
		t.Run(name, func(t *testing.T) {
			errs := computeFindings(t, publishWorkflow(tc.args))
			assert.True(t, hasFinding(errs, tc.finding), "want a finding containing %q: %v", tc.finding, messages(errs))
		})
	}
}

// A wildcard allow list is allowed, with a nudge: it is the policy's weakest setting.
func TestGitPublishValidation_WildcardAllowWarns(t *testing.T) {
	wf := publishWorkflow(publishBase + "      branch: x\n      allow: [\"*\"]\n")
	assert.Empty(t, computeFindings(t, wf), "a warning is not an error")
}
