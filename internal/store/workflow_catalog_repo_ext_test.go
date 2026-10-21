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

// storedDraft is the row newDraft(...) reads back as: at version 1, an unset
// status stored as a draft.
func storedDraft(d *core.WorkflowDraft) *core.WorkflowDraft {
	want := *d
	want.Version = 1
	if want.Status == "" {
		want.Status = core.WorkflowDraftStatusDraft
	}
	return &want
}

func draftIDs(ds []*core.WorkflowDraft) []string {
	ids := make([]string, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	return ids
}

// seedDrafts writes four of the seed user's drafts, created a minute apart:
// an imported complete one, a work-in-progress one with no status, a hidden
// complete one, and a plain complete one; plus one belonging to someone else
// with the slug "wip".
func seedDrafts(t *testing.T, env *storetest.Env) map[string]*core.WorkflowDraft {
	t.Helper()
	ctx := context.Background()
	imported := newDraft("d-imported", "imported", core.WorkflowDraftStatusComplete, l2Base)
	imported.SourcePath = ptr("/repo/.reliant/workflows/imported.yaml")
	imported.ForkedFrom = ptr("builtin://agent")
	hidden := newDraft("d-hidden", "hidden", core.WorkflowDraftStatusComplete, l2Base.Add(time.Minute))
	hidden.IsHidden = true
	foreign := newDraft("d-foreign", "wip", core.WorkflowDraftStatusComplete, l2Base.Add(4*time.Minute))
	foreign.UserID = "someone-else"
	seeded := map[string]*core.WorkflowDraft{}
	for _, d := range []*core.WorkflowDraft{
		imported,
		newDraft("d-draft", "wip", "", l2Base.Add(2*time.Minute)), // unset status: a draft
		hidden,
		newDraft("d-done", "done", core.WorkflowDraftStatusComplete, l2Base.Add(3*time.Minute)),
		foreign,
	} {
		if err := store.InsertWorkflowDraft(ctx, env.ORM, d); err != nil {
			t.Fatalf("seed %s: %v", d.ID, err)
		}
		seeded[d.ID] = storedDraft(d)
	}
	return seeded
}

func TestWorkflowDraftReads(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seeded := seedDrafts(t, env)
	user := storetest.SeedUserID

	// Every column round-trips at version 1, and an unset status is a draft.
	for id, want := range seeded {
		got, err := store.LoadWorkflowDraft(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "draft "+id, got, want)
	}
	if d, _ := store.LoadWorkflowDraft(ctx, env.ORM, "d-draft"); d.Status != core.WorkflowDraftStatusDraft {
		t.Fatalf("unset status stored as %q, want draft", d.Status)
	}
	if _, err := store.LoadWorkflowDraft(ctx, env.ORM, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("LoadWorkflowDraft(missing) = %v, want sql.ErrNoRows", err)
	}

	finds := []struct {
		name string
		want string // "" for nil
		find func() (*core.WorkflowDraft, error)
	}{
		{"by slug", "d-draft", func() (*core.WorkflowDraft, error) { return store.FindWorkflowDraftBySlug(ctx, env.ORM, user, "wip") }},
		{"by slug: the other user's own", "d-foreign", func() (*core.WorkflowDraft, error) {
			return store.FindWorkflowDraftBySlug(ctx, env.ORM, "someone-else", "wip")
		}},
		{"by slug: unknown", "", func() (*core.WorkflowDraft, error) { return store.FindWorkflowDraftBySlug(ctx, env.ORM, user, "nope") }},
		{"by slug: a user with none", "", func() (*core.WorkflowDraft, error) {
			return store.FindWorkflowDraftBySlug(ctx, env.ORM, "nobody", "wip")
		}},
		{"by slug: case matters", "", func() (*core.WorkflowDraft, error) { return store.FindWorkflowDraftBySlug(ctx, env.ORM, user, "WIP") }},
		{"by name, any case", "d-done", func() (*core.WorkflowDraft, error) {
			return store.FindWorkflowDraftByName(ctx, env.ORM, user, "DRAFT done")
		}},
		{"by name: the whole name", "", func() (*core.WorkflowDraft, error) { return store.FindWorkflowDraftByName(ctx, env.ORM, user, "done") }},
		{"by name: another user's", "", func() (*core.WorkflowDraft, error) {
			return store.FindWorkflowDraftByName(ctx, env.ORM, "nobody", "Draft done")
		}},
		{"by source path", "d-imported", func() (*core.WorkflowDraft, error) {
			return store.FindWorkflowDraftBySourcePath(ctx, env.ORM, user, "/repo/.reliant/workflows/imported.yaml")
		}},
		{"by source path: unknown", "", func() (*core.WorkflowDraft, error) {
			return store.FindWorkflowDraftBySourcePath(ctx, env.ORM, user, "/elsewhere.yaml")
		}},
		{"usable", "d-done", func() (*core.WorkflowDraft, error) { return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "done") }},
		{"imported is usable", "d-imported", func() (*core.WorkflowDraft, error) {
			return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "imported")
		}},
		{"hidden is not usable", "", func() (*core.WorkflowDraft, error) {
			return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "hidden")
		}},
		{"a draft is not usable", "", func() (*core.WorkflowDraft, error) { return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "wip") }},
		{"another user's complete workflow is theirs", "d-foreign", func() (*core.WorkflowDraft, error) {
			return store.FindUsableWorkflowBySlug(ctx, env.ORM, "someone-else", "wip")
		}},
		{"usable: unknown", "", func() (*core.WorkflowDraft, error) { return store.FindUsableWorkflowBySlug(ctx, env.ORM, user, "nope") }},
	}
	for _, f := range finds {
		got, err := f.find()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if f.want == "" {
			if got != nil {
				t.Fatalf("%s: found %+v, want nil", f.name, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("%s: found nothing, want %s", f.name, f.want)
		}
		requireSame(t, f.name, got, seeded[f.want])
	}

	// Listing: the user's own, most recently updated first.
	list, err := store.ListUserWorkflowDrafts(ctx, env.ORM, user)
	if err != nil {
		t.Fatal(err)
	}
	if ids := draftIDs(list); fmt.Sprint(ids) != "[d-done d-draft d-hidden d-imported]" {
		t.Fatalf("list order %v, want most recently updated first", ids)
	}
	for _, d := range list {
		requireSame(t, "listed "+d.ID, d, seeded[d.ID])
	}
	for user, want := range map[string]string{"someone-else": "[d-foreign]", "nobody": "[]"} {
		list, err := store.ListUserWorkflowDrafts(ctx, env.ORM, user)
		if err != nil || fmt.Sprint(draftIDs(list)) != want {
			t.Fatalf("list of %s = %v, %v; want %s", user, draftIDs(list), err, want)
		}
	}

	for user, want := range map[string]int64{user: 4, "someone-else": 1, "nobody": 0} {
		if n, err := store.CountUserWorkflowDrafts(ctx, env.ORM, user); err != nil || n != want {
			t.Fatalf("count of %s = %d, %v; want %d", user, n, err, want)
		}
	}
	for _, c := range []struct {
		user, slug string
		want       bool
	}{
		{user, "wip", true}, {user, "hidden", true}, {user, "nope", false},
		{"someone-else", "wip", true}, {"someone-else", "done", false}, {"nobody", "wip", false},
	} {
		got, err := store.WorkflowSlugExists(ctx, env.ORM, c.user, c.slug)
		if err != nil || got != c.want {
			t.Fatalf("WorkflowSlugExists(%s, %s) = %v, %v; want %v", c.user, c.slug, got, err, c.want)
		}
	}
}

// TestWorkflowDraftWrites applies each write to a fresh draft and checks the
// whole stored row: the fields it changes, version bumped once, updated_at
// restamped, and everything else as it was.
func TestWorkflowDraftWrites(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	writes := []struct {
		name   string
		write  func(id string) (*core.WorkflowDraft, error) // returns the row the write reports, if it does
		change func(d *core.WorkflowDraft)                  // what the write does to a newDraft(id, "s-"+id, draft)
	}{
		{"replace",
			func(id string) (*core.WorkflowDraft, error) {
				d := newDraft(id, "replaced-"+id, core.WorkflowDraftStatusComplete, l2Base)
				d.Description, d.IsHidden, d.Definition = nil, true, "name: replaced"
				return nil, store.ReplaceWorkflowDraft(ctx, env.ORM, d)
			},
			func(d *core.WorkflowDraft) {
				d.Name, d.Slug, d.Description = "Draft replaced-"+d.ID, "replaced-"+d.ID, nil
				d.Status, d.IsHidden, d.Definition = core.WorkflowDraftStatusComplete, true, "name: replaced"
			}},
		{"replace with an unset status makes it a draft",
			func(id string) (*core.WorkflowDraft, error) {
				d := newDraft(id, "s-"+id, "", l2Base)
				d.Definition = "name: edited"
				return nil, store.ReplaceWorkflowDraft(ctx, env.ORM, d)
			},
			func(d *core.WorkflowDraft) { d.Definition = "name: edited" }},
		{"definition",
			func(id string) (*core.WorkflowDraft, error) {
				return nil, store.SetWorkflowDraftDefinition(ctx, env.ORM, id, "Renamed", "renamed-"+id, "name: renamed", core.WorkflowDraftStatusComplete)
			},
			func(d *core.WorkflowDraft) {
				d.Name, d.Slug, d.Definition, d.Status = "Renamed", "renamed-"+d.ID, "name: renamed", core.WorkflowDraftStatusComplete
			}},
		{"definition with an unset status stores a draft",
			func(id string) (*core.WorkflowDraft, error) {
				return nil, store.SetWorkflowDraftDefinition(ctx, env.ORM, id, "Renamed", "renamed-"+id, "name: renamed", "")
			},
			func(d *core.WorkflowDraft) {
				d.Name, d.Slug, d.Definition = "Renamed", "renamed-"+d.ID, "name: renamed"
			}},
		{"status",
			func(id string) (*core.WorkflowDraft, error) {
				return store.SetWorkflowDraftStatus(ctx, env.ORM, id, core.WorkflowDraftStatusComplete)
			},
			func(d *core.WorkflowDraft) { d.Status = core.WorkflowDraftStatusComplete }},
		{"status back to draft",
			func(id string) (*core.WorkflowDraft, error) {
				if _, err := store.SetWorkflowDraftStatus(ctx, env.ORM, id, core.WorkflowDraftStatusComplete); err != nil {
					return nil, err
				}
				return store.SetWorkflowDraftStatus(ctx, env.ORM, id, core.WorkflowDraftStatusDraft)
			},
			func(d *core.WorkflowDraft) {}}, // version is checked separately below: two bumps
		{"hidden",
			func(id string) (*core.WorkflowDraft, error) {
				return store.SetWorkflowDraftHidden(ctx, env.ORM, id, true)
			},
			func(d *core.WorkflowDraft) { d.IsHidden = true }},
		{"forked from",
			func(id string) (*core.WorkflowDraft, error) {
				return store.SetWorkflowDraftForkedFrom(ctx, env.ORM, id, "builtin://plan")
			},
			func(d *core.WorkflowDraft) { d.ForkedFrom = ptr("builtin://plan") }},
	}
	for i, w := range writes {
		id := fmt.Sprintf("w%d", i)
		seed := newDraft(id, "s-"+id, core.WorkflowDraftStatusDraft, l2Base)
		seed.Definition = "name: seed"
		bystander := newDraft(id+"-by", "by-"+id, core.WorkflowDraftStatusDraft, l2Base)
		for _, d := range []*core.WorkflowDraft{seed, bystander} {
			if err := store.InsertWorkflowDraft(ctx, env.ORM, d); err != nil {
				t.Fatal(err)
			}
		}
		reported, err := w.write(id)
		if err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}

		want := storedDraft(seed)
		w.change(want)
		want.Version = 2
		if w.name == "status back to draft" {
			want.Version = 3
		}
		got, err := store.LoadWorkflowDraft(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		requireRecent(t, w.name+" updated_at", got.UpdatedAt)
		want.UpdatedAt = got.UpdatedAt
		requireSame(t, w.name, got, want)
		if reported != nil {
			requireSame(t, w.name+": the row the write returned", reported, want)
		}
		other, _ := store.LoadWorkflowDraft(ctx, env.ORM, bystander.ID)
		requireSame(t, w.name+": bystander", other, storedDraft(bystander))
	}

	// A missing draft is sql.ErrNoRows from every write.
	if err := store.ReplaceWorkflowDraft(ctx, env.ORM, newDraft("missing", "x", "", l2Base)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("replace missing = %v, want sql.ErrNoRows", err)
	}
	if err := store.SetWorkflowDraftDefinition(ctx, env.ORM, "missing", "n", "s", "d", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("set definition of missing = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.SetWorkflowDraftStatus(ctx, env.ORM, "missing", core.WorkflowDraftStatusComplete); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("set status of missing = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.SetWorkflowDraftHidden(ctx, env.ORM, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hide missing = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.SetWorkflowDraftForkedFrom(ctx, env.ORM, "missing", "builtin://plan"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fork missing = %v, want sql.ErrNoRows", err)
	}
}

func TestUpsertWorkflowDraftBySlug(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	// A new slug inserts at version 1 with the caller's stamps.
	first := newDraft("up-1", "up", core.WorkflowDraftStatusDraft, l2Base)
	first.ForkedFrom, first.SourcePath = ptr("builtin://agent"), ptr("/a.yaml")
	got, err := store.UpsertWorkflowDraftBySlug(ctx, env.ORM, first)
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "upsert insert", got, storedDraft(first))

	// The same slug updates the content in place and keeps the identity and
	// origin: id, created_at, forked_from and source_path.
	second := newDraft("up-2", "up", core.WorkflowDraftStatusComplete, l2Base.Add(time.Hour))
	second.Name, second.Description, second.Definition, second.IsHidden = "Upserted", ptr("new text"), "name: upserted", true
	second.ForkedFrom, second.SourcePath = ptr("builtin://plan"), ptr("/b.yaml")
	got, err = store.UpsertWorkflowDraftBySlug(ctx, env.ORM, second)
	if err != nil {
		t.Fatal(err)
	}
	requireRecent(t, "upsert updated_at", got.UpdatedAt)
	requireSame(t, "upsert update", got, &core.WorkflowDraft{
		ID: "up-1", UserID: storetest.SeedUserID, Name: "Upserted", Slug: "up", Description: ptr("new text"),
		Definition: "name: upserted", Status: core.WorkflowDraftStatusComplete, SourcePath: ptr("/a.yaml"), ForkedFrom: ptr("builtin://agent"),
		CreatedAt: l2Base, UpdatedAt: got.UpdatedAt, IsHidden: true, Version: 2,
	})
	stored, err := store.LoadWorkflowDraft(ctx, env.ORM, "up-1")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "stored after the upsert", stored, got)
	if _, err := store.LoadWorkflowDraft(ctx, env.ORM, "up-2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the upserted id exists as a second row: %v", err)
	}

	// Upserting again bumps again, and clearing the description sticks.
	third := newDraft("up-3", "up", core.WorkflowDraftStatusDraft, l2Base)
	third.Description = nil
	got, err = store.UpsertWorkflowDraftBySlug(ctx, env.ORM, third)
	if err != nil || got.Version != 3 || got.Description != nil || got.Status != core.WorkflowDraftStatusDraft || got.IsHidden {
		t.Fatalf("third upsert = %+v, %v; want version 3, no description, draft, visible", got, err)
	}

	// Another user's draft with the same slug is a different row.
	foreign := newDraft("up-f", "up", core.WorkflowDraftStatusDraft, l2Base)
	foreign.UserID = "someone-else"
	got, err = store.UpsertWorkflowDraftBySlug(ctx, env.ORM, foreign)
	if err != nil || got.ID != "up-f" || got.Version != 1 {
		t.Fatalf("another user's upsert = %+v, %v; want a new row at version 1", got, err)
	}
	if n, _ := store.CountUserWorkflowDrafts(ctx, env.ORM, storetest.SeedUserID); n != 1 {
		t.Fatalf("the seed user has %d drafts, want 1", n)
	}
}

func TestRemoveWorkflowDraft(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seeded := seedDrafts(t, env)

	// Removal returns the row it removed.
	removed, err := store.RemoveWorkflowDraft(ctx, env.ORM, "d-imported")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "removed by id", removed, seeded["d-imported"])
	if _, err := store.RemoveWorkflowDraft(ctx, env.ORM, "d-imported"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removing it again = %v, want sql.ErrNoRows", err)
	}

	// By slug: the user's own draft, never someone else's with the same slug.
	removed, err = store.RemoveWorkflowDraftBySlug(ctx, env.ORM, storetest.SeedUserID, "wip")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "removed by slug", removed, seeded["d-draft"])
	if _, err := store.RemoveWorkflowDraftBySlug(ctx, env.ORM, storetest.SeedUserID, "wip"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removing the removed slug = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.RemoveWorkflowDraftBySlug(ctx, env.ORM, "nobody", "done"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removing another user's slug = %v, want sql.ErrNoRows", err)
	}
	if foreign, err := store.LoadWorkflowDraft(ctx, env.ORM, "d-foreign"); err != nil || foreign.Slug != "wip" {
		t.Fatalf("the other user's draft with the same slug was removed: %v", err)
	}
	list, _ := store.ListUserWorkflowDrafts(ctx, env.ORM, storetest.SeedUserID)
	if ids := draftIDs(list); fmt.Sprint(ids) != "[d-done d-hidden]" {
		t.Fatalf("drafts left = %v, want [d-done d-hidden]", ids)
	}
}

