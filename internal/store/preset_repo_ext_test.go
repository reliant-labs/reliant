// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestPresetQueries ports presets.sql, interleaved with the Repository.
func TestPresetQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	user := storetest.SeedUserID
	project := storetest.SeedProjectID

	preset := func(id, name, slug, tag string, projectID *string) *core.Preset {
		return &core.Preset{
			ID: id, UserID: user, ProjectID: projectID, Name: name, Slug: slug, Tag: tag,
			Description: strp(name + " preset"), Params: map[string]interface{}{"model": name, "temperature": 0.5},
		}
	}

	created, err := store.InsertPreset(ctx, env.ORM, preset("p-fast", "Fast", "fast", "coder", nil))
	if err != nil {
		t.Fatalf("InsertPreset: %v", err)
	}
	if created.CreatedAt.IsZero() || created.Params["model"] != "Fast" || created.Params["temperature"] != 0.5 {
		t.Fatalf("InsertPreset = %+v, want stamps and the params round-tripped", created)
	}
	if _, err := env.Repo.CreatePreset(ctx, preset("p-smart", "Smart", "smart", "coder", strp(project))); err != nil {
		t.Fatalf("Repo.CreatePreset: %v", err)
	}
	if _, err := env.Repo.CreatePreset(ctx, preset("p-plan", "Planner", "plan", "planner", nil)); err != nil {
		t.Fatalf("Repo.CreatePreset(plan): %v", err)
	}

	twin, err := env.Repo.GetPreset(ctx, "p-fast")
	if err != nil || twin.Name != "Fast" || twin.Tag != "coder" || *twin.Description != "Fast preset" || twin.ProjectID != nil || twin.Params["model"] != "Fast" {
		t.Fatalf("Repo.GetPreset = %+v, %v; want the store's write", twin, err)
	}
	got, err := store.GetPreset(ctx, env.ORM, "p-smart")
	if err != nil || got.Slug != "smart" || *got.ProjectID != project || got.Params["temperature"] != 0.5 {
		t.Fatalf("GetPreset = %+v, %v; want the Repository's write", got, err)
	}
	if _, err := store.GetPreset(ctx, env.ORM, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetPreset(missing) err = %v, want sql.ErrNoRows", err)
	}

	// By slug: the global lookup never sees a project preset and vice versa.
	if g, err := store.GetPresetBySlug(ctx, env.ORM, user, "fast"); err != nil || g.ID != "p-fast" {
		t.Fatalf("GetPresetBySlug(fast) = %v, %v", g, err)
	}
	if _, err := store.GetPresetBySlug(ctx, env.ORM, user, "smart"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetPresetBySlug(project-only slug) err = %v, want sql.ErrNoRows", err)
	}
	if g, err := store.GetPresetBySlugAndProject(ctx, env.ORM, user, "smart", project); err != nil || g.ID != "p-smart" {
		t.Fatalf("GetPresetBySlugAndProject(smart) = %v, %v", g, err)
	}
	if _, err := store.GetPresetBySlugAndProject(ctx, env.ORM, user, "fast", project); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetPresetBySlugAndProject(global slug) err = %v, want sql.ErrNoRows", err)
	}
	if tw, err := env.Repo.GetPresetBySlugAndProject(ctx, user, "smart", project); err != nil || tw.ID != "p-smart" {
		t.Fatalf("Repo.GetPresetBySlugAndProject = %v, %v", tw, err)
	}

	ids := func(ps []*core.Preset) string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.ID
		}
		return strings.Join(out, ",")
	}
	check := func(what string, got []*core.Preset, err error, twin []*core.Preset, twinErr error, want string) {
		t.Helper()
		if err != nil || twinErr != nil {
			t.Fatalf("%s: %v / twin %v", what, err, twinErr)
		}
		if ids(got) != want || ids(twin) != want {
			t.Fatalf("%s = %s, twin %s, want %s", what, ids(got), ids(twin), want)
		}
	}
	all, err := store.ListUserPresets(ctx, env.ORM, user)
	twinAll, twinErr := env.Repo.ListUserPresets(ctx, user)
	check("ListUserPresets", all, err, twinAll, twinErr, "p-fast,p-plan,p-smart")
	global, err := store.ListUserPresetsGlobal(ctx, env.ORM, user)
	twinGlobal, twinErr := env.Repo.ListUserPresetsGlobal(ctx, user)
	check("ListUserPresetsGlobal", global, err, twinGlobal, twinErr, "p-fast,p-plan")
	byProject, err := store.ListUserPresetsByProject(ctx, env.ORM, user, project)
	twinByProject, twinErr := env.Repo.ListUserPresetsByProject(ctx, user, project)
	check("ListUserPresetsByProject", byProject, err, twinByProject, twinErr, "p-fast,p-plan,p-smart")
	otherProject, err := store.ListUserPresetsByProject(ctx, env.ORM, user, "other-project")
	twinOther, twinErr := env.Repo.ListUserPresetsByProject(ctx, user, "other-project")
	check("ListUserPresetsByProject(other)", otherProject, err, twinOther, twinErr, "p-fast,p-plan")
	byTag, err := store.ListPresetsByTag(ctx, env.ORM, user, "coder", project)
	twinByTag, twinErr := env.Repo.ListPresetsByTag(ctx, user, "coder", project)
	check("ListPresetsByTag(coder)", byTag, err, twinByTag, twinErr, "p-fast,p-smart")
	byTagOther, err := store.ListPresetsByTag(ctx, env.ORM, user, "coder", "other-project")
	twinByTagOther, twinErr := env.Repo.ListPresetsByTag(ctx, user, "coder", "other-project")
	check("ListPresetsByTag(coder, other)", byTagOther, err, twinByTagOther, twinErr, "p-fast")

	// Upsert: a project preset of an existing slug is replaced in place; a
	// global one never conflicts (NULL project_id), so it adds a row.
	up, err := store.UpsertPreset(ctx, env.ORM, &core.Preset{
		ID: "p-smart-2", UserID: user, ProjectID: strp(project), Name: "Smarter", Slug: "smart", Tag: "reviewer",
		Params: map[string]interface{}{"model": "opus"},
	})
	if err != nil {
		t.Fatalf("UpsertPreset(project): %v", err)
	}
	if up.ID != "p-smart" || up.Name != "Smarter" || up.Tag != "reviewer" || up.Description != nil || up.Params["model"] != "opus" || !up.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("UpsertPreset(project) = %+v, want p-smart replaced in place", up)
	}
	twinUp, err := env.Repo.UpsertPreset(ctx, &core.Preset{ID: "p-fast-2", UserID: user, Name: "Fast again", Slug: "fast", Tag: "coder"})
	if err != nil {
		t.Fatalf("Repo.UpsertPreset(global): %v", err)
	}
	if twinUp.ID != "p-fast-2" {
		t.Fatalf("Repo.UpsertPreset(global) = %s, want a new row: a global preset never conflicts", twinUp.ID)
	}
	upGlobal, err := store.UpsertPreset(ctx, env.ORM, &core.Preset{ID: "p-fast-3", UserID: user, Name: "Fast thrice", Slug: "fast", Tag: "coder"})
	if err != nil || upGlobal.ID != "p-fast-3" {
		t.Fatalf("UpsertPreset(global) = %v, %v; want a new row", upGlobal, err)
	}

	// Update: name, description, tag and params; the rest is kept.
	updated, err := store.ReplacePreset(ctx, env.ORM, &core.Preset{ID: "p-plan", Name: "Planner v2", Tag: "planner", Params: map[string]interface{}{"depth": 3.0}})
	if err != nil {
		t.Fatalf("ReplacePreset: %v", err)
	}
	if updated.Name != "Planner v2" || updated.Slug != "plan" || updated.UserID != user || updated.Description != nil || updated.Params["depth"] != 3.0 || updated.CreatedAt.IsZero() {
		t.Fatalf("ReplacePreset = %+v, want the row as stored, slug and owner kept", updated)
	}
	twinPlan, err := env.Repo.GetPreset(ctx, "p-plan")
	if err != nil || twinPlan.Name != "Planner v2" || !twinPlan.UpdatedAt.After(twinPlan.CreatedAt) {
		t.Fatalf("Repo.GetPreset after ReplacePreset = %+v, %v", twinPlan, err)
	}
	if _, err := store.ReplacePreset(ctx, env.ORM, &core.Preset{ID: "missing", Name: "x"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ReplacePreset(missing) err = %v, want sql.ErrNoRows", err)
	}

	// Deletes by slug touch their own scope only.
	if err := store.DeletePresetBySlugAndProject(ctx, env.ORM, user, "fast", project); err != nil {
		t.Fatalf("DeletePresetBySlugAndProject(global slug): %v", err)
	}
	if _, err := env.Repo.GetPreset(ctx, "p-fast"); err != nil {
		t.Fatalf("p-fast gone after a project-scoped delete of its slug: %v", err)
	}
	if err := store.DeletePresetBySlugAndProject(ctx, env.ORM, user, "smart", project); err != nil {
		t.Fatalf("DeletePresetBySlugAndProject: %v", err)
	}
	if _, err := env.Repo.GetPreset(ctx, "p-smart"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Repo.GetPreset(p-smart) after delete err = %v, want sql.ErrNoRows", err)
	}
	if err := store.DeletePresetBySlug(ctx, env.ORM, user, "fast"); err != nil {
		t.Fatalf("DeletePresetBySlug: %v", err)
	}
	left, err := store.ListUserPresets(ctx, env.ORM, user)
	if err != nil || ids(left) != "p-plan" {
		t.Fatalf("after DeletePresetBySlug(fast) presets = %s, %v; want every global fast gone", ids(left), err)
	}
	if err := store.RemovePreset(ctx, env.ORM, "p-plan"); err != nil {
		t.Fatalf("RemovePreset: %v", err)
	}
	if err := store.RemovePreset(ctx, env.ORM, "p-plan"); err != nil {
		t.Fatalf("RemovePreset(again) = %v, want nil", err)
	}
}
