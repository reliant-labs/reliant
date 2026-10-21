// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func newDraft(id, slug string, status core.WorkflowDraftStatus, at time.Time) *core.WorkflowDraft {
	return &core.WorkflowDraft{
		ID: id, UserID: storetest.SeedUserID, Name: "Draft " + slug, Slug: slug,
		Description: ptr("about " + slug), Definition: "name: " + slug, Status: status,
		CreatedAt: at, UpdatedAt: at,
	}
}

// TestWorkflowDraftReadsAgreeWithSQLC: every draft read of the port returns
// what its twin does, over drafts both layers wrote.
func TestWorkflowDraftReadsAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	imported := newDraft("d-imported", "imported", core.WorkflowDraftStatusComplete, l2Base)
	imported.SourcePath = ptr("/repo/.reliant/workflows/imported.yaml")
	imported.ForkedFrom = ptr("builtin://agent")
	hidden := newDraft("d-hidden", "hidden", core.WorkflowDraftStatusComplete, l2Base.Add(time.Minute))
	hidden.IsHidden = true
	for i, d := range []*core.WorkflowDraft{
		imported,
		newDraft("d-draft", "wip", "", l2Base.Add(2*time.Minute)), // unset status: a draft
		hidden,
		newDraft("d-done", "done", core.WorkflowDraftStatusComplete, l2Base.Add(3*time.Minute)),
	} {
		var err error
		if i%2 == 0 {
			err = store.InsertWorkflowDraft(ctx, env.ORM, d)
		} else {
			err = env.Repo.CreateWorkflowDraft(ctx, d)
		}
		if err != nil {
			t.Fatalf("seed %s: %v", d.ID, err)
		}
	}

	for _, id := range []string{"d-imported", "d-draft", "d-hidden", "d-done"} {
		port, err := store.LoadWorkflowDraft(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		twin, err := env.Repo.GetWorkflowDraft(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "draft "+id, port, twin)
		if port.Version != 1 {
			t.Fatalf("%s at version %d, want 1", id, port.Version)
		}
	}
	if d, _ := store.LoadWorkflowDraft(ctx, env.ORM, "d-draft"); d.Status != core.WorkflowDraftStatusDraft {
		t.Fatalf("unset status stored as %q, want draft", d.Status)
	}
	if _, err := store.LoadWorkflowDraft(ctx, env.ORM, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("LoadWorkflowDraft(missing) = %v, want sql.ErrNoRows as its twin", err)
	}

	user := storetest.SeedUserID
	finds := []struct {
		name string
		want string // "" for nil
		port func() (*core.WorkflowDraft, error)
		twin func() (*core.WorkflowDraft, error)
	}{
		{"by slug", "d-draft",
			func() (*core.WorkflowDraft, error) { return store.FindWorkflowDraftBySlug(ctx, env.ORM, user, "wip") },
			func() (*core.WorkflowDraft, error) { return env.Repo.GetWorkflowDraftBySlug(ctx, user, "wip") }},
		{"by name, any case", "d-done",
			func() (*core.WorkflowDraft, error) {
				return store.FindWorkflowDraftByName(ctx, env.ORM, user, "DRAFT done")
			},
			func() (*core.WorkflowDraft, error) { return env.Repo.GetWorkflowDraftByName(ctx, user, "DRAFT done") }},
		{"by source path", "d-imported",
			func() (*core.WorkflowDraft, error) {
				return store.FindWorkflowDraftBySourcePath(ctx, env.ORM, user, "/repo/.reliant/workflows/imported.yaml")
			},
			func() (*core.WorkflowDraft, error) {
				return env.Repo.GetWorkflowDraftBySourcePath(ctx, user, "/repo/.reliant/workflows/imported.yaml")
			}},
		{"usable", "d-done",
			func() (*core.WorkflowDraft, error) { return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "done") },
			func() (*core.WorkflowDraft, error) { return env.Repo.GetUsableWorkflowBySlug(ctx, user, "done") }},
		{"hidden is not usable", "",
			func() (*core.WorkflowDraft, error) {
				return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "hidden")
			},
			func() (*core.WorkflowDraft, error) { return env.Repo.GetUsableWorkflowBySlug(ctx, user, "hidden") }},
		{"a draft is not usable", "",
			func() (*core.WorkflowDraft, error) { return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "wip") },
			func() (*core.WorkflowDraft, error) {
				// The Repository adds the reason on top of the twin's nil.
				d, err := env.Repo.GetUsableWorkflowBySlug(ctx, user, "wip")
				var notRunnable *core.WorkflowDraftNotRunnableError
				if errors.As(err, &notRunnable) {
					return d, nil
				}
				return d, err
			}},
		{"another user's", "",
			func() (*core.WorkflowDraft, error) {
				return store.FindWorkflowDraftBySlug(ctx, env.ORM, "someone-else", "wip")
			},
			func() (*core.WorkflowDraft, error) {
				return env.Repo.GetWorkflowDraftBySlug(ctx, "someone-else", "wip")
			}},
	}
	for _, f := range finds {
		port, err := f.port()
		if err != nil {
			t.Fatalf("%s: port: %v", f.name, err)
		}
		twin, err := f.twin()
		if err != nil {
			t.Fatalf("%s: twin: %v", f.name, err)
		}
		got := ""
		if port != nil {
			got = port.ID
		}
		if got != f.want {
			t.Fatalf("%s: port found %q, want %q", f.name, got, f.want)
		}
		requireSame(t, f.name, port, twin)
	}

	list, err := store.ListUserWorkflowDrafts(ctx, env.ORM, user)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	if fmt.Sprint(ids) != "[d-done d-draft d-hidden d-imported]" {
		t.Fatalf("list order %v, want most recently updated first", ids)
	}
	twinList, _ := env.Repo.ListWorkflowDraftsByUser(ctx, user)
	requireSame(t, "list", list, twinList)

	if n, err := store.CountUserWorkflowDrafts(ctx, env.ORM, user); err != nil || n != 4 {
		t.Fatalf("count = %d, %v", n, err)
	}
	for slug, want := range map[string]bool{"wip": true, "nope": false} {
		port, err := store.WorkflowSlugExists(ctx, env.ORM, user, slug)
		if err != nil {
			t.Fatal(err)
		}
		twin, _ := env.Repo.WorkflowSlugExists(ctx, user, slug)
		if port != want || twin != want {
			t.Fatalf("slug %q exists: port %v, twin %v, want %v", slug, port, twin, want)
		}
	}
}

