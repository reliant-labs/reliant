// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestModelEndpointQueries ports the model_endpoints hand SQL, interleaved
// with the Repository. Every query is scoped to the owner.
func TestModelEndpointQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	owner, other := storetest.SeedUserID, "someone-else"

	direct := &core.ModelEndpoint{ID: "me-direct", UserID: owner, Name: "b-direct", BaseURL: "https://llm.example/v1", Route: core.ModelEndpointRouteDirect}
	if err := store.InsertModelEndpoint(ctx, env.ORM, direct); err != nil {
		t.Fatalf("InsertModelEndpoint: %v", err)
	}
	if direct.CreatedAt.IsZero() || direct.UpdatedAt.IsZero() {
		t.Fatalf("InsertModelEndpoint stamps = {%v, %v}, want both set", direct.CreatedAt, direct.UpdatedAt)
	}
	twin, err := env.Repo.GetModelEndpoint(ctx, owner, "me-direct")
	if err != nil {
		t.Fatalf("Repo.GetModelEndpoint: %v", err)
	}
	if twin.Name != "b-direct" || twin.ModelsJSON != "[]" || len(twin.HeaderNames) != 0 || twin.ProbeJSON != "" || twin.DaemonID != nil || twin.CredentialConnectionID != nil {
		t.Fatalf("Repo.GetModelEndpoint = %+v, want the store's write with the empty defaults", twin)
	}

	if err := env.Repo.CreateModelEndpoint(ctx, &core.ModelEndpoint{
		ID: "me-daemon", UserID: owner, Name: "a-daemon", BaseURL: "http://localhost:11434", Route: core.ModelEndpointRouteViaDaemon,
		DaemonID: strp("daemon-1"), CredentialConnectionID: strp("conn-1"), HeaderNames: []string{"X-Org", "X-Team"},
		ModelsJSON: `[{"id":"llama"}]`, ProbeJSON: `{"ok":true}`,
	}); err != nil {
		t.Fatalf("Repo.CreateModelEndpoint: %v", err)
	}
	got, err := store.GetModelEndpoint(ctx, env.ORM, owner, "me-daemon")
	if err != nil {
		t.Fatalf("GetModelEndpoint: %v", err)
	}
	if *got.DaemonID != "daemon-1" || *got.CredentialConnectionID != "conn-1" || len(got.HeaderNames) != 2 || got.HeaderNames[1] != "X-Team" ||
		got.ModelsJSON != `[{"id":"llama"}]` || got.ProbeJSON != `{"ok":true}` || got.Route != core.ModelEndpointRouteViaDaemon {
		t.Fatalf("GetModelEndpoint = %+v, want the Repository's write", got)
	}
	if _, err := store.GetModelEndpoint(ctx, env.ORM, other, "me-daemon"); !errors.Is(err, core.ErrModelEndpointNotFound) {
		t.Fatalf("GetModelEndpoint(another user) err = %v, want ErrModelEndpointNotFound", err)
	}

	// The name is unique per user, through either layer.
	if err := store.InsertModelEndpoint(ctx, env.ORM, &core.ModelEndpoint{ID: "me-dup", UserID: owner, Name: "a-daemon", BaseURL: "x", Route: core.ModelEndpointRouteDirect}); !errors.Is(err, core.ErrModelEndpointNameTaken) {
		t.Fatalf("InsertModelEndpoint(duplicate name) err = %v, want ErrModelEndpointNameTaken", err)
	}
	if err := store.InsertModelEndpoint(ctx, env.ORM, &core.ModelEndpoint{ID: "me-theirs", UserID: other, Name: "a-daemon", BaseURL: "x", Route: core.ModelEndpointRouteDirect}); err != nil {
		t.Fatalf("InsertModelEndpoint(same name, other user): %v", err)
	}

	listed, err := store.ListModelEndpoints(ctx, env.ORM, owner)
	if err != nil {
		t.Fatalf("ListModelEndpoints: %v", err)
	}
	twinListed, err := env.Repo.ListModelEndpoints(ctx, owner)
	if err != nil {
		t.Fatalf("Repo.ListModelEndpoints: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != "me-daemon" || listed[1].ID != "me-direct" || len(twinListed) != 2 || twinListed[0].ID != "me-daemon" {
		t.Fatalf("ListModelEndpoints = %v, twin %v; want [me-daemon me-direct] by name", listed, twinListed)
	}
	if none, err := store.ListModelEndpoints(ctx, env.ORM, "nobody"); err != nil || none != nil {
		t.Fatalf("ListModelEndpoints(nobody) = %v, %v; want nil", none, err)
	}

	// Update replaces every mutable column, for the owner only.
	before := direct.UpdatedAt
	replace := &core.ModelEndpoint{ID: "me-direct", UserID: owner, Name: "c-direct", BaseURL: "https://llm.example/v2", Route: core.ModelEndpointRouteDirect,
		HeaderNames: []string{"Authorization"}, ModelsJSON: `[{"id":"m"}]`}
	if err := store.ReplaceModelEndpoint(ctx, env.ORM, replace); err != nil {
		t.Fatalf("ReplaceModelEndpoint: %v", err)
	}
	twin, err = env.Repo.GetModelEndpoint(ctx, owner, "me-direct")
	if err != nil || twin.Name != "c-direct" || twin.BaseURL != "https://llm.example/v2" || len(twin.HeaderNames) != 1 || twin.ModelsJSON != `[{"id":"m"}]` ||
		!twin.UpdatedAt.After(before) || !sameInstant(twin.UpdatedAt, replace.UpdatedAt) || !sameInstant(twin.CreatedAt, direct.CreatedAt) {
		t.Fatalf("Repo.GetModelEndpoint after ReplaceModelEndpoint = %+v, %v", twin, err)
	}
	if err := store.ReplaceModelEndpoint(ctx, env.ORM, &core.ModelEndpoint{ID: "me-direct", UserID: other, Name: "stolen", BaseURL: "x", Route: core.ModelEndpointRouteDirect}); !errors.Is(err, core.ErrModelEndpointNotFound) {
		t.Fatalf("ReplaceModelEndpoint(another user) err = %v, want ErrModelEndpointNotFound", err)
	}
	if err := store.ReplaceModelEndpoint(ctx, env.ORM, &core.ModelEndpoint{ID: "me-direct", UserID: owner, Name: "a-daemon", BaseURL: "x", Route: core.ModelEndpointRouteDirect}); !errors.Is(err, core.ErrModelEndpointNameTaken) {
		t.Fatalf("ReplaceModelEndpoint(taken name) err = %v, want ErrModelEndpointNameTaken", err)
	}
	if err := env.Repo.UpdateModelEndpoint(ctx, &core.ModelEndpoint{ID: "me-daemon", UserID: owner, Name: "a-daemon", BaseURL: "http://localhost:1", Route: core.ModelEndpointRouteDirect}); err != nil {
		t.Fatalf("Repo.UpdateModelEndpoint: %v", err)
	}
	got, err = store.GetModelEndpoint(ctx, env.ORM, owner, "me-daemon")
	if err != nil || got.DaemonID != nil || got.CredentialConnectionID != nil || len(got.HeaderNames) != 0 || got.ModelsJSON != "[]" || got.ProbeJSON != "" {
		t.Fatalf("GetModelEndpoint after Repo.UpdateModelEndpoint = %+v, %v; want the optionals cleared", got, err)
	}

	// The probe write touches probe_json alone.
	if err := store.SetModelEndpointProbe(ctx, env.ORM, owner, "me-direct", `{"latency_ms":12}`); err != nil {
		t.Fatalf("SetModelEndpointProbe: %v", err)
	}
	if err := store.SetModelEndpointProbe(ctx, env.ORM, owner, "gone", `{}`); err != nil {
		t.Fatalf("SetModelEndpointProbe(missing) = %v, want nil", err)
	}
	if err := store.SetModelEndpointProbe(ctx, env.ORM, other, "me-direct", `{"stolen":true}`); err != nil {
		t.Fatalf("SetModelEndpointProbe(another user) = %v, want nil", err)
	}
	probed, err := env.Repo.GetModelEndpoint(ctx, owner, "me-direct")
	if err != nil || probed.ProbeJSON != `{"latency_ms":12}` || !probed.UpdatedAt.Equal(twin.UpdatedAt) || probed.Name != "c-direct" {
		t.Fatalf("Repo.GetModelEndpoint after SetModelEndpointProbe = %+v, %v; want only probe_json changed", probed, err)
	}

	if err := store.RemoveModelEndpoint(ctx, env.ORM, other, "me-direct"); !errors.Is(err, core.ErrModelEndpointNotFound) {
		t.Fatalf("RemoveModelEndpoint(another user) err = %v, want ErrModelEndpointNotFound", err)
	}
	if err := store.RemoveModelEndpoint(ctx, env.ORM, owner, "me-direct"); err != nil {
		t.Fatalf("RemoveModelEndpoint: %v", err)
	}
	if err := store.RemoveModelEndpoint(ctx, env.ORM, owner, "me-direct"); !errors.Is(err, core.ErrModelEndpointNotFound) {
		t.Fatalf("RemoveModelEndpoint(again) err = %v, want ErrModelEndpointNotFound", err)
	}
	if _, err := env.Repo.GetModelEndpoint(ctx, owner, "me-direct"); !errors.Is(err, core.ErrModelEndpointNotFound) {
		t.Fatalf("Repo.GetModelEndpoint after RemoveModelEndpoint err = %v", err)
	}
}

