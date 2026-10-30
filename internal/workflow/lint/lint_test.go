// Copyright (c) 2025 Reliant Labs
package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakeToken stands in for __FAKE_GITHUB_TOKEN__ in the secret-literal
// fixture. Built here so no token-shaped text is committed.
var fakeToken = "ghp" + "_" + strings.Repeat("a1B2", 9)

// agentChild is what every ref resolves to in these tests: an agent loop
// with a turn budget, the shape of builtin://autopilot-agent.
var agentChild = &reliantv1.Workflow{
	Name: "agent",
	Inputs: map[string]*reliantv1.Input{
		"max_turns": {Type: "integer", Config: &reliantv1.Input_IntegerInput{IntegerInput: &reliantv1.IntegerInputConfig{Default: proto.Int64(60)}}},
	},
}

// withHandlerChild declares an on_failure, as project://release does.
var withHandlerChild = &reliantv1.Workflow{
	Name:      "with-handler",
	OnFailure: &reliantv1.Workflow{Nodes: []*reliantv1.Node{{Id: "reconcile_tag", Type: "run"}}},
}

func loadFixture(t *testing.T, path string) Input {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "__FAKE_GITHUB_TOKEN__", fakeToken))
	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
	in := Input{Workflow: wf, Source: data, LoadRef: func(ref string) (*reliantv1.Workflow, error) {
		if ref == "project://with-handler" {
			return withHandlerChild, nil
		}
		return agentChild, nil
	}}
	sc := strings.TrimSuffix(path, ".yaml") + ".scenarios.yaml"
	if raw, err := os.ReadFile(sc); err == nil {
		parsed, err := scenario.ParseScenarioYAML(raw)
		if err != nil {
			t.Fatalf("%s does not parse: %v", sc, err)
		}
		in.Scenarios = parsed
	}
	return in
}