func sampleScenarios(draft sql.NullString) []*core.WorkflowScenario {
	return []*core.WorkflowScenario{
		{ID: "sc-1", WorkflowDraftID: draft, Name: "happy", Events: `[{"a":1}]`, Expect: nstr(`{"ok":true}`), CreatedAt: l2Base, UpdatedAt: l2Base.Add(3 * time.Minute)},
		{ID: "sc-2", WorkflowDraftID: draft, Name: "sad", Description: nstr("fails"), Events: `[]`, CreatedAt: l2Base.Add(time.Minute), UpdatedAt: l2Base.Add(time.Minute)},
		{ID: "sc-raw", Name: "raw yaml", Events: `[]`, CreatedAt: l2Base.Add(2 * time.Minute), UpdatedAt: l2Base.Add(2 * time.Minute)},
	}
}

// seedScenarios writes sampleScenarios for the seed user under draft d-sc, plus
// one scenario of someone else's, and returns what each reads back as: at
// version 1, never run.
func seedScenarios(t *testing.T, env *storetest.Env) map[string]*core.WorkflowScenario {
	t.Helper()
	ctx := context.Background()
	if err := store.InsertWorkflowDraft(ctx, env.ORM, newDraft("d-sc", "sc", "", l2Base)); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertWorkflowDraft(ctx, env.ORM, newDraft("d-sc-2", "sc-2", "", l2Base)); err != nil {
		t.Fatal(err)
	}
	foreign := &core.WorkflowScenario{ID: "sc-foreign", WorkflowDraftID: nstr("d-sc-2"), UserID: "someone-else", Name: "happy", Events: `[]`,
		CreatedAt: l2Base.Add(10 * time.Minute), UpdatedAt: l2Base.Add(10 * time.Minute)}
	want := map[string]*core.WorkflowScenario{}
	for _, s := range append(sampleScenarios(nstr("d-sc")), foreign) {
		if s.UserID == "" {
			s.UserID = storetest.SeedUserID
		}
		if err := store.InsertWorkflowScenario(ctx, env.ORM, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
		stored := *s
		stored.Version = 1
		want[s.ID] = &stored
	}
	return want
}

func scenarioIDs(ss []*core.WorkflowScenario) []string {
	ids := make([]string, len(ss))
	for i, s := range ss {
		ids[i] = s.ID
	}
	return ids
}

func TestWorkflowScenarioReads(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	want := seedScenarios(t, env)
	user := storetest.SeedUserID

	// Every column round-trips, version 1 included (a scenario once read back at
	// version 0, so the scenario tools' conflict check compared against 0).
	for id, w := range want {
		got, err := store.FindWorkflowScenario(ctx, env.ORM, id)
		if err != nil || got == nil {
			t.Fatalf("FindWorkflowScenario(%s) = %+v, %v", id, got, err)
		}
		requireSame(t, "scenario "+id, got, w)
	}
	if miss, err := store.FindWorkflowScenario(ctx, env.ORM, "missing"); miss != nil || err != nil {
		t.Fatalf("missing scenario = %+v, %v; want nil, nil", miss, err)
	}

	for _, c := range []struct{ draft, name, want string }{
		{"d-sc", "sad", "sc-2"},
		{"d-sc", "happy", "sc-1"},
		{"d-sc-2", "happy", "sc-foreign"}, // names are per draft
		{"d-sc", "raw yaml", ""},          // a raw-yaml scenario has no draft
		{"d-sc", "nope", ""},
		{"no-draft", "happy", ""},
	} {
		got, err := store.FindWorkflowScenarioByName(ctx, env.ORM, c.draft, c.name)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if got != nil {
				t.Fatalf("by name (%s, %s) = %+v, want nil", c.draft, c.name, got)
			}
			continue
		}
		requireSame(t, "by name "+c.name, got, want[c.want])
	}

	lists := []struct {
		name string
		want string
		list func() ([]*core.WorkflowScenario, error)
	}{
		{"by draft, newest first", "[sc-2 sc-1]", func() ([]*core.WorkflowScenario, error) {
			return store.ListDraftWorkflowScenarios(ctx, env.ORM, "d-sc")
		}},
		{"by another draft", "[sc-foreign]", func() ([]*core.WorkflowScenario, error) {
			return store.ListDraftWorkflowScenarios(ctx, env.ORM, "d-sc-2")
		}},
		{"by a draft with none", "[]", func() ([]*core.WorkflowScenario, error) {
			return store.ListDraftWorkflowScenarios(ctx, env.ORM, "no-draft")
		}},
		{"by user, last updated first", "[sc-1 sc-raw sc-2]", func() ([]*core.WorkflowScenario, error) { return store.ListUserWorkflowScenarios(ctx, env.ORM, user) }},
		{"by another user", "[sc-foreign]", func() ([]*core.WorkflowScenario, error) {
			return store.ListUserWorkflowScenarios(ctx, env.ORM, "someone-else")
		}},
		{"by a user with none", "[]", func() ([]*core.WorkflowScenario, error) {
			return store.ListUserWorkflowScenarios(ctx, env.ORM, "nobody")
		}},
	}
	for _, l := range lists {
		got, err := l.list()
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		if ids := scenarioIDs(got); fmt.Sprint(ids) != l.want {
			t.Fatalf("%s: %v, want %s", l.name, ids, l.want)
		}
		for _, s := range got {
			requireSame(t, l.name+" "+s.ID, s, want[s.ID])
		}
	}
}

