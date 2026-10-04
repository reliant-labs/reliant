// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Trigger persistence is the layer every automated run passes through, and two
// of its guarantees are database constraints rather than Go code:
//
//   - UNIQUE (kind, dedupe_key) on trigger_events is what makes "a chat starts
//     exactly once" and "a retried scheduled fire launches once" true. Only a
//     real Postgres can demonstrate it, so these tests are DB-backed.
//   - The ON DELETE behaviors decide what survives a deletion: a deleted
//     trigger must not erase the record of the runs it started (SET NULL),
//     while a deleted project must take its triggers with it (CASCADE).
//
// Everything else here is round-trip coverage for the jsonb ↔ Go mapping,
// which is the other place a silent wrong answer is possible: a nil map that
// marshals to JSON `null` reads back as nil and makes every consumer
// nil-check a NOT NULL column.

// createTestTriggerProject inserts a project owned by userID, for the
// project-scoped filtering and CASCADE tests. The seeded "test-project"
// belongs to "test-user" and must not be deleted, since the template is shared.
func createTestTriggerProject(t *testing.T, repo *Repo, projectID, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := repo.CreateProject(context.Background(), &Project{
		ID:         projectID,
		Name:       projectID,
		Path:       "/tmp/" + projectID,
		UserID:     userID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("createTestTriggerProject(%s): %v", projectID, err)
	}
}

// newTestTrigger builds a schedule trigger with every jsonb field populated,
// so a round trip exercises the real mapping rather than empty defaults.
func newTestTrigger(id, userID, projectID, name string) *core.Trigger {
	config, _ := json.Marshal(core.ScheduleConfig{
		Cron:          []string{"0 9 * * 1-5"},
		Timezone:      "America/New_York",
		Overlap:       core.ScheduleOverlapSkip,
		CatchupWindow: "10m",
	})
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &core.Trigger{
		ID:        id,
		UserID:    userID,
		ProjectID: projectID,
		Name:      name,
		Kind:      core.TriggerKindSchedule,
		Enabled:   true,
		Workflow:  "nightly-audit",
		Presets:   map[string]string{"": "careful", "Critic": "strict"},
		Params:    map[string]any{"depth": float64(3), "unattended": true, "label": "nightly"},
		Message:   "audit the repo and open issues for what you find",
		DaemonID:  "daemon-" + id,
		Config:    config,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func newTestTriggerEvent(id, triggerID, userID, dedupeKey string, occurredAt time.Time) *core.TriggerEvent {
	var triggerRef *string
	if triggerID != "" {
		triggerRef = &triggerID
	}
	return &core.TriggerEvent{
		ID:         id,
		TriggerID:  triggerRef,
		UserID:     userID,
		Kind:       core.TriggerEventKindSchedule,
		DedupeKey:  dedupeKey,
		OccurredAt: occurredAt.UTC().Truncate(time.Microsecond),
		Payload:    map[string]any{"scheduled_for": occurredAt.UTC().Format(time.RFC3339), "trigger_name": "nightly audit"},
		Outcome:    core.TriggerEventSkipped,
		CreatedAt:  time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestTriggerCRUDRoundTrip(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	want := newTestTrigger("trg-crud", "test-user", "test-project", "nightly audit")
	if err := repo.CreateTrigger(ctx, want); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	got, err := repo.GetTrigger(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}

	if got.UserID != want.UserID || got.ProjectID != want.ProjectID || got.Name != want.Name {
		t.Errorf("identity fields: got (%s, %s, %s), want (%s, %s, %s)",
			got.UserID, got.ProjectID, got.Name, want.UserID, want.ProjectID, want.Name)
	}
	if got.Kind != core.TriggerKindSchedule {
		t.Errorf("Kind: got %q, want %q", got.Kind, core.TriggerKindSchedule)
	}
	if got.DaemonID != want.DaemonID {
		t.Errorf("DaemonID: got %q, want %q", got.DaemonID, want.DaemonID)
	}
	if !got.Enabled {
		t.Error("Enabled: got false, want true")
	}
	if got.Workflow != want.Workflow || got.Message != want.Message {
		t.Errorf("Workflow/Message: got (%q, %q), want (%q, %q)", got.Workflow, got.Message, want.Workflow, want.Message)
	}
	if got.WorktreeID != nil {
		t.Errorf("WorktreeID: got %v, want nil", *got.WorktreeID)
	}

	// The jsonb fields are where a wrong mapping is silent.
	if len(got.Presets) != 2 || got.Presets[""] != "careful" || got.Presets["Critic"] != "strict" {
		t.Errorf("Presets: got %#v, want %#v", got.Presets, want.Presets)
	}
	if got.Params["depth"] != float64(3) || got.Params["unattended"] != true || got.Params["label"] != "nightly" {
		t.Errorf("Params: got %#v, want %#v", got.Params, want.Params)
	}
	var config core.ScheduleConfig
	if err := json.Unmarshal(got.Config, &config); err != nil {
		t.Fatalf("unmarshal round-tripped Config: %v", err)
	}
	if len(config.Cron) != 1 || config.Cron[0] != "0 9 * * 1-5" || config.Timezone != "America/New_York" {
		t.Errorf("Config: got %#v", config)
	}
	if config.Overlap != core.ScheduleOverlapSkip || config.CatchupWindow != "10m" {
		t.Errorf("Config overlap/catchup: got (%q, %q)", config.Overlap, config.CatchupWindow)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps: want both set")
	}

	// Update replaces the definition, including the jsonb fields.
	got.Name = "nightly audit (renamed)"
	got.Workflow = "weekly-audit"
	got.Enabled = false
	got.Presets = map[string]string{"": "fast"}
	got.Params = map[string]any{"depth": float64(1)}
	got.Message = "shorter prompt"
	got.DaemonID = "daemon-replaced"
	got.Config = json.RawMessage(`{"interval":"1h"}`)
	got.UpdatedAt = time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	if err := repo.UpdateTrigger(ctx, got); err != nil {
		t.Fatalf("UpdateTrigger: %v", err)
	}

	updated, err := repo.GetTrigger(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetTrigger after update: %v", err)
	}
	if updated.Name != "nightly audit (renamed)" || updated.Workflow != "weekly-audit" || updated.Enabled {
		t.Errorf("after update: got (%q, %q, enabled=%v)", updated.Name, updated.Workflow, updated.Enabled)
	}
	if updated.DaemonID != "daemon-replaced" {
		t.Errorf("after update DaemonID: got %q, want daemon-replaced", updated.DaemonID)
	}
	if len(updated.Presets) != 1 || updated.Presets[""] != "fast" {
		t.Errorf("after update Presets: got %#v", updated.Presets)
	}
	if len(updated.Params) != 1 || updated.Params["depth"] != float64(1) {
		t.Errorf("after update Params: got %#v", updated.Params)
	}
	if string(updated.Config) != `{"interval": "1h"}` && string(updated.Config) != `{"interval":"1h"}` {
		t.Errorf("after update Config: got %s", updated.Config)
	}

	if err := repo.DeleteTrigger(ctx, want.ID); err != nil {
		t.Fatalf("DeleteTrigger: %v", err)
	}
	if _, err := repo.GetTrigger(ctx, want.ID); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Errorf("GetTrigger after delete: got %v, want ErrTriggerNotFound", err)
	}
}

// A nil Presets/Params map must read back as an empty map, not nil: the
// columns are NOT NULL with a '{}' default, so a JSON `null` would be both a
// lie about the stored value and a nil-check every consumer has to write.
func TestCreateTriggerNilMapsReadBackEmpty(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-nilmaps", "test-user", "test-project", "bare")
	trigger.Presets = nil
	trigger.Params = nil
	trigger.Config = nil
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	got, err := repo.GetTrigger(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if got.Presets == nil || len(got.Presets) != 0 {
		t.Errorf("Presets: got %#v, want empty non-nil map", got.Presets)
	}
	if got.Params == nil || len(got.Params) != 0 {
		t.Errorf("Params: got %#v, want empty non-nil map", got.Params)
	}
	if len(got.Config) == 0 {
		t.Error("Config: want the '{}' default, got empty")
	}
}

func TestUpdateTriggerMissingReturnsNotFound(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	missing := newTestTrigger("trg-missing", "test-user", "test-project", "ghost")
	if err := repo.UpdateTrigger(context.Background(), missing); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Errorf("UpdateTrigger on missing row: got %v, want ErrTriggerNotFound", err)
	}
}

func TestListTriggersFiltersByUserAndProject(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	createTestTriggerProject(t, repo, "proj-other", "test-user")
	createTestTriggerProject(t, repo, "proj-bob", "bob")

	mine := newTestTrigger("trg-mine-a", "test-user", "test-project", "mine a")
	minesOther := newTestTrigger("trg-mine-b", "test-user", "proj-other", "mine b")
	bobs := newTestTrigger("trg-bob", "bob", "proj-bob", "bobs")
	for _, trigger := range []*core.Trigger{mine, minesOther, bobs} {
		if err := repo.CreateTrigger(ctx, trigger); err != nil {
			t.Fatalf("CreateTrigger(%s): %v", trigger.ID, err)
		}
	}

	byUser, err := repo.ListTriggers(ctx, core.TriggerFilters{UserID: "test-user"})
	if err != nil {
		t.Fatalf("ListTriggers(user): %v", err)
	}
	if got := idsOf(byUser); len(got) != 2 || !contains(got, "trg-mine-a") || !contains(got, "trg-mine-b") {
		t.Errorf("ListTriggers(user=test-user): got %v, want the two test-user triggers", got)
	}

	projectID := "proj-other"
	byProject, err := repo.ListTriggers(ctx, core.TriggerFilters{UserID: "test-user", ProjectID: &projectID})
	if err != nil {
		t.Fatalf("ListTriggers(user+project): %v", err)
	}
	if got := idsOf(byProject); len(got) != 1 || got[0] != "trg-mine-b" {
		t.Errorf("ListTriggers(project=proj-other): got %v, want [trg-mine-b]", got)
	}

	// Listing without a user is refused: "empty means everyone" is a footgun.
	if _, err := repo.ListTriggers(ctx, core.TriggerFilters{}); err == nil {
		t.Error("ListTriggers with no user must fail rather than list every tenant's triggers")
	}

	// The schedule syncer's reconciliation lists everyone's through its own
	// query, so no schedule is left unconverged.
	all, err := repo.ListAllTriggers(ctx)
	if err != nil {
		t.Fatalf("ListAllTriggers: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListAllTriggers: got %d triggers, want 3", len(all))
	}
}

func TestSetTriggerEnabled(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-enable", "test-user", "test-project", "toggle me")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	if err := repo.SetTriggerEnabled(ctx, trigger.ID, false); err != nil {
		t.Fatalf("SetTriggerEnabled(false): %v", err)
	}
	got, err := repo.GetTrigger(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if got.Enabled {
		t.Error("after SetTriggerEnabled(false): got enabled=true")
	}

	if err := repo.SetTriggerEnabled(ctx, trigger.ID, true); err != nil {
		t.Fatalf("SetTriggerEnabled(true): %v", err)
	}
	got, err = repo.GetTrigger(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if !got.Enabled {
		t.Error("after SetTriggerEnabled(true): got enabled=false")
	}

	if err := repo.SetTriggerEnabled(ctx, "trg-nope", true); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Errorf("SetTriggerEnabled on missing row: got %v, want ErrTriggerNotFound", err)
	}
}

// The constraint the whole launch design leans on: a second insert for the
// same (kind, dedupe_key) writes nothing and says so, rather than erroring or
// creating a duplicate firing.
func TestCreateTriggerEventDedupes(t *testing.T) {
	repo, cleanup, rawDB := setupTriggerTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-dedupe", "test-user", "test-project", "deduped")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	first := newTestTriggerEvent("ev-dedupe-1", trigger.ID, "test-user", "fire-key-1", time.Now())
	created, err := repo.CreateTriggerEvent(ctx, first)
	if err != nil {
		t.Fatalf("CreateTriggerEvent(first): %v", err)
	}
	if !created {
		t.Fatal("CreateTriggerEvent(first): got created=false, want true")
	}

	// A retried fire: different event id, same (kind, dedupe_key).
	second := newTestTriggerEvent("ev-dedupe-2", trigger.ID, "test-user", "fire-key-1", time.Now())
	created, err = repo.CreateTriggerEvent(ctx, second)
	if err != nil {
		t.Fatalf("CreateTriggerEvent(duplicate): %v", err)
	}
	if created {
		t.Error("CreateTriggerEvent(duplicate): got created=true, want false")
	}

	var count int
	if err := rawDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM trigger_events WHERE kind = $1 AND dedupe_key = $2`,
		string(core.TriggerEventKindSchedule), "fire-key-1").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("rows for (schedule, fire-key-1): got %d, want 1", count)
	}

	// The same dedupe key under a DIFFERENT kind is a different firing — the
	// constraint is on the pair, and chat ids and fire-workflow ids share no
	// namespace.
	otherKind := newTestTriggerEvent("ev-dedupe-3", "", "test-user", "fire-key-1", time.Now())
	otherKind.Kind = core.TriggerEventKindChatStart
	created, err = repo.CreateTriggerEvent(ctx, otherKind)
	if err != nil {
		t.Fatalf("CreateTriggerEvent(other kind): %v", err)
	}
	if !created {
		t.Error("CreateTriggerEvent(other kind, same key): got created=false, want true")
	}
}

func TestGetTriggerEventByDedupe(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-bydedupe", "test-user", "test-project", "by dedupe")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	occurred := time.Now().UTC().Truncate(time.Microsecond)
	event := newTestTriggerEvent("ev-bydedupe", trigger.ID, "test-user", "fire-key-lookup", occurred)
	if _, err := repo.CreateTriggerEvent(ctx, event); err != nil {
		t.Fatalf("CreateTriggerEvent: %v", err)
	}

	got, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, "fire-key-lookup")
	if err != nil {
		t.Fatalf("GetTriggerEventByDedupe: %v", err)
	}
	if got.ID != "ev-bydedupe" {
		t.Errorf("ID: got %q, want ev-bydedupe", got.ID)
	}
	if got.TriggerID == nil || *got.TriggerID != trigger.ID {
		t.Errorf("TriggerID: got %v, want %q", got.TriggerID, trigger.ID)
	}
	if got.Outcome != core.TriggerEventSkipped {
		t.Errorf("Outcome: got %q, want %q", got.Outcome, core.TriggerEventSkipped)
	}
	if !got.OccurredAt.Equal(occurred) {
		t.Errorf("OccurredAt: got %v, want %v", got.OccurredAt, occurred)
	}
	if got.Payload["trigger_name"] != "nightly audit" {
		t.Errorf("Payload: got %#v", got.Payload)
	}
	if got.ChatID != nil {
		t.Errorf("ChatID: got %v, want nil", *got.ChatID)
	}

	_, err = repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, "never-fired")
	if !errors.Is(err, core.ErrTriggerEventNotFound) {
		t.Errorf("GetTriggerEventByDedupe(miss): got %v, want ErrTriggerEventNotFound", err)
	}
}

func TestUpdateTriggerEventOutcome(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-outcome", "test-user", "test-project", "outcome")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	event := newTestTriggerEvent("ev-outcome", trigger.ID, "test-user", "fire-key-outcome", time.Now())
	if _, err := repo.CreateTriggerEvent(ctx, event); err != nil {
		t.Fatalf("CreateTriggerEvent: %v", err)
	}

	chatID := "chat-outcome"
	createActivityTestChat(t, repo, chatID)
	if err := repo.UpdateTriggerEventOutcome(ctx, event.ID, core.TriggerEventLaunched, "", &chatID); err != nil {
		t.Fatalf("UpdateTriggerEventOutcome(launched): %v", err)
	}

	got, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, "fire-key-outcome")
	if err != nil {
		t.Fatalf("GetTriggerEventByDedupe: %v", err)
	}
	if got.Outcome != core.TriggerEventLaunched {
		t.Errorf("Outcome: got %q, want launched", got.Outcome)
	}
	if got.ChatID == nil || *got.ChatID != chatID {
		t.Errorf("ChatID: got %v, want %q", got.ChatID, chatID)
	}

	if err := repo.UpdateTriggerEventOutcome(ctx, event.ID, core.TriggerEventFailed, "workflow not found", nil); err != nil {
		t.Fatalf("UpdateTriggerEventOutcome(failed): %v", err)
	}
	got, err = repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, "fire-key-outcome")
	if err != nil {
		t.Fatalf("GetTriggerEventByDedupe: %v", err)
	}
	if got.Outcome != core.TriggerEventFailed || got.OutcomeDetail != "workflow not found" {
		t.Errorf("after failed: got (%q, %q)", got.Outcome, got.OutcomeDetail)
	}
	if got.ChatID != nil {
		t.Errorf("ChatID: got %v, want nil after a nil chatID write", *got.ChatID)
	}

	if err := repo.UpdateTriggerEventOutcome(ctx, "ev-nope", core.TriggerEventFailed, "x", nil); !errors.Is(err, core.ErrTriggerEventNotFound) {
		t.Errorf("UpdateTriggerEventOutcome on missing row: got %v, want ErrTriggerEventNotFound", err)
	}
}

func TestListTriggerEventsOrderAndLimit(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-list-ev", "test-user", "test-project", "list events")
	other := newTestTrigger("trg-list-ev-other", "test-user", "test-project", "other events")
	for _, tr := range []*core.Trigger{trigger, other} {
		if err := repo.CreateTrigger(ctx, tr); err != nil {
			t.Fatalf("CreateTrigger(%s): %v", tr.ID, err)
		}
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	// Oldest first on insert, so an unsorted read would return the wrong order.
	for i := 0; i < 4; i++ {
		ev := newTestTriggerEvent(
			fmt.Sprintf("ev-list-%d", i), trigger.ID, "test-user",
			fmt.Sprintf("fire-key-list-%d", i), base.Add(time.Duration(i)*time.Minute))
		if _, err := repo.CreateTriggerEvent(ctx, ev); err != nil {
			t.Fatalf("CreateTriggerEvent(%d): %v", i, err)
		}
	}
	// A firing of a different trigger must not appear.
	foreign := newTestTriggerEvent("ev-list-foreign", other.ID, "test-user", "fire-key-foreign", base.Add(time.Hour))
	if _, err := repo.CreateTriggerEvent(ctx, foreign); err != nil {
		t.Fatalf("CreateTriggerEvent(foreign): %v", err)
	}

	all, _, err := repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: "test-user", TriggerID: trigger.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListTriggerEvents: %v", err)
	}
	wantOrder := []string{"ev-list-3", "ev-list-2", "ev-list-1", "ev-list-0"}
	if got := idsOfEvents(all); !equalStrings(got, wantOrder) {
		t.Errorf("ListTriggerEvents order: got %v, want %v (newest first)", got, wantOrder)
	}

	limited, _, err := repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: "test-user", TriggerID: trigger.ID, Limit: 2})
	if err != nil {
		t.Fatalf("ListTriggerEvents(limit=2): %v", err)
	}
	if got := idsOfEvents(limited); !equalStrings(got, []string{"ev-list-3", "ev-list-2"}) {
		t.Errorf("ListTriggerEvents(limit=2): got %v, want the two newest", got)
	}
}

func TestGetLatestTriggerEvent(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-latest", "test-user", "test-project", "latest")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	// Never fired: (nil, nil), not an error. This is the overlap check's first
	// run, and it must not look like a failure.
	latest, err := repo.GetLatestTriggerEvent(ctx, trigger.ID, nil)
	if err != nil {
		t.Fatalf("GetLatestTriggerEvent(no events): %v", err)
	}
	if latest != nil {
		t.Errorf("GetLatestTriggerEvent(no events): got %#v, want nil", latest)
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	launched := newTestTriggerEvent("ev-latest-launched", trigger.ID, "test-user", "fire-key-latest-1", base)
	launched.Outcome = core.TriggerEventLaunched
	skipped := newTestTriggerEvent("ev-latest-skipped", trigger.ID, "test-user", "fire-key-latest-2", base.Add(time.Minute))
	skipped.Outcome = core.TriggerEventSkipped
	for _, ev := range []*core.TriggerEvent{launched, skipped} {
		if _, err := repo.CreateTriggerEvent(ctx, ev); err != nil {
			t.Fatalf("CreateTriggerEvent(%s): %v", ev.ID, err)
		}
	}

	latest, err = repo.GetLatestTriggerEvent(ctx, trigger.ID, nil)
	if err != nil {
		t.Fatalf("GetLatestTriggerEvent(unfiltered): %v", err)
	}
	if latest == nil || latest.ID != "ev-latest-skipped" {
		t.Errorf("GetLatestTriggerEvent(unfiltered): got %v, want ev-latest-skipped", latest)
	}

	// The overlap check asks specifically for the latest LAUNCHED firing: a
	// skipped one started no run, so it says nothing about whether the
	// previous run is still going.
	wantLaunched := core.TriggerEventLaunched
	latest, err = repo.GetLatestTriggerEvent(ctx, trigger.ID, &wantLaunched)
	if err != nil {
		t.Fatalf("GetLatestTriggerEvent(launched): %v", err)
	}
	if latest == nil || latest.ID != "ev-latest-launched" {
		t.Errorf("GetLatestTriggerEvent(launched): got %v, want ev-latest-launched", latest)
	}

	// An outcome that never occurred is also (nil, nil).
	wantFailed := core.TriggerEventFailed
	latest, err = repo.GetLatestTriggerEvent(ctx, trigger.ID, &wantFailed)
	if err != nil {
		t.Fatalf("GetLatestTriggerEvent(failed): %v", err)
	}
	if latest != nil {
		t.Errorf("GetLatestTriggerEvent(failed): got %#v, want nil", latest)
	}
}

// Deleting a trigger must not erase the record of the runs it started: the
// event rows are evidence about chats that still exist, so trigger_id is
// cleared rather than the rows being cascaded away.
func TestDeleteTriggerNullsEventTriggerID(t *testing.T) {
	repo, cleanup, rawDB := setupTriggerTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-ondelete", "test-user", "test-project", "on delete")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	event := newTestTriggerEvent("ev-ondelete", trigger.ID, "test-user", "fire-key-ondelete", time.Now())
	if _, err := repo.CreateTriggerEvent(ctx, event); err != nil {
		t.Fatalf("CreateTriggerEvent: %v", err)
	}

	if err := repo.DeleteTrigger(ctx, trigger.ID); err != nil {
		t.Fatalf("DeleteTrigger: %v", err)
	}

	surviving, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, "fire-key-ondelete")
	if err != nil {
		t.Fatalf("GetTriggerEventByDedupe after trigger delete: %v", err)
	}
	if surviving.TriggerID != nil {
		t.Errorf("TriggerID after trigger delete: got %q, want nil", *surviving.TriggerID)
	}
	// And it is still a complete record of what happened.
	if surviving.UserID != "test-user" || surviving.DedupeKey != "fire-key-ondelete" {
		t.Errorf("surviving event lost detail: %#v", surviving)
	}

	var count int
	if err := rawDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM trigger_events WHERE id = $1`, event.ID).Scan(&count); err != nil {
		t.Fatalf("count surviving event: %v", err)
	}
	if count != 1 {
		t.Errorf("event rows after trigger delete: got %d, want 1", count)
	}
}

// A trigger whose project is gone can only ever fail to fire, so the project
// takes its triggers with it.
func TestDeleteProjectCascadesTriggers(t *testing.T) {
	repo, cleanup, rawDB := setupTriggerTestDB(t)
	defer cleanup()
	ctx := context.Background()

	createTestTriggerProject(t, repo, "proj-cascade", "test-user")
	trigger := newTestTrigger("trg-cascade", "test-user", "proj-cascade", "cascaded")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	if _, err := rawDB.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, "proj-cascade"); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	if _, err := repo.GetTrigger(ctx, trigger.ID); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Errorf("GetTrigger after project delete: got %v, want ErrTriggerNotFound", err)
	}
}

// The launcher writes the event row in the SAME transaction as the chat it
// launches. This pins the half that makes that worth doing: a rolled-back
// transaction leaves no event row, so a failed launch cannot consume its own
// dedupe key and lock out the retry.
func TestCreateTriggerEventRollsBackWithTransaction(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	trigger := newTestTrigger("trg-runtx", "test-user", "test-project", "runtx")
	if err := repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}

	sentinel := errors.New("launch failed after the event row was written")
	err := repo.RunTx(ctx, func(txCtx context.Context) error {
		created, err := repo.CreateTriggerEvent(txCtx, newTestTriggerEvent(
			"ev-runtx", trigger.ID, "test-user", "fire-key-runtx", time.Now()))
		if err != nil {
			return err
		}
		if !created {
			return errors.New("CreateTriggerEvent inside RunTx: got created=false, want true")
		}
		// Prove the write is visible to the rest of the transaction — that is
		// what makes "same transaction as the chat" meaningful.
		if _, err := repo.GetTriggerEventByDedupe(txCtx, core.TriggerEventKindSchedule, "fire-key-runtx"); err != nil {
			return fmt.Errorf("read-own-write inside RunTx: %w", err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunTx: got %v, want the sentinel error", err)
	}

	if _, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, "fire-key-runtx"); !errors.Is(err, core.ErrTriggerEventNotFound) {
		t.Errorf("after rollback: got %v, want ErrTriggerEventNotFound", err)
	}

	// And the dedupe key is still free, so the retry can launch.
	created, err := repo.CreateTriggerEvent(ctx, newTestTriggerEvent(
		"ev-runtx-retry", trigger.ID, "test-user", "fire-key-runtx", time.Now()))
	if err != nil {
		t.Fatalf("CreateTriggerEvent(retry): %v", err)
	}
	if !created {
		t.Error("CreateTriggerEvent(retry after rollback): got created=false, want true")
	}
}

// setupTriggerTestDB returns the repo plus the raw pool, for the tests that
// assert on what is PHYSICALLY in the table — a duplicate insert writing no
// second row, and the ON DELETE behaviors. Those are claims about the
// database, and asking the repo would only re-ask the code under test.
func setupTriggerTestDB(t *testing.T) (*Repo, func(), *sql.DB) {
	t.Helper()
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	return repo, cleanup, rawDB
}

func idsOf(triggers []*core.Trigger) []string {
	ids := make([]string, len(triggers))
	for i, trigger := range triggers {
		ids[i] = trigger.ID
	}
	return ids
}

func idsOfEvents(events []*core.TriggerEventWithRun) []string {
	ids := make([]string, len(events))
	for i, ev := range events {
		ids[i] = ev.Event.ID
	}
	return ids
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestLockTriggerRequiresAnExistingRow(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := repo.RunTx(ctx, func(txCtx context.Context) error {
		return repo.LockTrigger(txCtx, "no-such-trigger")
	}); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Errorf("LockTrigger(missing) = %v, want ErrTriggerNotFound", err)
	}
}
