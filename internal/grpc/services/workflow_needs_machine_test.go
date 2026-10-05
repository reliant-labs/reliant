// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// The Automations form offers "No machine (server only)" only for a workflow
// that can run without one, so every listed workflow says why it cannot.
func TestListWorkflows_ReportsWhatNeedsAMachine(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	now := time.Now().UTC()
	create := func(slug, def string) {
		require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: "test-user", Name: slug, Slug: slug, Definition: def,
			Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
		}))
	}
	create("digest", `
name: digest
entry: [research]
nodes:
  - id: research
    type: call_llm
    args:
      model: {id: mock}
      tools_config:
        preloaded_tools: ["tag:web"]
`)
	create("build", `
name: build
entry: [compile]
nodes:
  - id: compile
    type: run
    args: {command: make}
`)

	// include_hidden: the fixtures are minimal and need not pass the chat-safe
	// listing's validation to carry a needs_machine verdict.
	resp, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: projectID, IncludeHidden: true}))
	require.NoError(t, err)
	byName := map[string]*reliantv1.WorkflowListItem{}
	for _, wf := range resp.Msg.GetWorkflows() {
		byName[wf.GetName()] = wf
	}

	require.Contains(t, byName, "digest")
	assert.Empty(t, byName["digest"].GetNeedsMachine(), "a web-only workflow runs without a machine")

	require.Contains(t, byName, "build")
	require.NotEmpty(t, byName["build"].GetNeedsMachine())
	assert.Contains(t, byName["build"].GetNeedsMachine()[0], "shell command")

	// The builtin agent at its default tools (tag:coding:default) reaches the
	// shell and the file tools.
	require.Contains(t, byName, "builtin://agent")
	assert.NotEmpty(t, byName["builtin://agent"].GetNeedsMachine())
}
