// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A broken version of a workflow file is ONE inbox item, however many
// automations run it and however often they fire: it names the file, the
// error and the branch and commit, lists the automations it refuses, and
// replaces their own broken, failing and could-not-start items. A different
// broken version is a new item; a valid version resolves it.
func TestListInbox_OneItemPerBrokenDefinitionVersion(t *testing.T) {
	f := newInboxFixture(t)
	cicdID := uuid.NewString()
	host := "cicd"
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: cicdID, UserID: f.userID, Hostname: &host}))
	const (
		path   = "control-plane/.reliant/workflows/release.yaml"
		commit = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	)
	push := func(at int64, branch, content string) {
		t.Helper()
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "release", Name: "release", RelativePath: path, YAMLContent: content}})
		require.NoError(t, err)
		sj, err := config.EncodeCatalogJSON([]config.SyncedRepoState{{Repo: "control-plane", Branch: branch, Commit: commit, DefaultBranch: "main"}})
		require.NoError(t, err)
		_, err = f.repo.SyncProjectConfigSnapshot(f.ctx, &db.ProjectConfigRecord{
			ProjectID: f.project(), DaemonID: cicdID, ProjectWorkflowsJSON: wj, RepoStatesJSON: sj, PushedAt: time.UnixMilli(at).UTC(),
		}, false)
		require.NoError(t, err)
	}
	push(1000, "main", releaseWorkflow)

	now := time.Now().UTC().Truncate(time.Microsecond)
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	create := func(id string, declared bool) {
		t.Helper()
		tr := &core.Trigger{
			ID: id, UserID: f.userID, ProjectID: f.project(), Name: "Release " + id,
			Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "release",
			DaemonID: cicdID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
			CreatedAt: now, UpdatedAt: now,
		}
		if declared {
			name := "merged"
			tr.WorkflowTrigger = &name
		}
		require.NoError(t, f.repo.CreateTrigger(f.ctx, tr))
	}
	create("act-1", true)
	create("act-2", true)
	create("hand-made", false)
	require.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN), "nothing is broken yet")

	const broken = "name: release\nentry: [a\n"
	push(2000, "feat/x", broken)
	// Firings refused against it: they must not add items of their own.
	base := now.Add(-time.Hour)
	for i, id := range []string{"act-1", "act-1", "act-2", "hand-made"} {
		f.firing(t, uuid.NewString(), id, core.TriggerEventFailed, "refused: broken", base.Add(time.Duration(i)*time.Minute))
	}

	items := f.list(t, nil).Items
	defs := inboxByKind(items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN)
	require.Len(t, defs, 1, "one item for the one broken version")
	item := defs[0]
	assert.True(t, strings.HasPrefix(item.ItemId, "automation_definition_broken:"), item.ItemId)
	assert.Equal(t, f.project(), item.ProjectId)
	assert.Equal(t, "release", item.WorkflowName)
	payload := item.GetAutomationDefinitionBroken()
	def := payload.GetDefinition()
	assert.Equal(t, path, def.GetPath())
	assert.Equal(t, "cicd", def.GetDaemonName())
	assert.Equal(t, "feat/x", def.GetBranch())
	assert.Equal(t, commit, def.GetCommit())
	assert.Equal(t, config.DefinitionHash([]byte(broken)), def.GetContentHash())
	assert.Contains(t, def.GetError(), "does not parse")
	var refused []string
	for _, a := range payload.GetAutomations() {
		refused = append(refused, a.GetTriggerId())
	}
	assert.ElementsMatch(t, []string{"act-1", "act-2", "hand-made"}, refused)
	for _, it := range items {
		switch it.TriggerId {
		case "act-1", "act-2", "hand-made":
			t.Errorf("the broken definition replaces %s's own item %s", it.TriggerId, it.ItemId)
		}
	}

	// Another read, and more refused firings: still the same one item.
	f.firing(t, uuid.NewString(), "act-2", core.TriggerEventFailed, "refused: broken", base.Add(10*time.Minute))
	again := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN)
	require.Len(t, again, 1)
	assert.Equal(t, item.ItemId, again[0].ItemId)

	// Dismissed, it stays dismissed for this version; a different broken
	// version is news again.
	f.dismiss(t, item.ItemId)
	require.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN))
	invalid := strings.Replace(releaseWorkflow, "entry: [a]", "entry: [b]", 1)
	push(3000, "feat/x", invalid)
	next := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN)
	require.Len(t, next, 1, "a version that parses but does not validate is broken too")
	assert.NotEqual(t, item.ItemId, next[0].ItemId)
	assert.Equal(t, config.DefinitionHash([]byte(invalid)), next[0].GetAutomationDefinitionBroken().GetDefinition().GetContentHash())

	// A valid version lands: the item resolves.
	push(4000, "main", releaseWorkflow)
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN))
}