// TestProjectConfigQueries ports the project_configs hand SQL, interleaved
// with the Repository.
func TestProjectConfigQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	project := storetest.SeedProjectID

	for _, read := range []func() error{
		func() error { _, err := store.GetProjectConfigRecord(ctx, env.ORM, project); return err },
		func() error { _, err := store.GetProjectConfigPushedAt(ctx, env.ORM, project); return err },
		func() error { _, err := store.GetProjectConfigVersion(ctx, env.ORM, project); return err },
		func() error { _, err := store.GetProjectWorkflowsJSON(ctx, env.ORM, project); return err },
	} {
		// No record is a classified miss: NotFound for a handler, and still
		// sql.ErrNoRows for the callers that read it as an empty config.
		if err := read(); !svcerr.IsNotFound(err) || !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("read with no record err = %v, want svcerr.NotFound and sql.ErrNoRows", err)
		}
	}

	pushed := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	rec := &core.ProjectConfigRecord{
		ID: "pc-1", ProjectID: project, DaemonID: "daemon-1",
		UserConfigYAML: strp("user: 1"), ProjectConfigYAML: strp("project: 1"), MCPConfigs: strp(`{"servers":{}}`),
		ProjectWorkflowsJSON: strp(`[{"name":"wf"}]`), ProjectPresetsJSON: strp(`[]`), ProjectScenariosJSON: strp(`[{"s":1}]`),
		ProjectSkillsJSON: strp(`[{"skill":"big"}]`), RuntimeType: strp("kata"), PushedAt: pushed,
	}
	if err := store.UpsertProjectConfigRecord(ctx, env.ORM, rec); err != nil {
		t.Fatalf("UpsertProjectConfigRecord: %v", err)
	}
	twin, err := env.Repo.GetProjectConfigRecord(ctx, project)
	if err != nil {
		t.Fatalf("Repo.GetProjectConfigRecord: %v", err)
	}
	if twin.ID != "pc-1" || twin.DaemonID != "daemon-1" || *twin.UserConfigYAML != "user: 1" || twin.LocalConfigYAML != nil || *twin.RuntimeType != "kata" ||
		!twin.PushedAt.Equal(pushed) || twin.CreatedAt.IsZero() || *twin.ProjectSkillsJSON != `[{"skill":"big"}]` {
		t.Fatalf("Repo.GetProjectConfigRecord = %+v, want the store's write", twin)
	}
	firstCreated := twin.CreatedAt

	v1, err := store.GetProjectConfigVersion(ctx, env.ORM, project)
	if err != nil {
		t.Fatalf("GetProjectConfigVersion: %v", err)
	}
	twinV1, err := env.Repo.GetProjectConfigVersion(ctx, project)
	if err != nil || twinV1 != v1 {
		t.Fatalf("Repo.GetProjectConfigVersion = %q, %v; want the store's %q", twinV1, err, v1)
	}
	at, err := store.GetProjectConfigPushedAt(ctx, env.ORM, project)
	if err != nil || !at.Equal(pushed) {
		t.Fatalf("GetProjectConfigPushedAt = %v, %v; want %v", at, err, pushed)
	}

	for name, read := range map[string]func(context.Context, string) (*string, error){
		"workflows": func(ctx context.Context, p string) (*string, error) {
			return store.GetProjectWorkflowsJSON(ctx, env.ORM, p)
		},
		"presets": func(ctx context.Context, p string) (*string, error) {
			return store.GetProjectPresetsJSON(ctx, env.ORM, p)
		},
		"scenarios": func(ctx context.Context, p string) (*string, error) {
			return store.GetProjectScenariosJSON(ctx, env.ORM, p)
		},
		"mcp": func(ctx context.Context, p string) (*string, error) {
			return store.GetProjectMCPConfigsJSON(ctx, env.ORM, p)
		},
	} {
		want := map[string]string{"workflows": `[{"name":"wf"}]`, "presets": `[]`, "scenarios": `[{"s":1}]`, "mcp": `{"servers":{}}`}[name]
		got, err := read(ctx, project)
		if err != nil || got == nil || *got != want {
			t.Fatalf("project config column %s = %v, %v; want %s", name, got, err, want)
		}
	}
	if tw, err := env.Repo.GetProjectWorkflowsJSON(ctx, project); err != nil || *tw != `[{"name":"wf"}]` {
		t.Fatalf("Repo.GetProjectWorkflowsJSON = %v, %v", tw, err)
	}

	// The Repository rewrites the record: every column but id and created_at
	// is replaced, a NULL included, and the version token moves.
	if err := env.Repo.UpsertProjectConfigRecord(ctx, &core.ProjectConfigRecord{ID: "pc-ignored", ProjectID: project, DaemonID: "daemon-2", LocalConfigYAML: strp("local: 1")}); err != nil {
		t.Fatalf("Repo.UpsertProjectConfigRecord: %v", err)
	}
	got, err := store.GetProjectConfigRecord(ctx, env.ORM, project)
	if err != nil {
		t.Fatalf("GetProjectConfigRecord: %v", err)
	}
	if got.ID != "pc-1" || got.DaemonID != "daemon-2" || got.UserConfigYAML != nil || *got.LocalConfigYAML != "local: 1" || got.ProjectWorkflowsJSON != nil ||
		got.RuntimeType != nil || !got.CreatedAt.Equal(firstCreated) || !got.PushedAt.After(pushed) {
		t.Fatalf("GetProjectConfigRecord after the Repository's upsert = %+v", got)
	}
	if wf, err := store.GetProjectWorkflowsJSON(ctx, env.ORM, project); err != nil || wf != nil {
		t.Fatalf("GetProjectWorkflowsJSON after the column was cleared = %v, %v; want (nil, nil)", wf, err)
	}
	v2, err := store.GetProjectConfigVersion(ctx, env.ORM, project)
	if err != nil || v2 == v1 {
		t.Fatalf("GetProjectConfigVersion after a rewrite = %q, %v; want it changed from %q", v2, err, v1)
	}
}
