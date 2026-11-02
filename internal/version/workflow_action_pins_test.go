// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// privilegedWorkflows are the workflows whose credential-bearing jobs build,
// sign, publish and record releases. Named literally so the affected-test
// planner (tools/test-affected) selects this test when any of them changes.
var privilegedWorkflows = []string{
	".github/workflows/release.yml",
	".github/workflows/build-images.yml",
	".github/workflows/ci.yml",
}

// TestPrivilegedWorkflowJobsPinActionsToCommitSHAs pins every third-party
// action a privileged job runs to a full commit SHA. A tag (`@v4`) or branch
// is a moving target: whoever controls the action's repository can repoint it,
// and the next run executes their code with this job's GITHUB_TOKEN write
// scopes, its OIDC identity (id-token: write) and its secrets. A job is
// privileged when its effective permissions grant any write scope.
func TestPrivilegedWorkflowJobsPinActionsToCommitSHAs(t *testing.T) {
	t.Parallel()

	root := repoRootFromTest(t)
	for _, rel := range privilegedWorkflows {
		// In-process read: a child process's reads are invisible to Go's test
		// cache, so a cached pass could outlive an edit to these files.
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		violations, privileged, err := unpinnedPrivilegedActions(data)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if privileged == 0 {
			t.Errorf("%s has no privileged job; this test no longer guards it (update privilegedWorkflows)", rel)
		}
		for _, v := range violations {
			t.Errorf("%s: %s\nPin it to a full commit SHA with the release as a comment: `uses: owner/action@<40-hex sha> # vX.Y.Z`.", rel, v)
		}
	}
}

// TestUnpinnedPrivilegedActionsRejectsMovingRefs proves the checker above
// catches what it guards against, so a green run means something.
func TestUnpinnedPrivilegedActionsRejectsMovingRefs(t *testing.T) {
	t.Parallel()

	const sha = "11d5960a326750d5838078e36cf38b85af677262"
	cases := []struct {
		name       string
		workflow   string
		violations int
	}{
		{
			name: "workflow-level write, tag ref",
			workflow: `
permissions:
  contents: write
jobs:
  publish:
    steps:
      - uses: actions/checkout@v4
`,
			violations: 1,
		},
		{
			name: "job-level id-token write, branch ref",
			workflow: `
permissions:
  contents: read
jobs:
  push:
    permissions:
      id-token: write
    steps:
      - uses: google-github-actions/auth@main
      - uses: docker/build-push-action@` + sha[:7] + `
`,
			violations: 2,
		},
		{
			name: "write-all shorthand",
			workflow: `
permissions: write-all
jobs:
  publish:
    steps:
      - uses: actions/upload-artifact@v4.3.0
`,
			violations: 1,
		},
		{
			name: "no permissions block defaults to privileged",
			workflow: `
jobs:
  publish:
    steps:
      - uses: actions/checkout@v4
`,
			violations: 1,
		},
		{
			name: "reusable workflow call in a privileged job",
			workflow: `
permissions:
  contents: write
jobs:
  call:
    uses: someorg/repo/.github/workflows/x.yml@v1
`,
			violations: 1,
		},
		{
			name: "pinned, local and docker digest refs pass",
			workflow: `
permissions:
  contents: write
jobs:
  publish:
    steps:
      - uses: actions/checkout@` + sha + ` # v4.4.0
      - uses: ./.github/actions/setup
      - uses: docker://alpine@sha256:` + strings.Repeat("a", 64) + `
`,
			violations: 0,
		},
		{
			name: "read-only job may use tags",
			workflow: `
permissions:
  contents: write
jobs:
  test:
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4
`,
			violations: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := unpinnedPrivilegedActions([]byte(tc.workflow))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.violations {
				t.Errorf("got %d violations %q, want %d", len(got), got, tc.violations)
			}
		})
	}
}

var (
	fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dockerDigest  = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

type workflowFile struct {
	Permissions yaml.Node              `yaml:"permissions"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Permissions yaml.Node `yaml:"permissions"`
	Uses        string    `yaml:"uses"`
	Steps       []struct {
		Uses string `yaml:"uses"`
	} `yaml:"steps"`
}

// unpinnedPrivilegedActions returns one message per external action (or
// reusable workflow) a privileged job references by anything other than a
// full commit SHA, and how many privileged jobs the workflow has.
func unpinnedPrivilegedActions(data []byte) ([]string, int, error) {
	var wf workflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, 0, fmt.Errorf("parse workflow: %w", err)
	}
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)

	var violations []string
	privileged := 0
	for _, name := range names {
		job := wf.Jobs[name]
		perms := &job.Permissions
		if perms.Kind == 0 {
			perms = &wf.Permissions
		}
		if !grantsWrite(perms) {
			continue
		}
		privileged++
		refs := []string{job.Uses}
		for _, s := range job.Steps {
			refs = append(refs, s.Uses)
		}
		for _, ref := range refs {
			if ref == "" || isPinned(ref) {
				continue
			}
			violations = append(violations, fmt.Sprintf("privileged job %q uses %q, a mutable ref", name, ref))
		}
	}
	return violations, privileged, nil
}

// grantsWrite reports whether a permissions node grants any write scope. An
// absent block leaves the repository's default token, which may be
// read-write, so it counts as privileged.
func grantsWrite(n *yaml.Node) bool {
	switch n.Kind {
	case 0:
		return true
	case yaml.ScalarNode:
		return n.Value == "write-all"
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if n.Content[i].Value == "write" {
				return true
			}
		}
	}
	return false
}

// isPinned reports whether a `uses:` ref is immutable: a local action, a
// docker image by digest, or a remote action at a full commit SHA.
func isPinned(ref string) bool {
	if strings.HasPrefix(ref, "./") {
		return true
	}
	if strings.HasPrefix(ref, "docker://") {
		return dockerDigest.MatchString(ref)
	}
	at := strings.LastIndex(ref, "@")
	return at >= 0 && fullCommitSHA.MatchString(ref[at+1:])
}
