// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A workflow's `triggers:` block is the WHEN of a workflow: what makes it run.
// It is stored as YAML (.reliant/workflows/*.yaml and the DB draft) and read
// back on every activation and every fire, so it must round-trip with no
// loss and marshal to the same bytes every time.

// The golden files are the canonical form: what MarshalWorkflow emits for a
// workflow parsed from them. Each one is parsed, re-marshaled, and compared
// byte for byte, so a golden is both an input and the expected output.
func TestTriggersGoldenRoundTrip(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("testdata", "triggers", "*.golden.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, goldens, "golden files are missing")

	for _, path := range goldens {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			wf, err := ParseWorkflow(data)
			require.NoError(t, err)
			require.NotEmpty(t, wf.GetTriggers(), "the golden declares triggers")

			out, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			assert.Equal(t, string(data), string(out), "canonical YAML must marshal back to itself")

			again, err := ParseWorkflow(out)
			require.NoError(t, err)
			assert.True(t, proto.Equal(wf, again), "proto must survive a second round trip unchanged")
		})
	}
}

// Every source kind has a golden, so a new arm on WorkflowTrigger.source that
// the codec does not know about fails here rather than silently dropping.
func TestTriggersGoldenCoversEverySourceKind(t *testing.T) {
	seen := map[string]bool{}
	goldens, err := filepath.Glob(filepath.Join("testdata", "triggers", "*.golden.yaml"))
	require.NoError(t, err)
	for _, path := range goldens {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		wf, err := ParseWorkflow(data)
		require.NoError(t, err)
		for _, tr := range wf.GetTriggers() {
			if src := tr.ProtoReflect().WhichOneof(tr.ProtoReflect().Descriptor().Oneofs().ByName("source")); src != nil {
				seen[string(src.Name())] = true
			}
		}
	}
	oneof := (&reliantv1.WorkflowTrigger{}).ProtoReflect().Descriptor().Oneofs().ByName("source")
	for i := 0; i < oneof.Fields().Len(); i++ {
		name := string(oneof.Fields().Get(i).Name())
		assert.True(t, seen[name], "no golden exercises the %q source", name)
	}
}

