// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// seedRun writes a chat whose root workflow has the given status, for the
// firing reads that join the run a firing launched.
func seedRun(t *testing.T, env *storetest.Env, chatID string, status core.WorkflowStatus) {
	t.Helper()
	ctx := context.Background()
	if err := env.Repo.CreateChat(ctx, &db.Chat{ID: chatID, Title: "run " + chatID, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID, WorkflowID: &chatID}); err != nil {
		t.Fatalf("CreateChat(%s): %v", chatID, err)
	}
	if err := env.Repo.CreateWorkflow(ctx, &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: "wf", Thread: chatID, Status: status}); err != nil {
		t.Fatalf("CreateWorkflow(%s): %v", chatID, err)
	}
}

func newStoreTriggerEvent(id, triggerID string, outcome core.TriggerEventOutcome, at time.Time) *core.TriggerEvent {
	return &core.TriggerEvent{
		ID: id, TriggerID: &triggerID, UserID: storetest.SeedUserID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: "dd-" + id, OccurredAt: at, Payload: map[string]any{"n": float64(1)},
		Outcome: outcome, CreatedAt: at,
	}
}

// TestTriggerEventQueries ports the trigger_events queries of triggers.sql,
// interleaved with the Repository.
func TestTriggerEventQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := store.InsertTrigger(ctx, env.ORM, newStoreTrigger("trg-ev", "ev", base)); err != nil {
		t.Fatalf("InsertTrigger: %v", err)
	}

	// Insert-if-absent, from either layer; the first row for a key wins.
	first := newStoreTriggerEvent("ev-1", "trg-ev", core.TriggerEventSkipped, base)
	first.OutcomeDetail = "overlap"
	first.Sender = &core.TriggerSender{Kind: core.TriggerSenderKind("user"), ID: "u-1", DisplayName: "Ada", Verified: true}
	first.Origin = &core.TriggerOrigin{CausedByReliant: true, Confidence: core.OriginConfidence("exact"), RunID: "chat-x"}
	created, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, first)
	if err != nil || !created {
		t.Fatalf("InsertTriggerEventIfAbsent = %v, %v; want created", created, err)
	}
	dup := newStoreTriggerEvent("ev-dup", "trg-ev", core.TriggerEventLaunched, base)
	dup.DedupeKey = first.DedupeKey
	if created, err := env.Repo.CreateTriggerEvent(ctx, dup); err != nil || created {
		t.Fatalf("Repo.CreateTriggerEvent(duplicate) = %v, %v; want not created", created, err)
	}
	if created, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, dup); err != nil || created {
		t.Fatalf("InsertTriggerEventIfAbsent(duplicate) = %v, %v; want not created", created, err)
	}
	second := newStoreTriggerEvent("ev-2", "trg-ev", core.TriggerEventFailed, base.Add(time.Minute))
	second.Payload = nil
	if created, err := env.Repo.CreateTriggerEvent(ctx, second); err != nil || !created {
		t.Fatalf("Repo.CreateTriggerEvent = %v, %v", created, err)
	}
	for _, key := range []string{first.DedupeKey, second.DedupeKey} {
		port, err := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindSchedule, key)
		if err != nil {
			t.Fatalf("GetTriggerEventByDedupe(%s): %v", key, err)
		}
		twin, err := env.Repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, key)
		if err != nil {
			t.Fatalf("Repo.GetTriggerEventByDedupe(%s): %v", key, err)
		}
		sameValue(t, "GetTriggerEventByDedupe", port, twin)
	}
	got, _ := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindSchedule, first.DedupeKey)
	if got.ID != "ev-1" || got.Outcome != core.TriggerEventSkipped || got.OutcomeDetail != "overlap" || got.Sender == nil || got.Sender.DisplayName != "Ada" || got.Origin == nil || got.Origin.RunID != "chat-x" || !got.OccurredAt.Equal(base) || got.RunStatus != "" {
		t.Fatalf("GetTriggerEventByDedupe(ev-1) = %+v", got)
	}
	got, _ = store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindSchedule, second.DedupeKey)
	if got.Sender != nil || got.Origin != nil || got.Payload == nil || len(got.Payload) != 0 {
		t.Fatalf("GetTriggerEventByDedupe(ev-2) = %+v, want NULL sender/origin and an empty payload", got)
	}
	if _, err := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindWebhook, first.DedupeKey); !errors.Is(err, core.ErrTriggerEventNotFound) {
		t.Fatalf("GetTriggerEventByDedupe(other kind) err = %v, want ErrTriggerEventNotFound", err)
	}

	// The events of one chat: the launch is the earliest.
	seedRun(t, env, "chat-ev", core.Active())
	launch := newStoreTriggerEvent("ev-launch", "trg-ev", core.TriggerEventLaunched, base.Add(2*time.Minute))
	launch.Kind, launch.ChatID = core.TriggerEventKindAgentStartRun, strp("chat-ev")
	replay := newStoreTriggerEvent("ev-replay", "trg-ev", core.TriggerEventLaunched, base.Add(3*time.Minute))
	replay.ChatID = strp("chat-ev")
	for _, ev := range []*core.TriggerEvent{launch, replay} {
		if _, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, ev); err != nil {
			t.Fatalf("InsertTriggerEventIfAbsent(%s): %v", ev.ID, err)
		}
	}
	byChat, err := store.GetTriggerEventByChatID(ctx, env.ORM, "chat-ev")
	if err != nil {
		t.Fatalf("GetTriggerEventByChatID: %v", err)
	}
	twinByChat, err := env.Repo.GetTriggerEventByChatID(ctx, "chat-ev")
	if err != nil {
		t.Fatalf("Repo.GetTriggerEventByChatID: %v", err)
	}
	sameValue(t, "GetTriggerEventByChatID", byChat, twinByChat)
	if byChat.ID != "ev-launch" {
		t.Fatalf("GetTriggerEventByChatID = %s, want the earliest, ev-launch", byChat.ID)
	}
	for _, kind := range []core.TriggerEventKind{core.TriggerEventKindAgentStartRun, core.TriggerEventKindSchedule} {
		port, err := store.GetTriggerEventByChat(ctx, env.ORM, kind, "chat-ev")
		if err != nil {
			t.Fatalf("GetTriggerEventByChat(%s): %v", kind, err)
		}
		twin, err := env.Repo.GetTriggerEventByChat(ctx, kind, "chat-ev")
		if err != nil {
			t.Fatalf("Repo.GetTriggerEventByChat(%s): %v", kind, err)
		}
		sameValue(t, "GetTriggerEventByChat", port, twin)
	}
	for _, read := range []func() error{
		func() error { _, err := store.GetTriggerEventByChatID(ctx, env.ORM, "chat-none"); return err },
		func() error {
			_, err := store.GetTriggerEventByChat(ctx, env.ORM, core.TriggerEventKindWebhook, "chat-ev")
			return err
		},
	} {
		if err := read(); !errors.Is(err, core.ErrTriggerEventNotFound) {
			t.Fatalf("event of a chat with none err = %v, want ErrTriggerEventNotFound", err)
		}
	}

	// Live launched runs: the active agent-started chat, and nothing else.
	seedRun(t, env, "chat-done", core.Completed())
	done := newStoreTriggerEvent("ev-done", "trg-ev", core.TriggerEventLaunched, base.Add(4*time.Minute))
	done.Kind, done.ChatID = core.TriggerEventKindAgentStartRun, strp("chat-done")
	if _, err := env.Repo.CreateTriggerEvent(ctx, done); err != nil {
		t.Fatalf("Repo.CreateTriggerEvent(done): %v", err)
	}
	for _, user := range []string{storetest.SeedUserID, "nobody"} {
		port, err := store.CountLiveLaunchedRuns(ctx, env.ORM, user, core.TriggerEventKindAgentStartRun)
		if err != nil {
			t.Fatalf("CountLiveLaunchedRuns(%s): %v", user, err)
		}
		twin, err := env.Repo.CountLiveLaunchedRuns(ctx, user, core.TriggerEventKindAgentStartRun)
		if err != nil {
			t.Fatalf("Repo.CountLiveLaunchedRuns(%s): %v", user, err)
		}
		want := 0
		if user == storetest.SeedUserID {
			want = 1
		}
		if port != want || twin != want {
			t.Fatalf("CountLiveLaunchedRuns(%s): port %d, twin %d, want %d", user, port, twin, want)
		}
	}

	// Outcome and payload rewrites.
	if err := store.UpdateTriggerEventOutcome(ctx, env.ORM, "ev-2", core.TriggerEventLaunched, "", strp("chat-done")); err != nil {
		t.Fatalf("UpdateTriggerEventOutcome: %v", err)
	}
	twin, _ := env.Repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, second.DedupeKey)
	if twin.Outcome != core.TriggerEventLaunched || twin.ChatID == nil || *twin.ChatID != "chat-done" || twin.Kind != core.TriggerEventKindSchedule {
		t.Fatalf("after UpdateTriggerEventOutcome = %+v", twin)
	}
	if err := env.Repo.UpdateTriggerEventOutcome(ctx, "ev-2", core.TriggerEventFailed, "boom", nil); err != nil {
		t.Fatalf("Repo.UpdateTriggerEventOutcome: %v", err)
	}
	if got, _ := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindSchedule, second.DedupeKey); got.Outcome != core.TriggerEventFailed || got.OutcomeDetail != "boom" || got.ChatID != nil {
		t.Fatalf("after Repo.UpdateTriggerEventOutcome(nil chat) = %+v, want the chat cleared", got)
	}
	if err := store.UpdateTriggerEventPayload(ctx, env.ORM, "ev-2", map[string]any{"replayed": true}); err != nil {
		t.Fatalf("UpdateTriggerEventPayload: %v", err)
	}
	if twin, _ := env.Repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, second.DedupeKey); twin.Payload["replayed"] != true || twin.OutcomeDetail != "boom" {
		t.Fatalf("after UpdateTriggerEventPayload = %+v", twin)
	}
	for _, write := range []func() error{
		func() error {
			return store.UpdateTriggerEventOutcome(ctx, env.ORM, "missing", core.TriggerEventFailed, "", nil)
		},
		func() error { return store.UpdateTriggerEventPayload(ctx, env.ORM, "missing", nil) },
	} {
		if err := write(); !errors.Is(err, core.ErrTriggerEventNotFound) {
			t.Fatalf("rewrite of a missing event err = %v, want ErrTriggerEventNotFound", err)
		}
	}

	// The run status lands on the chat's launch event only.
	if err := store.SetLaunchEventRunStatus(ctx, env.ORM, "chat-ev", core.TriggerRunCompleted); err != nil {
		t.Fatalf("SetLaunchEventRunStatus: %v", err)
	}
	if err := env.Repo.SetLaunchEventRunStatus(ctx, "chat-none", core.TriggerRunFailed); err != nil {
		t.Fatalf("Repo.SetLaunchEventRunStatus(no launch event): %v", err)
	}
	if err := store.SetLaunchEventRunStatus(ctx, env.ORM, "chat-none", core.TriggerRunFailed); err != nil {
		t.Fatalf("SetLaunchEventRunStatus(no launch event): %v", err)
	}
	if l, _ := env.Repo.GetTriggerEventByChatID(ctx, "chat-ev"); l.RunStatus != core.TriggerRunCompleted {
		t.Fatalf("launch run status = %q, want completed", l.RunStatus)
	}
	if r, _ := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindSchedule, replay.DedupeKey); r.RunStatus != "" {
		t.Fatalf("replay run status = %q, want untouched", r.RunStatus)
	}

	// Latest event, of any outcome and of one.
	for _, outcome := range []*core.TriggerEventOutcome{nil, ptrOutcome(core.TriggerEventSkipped), ptrOutcome(core.TriggerEventPending)} {
		port, err := store.GetLatestTriggerEvent(ctx, env.ORM, "trg-ev", outcome)
		if err != nil {
			t.Fatalf("GetLatestTriggerEvent(%v): %v", outcome, err)
		}
		twin, err := env.Repo.GetLatestTriggerEvent(ctx, "trg-ev", outcome)
		if err != nil {
			t.Fatalf("Repo.GetLatestTriggerEvent(%v): %v", outcome, err)
		}
		sameValue(t, "GetLatestTriggerEvent", port, twin)
	}
	if latest, _ := store.GetLatestTriggerEvent(ctx, env.ORM, "trg-ev", nil); latest == nil || latest.ID != "ev-done" {
		t.Fatalf("GetLatestTriggerEvent(any) = %+v, want ev-done", latest)
	}
	if none, err := store.GetLatestTriggerEvent(ctx, env.ORM, "trg-none", nil); none != nil || err != nil {
		t.Fatalf("GetLatestTriggerEvent(no events) = %v, %v; want (nil, nil)", none, err)
	}

	// Pending inbound events: listed when stale, claimed once, settled once.
	for i, at := range []time.Time{base.Add(-3 * time.Hour), base.Add(-2 * time.Hour), base} {
		ev := newStoreTriggerEvent(fmt.Sprintf("ev-pending-%d", i), "trg-ev", core.TriggerEventPending, at)
		ev.Kind = core.TriggerEventKindWebhook
		if _, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, ev); err != nil {
			t.Fatalf("InsertTriggerEventIfAbsent(pending %d): %v", i, err)
		}
	}
	cutoff := base.Add(-time.Hour)
	for _, limit := range []int{0, 1} {
		port, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, cutoff, limit)
		if err != nil {
			t.Fatalf("ListStalePendingTriggerEvents(%d): %v", limit, err)
		}
		twin, err := env.Repo.ListStalePendingTriggerEvents(ctx, cutoff, limit)
		if err != nil {
			t.Fatalf("Repo.ListStalePendingTriggerEvents(%d): %v", limit, err)
		}
		sameValue(t, "ListStalePendingTriggerEvents", port, twin)
	}
	if stale, _ := store.ListStalePendingTriggerEvents(ctx, env.ORM, cutoff, 0); len(stale) != 2 || stale[0].ID != "ev-pending-0" || stale[1].ID != "ev-pending-1" {
		t.Fatalf("ListStalePendingTriggerEvents = %+v, want the two stale ones, oldest first", stale)
	}
	if claimed, err := store.ClaimPendingTriggerEvent(ctx, env.ORM, "ev-pending-0", map[string]any{"start": "x"}, nil); err != nil || !claimed {
		t.Fatalf("ClaimPendingTriggerEvent = %v, %v; want claimed", claimed, err)
	}
	if claimed, err := env.Repo.ClaimPendingTriggerEvent(ctx, "ev-pending-0", nil, nil); err != nil || claimed {
		t.Fatalf("Repo.ClaimPendingTriggerEvent(claimed) = %v, %v; want the loser", claimed, err)
	}
	if settled, err := store.SettlePendingTriggerEvent(ctx, env.ORM, "ev-pending-0", core.TriggerEventSkipped, "late", nil); err != nil || settled {
		t.Fatalf("SettlePendingTriggerEvent(launched) = %v, %v; want no overwrite", settled, err)
	}
	if c, _ := env.Repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindWebhook, "dd-ev-pending-0"); c.Outcome != core.TriggerEventLaunched || c.OutcomeDetail != "" || c.Payload["start"] != "x" {
		t.Fatalf("claimed event = %+v", c)
	}
	if settled, err := store.SettlePendingTriggerEvent(ctx, env.ORM, "ev-pending-1", core.TriggerEventSkipped, "filtered", nil); err != nil || !settled {
		t.Fatalf("SettlePendingTriggerEvent = %v, %v; want settled", settled, err)
	}
	if settled, err := env.Repo.SettlePendingTriggerEvent(ctx, "ev-pending-1", core.TriggerEventFailed, "again", nil); err != nil || settled {
		t.Fatalf("Repo.SettlePendingTriggerEvent(settled) = %v, %v; want not applied", settled, err)
	}
	if claimed, err := env.Repo.ClaimPendingTriggerEvent(ctx, "ev-pending-2", map[string]any{"start": "y"}, nil); err != nil || !claimed {
		t.Fatalf("Repo.ClaimPendingTriggerEvent = %v, %v", claimed, err)
	}
	if c, _ := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindWebhook, "dd-ev-pending-2"); c.Outcome != core.TriggerEventLaunched || c.Payload["start"] != "y" {
		t.Fatalf("Repo-claimed event = %+v", c)
	}
}