func TestWorkflowScenarioWrites(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	want := seedScenarios(t, env)

	// An edit rewrites name, description, events and expect, bumps the version
	// and updated_at, and leaves the draft, owner, created_at and last run.
	edited := *want["sc-1"]
	edited.Name, edited.Events, edited.Description = "happy path", `[{"a":2}]`, nstr("now described")
	edited.Expect = sql.NullString{} // cleared
	edited.UserID, edited.CreatedAt = "ignored", l2Base.Add(99*time.Hour)
	if err := store.ReplaceWorkflowScenario(ctx, env.ORM, &edited); err != nil {
		t.Fatal(err)
	}
	// A result is recorded by a separate write that bumps the version again.
	if err := store.RecordWorkflowScenarioResult(ctx, env.ORM, "sc-1", "passed", `{"steps":3}`); err != nil {
		t.Fatal(err)
	}
	sc1, err := store.FindWorkflowScenario(ctx, env.ORM, "sc-1")
	if err != nil {
		t.Fatal(err)
	}
	requireRecent(t, "updated_at", sc1.UpdatedAt)
	if !sc1.LastRunAt.Valid {
		t.Fatal("last_run_at not stamped")
	}
	requireRecent(t, "last_run_at", sc1.LastRunAt.Time)
	requireSame(t, "sc-1 after an edit and a result", sc1, &core.WorkflowScenario{
		ID: "sc-1", WorkflowDraftID: nstr("d-sc"), UserID: storetest.SeedUserID, Name: "happy path", Description: nstr("now described"),
		Events: `[{"a":2}]`, LastRunAt: sc1.LastRunAt, LastRunStatus: nstr("passed"), LastRunResult: nstr(`{"steps":3}`),
		CreatedAt: l2Base, UpdatedAt: sc1.UpdatedAt, Version: 3,
	})

	// A result alone changes nothing else, and a later result replaces the earlier one.
	if err := store.RecordWorkflowScenarioResult(ctx, env.ORM, "sc-2", "failed", `{"steps":1}`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkflowScenarioResult(ctx, env.ORM, "sc-2", "error", `{"steps":0}`); err != nil {
		t.Fatal(err)
	}
	sc2, _ := store.FindWorkflowScenario(ctx, env.ORM, "sc-2")
	exp := *want["sc-2"]
	exp.LastRunStatus, exp.LastRunResult, exp.LastRunAt, exp.UpdatedAt, exp.Version = nstr("error"), nstr(`{"steps":0}`), sc2.LastRunAt, sc2.UpdatedAt, 3
	requireSame(t, "sc-2 after two results", sc2, &exp)
	requireRecent(t, "sc-2 last_run_at", sc2.LastRunAt.Time)

	// The others were not touched.
	for _, id := range []string{"sc-raw", "sc-foreign"} {
		got, _ := store.FindWorkflowScenario(ctx, env.ORM, id)
		requireSame(t, id+" untouched", got, want[id])
	}

	if err := store.ReplaceWorkflowScenario(ctx, env.ORM, &core.WorkflowScenario{ID: "missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("edit missing = %v, want sql.ErrNoRows", err)
	}
	if err := store.RecordWorkflowScenarioResult(ctx, env.ORM, "missing", "passed", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("result for missing = %v, want sql.ErrNoRows", err)
	}
}

func TestWorkflowScenarioRemoval(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	want := seedScenarios(t, env)
	user := storetest.SeedUserID

	if err := store.RemoveWorkflowScenario(ctx, env.ORM, "sc-raw"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sc-raw", "missing"} {
		if err := store.RemoveWorkflowScenario(ctx, env.ORM, id); err != nil {
			t.Fatalf("removing %s again is a no-op: %v", id, err)
		}
	}
	left, _ := store.ListUserWorkflowScenarios(ctx, env.ORM, user)
	if ids := scenarioIDs(left); fmt.Sprint(ids) != "[sc-1 sc-2]" {
		t.Fatalf("after removing one: %v, want [sc-1 sc-2]", ids)
	}

	// Removing a draft's scenarios takes all of its own, and no other draft's.
	if err := store.RemoveDraftWorkflowScenarios(ctx, env.ORM, "d-sc"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveDraftWorkflowScenarios(ctx, env.ORM, "d-sc"); err != nil {
		t.Fatalf("removing a draft's scenarios again is a no-op: %v", err)
	}
	if err := store.RemoveDraftWorkflowScenarios(ctx, env.ORM, "no-draft"); err != nil {
		t.Fatalf("removing the scenarios of an unknown draft is a no-op: %v", err)
	}
	if left, _ := store.ListUserWorkflowScenarios(ctx, env.ORM, user); len(left) != 0 {
		t.Fatalf("%v left for the owner", scenarioIDs(left))
	}
	foreign, err := store.FindWorkflowScenario(ctx, env.ORM, "sc-foreign")
	if err != nil || foreign == nil {
		t.Fatalf("another draft's scenario was removed: %v", err)
	}
	requireSame(t, "other draft's scenario", foreign, want["sc-foreign"])
}
