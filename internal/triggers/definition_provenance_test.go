// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const provenanceWorkflow = `name: release
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: release
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

const (
	provenancePath   = "control-plane/.reliant/workflows/release.yaml"
	provenanceCommit = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
)

// provenanceProject is a project whose workflows machine "cicd" synced from
// control-plane at main 1a2b3c4, with a scheduled activation of release's
// nightly on cicd and a webhook trigger running release there.
type provenanceProject struct {
	repo      *db.Repo
	ctx       context.Context
	userID    string
	projectID string
	daemonID  string
	schedule  *core.Trigger
	webhook   *core.Trigger
}

func newProvenanceProject(t *testing.T) *provenanceProject {
	t.Helper()
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	p := &provenanceProject{repo: repo, ctx: ctx, userID: uuid.NewString(), daemonID: uuid.NewString()}
	p.projectID = "p-" + p.userID
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: p.projectID, UserID: p.userID, Name: "proj", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	host := "cicd"
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: p.daemonID, UserID: p.userID, Hostname: &host}))
	p.push(t, 1000, provenanceWorkflow)

	nightly := "nightly"
	cfg, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * *"}})
	p.schedule = &core.Trigger{
		ID: uuid.NewString(), UserID: p.userID, ProjectID: p.projectID, Name: "Nightly release",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "release", WorkflowTrigger: &nightly,
		DaemonID: p.daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: cfg,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateTrigger(ctx, p.schedule))
	p.webhook = &core.Trigger{
		ID: uuid.NewString(), UserID: p.userID, ProjectID: p.projectID, Name: "Release on push",
		Kind: core.TriggerKindWebhook, Enabled: true, Workflow: "release", Message: "release",
		// A filter this event never matches: a broken definition must be
		// refused, not reported as a filter miss.
		Filter:   `trigger.payload.ref == "refs/heads/never"`,
		DaemonID: p.daemonID, Presets: map[string]string{}, Params: map[string]any{}, Config: json.RawMessage(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateTrigger(ctx, p.webhook))
	return p
}

// push syncs release.yaml (as content) from cicd's control-plane checkout.
func (p *provenanceProject) push(t *testing.T, at int64, content string) {
	t.Helper()
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "release", Name: "release", RelativePath: provenancePath, YAMLContent: content}})
	require.NoError(t, err)
	sj, err := config.EncodeCatalogJSON([]config.SyncedRepoState{
		{Repo: "", Branch: "main", Commit: provenanceCommit, DefaultBranch: "main"},
		{Repo: "control-plane", Branch: "main", Commit: provenanceCommit, DefaultBranch: "main"},
	})
	require.NoError(t, err)
	_, err = p.repo.SyncProjectConfigSnapshot(p.ctx, &db.ProjectConfigRecord{
		ProjectID: p.projectID, DaemonID: p.daemonID, ProjectWorkflowsJSON: wj, RepoStatesJSON: sj, PushedAt: time.UnixMilli(at).UTC(),
	}, false)
	require.NoError(t, err)
}

func (p *provenanceProject) workflows() LaunchWorkflows { return LaunchWorkflows{Repo: p.repo} }

func (p *provenanceProject) fire(t *testing.T, launcher *fakeLauncher) (*FireOutput, error) {
	t.Helper()
	return NewFirer(p.repo, launcher).WithWorkflows(p.workflows()).Fire(p.ctx, FireRequest{
		TriggerID: p.schedule.ID, FireWorkflowID: "fire-" + uuid.NewString(), ScheduledAt: time.Now().UTC().Truncate(time.Second),
	})
}

// assertProvenance checks def names the file, machine, branch, commit and
// version that content is.
func (p *provenanceProject) assertProvenance(t *testing.T, def *core.TriggerEventDefinition, content string) {
	t.Helper()
	require.NotNil(t, def, "the firing records which definition it resolved")
	assert.Equal(t, "release", def.Workflow)
	assert.Equal(t, "project", def.Source)
	assert.Equal(t, provenancePath, def.Path)
	assert.Equal(t, p.daemonID, def.DaemonID)
	assert.Equal(t, "cicd", def.Machine)
	assert.Equal(t, "control-plane", def.Repo)
	assert.Equal(t, "main", def.Branch)
	assert.Equal(t, provenanceCommit, def.Commit)
	assert.False(t, def.Dirty)
	assert.Equal(t, config.DefinitionHash([]byte(content)), def.ContentHash)
}