func findingsOf(fs []Finding, rule string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// TestEveryRuleHasAFixtureThatFailsThenPasses: testdata/<rule>/bad.yaml is a
// workflow the rule reports and good.yaml the same workflow fixed, which it
// does not. Every rule has both.
func TestEveryRuleHasAFixtureThatFailsThenPasses(t *testing.T) {
	for _, r := range Rules() {
		t.Run(r.ID, func(t *testing.T) {
			dir := filepath.Join("testdata", r.ID)
			bad := Check(loadFixture(t, filepath.Join(dir, "bad.yaml")))
			got := findingsOf(bad, r.ID)
			if len(got) == 0 {
				t.Fatalf("bad.yaml: %s reported nothing; findings: %v", r.ID, bad)
			}
			for _, f := range got {
				if f.Docs != DocsBase+"#"+r.ID {
					t.Errorf("finding docs link %q", f.Docs)
				}
			}
			good := Check(loadFixture(t, filepath.Join(dir, "good.yaml")))
			if got := findingsOf(good, r.ID); len(got) > 0 {
				t.Fatalf("good.yaml: %s still reported: %v", r.ID, got)
			}
		})
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := ruleSeverity(e.Name()); !ok {
			t.Errorf("testdata/%s names no rule", e.Name())
		}
	}
}

// The severity a rule declares is the one its fixture's finding carries,
// except shared-checkout-fetch's documented downgrade.
func TestFindingSeverityMatchesTheRule(t *testing.T) {
	for _, r := range Rules() {
		for _, f := range findingsOf(Check(loadFixture(t, filepath.Join("testdata", r.ID, "bad.yaml"))), r.ID) {
			if f.Severity != r.Severity {
				t.Errorf("%s: finding is %s, rule is %s", r.ID, f.Severity, r.Severity)
			}
		}
	}
}

func TestSharedCheckoutFetchInAnotherDirectoryWarns(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: fixture
entry: [count]
nodes:
- id: count
  type: run
  timeout: 5m
  args:
    command: 'tmp=$(mktemp -d) && git clone -q https://github.com/acme/app "$tmp" && git -C "$tmp" fetch origin main || echo "git fetch origin main failed {{ nodes.count.exit_code }}"'
`))
	if err != nil {
		t.Fatal(err)
	}
	got := findingsOf(Check(Input{Workflow: wf}), RuleSharedCheckoutFetch)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want one warning, got %v", got)
	}
}

// A slot-bounded workflow's child holds the slot while it runs, so the
// child's unbounded steps (through refs it reaches in turn) are reported on
// the node that runs it.
func TestSlotBoundedChildIsReadThroughItsRefs(t *testing.T) {
	parse := func(src string) *reliantv1.Workflow {
		wf, err := wfyaml.ParseWorkflow([]byte(src))
		require.NoError(t, err)
		return wf
	}
	children := map[string]*reliantv1.Workflow{
		"project://release": parse(`
name: release
entry: [cut]
nodes:
- id: cut
  type: workflow
  ref: project://captain
`),
		"project://captain": parse(`
name: captain
entry: [tag]
nodes:
- id: tag
  type: run
  args: {command: 'git tag v1 && echo {{ nodes.tag.exit_code }}'}
`),
	}
	wf := parse(`
name: release-cp
concurrency: {max_running: 1}
entry: [release]
nodes:
- id: release
  type: workflow
  ref: project://release
`)
	got := findingsOf(Check(Input{Workflow: wf, LoadRef: func(ref string) (*reliantv1.Workflow, error) { return children[ref], nil }}), RuleSlotUnboundedWait)
	require.Len(t, got, 1, "%v", got)
	assert.Equal(t, "release", got[0].Node)
	assert.Contains(t, got[0].Message, "project://release → cut → tag")
}

// Cases that are complements cover every value: no default is needed.
func TestComplementaryCasesNeedNoDefault(t *testing.T) {
	for _, pair := range [][2]string{
		{"nodes.t.exit_code == 0", "nodes.t.exit_code != 0"},
		{"nodes.t.exit_code != 0", "nodes.t.exit_code==0"},
		{"nodes.t.ok", "!nodes.t.ok"},
		{"nodes.t.ok", "true"},
	} {
		wf, err := wfyaml.ParseWorkflow([]byte(`
name: fixture
entry: [t]
nodes:
- id: t
  type: run
  timeout: 1m
  args: {command: make test}
- id: a
  type: save_message
  args: {role: assistant, content: a}
- id: b
  type: save_message
  args: {role: assistant, content: b}
edges:
- from: t
  cases:
  - {to: a, condition: '` + pair[0] + `'}
  - {to: b, condition: '` + pair[1] + `'}
`))
		require.NoError(t, err)
		fs := Check(Input{Workflow: wf})
		assert.Empty(t, findingsOf(fs, RuleExitCodeUnhandled), pair)
		assert.Empty(t, findingsOf(fs, RuleEdgeNoDefault), pair)
	}
}

func TestAllowanceForTheWholeWorkflow(t *testing.T) {
	src := []byte(`# lint:allow run-no-timeout, run-exit-code-unread — fixture commands finish in milliseconds
name: fixture
entry: [a]
nodes:
- id: a
  type: run
  args: {command: echo a}
`)
	wf, err := wfyaml.ParseWorkflow(src)
	if err != nil {
		t.Fatal(err)
	}
	fs := Check(Input{Workflow: wf, Source: src})
	for _, rule := range []string{RuleRunNoTimeout, RuleRunExitCodeUnread, RuleAllowInvalid} {
		if got := findingsOf(fs, rule); len(got) > 0 {
			t.Errorf("%s: %v", rule, got)
		}
	}
}

// An allowance on one node does not silence the same rule on another.
func TestNodeAllowanceIsScopedToItsNode(t *testing.T) {
	src := []byte(`name: fixture
entry: [a]
nodes:
# lint:allow run-no-timeout — echo cannot hang
- id: a
  type: run
  args: {command: 'echo {{ nodes.a.exit_code }}'}
- id: b
  type: run
  args: {command: 'make {{ nodes.b.exit_code }}'}
edges:
- {from: a, default: b}
`)
	wf, err := wfyaml.ParseWorkflow(src)
	if err != nil {
		t.Fatal(err)
	}
	got := findingsOf(Check(Input{Workflow: wf, Source: src}), RuleRunNoTimeout)
	if len(got) != 1 || got[0].Node != "b" {
		t.Fatalf("want run-no-timeout on b only, got %v", got)
	}
}

// Every rule has a section in the docs page its findings link to.
func TestEveryRuleIsDocumented(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "workflows", "lint.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range Rules() {
		if !strings.Contains(string(page), "### "+r.ID+"\n") {
			t.Errorf("docs/workflows/lint.mdx has no `### %s` section", r.ID)
		}
	}
}