// TestWorkflowDraftWritesAgreeWithSQLC applies each write through the port
// to one draft and through its twin to an identical one, and requires the
// same stored result: each write bumps the version once.
func TestWorkflowDraftWritesAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	type write struct {
		name string
		port func(id string) (*core.WorkflowDraft, error)
		twin func(id string) (*core.WorkflowDraft, error)
	}
	reread := func(id string, err error) (*core.WorkflowDraft, error) {
		if err != nil {
			return nil, err
		}
		return store.LoadWorkflowDraft(ctx, env.ORM, id)
	}
	writes := []write{
		{"replace",
			func(id string) (*core.WorkflowDraft, error) {
				d := newDraft(id, "replaced-"+id[:1], core.WorkflowDraftStatusComplete, l2Base)
				d.Description, d.IsHidden, d.Definition = nil, true, "name: replaced"
				return reread(id, store.ReplaceWorkflowDraft(ctx, env.ORM, d))
			},
			func(id string) (*core.WorkflowDraft, error) {
				d := newDraft(id, "replaced-"+id[:1], core.WorkflowDraftStatusComplete, l2Base)
				d.Description, d.IsHidden, d.Definition = nil, true, "name: replaced"
				return reread(id, env.Repo.UpdateWorkflowDraft(ctx, d))
			}},
		{"definition",
			func(id string) (*core.WorkflowDraft, error) {
				return reread(id, store.SetWorkflowDraftDefinition(ctx, env.ORM, id, "Renamed", "renamed-"+id[:1], "name: renamed", ""))
			},
			func(id string) (*core.WorkflowDraft, error) {
				return reread(id, env.Repo.UpdateWorkflowDraftDefinition(ctx, id, "Renamed", "renamed-"+id[:1], "name: renamed", ""))
			}},
		{"status",
			func(id string) (*core.WorkflowDraft, error) {
				return store.SetWorkflowDraftStatus(ctx, env.ORM, id, core.WorkflowDraftStatusComplete)
			},
			func(id string) (*core.WorkflowDraft, error) {
				return env.Repo.SetWorkflowDraftStatus(ctx, id, core.WorkflowDraftStatusComplete)
			}},
		{"hidden",
			func(id string) (*core.WorkflowDraft, error) {
				return store.SetWorkflowDraftHidden(ctx, env.ORM, id, true)
			},
			func(id string) (*core.WorkflowDraft, error) { return env.Repo.SetWorkflowDraftHidden(ctx, id, true) }},
		{"forked from",
			func(id string) (*core.WorkflowDraft, error) {
				return store.SetWorkflowDraftForkedFrom(ctx, env.ORM, id, "builtin://plan")
			},
			func(id string) (*core.WorkflowDraft, error) {
				return env.Repo.UpdateWorkflowForkedFrom(ctx, id, "builtin://plan")
			}},
	}
	for i, w := range writes {
		ids := [2]string{fmt.Sprintf("p%d", i), fmt.Sprintf("q%d", i)}
		for _, id := range ids {
			d := newDraft(id, "s-"+id, core.WorkflowDraftStatusDraft, l2Base)
			d.Definition = "name: seed"
			if err := store.InsertWorkflowDraft(ctx, env.ORM, d); err != nil {
				t.Fatal(err)
			}
		}
		port, err := w.port(ids[0])
		if err != nil {
			t.Fatalf("%s: port: %v", w.name, err)
		}
		twin, err := w.twin(ids[1])
		if err != nil {
			t.Fatalf("%s: twin: %v", w.name, err)
		}
		if port.Version != 2 || !port.UpdatedAt.After(l2Base) {
			t.Fatalf("%s: version %d, updated_at %v; want 2 and restamped", w.name, port.Version, port.UpdatedAt)
		}
		// The rows differ only in their keys and NOW() stamps.
		for _, d := range []*core.WorkflowDraft{port, twin} {
			d.ID, d.Name, d.UpdatedAt, d.Description = "", "", l2Base, nil
			if len(d.Slug) > 2 {
				d.Slug = d.Slug[:len(d.Slug)-2]
			}
		}
		requireSame(t, w.name, port, twin)
	}

	// Missing drafts.
	if err := store.ReplaceWorkflowDraft(ctx, env.ORM, newDraft("missing", "x", "", l2Base)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("replace missing = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.SetWorkflowDraftHidden(ctx, env.ORM, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hide missing = %v, want sql.ErrNoRows", err)
	}

	// Upsert: insert, then update in place, keeping the origin fields.
	first := newDraft("up-1", "up", core.WorkflowDraftStatusDraft, l2Base)
	first.ForkedFrom, first.SourcePath = ptr("builtin://agent"), ptr("/a.yaml")
	got, err := store.UpsertWorkflowDraftBySlug(ctx, env.ORM, first)
	if err != nil || got.ID != "up-1" || got.Version != 1 {
		t.Fatalf("upsert insert = %+v, %v", got, err)
	}
	second := newDraft("up-2", "up", core.WorkflowDraftStatusComplete, l2Base.Add(time.Hour))
	second.Name, second.ForkedFrom, second.SourcePath, second.IsHidden = "Upserted", ptr("builtin://plan"), ptr("/b.yaml"), true
	got, err = store.UpsertWorkflowDraftBySlug(ctx, env.ORM, second)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "up-1" || got.Version != 2 || got.Name != "Upserted" || got.Status != core.WorkflowDraftStatusComplete ||
		!got.IsHidden || *got.ForkedFrom != "builtin://agent" || *got.SourcePath != "/a.yaml" || !got.CreatedAt.Equal(l2Base) {
		t.Fatalf("upsert update = %+v", got)
	}
	twin, err := env.Repo.UpsertWorkflowDraft(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if twin.ID != "up-1" || twin.Version != 3 {
		t.Fatalf("twin upsert over the port's row = %+v", twin)
	}

	// Deletes return the row they removed.
	removed, err := store.RemoveWorkflowDraft(ctx, env.ORM, "up-1")
	if err != nil || removed.Slug != "up" || removed.Version != 3 {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	if _, err := store.RemoveWorkflowDraft(ctx, env.ORM, "up-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("remove missing = %v, want sql.ErrNoRows", err)
	}
	removed, err = store.RemoveWorkflowDraftBySlug(ctx, env.ORM, storetest.SeedUserID, "s-p2")
	if err != nil || removed.ID != "p2" {
		t.Fatalf("remove by slug = %+v, %v", removed, err)
	}
	if err := env.Repo.DeleteWorkflowDraftBySlug(ctx, storetest.SeedUserID, "s-p2"); err != nil {
		t.Fatalf("twin remove of the removed slug = %v, want the silent no-op", err)
	}
	if _, err := store.RemoveWorkflowDraftBySlug(ctx, env.ORM, storetest.SeedUserID, "s-p2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("remove of the removed slug = %v, want sql.ErrNoRows", err)
	}
}

// TestWorkflowScenariosAgreeWithSQLC covers the scenario lifecycle through
// both layers. The port also reads version, which the sqlc mapping dropped
// (every scenario read back at version 0, so the scenario tools' conflict
// check compared against 0).
func TestWorkflowScenariosAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	if err := store.InsertWorkflowDraft(ctx, env.ORM, newDraft("d-sc", "sc", "", l2Base)); err != nil {
		t.Fatal(err)
	}
	draftID := sql.NullString{String: "d-sc", Valid: true}
	seeds := []*core.WorkflowScenario{
		{ID: "sc-1", WorkflowDraftID: draftID, Name: "happy", Events: `[{"a":1}]`, Expect: sql.NullString{String: `{"ok":true}`, Valid: true}, CreatedAt: l2Base, UpdatedAt: l2Base.Add(3 * time.Minute)},
		{ID: "sc-2", WorkflowDraftID: draftID, Name: "sad", Description: sql.NullString{String: "fails", Valid: true}, Events: `[]`, CreatedAt: l2Base.Add(time.Minute), UpdatedAt: l2Base.Add(time.Minute)},
		{ID: "sc-raw", Name: "raw yaml", Events: `[]`, CreatedAt: l2Base.Add(2 * time.Minute), UpdatedAt: l2Base.Add(2 * time.Minute)},
	}
	for i, s := range seeds {
		s.UserID = storetest.SeedUserID
		var err error
		if i%2 == 0 {
			err = store.InsertWorkflowScenario(ctx, env.ORM, s)
		} else {
			err = env.Repo.CreateWorkflowScenario(ctx, s)
		}
		if err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}
	// The twin drops version on read; everything else must agree.
	sameButVersion := func(what string, port, twin *core.WorkflowScenario) {
		t.Helper()
		if port == nil || twin == nil {
			requireSame(t, what, port, twin)
			return
		}
		p := *port
		p.Version = twin.Version
		requireSame(t, what, &p, twin)
	}

	for _, id := range []string{"sc-1", "sc-2", "sc-raw"} {
		port, err := store.FindWorkflowScenario(ctx, env.ORM, id)
		if err != nil || port == nil || port.Version != 1 {
			t.Fatalf("FindWorkflowScenario(%s) = %+v, %v; want version 1", id, port, err)
		}
		twin, _ := env.Repo.GetWorkflowScenario(ctx, id)
		sameButVersion("scenario "+id, port, twin)
	}
	if miss, err := store.FindWorkflowScenario(ctx, env.ORM, "missing"); miss != nil || err != nil {
		t.Fatalf("missing scenario = %+v, %v; want nil, nil", miss, err)
	}
	port, _ := store.FindWorkflowScenarioByName(ctx, env.ORM, "d-sc", "sad")
	twin, _ := env.Repo.GetWorkflowScenarioByName(ctx, "d-sc", "sad")
	if port == nil || port.ID != "sc-2" {
		t.Fatalf("by name = %+v", port)
	}
	sameButVersion("by name", port, twin)

	byDraft, _ := store.ListDraftWorkflowScenarios(ctx, env.ORM, "d-sc")
	twinByDraft, _ := env.Repo.ListWorkflowScenariosByDraft(ctx, "d-sc")
	byUser, _ := store.ListUserWorkflowScenarios(ctx, env.ORM, storetest.SeedUserID)
	twinByUser, _ := env.Repo.ListWorkflowScenariosByUser(ctx, storetest.SeedUserID)
	for _, l := range []struct {
		name       string
		port, twin []*core.WorkflowScenario
		want       string
	}{{"by draft, newest first", byDraft, twinByDraft, "[sc-2 sc-1]"}, {"by user, last updated first", byUser, twinByUser, "[sc-1 sc-raw sc-2]"}} {
		ids := make([]string, len(l.port))
		for i, s := range l.port {
			ids[i] = s.ID
			sameButVersion(l.name, s, l.twin[i])
		}
		if fmt.Sprint(ids) != l.want || len(l.twin) != len(l.port) {
			t.Fatalf("%s: %v, want %s", l.name, ids, l.want)
		}
	}

	// Writes, one through each layer, each bumping the version once.
	edited := *seeds[0]
	edited.Name, edited.Events, edited.Description = "happy path", `[{"a":2}]`, sql.NullString{String: "now described", Valid: true}
	if err := store.ReplaceWorkflowScenario(ctx, env.ORM, &edited); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.UpdateWorkflowScenarioResult(ctx, "sc-1", "passed", `{"steps":3}`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkflowScenarioResult(ctx, env.ORM, "sc-2", "failed", `{"steps":1}`); err != nil {
		t.Fatal(err)
	}
	sc1, _ := store.FindWorkflowScenario(ctx, env.ORM, "sc-1")
	if sc1.Version != 3 || sc1.Name != "happy path" || sc1.Description.String != "now described" || sc1.LastRunStatus.String != "passed" || !sc1.LastRunAt.Valid {
		t.Fatalf("sc-1 after an edit and a result = %+v", sc1)
	}
	sc2, _ := env.Repo.GetWorkflowScenario(ctx, "sc-2")
	if sc2.LastRunStatus.String != "failed" || sc2.LastRunResult.String != `{"steps":1}` || !sc2.LastRunAt.Valid {
		t.Fatalf("twin read of the port's result = %+v", sc2)
	}
	if err := store.ReplaceWorkflowScenario(ctx, env.ORM, &core.WorkflowScenario{ID: "missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("edit missing = %v, want sql.ErrNoRows", err)
	}
	if err := store.RecordWorkflowScenarioResult(ctx, env.ORM, "missing", "passed", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("result for missing = %v, want sql.ErrNoRows", err)
	}

	if err := store.RemoveWorkflowScenario(ctx, env.ORM, "sc-raw"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveWorkflowScenario(ctx, env.ORM, "sc-raw"); err != nil {
		t.Fatalf("removing a missing scenario is a no-op, as its twin is: %v", err)
	}
	if err := store.RemoveDraftWorkflowScenarios(ctx, env.ORM, "d-sc"); err != nil {
		t.Fatal(err)
	}
	if left, _ := env.Repo.ListWorkflowScenariosByUser(ctx, storetest.SeedUserID); len(left) != 0 {
		t.Fatalf("%d scenarios left", len(left))
	}
}