// Every firing records the definition it resolved: which machine's copy of
// which file, at which branch and commit, and which version of it (the
// content hash). It rides on the launched event row and reads back from the
// store and the API.
func TestFiringRecordsTheDefinitionItResolved(t *testing.T) {
	p := newProvenanceProject(t)
	launcher := &fakeLauncher{}
	out, err := p.fire(t, launcher)
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	calls := launcher.snapshot()
	require.Len(t, calls, 1)
	def := calls[0].Event.Definition
	p.assertProvenance(t, def, provenanceWorkflow)
	assert.Empty(t, def.Error)
	require.NotEmpty(t, def.CatalogPin, "the fire pinned the catalog it described")
	assert.Equal(t, def.CatalogPin, calls[0].Spec.CatalogPin, "and launches its run on that snapshot")
	assert.Equal(t, "machine cicd at "+provenancePath+" @ main 1a2b3c4, clean (sha256 "+def.ContentHash[:12]+")", def.Describe())

	// The row the launcher writes from it reads back with it.
	row := &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &p.schedule.ID, UserID: p.userID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: uuid.NewString(), OccurredAt: time.Now().UTC(), Payload: map[string]any{},
		Outcome: core.TriggerEventSkipped, Definition: def,
	}
	created, err := p.repo.CreateTriggerEvent(p.ctx, row)
	require.NoError(t, err)
	require.True(t, created)
	stored, err := p.repo.GetTriggerEventByDedupe(p.ctx, row.Kind, row.DedupeKey)
	require.NoError(t, err)
	assert.Equal(t, def, stored.Definition)
	wire, err := EventToProto(stored)
	require.NoError(t, err)
	assert.Equal(t, provenancePath, wire.GetDefinition().GetPath())
	assert.Equal(t, provenanceCommit, wire.GetDefinition().GetCommit())
	assert.Equal(t, def.ContentHash, wire.GetDefinition().GetContentHash())
	assert.Equal(t, def.Describe(), wire.GetDefinition().GetSummary())
}

// The newest version of the file does not parse, or parses and does not
// validate: the firing is refused and recorded as refused, naming the file,
// the error and the commit. It never launches, never reads as a skip, and
// never runs another version in its place.
func TestFiringAgainstABrokenDefinitionIsRefused(t *testing.T) {
	for name, content := range map[string]string{
		"does not parse":    "name: release\nentry: [a\n",
		"does not validate": strings.Replace(provenanceWorkflow, "entry: [a]", "entry: [b]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvenanceProject(t)
			p.push(t, 2000, content)

			launcher := &fakeLauncher{}
			out, err := p.fire(t, launcher)
			require.Error(t, err, "a refused fire is final, not retried")
			assert.Nil(t, out)
			assert.Empty(t, launcher.snapshot(), "nothing launched: no older version runs in its place")

			failed := core.TriggerEventFailed
			ev, err := p.repo.GetLatestTriggerEvent(p.ctx, p.schedule.ID, &failed)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(ev.OutcomeDetail, "refused: "), ev.OutcomeDetail)
			assert.Contains(t, ev.OutcomeDetail, provenancePath)
			assert.Contains(t, ev.OutcomeDetail, "main 1a2b3c4")
			p.assertProvenance(t, ev.Definition, content)
			assert.True(t, ev.Definition.Broken())
			if name == "does not parse" {
				assert.Contains(t, ev.Definition.Error, "does not parse")
			} else {
				assert.NotContains(t, ev.Definition.Error, "does not parse")
				assert.Contains(t, ev.Definition.Error, "unknown node 'b'")
			}
		})
	}
}

// An inbound event against a broken definition is recorded at intake as
// refused, with the definition, before its filter is read: a broken file
// never reads as "did not match". Its fire, if one is already pending, is
// refused the same way and stamped with what it re-read.
func TestInboundEventAgainstABrokenDefinitionIsRefused(t *testing.T) {
	p := newProvenanceProject(t)
	const broken = "name: release\nentry: [a\n"
	p.push(t, 2000, broken)

	starter := &recordingStarter{}
	intake := NewIntake(p.repo, starter, "").WithWorkflows(p.workflows())
	res, err := intake.Accept(p.ctx, p.webhook, InboundEvent{
		Kind: core.TriggerEventKindWebhook, DedupeKey: p.webhook.ID + ":d1",
		Payload: map[string]any{"ref": "refs/heads/main"},
		Sender:  &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: "token", Verified: true},
	}, AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventFailed, res.Outcome)
	assert.True(t, strings.HasPrefix(res.Detail, "refused: "), res.Detail)
	assert.Empty(t, starter.starts, "a refused event starts no fire")
	ev, err := p.repo.GetTriggerEventByDedupe(p.ctx, core.TriggerEventKindWebhook, p.webhook.ID+":d1")
	require.NoError(t, err)
	p.assertProvenance(t, ev.Definition, broken)

	// An event accepted while the file was valid fires after it broke.
	p.push(t, 3000, provenanceWorkflow)
	pending := &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &p.webhook.ID, UserID: p.userID, Kind: core.TriggerEventKindWebhook,
		DedupeKey: p.webhook.ID + ":d2", OccurredAt: time.Now().UTC(), Payload: map[string]any{},
		Sender:  &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: "token", Verified: true},
		Outcome: core.TriggerEventPending, CreatedAt: time.Now().UTC(),
	}
	created, err := p.repo.CreateTriggerEvent(p.ctx, pending)
	require.NoError(t, err)
	require.True(t, created)
	p.push(t, 4000, broken)
	launcher := &fakeLauncher{}
	_, err = NewEventFirer(p.repo, launcher).WithWorkflows(p.workflows()).Fire(p.ctx, EventFireInput{
		TriggerID: p.webhook.ID, Kind: core.TriggerEventKindWebhook, DedupeKey: pending.DedupeKey,
	})
	require.Error(t, err)
	assert.Empty(t, launcher.snapshot())
	settled, err := p.repo.GetTriggerEventByDedupe(p.ctx, core.TriggerEventKindWebhook, pending.DedupeKey)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventFailed, settled.Outcome)
	assert.True(t, strings.HasPrefix(settled.OutcomeDetail, "refused: "), settled.OutcomeDetail)
	p.assertProvenance(t, settled.Definition, broken)
}
