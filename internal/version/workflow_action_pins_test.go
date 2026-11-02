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
// privileged when its effective permissions grant any write scope. Each pin
// must also be the reviewed commit for its release (reviewedActionPins); the
// live verification after a pin change is in dev-docs/action-pins.md.
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
		unreviewed, err := unreviewedPrivilegedPins(data, reviewedActionPins)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, v := range unreviewed {
			t.Errorf("%s: %s\nA new or changed pin is a review decision: resolve the release tag to its commit upstream, then add the pair to reviewedActionPins (dev-docs/action-pins.md).", rel, v)
		}
	}
}

// reviewedActionPins is the reviewed action-to-commit mapping: every external
// action a privileged job may run, keyed `owner/repo[/path]@<commit>`, valued
// with the release tag that commit was resolved from. A 40-hex ref is not
// enough on its own: an unintended commit (a typo, a fork's commit reachable
// through the upstream repository, a bump nobody resolved) is just as
// immutable as the right one. Changing a pin means changing this table, which
// puts the tag-to-commit resolution in front of a reviewer.
var reviewedActionPins = map[string]string{
	"actions/checkout@11d5960a326750d5838078e36cf38b85af677262":                   "v4.4.0",
	"actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803":                   "v6.1.0",
	"actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16":                   "v6.5.0",
	"actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38":                 "v6.5.0",
	"actions/cache@0057852bfaa89a56745cba8c7296529d2fc39830":                      "v4.3.0",
	"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02":            "v4.6.2",
	"actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093":          "v4.3.0",
	"pnpm/action-setup@b906affcce14559ad1aafd4ab0e942779e9f58b1":                  "v4.3.0",
	"docker/setup-buildx-action@8d2750c68a42422c14e847fe6c8ac0403b4cbd6f":         "v3.12.0",
	"docker/metadata-action@c299e40c65443455700f0fdfc63efafe5b349051":             "v5.10.0",
	"docker/build-push-action@10e90e3645eae34f1e60eeb005ba3a3d33f178e8":           "v6.19.2",
	"google-github-actions/auth@c200f3691d83b41bf9bbd8638997a462592937ed":         "v2.1.13",
	"google-github-actions/setup-gcloud@e427ad8a34f8676edf47cf7d7925499adf3eb74f": "v2.2.1",
}

// TestUnreviewedPrivilegedPinsRejectsUnmappedCommits proves the mapping check
// rejects a full-length commit that is not the reviewed one, and a reviewed
// commit whose release comment disagrees with the table.
func TestUnreviewedPrivilegedPinsRejectsUnmappedCommits(t *testing.T) {
	t.Parallel()

	reviewed := map[string]string{
		"actions/checkout@11d5960a326750d5838078e36cf38b85af677262": "v4.4.0",
	}
	cases := []struct {
		name       string
		step       string
		violations int
	}{
		{"reviewed commit and tag", "actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0", 0},
		{"full-length but incorrect commit", "actions/checkout@0123456789abcdef0123456789abcdef01234567 # v4.4.0", 1},
		{"reviewed commit of another action", "evil/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0", 1},
		{"tag comment disagrees with the table", "actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.5.0", 1},
		{"no tag comment", "actions/checkout@11d5960a326750d5838078e36cf38b85af677262", 1},
		{"local action needs no mapping", "./.github/actions/setup", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workflow := `
permissions:
  contents: write
jobs:
  publish:
    steps:
      - uses: ` + tc.step + `
  test:
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567
`
			got, err := unreviewedPrivilegedPins([]byte(workflow), reviewed)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.violations {
				t.Errorf("got %d violations %q, want %d", len(got), got, tc.violations)
			}
		})
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
	// usesLine reads a `uses:` value and its trailing release comment, which
	// the YAML decoder discards.
	usesLine = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)(?:\s+#\s*(\S+))?`)
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

// jobRef is one `uses:` reference in a privileged job.
type jobRef struct {
	job, ref string
}

// privilegedRefs returns every non-empty `uses:` reference of the workflow's
// privileged jobs, in job-name order, and how many privileged jobs it has.
func privilegedRefs(data []byte) ([]jobRef, int, error) {
	var wf workflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, 0, fmt.Errorf("parse workflow: %w", err)
	}
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)

	var refs []jobRef
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
		uses := []string{job.Uses}
		for _, s := range job.Steps {
			uses = append(uses, s.Uses)
		}
		for _, ref := range uses {
			if ref != "" {
				refs = append(refs, jobRef{job: name, ref: ref})
			}
		}
	}
	return refs, privileged, nil
}

// unpinnedPrivilegedActions returns one message per external action (or
// reusable workflow) a privileged job references by anything other than a
// full commit SHA, and how many privileged jobs the workflow has.
func unpinnedPrivilegedActions(data []byte) ([]string, int, error) {
	refs, privileged, err := privilegedRefs(data)
	if err != nil {
		return nil, 0, err
	}
	var violations []string
	for _, r := range refs {
		if !isPinned(r.ref) {
			violations = append(violations, fmt.Sprintf("privileged job %q uses %q, a mutable ref", r.job, r.ref))
		}
	}
	return violations, privileged, nil
}

// unreviewedPrivilegedPins returns one message per commit-pinned remote action
// a privileged job runs that is not in reviewed, or whose release comment is
// not the tag reviewed records for that commit. Mutable refs are
// unpinnedPrivilegedActions' to report; local actions and docker digests need
// no mapping.
func unreviewedPrivilegedPins(data []byte, reviewed map[string]string) ([]string, error) {
	refs, _, err := privilegedRefs(data)
	if err != nil {
		return nil, err
	}
	// The release comment of each ref as written. A ref written twice with
	// different comments keeps every comment, so a wrong one is still seen.
	comments := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if m := usesLine.FindStringSubmatch(line); m != nil {
			comments[m[1]] = append(comments[m[1]], m[2])
		}
	}
	var violations []string
	seen := map[jobRef]bool{}
	for _, r := range refs {
		if seen[r] || strings.HasPrefix(r.ref, "./") || strings.HasPrefix(r.ref, "docker://") || !isPinned(r.ref) {
			continue
		}
		seen[r] = true
		tag, ok := reviewed[r.ref]
		if !ok {
			violations = append(violations, fmt.Sprintf("privileged job %q uses %q, a commit not in the reviewed action-to-commit mapping", r.job, r.ref))
			continue
		}
		for _, c := range comments[r.ref] {
			if c != tag {
				violations = append(violations, fmt.Sprintf("privileged job %q uses %q with release comment %q; the reviewed mapping says %q", r.job, r.ref, c, tag))
				break
			}
		}
	}
	return violations, nil
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
