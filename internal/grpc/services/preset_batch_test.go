// Copyright (c) 2025 Reliant Labs
package services

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// setUserDefaultPresets writes the user-override setting that
// resolveDefaultPresets merges on top of the workflow's YAML defaults.
func setUserDefaultPresets(t *testing.T, service *PresetService, userID, workflowName string, defaults map[string]string) {
	t.Helper()
	blob, err := json.Marshal(defaults)
	if err != nil {
		t.Fatalf("failed to marshal defaults: %v", err)
	}
	ctx := createTestContext(userID)
	if err := service.upsertSetting(ctx, userID, "preset.defaults."+workflowName, string(blob)); err != nil {
		t.Fatalf("failed to seed default presets: %v", err)
	}
}

// The batch RPC must agree with the single RPC for every workflow. If these
// ever diverge the batch silently serves different presets than the UI expects.
func TestPresetService_GetDefaultPresetsBatch_MatchesSingleRPC(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)
	ctx := createTestContext(userID)

	workflows := []string{
		"builtin://get-it-right",
		"builtin://agent",
	}

	setUserDefaultPresets(t, service, userID, workflows[0], map[string]string{
		"":         "general",
		"Proposer": "researcher",
	})

	batchResp, err := service.GetDefaultPresetsBatch(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetsBatchRequest{
		ProjectId:     projectID,
		WorkflowNames: workflows,
	}))
	if err != nil {
		t.Fatalf("GetDefaultPresetsBatch failed: %v", err)
	}

	for _, workflowName := range workflows {
		singleResp, err := service.GetDefaultPreset(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetRequest{
			ProjectId:    projectID,
			WorkflowName: workflowName,
		}))
		if err != nil {
			t.Fatalf("GetDefaultPreset(%s) failed: %v", workflowName, err)
		}

		want := singleResp.Msg.Presets
		var got map[string]string
		if entry, ok := batchResp.Msg.PresetsByWorkflow[workflowName]; ok {
			got = entry.Presets
		}

		if len(want) == 0 {
			// A workflow with no defaults is OMITTED from the batch response;
			// the caller reads that as the same empty map the single RPC gives.
			if len(got) != 0 {
				t.Errorf("%s: batch returned %v, single RPC returned no defaults", workflowName, got)
			}
			continue
		}

		if len(got) != len(want) {
			t.Fatalf("%s: batch returned %v, want %v", workflowName, got, want)
		}
		for group, preset := range want {
			if got[group] != preset {
				t.Errorf("%s: group %q = %q, want %q", workflowName, group, got[group], preset)
			}
		}
	}
}

func TestPresetService_GetDefaultPresetsBatch_AppliesUserOverrides(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)
	ctx := createTestContext(userID)

	const workflowName = "builtin://get-it-right"
	setUserDefaultPresets(t, service, userID, workflowName, map[string]string{
		"": "my-override",
	})

	resp, err := service.GetDefaultPresetsBatch(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetsBatchRequest{
		ProjectId:     projectID,
		WorkflowNames: []string{workflowName},
	}))
	if err != nil {
		t.Fatalf("GetDefaultPresetsBatch failed: %v", err)
	}

	entry, ok := resp.Msg.PresetsByWorkflow[workflowName]
	if !ok {
		t.Fatalf("workflow %q missing from batch response", workflowName)
	}
	// The batch reads overrides with one ListSettingsByKey rather than a
	// GetSetting per workflow; the merged result must be unchanged.
	if entry.Presets[""] != "my-override" {
		t.Errorf(`top-level default = %q, want "my-override"`, entry.Presets[""])
	}
}

func TestPresetService_GetDefaultPresetsBatch_UnknownWorkflowIsOmittedNotAnError(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)
	ctx := createTestContext(userID)

	resp, err := service.GetDefaultPresetsBatch(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetsBatchRequest{
		ProjectId:     projectID,
		WorkflowNames: []string{"builtin://does-not-exist"},
	}))
	// Graceful degradation matches GetDefaultPreset: one bad name in a batch
	// must not fail the whole request for every other workflow on screen.
	if err != nil {
		t.Fatalf("expected graceful handling of unknown workflow, got error: %v", err)
	}
	if _, ok := resp.Msg.PresetsByWorkflow["builtin://does-not-exist"]; ok {
		t.Error("unknown workflow should be omitted from the response")
	}
}

func TestPresetService_GetDefaultPresetsBatch_DeduplicatesWorkflowNames(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)
	ctx := createTestContext(userID)

	const workflowName = "builtin://get-it-right"
	setUserDefaultPresets(t, service, userID, workflowName, map[string]string{"": "general"})

	resp, err := service.GetDefaultPresetsBatch(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetsBatchRequest{
		ProjectId:     projectID,
		WorkflowNames: []string{workflowName, workflowName, workflowName},
	}))
	if err != nil {
		t.Fatalf("GetDefaultPresetsBatch failed: %v", err)
	}
	if len(resp.Msg.PresetsByWorkflow) != 1 {
		t.Errorf("expected 1 entry for a repeated name, got %d", len(resp.Msg.PresetsByWorkflow))
	}
}

func TestPresetService_GetDefaultPresetsBatch_RequiresProjectID(t *testing.T) {
	service, _, userID, _ := setupTestPresetService(t)
	ctx := createTestContext(userID)

	_, err := service.GetDefaultPresetsBatch(ctx, connect.NewRequest(&reliantv1.GetDefaultPresetsBatchRequest{
		WorkflowNames: []string{"builtin://get-it-right"},
	}))
	if err == nil {
		t.Fatal("expected an error when project_id is missing")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", connect.CodeOf(err))
	}
}
