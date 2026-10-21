// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestSettingQueries ports settings.sql, interleaved with the Repository.
func TestSettingQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	user := storetest.SeedUserID
	project := strp(storetest.SeedProjectID)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	setting := func(id, key, value string, projectID *string, at time.Time) *core.Setting {
		return &core.Setting{ID: id, UserID: user, ProjectID: projectID, Key: key, Value: value, ValueType: "string", CreatedAt: at, UpdatedAt: at}
	}

	// A user-level and a project-level setting of the same key are two rows.
	if err := store.UpsertSetting(ctx, env.ORM, setting("s-user", "theme", "dark", nil, base)); err != nil {
		t.Fatalf("UpsertSetting(user): %v", err)
	}
	if err := env.Repo.CreateSetting(ctx, setting("s-proj", "theme", "light", project, base)); err != nil {
		t.Fatalf("Repo.CreateSetting(project): %v", err)
	}
	// Writing the key again replaces the value in place, in either scope and
	// through either layer: the row keeps its id and created_at.
	if err := env.Repo.CreateSetting(ctx, setting("s-user-2", "theme", "solarized", nil, base.Add(time.Minute))); err != nil {
		t.Fatalf("Repo.CreateSetting(user, again): %v", err)
	}
	if err := store.UpsertSetting(ctx, env.ORM, setting("s-proj-2", "theme", "contrast", project, base.Add(time.Minute))); err != nil {
		t.Fatalf("UpsertSetting(project, again): %v", err)
	}

	userTheme, err := store.GetSetting(ctx, env.ORM, user, nil, "theme")
	if err != nil {
		t.Fatalf("GetSetting(user): %v", err)
	}
	if userTheme.ID != "s-user" || userTheme.Value != "solarized" || !userTheme.CreatedAt.Equal(base) || !userTheme.UpdatedAt.Equal(base.Add(time.Minute)) || userTheme.ProjectID != nil {
		t.Fatalf("GetSetting(user) = %+v, want s-user replaced in place by the Repository's write", userTheme)
	}
	twin, err := env.Repo.GetSetting(ctx, user, project, "theme")
	if err != nil {
		t.Fatalf("Repo.GetSetting(project): %v", err)
	}
	if twin.ID != "s-proj" || twin.Value != "contrast" || *twin.ProjectID != storetest.SeedProjectID {
		t.Fatalf("Repo.GetSetting(project) = %+v, want s-proj replaced in place by the store's write", twin)
	}
	if _, err := store.GetSetting(ctx, env.ORM, user, nil, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("GetSetting(missing) err = %v, want a \"not found\" error", err)
	}
	if _, err := env.Repo.GetSetting(ctx, user, strp("other-project"), "theme"); err == nil {
		t.Fatalf("Repo.GetSetting(other project): want not found, the scopes are separate")
	}

	for _, s := range []*core.Setting{setting("s-a", "editor.font", "mono", nil, base), setting("s-b", "editor.size", "12", nil, base)} {
		if err := store.UpsertSetting(ctx, env.ORM, s); err != nil {
			t.Fatalf("UpsertSetting(%s): %v", s.Key, err)
		}
	}
	keys := func(ss []*core.Setting) string {
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = s.Key
		}
		return strings.Join(out, ",")
	}
	userList, err := store.ListSettings(ctx, env.ORM, user, nil)
	if err != nil {
		t.Fatalf("ListSettings(user): %v", err)
	}
	twinList, err := env.Repo.ListSettings(ctx, user, nil)
	if err != nil {
		t.Fatalf("Repo.ListSettings(user): %v", err)
	}
	if got, tw := keys(userList), keys(twinList); got != "editor.font,editor.size,theme" || tw != got {
		t.Fatalf("ListSettings(user) = %s, twin %s; want editor.font,editor.size,theme", got, tw)
	}
	projList, err := store.ListSettings(ctx, env.ORM, user, project)
	if err != nil || keys(projList) != "theme" || projList[0].Value != "contrast" {
		t.Fatalf("ListSettings(project) = %v, %v; want the one project theme", keys(projList), err)
	}
	byKey, err := store.ListSettingsByKey(ctx, env.ORM, user, "editor.%")
	if err != nil {
		t.Fatalf("ListSettingsByKey: %v", err)
	}
	twinByKey, err := env.Repo.ListSettingsByKey(ctx, user, "editor.%")
	if err != nil {
		t.Fatalf("Repo.ListSettingsByKey: %v", err)
	}
	if got, tw := keys(byKey), keys(twinByKey); got != "editor.font,editor.size" || tw != got {
		t.Fatalf("ListSettingsByKey(editor.%%) = %s, twin %s", got, tw)
	}
	allThemes, err := store.ListSettingsByKey(ctx, env.ORM, user, "theme")
	if err != nil || len(allThemes) != 2 {
		t.Fatalf("ListSettingsByKey(theme) = %v, %v; want both scopes", keys(allThemes), err)
	}

	// UpdateSetting rewrites value and value_type and stamps updated_at.
	if err := store.ReplaceSettingValue(ctx, env.ORM, &core.Setting{ID: "s-b", Value: "14", ValueType: "int"}); err != nil {
		t.Fatalf("ReplaceSettingValue: %v", err)
	}
	size, err := env.Repo.GetSetting(ctx, user, nil, "editor.size")
	if err != nil || size.Value != "14" || size.ValueType != "int" || !size.UpdatedAt.After(base) || size.Key != "editor.size" {
		t.Fatalf("Repo.GetSetting after ReplaceSettingValue = %+v, %v", size, err)
	}
	if err := store.ReplaceSettingValue(ctx, env.ORM, &core.Setting{ID: "missing", Value: "x"}); err != nil {
		t.Fatalf("ReplaceSettingValue(missing) = %v, want nil", err)
	}

	if err := store.RemoveSetting(ctx, env.ORM, "s-a"); err != nil {
		t.Fatalf("RemoveSetting: %v", err)
	}
	if err := store.RemoveSetting(ctx, env.ORM, "s-a"); err != nil {
		t.Fatalf("RemoveSetting(again) = %v, want nil", err)
	}
	if _, err := env.Repo.GetSetting(ctx, user, nil, "editor.font"); err == nil {
		t.Fatalf("Repo.GetSetting after RemoveSetting: want not found")
	}
}

