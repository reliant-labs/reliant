// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// The skill tool reads the project's skills through the worker's config
// provider — cached per config version — not by reading the whole config row
// and re-parsing every skill on each call (18 MB per skill call in prod).
//
// The provider here is the worker's own wiring (serverworker/run.go) over the
// same spy repository, so its one fill per config version is counted too:
// three skill calls make one whole-row read in all, not three.
func TestSkillToolReadsSkillsThroughTheConfigProvider(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()

	userID := "user-" + uuid.NewString()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), userID)
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, userID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	skills, err := json.Marshal([]cfgpkg.StoredSkill{
		{SkillPath: "house-deploy", Name: "house-deploy", Description: "How we deploy", Scope: "project", Body: "Deploy with the house script."},
		{SkillPath: "padding", Name: "padding", Description: "Row weight", Scope: "project", Body: strings.Repeat("x", 64<<10)},
	})
	require.NoError(t, err)
	skillsJSON := string(skills)
	require.NoError(t, h.Repo().UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: project.ID, DaemonID: "daemon-1", ProjectSkillsJSON: &skillsJSON,
	}))

	repo, ok := h.Repo().(*db.Repo)
	require.True(t, ok, "the helper's repository is %T", h.Repo())
	spy := &fullConfigRowReads{Repository: repo}
	provider := cfgpkg.NewCachedStoredConfigProvider(configadapter.NewRepoConfigStore(spy), repo, cfgpkg.DefaultParsedConfigCacheBytes)
	activity := NewExecuteToolsActivity(spy, serverToolExecutor(spy)).WithConfigProvider(provider)

	for i := 0; i < 3; i++ {
		callID := fmt.Sprintf("call-skill-%d", i)
		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID: chat.ID, Thread: chat.ID,
			ToolCalls: []message.ToolCall{{ID: callID, Name: tools.ToolSkill, Input: `{"action":"load","path":"house-deploy"}`}},
		}, &output))
		results := resultsByID(output.GetToolResults())
		require.Contains(t, results, callID)
		require.False(t, results[callID].GetIsError(), results[callID].GetContent())
		require.Contains(t, results[callID].GetContent(), "Deploy with the house script.",
			"the skill tool must load the project skill the config provider holds")
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.Len(t, spy.callers, 1,
		"three skill calls must cost one whole-row read (the provider's fill for this config version), not one per call: %v", spy.callers)
}
