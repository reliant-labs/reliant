package trigger

import (
	"context"
	"strings"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func TestActivationSnapshotRejectsCycleWithoutUnboundedExpansion(t *testing.T) {
	repo := &MockRepository{}
	repo.ListSettingsByKeyFunc = func(context.Context, string, string) ([]*db.Setting, error) { return nil, nil }
	root, err := wfyaml.ParseWorkflow([]byte("name: cyclic\nentry: [again]\nnodes:\n - {id: again, type: workflow, ref: project://cyclic}\n"))
	require.NoError(t, err)
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}))
	_, err = svc.activationSnapshot(context.Background(), "owner", &pb.TriggerDefinition{Workflow: "project://cyclic"}, root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validation")
}

func TestActivationSnapshotEffectiveInputsWithholdSecretButShowPrivileges(t *testing.T) {
	repo := &MockRepository{}
	repo.ListSettingsByKeyFunc = func(context.Context, string, string) ([]*db.Setting, error) { return nil, nil }
	repo.GetPresetBySlugFunc = func(context.Context, string, string) (*db.Preset, error) {
		return &db.Preset{Name: "operator", Params: map[string]interface{}{"tools": []any{"shell"}, "message": "preset-secret"}}, nil
	}
	root, err := wfyaml.ParseWorkflow([]byte(`name: privilege-test
inputs:
  tools: {type: tools, default: [view]}
  message: {type: string, default: ""}
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: {tags: [fast]}
      tools_config:
        preloaded_tools: "{{inputs.tools}}"
        loadable_tools: []
`))
	require.NoError(t, err)
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}))
	snap, err := svc.activationSnapshot(context.Background(), "owner", &pb.TriggerDefinition{Workflow: "project://privilege-test", Presets: map[string]string{"default": "operator"}}, root)
	require.NoError(t, err)
	require.Equal(t, []any{"shell"}, snap.EffectiveInputs["tools"])
	summary := strings.Join(snap.Summary, "\n")
	require.Contains(t, summary, "shell")
	require.NotContains(t, summary, "preset-secret")
}