// TestItemDefaultAndPresetAssignmentQueries ports the item_defaults and
// default_preset_assignments reads of visibility.sql. Their rows are written
// by migrations only, so the fixtures go through the generated store.
func TestItemDefaultAndPresetAssignmentQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	workflow := reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW
	preset := reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET
	for _, d := range []*store.ItemDefault{
		{ID: "id-1", ItemType: workflow, Slug: "zz-hidden", IsHidden: true, Reason: strp("deprecated")},
		{ID: "id-2", ItemType: workflow, Slug: "zz-alpha-hidden", IsHidden: true},
		{ID: "id-3", ItemType: workflow, Slug: "zz-shown", IsHidden: false},
		{ID: "id-4", ItemType: preset, Slug: "zz-preset-hidden", IsHidden: true},
	} {
		if err := store.CreateItemDefault(ctx, env.ORM, d); err != nil {
			t.Fatalf("CreateItemDefault(%s): %v", d.Slug, err)
		}
	}

	got, err := store.GetItemDefault(ctx, env.ORM, int32(workflow), "zz-hidden")
	if err != nil || !got.IsHidden || got.Reason == nil || *got.Reason != "deprecated" || got.Slug != "zz-hidden" || got.ItemType != int32(workflow) {
		t.Fatalf("GetItemDefault = %+v, %v", got, err)
	}
	twin, err := env.Repo.GetItemDefault(ctx, int32(workflow), "zz-hidden")
	if err != nil || twin.IsHidden != got.IsHidden || twin.Reason == nil || *twin.Reason != *got.Reason || twin.Slug != got.Slug || twin.ItemType != got.ItemType {
		t.Fatalf("Repo.GetItemDefault = %+v, %v; want %+v", twin, err, got)
	}
	if _, err := store.GetItemDefault(ctx, env.ORM, int32(preset), "zz-hidden"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetItemDefault(other type) err = %v, want sql.ErrNoRows", err)
	}

	hidden, err := store.ListHiddenItemDefaults(ctx, env.ORM, int32(workflow))
	if err != nil {
		t.Fatalf("ListHiddenItemDefaults: %v", err)
	}
	twinHidden, err := env.Repo.ListHiddenItemDefaults(ctx, int32(workflow))
	if err != nil {
		t.Fatalf("Repo.ListHiddenItemDefaults: %v", err)
	}
	ours := []string{}
	for _, s := range hidden {
		if strings.HasPrefix(s, "zz-") {
			ours = append(ours, s)
		}
	}
	if strings.Join(ours, ",") != "zz-alpha-hidden,zz-hidden" || strings.Join(hidden, ",") != strings.Join(twinHidden, ",") {
		t.Fatalf("ListHiddenItemDefaults = %v (ours %v), twin %v; want the hidden workflows by slug", hidden, ours, twinHidden)
	}

	for _, a := range []*store.DefaultPresetAssignment{
		{ID: "dpa-1", WorkflowName: "zz-flow", GroupName: "planner", PresetSlug: "fast"},
		{ID: "dpa-2", WorkflowName: "zz-flow", GroupName: "coder", PresetSlug: "smart"},
		{ID: "dpa-3", WorkflowName: "zz-other", GroupName: "coder", PresetSlug: "cheap"},
	} {
		if err := store.CreateDefaultPresetAssignment(ctx, env.ORM, a); err != nil {
			t.Fatalf("CreateDefaultPresetAssignment(%s): %v", a.ID, err)
		}
	}
	assignments, err := store.GetDefaultPresetAssignments(ctx, env.ORM, "zz-flow")
	if err != nil || len(assignments) != 2 || assignments["planner"] != "fast" || assignments["coder"] != "smart" {
		t.Fatalf("GetDefaultPresetAssignments = %v, %v", assignments, err)
	}
	twinAssignments, err := env.Repo.GetDefaultPresetAssignments(ctx, "zz-flow")
	if err != nil || len(twinAssignments) != 2 || twinAssignments["coder"] != "smart" {
		t.Fatalf("Repo.GetDefaultPresetAssignments = %v, %v", twinAssignments, err)
	}
	if none, err := store.GetDefaultPresetAssignments(ctx, env.ORM, "zz-none"); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("GetDefaultPresetAssignments(none) = %v, %v; want an empty map", none, err)
	}
}
