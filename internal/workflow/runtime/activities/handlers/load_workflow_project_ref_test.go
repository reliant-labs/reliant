// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #623, app half of bug 1: a run resolves project://X by the workflow's
// name:, exactly as `reliant workflow scenario run` does. The runtime used to
// hand the ref to the slugger with its scheme still on ("project://x" became
// the slug "projectx"), so no project:// ref resolved in a real run at all.
func TestLoadWorkflowActivity_ProjectRefResolvesByName(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	now := time.Now().UTC()
	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Project Refs", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	pipeline := `name: blog-content-pipeline
apiVersion: v2
entry: [draft]
nodes:
  - id: draft
    type: call_llm
    args:
      model: {tags: [fast]}
`
	contentNext := `name: content-next
apiVersion: v2
entry: [draft_blog]
nodes:
  - id: draft_blog
    type: workflow
    ref: project://blog-content-pipeline
`
	// What the daemon syncs: each file's path and content. blog.yaml's file
	// name says nothing about its name:.
	workflowsJSON := mustMarshalJSON(t, []map[string]string{
		{"slug": "blog-content-pipeline", "name": "blog-content-pipeline", "relative_path": ".reliant/workflows/blog.yaml", "yaml_content": pipeline},
		{"slug": "content-next", "name": "content-next", "relative_path": ".reliant/workflows/content-next.yaml", "yaml_content": contentNext},
	})
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: "daemon-" + uuid.NewString(),
		ProjectWorkflowsJSON: &workflowsJSON, PushedAt: now,
	}))

	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, ProjectID: projectID, Title: "test",
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	activity := NewLoadWorkflowActivity(repo)

	t.Run("the ref itself", func(t *testing.T) {
		out, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "project://blog-content-pipeline"})
		require.NoError(t, err)
		wf, err := wfyaml.ParseWorkflow(out.YAML)
		require.NoError(t, err)
		assert.Equal(t, "blog-content-pipeline", wf.GetName())
	})

	t.Run("a workflow whose child is a project ref validates", func(t *testing.T) {
		_, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "content-next"})
		require.NoError(t, err, "content-next's project://blog-content-pipeline child must resolve during tree validation")
	})

	t.Run("the file name is not an address", func(t *testing.T) {
		_, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "project://blog"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `blog.yaml is named "blog-content-pipeline"`,
			"the miss names the file the author probably meant and its real name")
	})
}