func ptrOutcome(o core.TriggerEventOutcome) *core.TriggerEventOutcome { return &o }

// TestTriggerFiringQueries ports ListTriggerEvents, ListRecentTriggerFirings
// and ListFiringsSinceLastSuccess, interleaved with the Repository.
func TestTriggerFiringQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Hour)
	for _, id := range []string{"trg-f1", "trg-f2", "trg-f3"} {
		if err := store.InsertTrigger(ctx, env.ORM, newStoreTrigger(id, id, base)); err != nil {
			t.Fatalf("InsertTrigger(%s): %v", id, err)
		}
	}
	// trg-f1: a failure, a completed run, then failures — two sharing an
	// instant — and a run still going. trg-f2: one firing of another user.
	seedRun(t, env, "chat-f-ok", core.Completed())
	seedRun(t, env, "chat-f-live", core.Active())
	write := func(ev *core.TriggerEvent) {
		t.Helper()
		if _, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, ev); err != nil {
			t.Fatalf("InsertTriggerEventIfAbsent(%s): %v", ev.ID, err)
		}
	}
	write(newStoreTriggerEvent("f1-old-fail", "trg-f1", core.TriggerEventFailed, base))
	ok := newStoreTriggerEvent("f1-ok", "trg-f1", core.TriggerEventLaunched, base.Add(time.Minute))
	ok.ChatID = strp("chat-f-ok")
	write(ok)
	if err := store.SetLaunchEventRunStatus(ctx, env.ORM, "chat-f-ok", core.TriggerRunCompleted); err != nil {
		t.Fatalf("SetLaunchEventRunStatus: %v", err)
	}
	tied := base.Add(2 * time.Minute)
	write(newStoreTriggerEvent("f1-fail-a", "trg-f1", core.TriggerEventFailed, tied))
	write(newStoreTriggerEvent("f1-fail-b", "trg-f1", core.TriggerEventSkipped, tied))
	live := newStoreTriggerEvent("f1-live", "trg-f1", core.TriggerEventLaunched, base.Add(3*time.Minute))
	live.ChatID = strp("chat-f-live")
	write(live)
	foreign := newStoreTriggerEvent("f2-foreign", "trg-f2", core.TriggerEventFailed, base)
	foreign.UserID = "someone-else"
	write(foreign)

	pageIDs := func(items []*core.TriggerEventWithRun) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.Event.ID
		}
		return out
	}
	var seen []string
	var cursor *core.TriggerEventCursor
	for page := 0; page < 10; page++ {
		f := core.TriggerEventFilters{UserID: storetest.SeedUserID, TriggerID: "trg-f1", Limit: 2, After: cursor}
		port, more, err := store.ListTriggerEventsPage(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("ListTriggerEventsPage: %v", err)
		}
		twin, twinMore, err := env.Repo.ListTriggerEvents(ctx, f)
		if err != nil {
			t.Fatalf("Repo.ListTriggerEvents: %v", err)
		}
		sameValue(t, "ListTriggerEventsPage", port, twin)
		if more != twinMore {
			t.Fatalf("ListTriggerEventsPage hasMore = %v, twin %v", more, twinMore)
		}
		seen = append(seen, pageIDs(port)...)
		if !more {
			break
		}
		last := port[len(port)-1].Event
		cursor = &core.TriggerEventCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	if want := []string{"f1-live", "f1-fail-b", "f1-fail-a", "f1-ok", "f1-old-fail"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("paged firings = %v, want %v", seen, want)
	}
	for _, f := range []core.TriggerEventFilters{
		{UserID: storetest.SeedUserID, TriggerID: "trg-f1", Outcomes: []core.TriggerEventOutcome{core.TriggerEventFailed}},
		{UserID: storetest.SeedUserID, TriggerID: "trg-f1", Outcomes: []core.TriggerEventOutcome{core.TriggerEventLaunched, core.TriggerEventSkipped}, Limit: 100},
		{UserID: storetest.SeedUserID, TriggerID: "trg-f2"},
	} {
		port, more, err := store.ListTriggerEventsPage(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("ListTriggerEventsPage(%+v): %v", f, err)
		}
		twin, twinMore, err := env.Repo.ListTriggerEvents(ctx, f)
		if err != nil {
			t.Fatalf("Repo.ListTriggerEvents(%+v): %v", f, err)
		}
		sameValue(t, "ListTriggerEventsPage(filtered)", port, twin)
		if more || twinMore {
			t.Fatalf("ListTriggerEventsPage(%+v) hasMore", f)
		}
	}
	failed, _, _ := store.ListTriggerEventsPage(ctx, env.ORM, core.TriggerEventFilters{UserID: storetest.SeedUserID, TriggerID: "trg-f1", Outcomes: []core.TriggerEventOutcome{core.TriggerEventFailed}})
	if g := pageIDs(failed); !reflect.DeepEqual(g, []string{"f1-fail-a", "f1-old-fail"}) {
		t.Fatalf("failed firings = %v", g)
	}
	all, _, _ := store.ListTriggerEventsPage(ctx, env.ORM, core.TriggerEventFilters{UserID: storetest.SeedUserID, TriggerID: "trg-f1"})
	if all[0].Run == nil || all[0].Run.ChatID != "chat-f-live" || all[0].Run.Title != "run chat-f-live" || all[0].Run.RootStatus != core.Active() || all[1].Run != nil {
		t.Fatalf("firing runs = %+v / %+v, want the live run joined and none for a failure", all[0].Run, all[1].Run)
	}

	triggerIDs := []string{"trg-f1", "trg-f2", "trg-f3"}
	for _, per := range []int{0, 2, 50} {
		port, err := store.RecentTriggerFirings(ctx, env.ORM, storetest.SeedUserID, triggerIDs, per)
		if err != nil {
			t.Fatalf("RecentTriggerFirings(%d): %v", per, err)
		}
		twin, err := env.Repo.RecentTriggerFirings(ctx, storetest.SeedUserID, triggerIDs, per)
		if err != nil {
			t.Fatalf("Repo.RecentTriggerFirings(%d): %v", per, err)
		}
		sameValue(t, "RecentTriggerFirings", port, twin)

		port, err = store.FiringsSinceLastSuccess(ctx, env.ORM, storetest.SeedUserID, triggerIDs, per)
		if err != nil {
			t.Fatalf("FiringsSinceLastSuccess(%d): %v", per, err)
		}
		twin, err = env.Repo.FiringsSinceLastSuccess(ctx, storetest.SeedUserID, triggerIDs, per)
		if err != nil {
			t.Fatalf("Repo.FiringsSinceLastSuccess(%d): %v", per, err)
		}
		sameValue(t, "FiringsSinceLastSuccess", port, twin)
	}
	recent, _ := store.RecentTriggerFirings(ctx, env.ORM, storetest.SeedUserID, triggerIDs, 2)
	if len(recent) != 1 || !reflect.DeepEqual(pageIDs(recent["trg-f1"]), []string{"f1-live", "f1-fail-b"}) {
		t.Fatalf("RecentTriggerFirings = %v, want trg-f1's newest two and nothing of another user's", recent)
	}
	since, _ := store.FiringsSinceLastSuccess(ctx, env.ORM, storetest.SeedUserID, triggerIDs, 50)
	if !reflect.DeepEqual(pageIDs(since["trg-f1"]), []string{"f1-live", "f1-fail-b", "f1-fail-a"}) {
		t.Fatalf("FiringsSinceLastSuccess = %v, want everything after f1-ok", pageIDs(since["trg-f1"]))
	}
	if empty, err := store.RecentTriggerFirings(ctx, env.ORM, storetest.SeedUserID, nil, 10); err != nil || len(empty) != 0 {
		t.Fatalf("RecentTriggerFirings(no ids) = %v, %v", empty, err)
	}
}