func TestTriggersParseEveryField(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: triage
inputs:
  issue_number:
    type: integer
triggers:
  - name: new-issue
    description: A new issue was opened
    integration:
      integration: github
      events: [issues.opened, issues.reopened]
      match: {repository: reliant-labs/reliant}
      poll_interval: 5m
    filter: "!trigger.payload.data.issue.labels.exists(l, l.name == 'wontfix')"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
  - name: nightly
    schedule:
      cron: ["0 9 * * 1-5", "0 12 * * 6"]
      interval: 1h
      timezone: America/New_York
      overlap: allow
      catchup_window: 30m
  - name: hook
    webhook:
      hmac: {header: X-Hub-Signature-256, algorithm: sha256, prefix: "sha256=", encoding: hex}
  - name: after-deploy
    workflow_event:
      workflows: [deploy, builtin://agent]
      outcomes: [finished, failed]
entry: [a]
nodes:
  - id: a
    type: call_llm
`))
	require.NoError(t, err)
	require.Len(t, wf.GetTriggers(), 4)

	issue := wf.GetTriggers()[0]
	assert.Equal(t, "new-issue", issue.GetName())
	assert.Equal(t, "A new issue was opened", issue.GetDescription())
	assert.Equal(t, "!trigger.payload.data.issue.labels.exists(l, l.name == 'wontfix')", issue.GetFilter())
	assert.Equal(t, map[string]string{"issue_number": "{{ trigger.payload.data.issue.number }}"}, issue.GetInputs())
	assert.True(t, proto.Equal(&reliantv1.IntegrationSource{
		Integration:  "github",
		Events:       []string{"issues.opened", "issues.reopened"},
		Match:        map[string]string{"repository": "reliant-labs/reliant"},
		PollInterval: "5m",
	}, issue.GetIntegration()))

	sched := wf.GetTriggers()[1].GetSchedule()
	require.NotNil(t, sched)
	assert.Equal(t, []string{"0 9 * * 1-5", "0 12 * * 6"}, sched.GetCron())
	assert.Equal(t, "1h", sched.GetInterval())
	assert.Equal(t, "America/New_York", sched.GetTimezone())
	assert.Equal(t, reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW, sched.GetOverlap())
	assert.Equal(t, "30m", sched.GetCatchupWindow())

	hook := wf.GetTriggers()[2].GetWebhook()
	require.NotNil(t, hook)
	assert.True(t, proto.Equal(&reliantv1.WebhookHmac{
		Header: "X-Hub-Signature-256", Algorithm: "sha256", Prefix: "sha256=", Encoding: "hex",
	}, hook.GetHmac()))

	event := wf.GetTriggers()[3].GetWorkflowEvent()
	require.NotNil(t, event)
	assert.Equal(t, []string{"deploy", "builtin://agent"}, event.GetWorkflows())
	assert.Equal(t, []string{"finished", "failed"}, event.GetOutcomes())
}

// The sugar a human (or agent) is likely to write is accepted and normalized
// to the canonical form: a single cron or event as a scalar, a webhook with no
// options as a bare key, and the proto's full enum spelling.
func TestTriggersSugarNormalizes(t *testing.T) {
	sugared := `name: s
triggers:
  - name: one-cron
    schedule: {cron: "0 9 * * *", overlap: TRIGGER_OVERLAP_POLICY_ALLOW}
  - name: one-event
    integration: {integration: github, events: issues.opened}
  - name: bare-hook
    webhook:
  - name: empty-hook
    webhook: {}
  - name: any-run
    workflow_event: {}
`
	wf, err := ParseWorkflow([]byte(sugared))
	require.NoError(t, err)
	require.Len(t, wf.GetTriggers(), 5)

	assert.Equal(t, []string{"0 9 * * *"}, wf.GetTriggers()[0].GetSchedule().GetCron())
	assert.Equal(t, reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW, wf.GetTriggers()[0].GetSchedule().GetOverlap())
	assert.Equal(t, []string{"issues.opened"}, wf.GetTriggers()[1].GetIntegration().GetEvents())
	assert.NotNil(t, wf.GetTriggers()[2].GetWebhook(), "a bare `webhook:` selects the webhook source")
	assert.NotNil(t, wf.GetTriggers()[3].GetWebhook())
	assert.NotNil(t, wf.GetTriggers()[4].GetWorkflowEvent(), "an empty workflow_event matches any run, and must survive as that arm")

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Equal(t, `name: s
triggers:
    - name: one-cron
      schedule:
        cron: [0 9 * * *]
        overlap: allow
    - name: one-event
      integration:
        integration: github
        events: [issues.opened]
    - name: bare-hook
      webhook: {}
    - name: empty-hook
      webhook: {}
    - name: any-run
      workflow_event: {}
`, string(out))
}

// Unknown keys are errors, at every level. A typo in a trigger is not
// harmless: `filtr:` silently dropped is a trigger that fires on everything,
// and `crn:` dropped is a schedule that can never fire.
func TestTriggersRejectWhatTheyCannotRepresent(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want string
	}{
		"unknown trigger key": {
			yaml: "triggers:\n  - name: a\n    filtr: x\n    webhook: {}\n",
			want: `unknown field "filtr"`,
		},
		"unknown source key": {
			yaml: "triggers:\n  - name: a\n    schedule: {crn: \"* * * * *\"}\n",
			want: `unknown field "crn"`,
		},
		"unknown nested key": {
			yaml: "triggers:\n  - name: a\n    webhook: {hmac: {hedaer: X}}\n",
			want: `unknown field "hedaer"`,
		},
		"two sources": {
			yaml: "triggers:\n  - name: a\n    webhook: {}\n    schedule: {cron: \"* * * * *\"}\n",
			want: "exactly one source",
		},
		"bad overlap": {
			yaml: "triggers:\n  - name: a\n    schedule: {cron: \"* * * * *\", overlap: sometimes}\n",
			want: `"sometimes"`,
		},
		"not a list": {
			yaml: "triggers:\n  name: a\n",
			want: "expected a list",
		},
		"inputs not strings": {
			yaml: "triggers:\n  - name: a\n    webhook: {}\n    inputs: {n: {x: 1}}\n",
			want: "inputs",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseWorkflow([]byte("name: w\n" + tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "triggers", "the error must say where it is")
		})
	}
}

// A trigger with no source is representable (it is a validation error, not a
// parse error, so the agent sees it with every other finding).
func TestTriggerWithNoSourceParses(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: w\ntriggers:\n  - name: a\n"))
	require.NoError(t, err)
	require.Len(t, wf.GetTriggers(), 1)
	assert.Nil(t, wf.GetTriggers()[0].GetSource())
}

// Inputs whose values are not strings (a number, a bool) are accepted as
// their literal text; the template system reads them as literals.
func TestTriggerInputScalarsKeepTheirText(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: w\ntriggers:\n  - name: a\n    webhook: {}\n    inputs: {limit: 10, dry_run: true}\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"limit": "10", "dry_run": "true"}, wf.GetTriggers()[0].GetInputs())
}

func TestMarshalTriggersIsDeterministic(t *testing.T) {
	wf := &reliantv1.Workflow{Name: "w", Triggers: []*reliantv1.WorkflowTrigger{{
		Name:   "a",
		Inputs: map[string]string{"f": "6", "a": "1", "e": "5", "b": "2", "d": "4", "c": "3"},
		Source: &reliantv1.WorkflowTrigger_Integration{Integration: &reliantv1.IntegrationSource{
			Integration: "github",
			Events:      []string{"issues.opened"},
			Match:       map[string]string{"z": "1", "y": "2", "x": "3", "w": "4", "v": "5", "u": "6"},
		}},
	}}}
	first, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		again, err := MarshalWorkflow(wf)
		require.NoError(t, err)
		require.Equal(t, string(first), string(again))
	}
	// Maps are emitted in key order, so a reader can diff them.
	assert.Less(t, strings.Index(string(first), "a: \"1\""), strings.Index(string(first), "f: \"6\""))
}

// Templates in triggers[].inputs are evaluated against the launch event at
// fire time, never when the workflow definition loads: a run started from a
// plain chat has no event, and must not fail resolving them.
func TestTriggersAreNotWorkflowLoadTemplates(t *testing.T) {
	assert.True(t, IsRuntimeEvaluatedTopLevelKey("triggers"))
}
