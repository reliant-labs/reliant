// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// newStoreTrigger is a trigger with every column the definition carries set,
// so a round trip through either layer proves each one.
func newStoreTrigger(id, name string, at time.Time) *core.Trigger {
	config, _ := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * 1-5"}, Timezone: "UTC", Overlap: core.ScheduleOverlapSkip})
	return &core.Trigger{
		ID: id, UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, Name: name,
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "nightly-audit",
		Presets:  map[string]string{"": "careful", "Critic": "strict"},
		Params:   map[string]any{"depth": float64(3), "unattended": true},
		Message:  "audit the repo",
		DaemonID: "daemon-trg", Config: config, Filter: "true",
		NotifyOnComplete: true, ActingIdentity: core.ActingIdentityApp,
		CreatedAt: at, UpdatedAt: at,
	}
}

func sameValue(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got %+v\nwant %+v", what, got, want)
	}
}

// TestTriggerQueries ports the triggers queries of triggers.sql, interleaved
// with the Repository.
func TestTriggerQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	host := "build-box"
	if err := env.Repo.UpsertDaemon(ctx, &db.Daemon{ID: "daemon-trg", UserID: storetest.SeedUserID, Hostname: &host}); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	// One trigger written by each layer, each read back through both.
	a := newStoreTrigger("trg-a", "a", base)
	if err := store.InsertTrigger(ctx, env.ORM, a); err != nil {
		t.Fatalf("InsertTrigger: %v", err)
	}
	b := newStoreTrigger("trg-b", "b", base.Add(time.Minute))
	b.DaemonID, b.NoMachine, b.ActingIdentity, b.Presets, b.Params = "", true, "", nil, nil
	if err := env.Repo.CreateTrigger(ctx, b); err != nil {
		t.Fatalf("Repo.CreateTrigger: %v", err)
	}
	for _, id := range []string{"trg-a", "trg-b"} {
		port, err := store.GetTrigger(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("GetTrigger(%s): %v", id, err)
		}
		twin, err := env.Repo.GetTrigger(ctx, id)
		if err != nil {
			t.Fatalf("Repo.GetTrigger(%s): %v", id, err)
		}
		sameValue(t, "GetTrigger("+id+")", port, twin)
	}
	got, _ := store.GetTrigger(ctx, env.ORM, "trg-a")
	if got.DaemonName != host || got.ProjectName == "" || got.ActingIdentity != core.ActingIdentityApp || !got.CreatedAt.Equal(base) || !got.UpdatedAt.Equal(base) || got.Presets["Critic"] != "strict" || !got.NotifyOnComplete || got.Filter != "true" {
		t.Fatalf("GetTrigger(trg-a) = %+v, want the written definition with display names", got)
	}
	got, _ = store.GetTrigger(ctx, env.ORM, "trg-b")
	if got.DaemonID != "" || !got.NoMachine || got.ActingIdentity != core.ActingIdentityOwner || got.Presets == nil || got.Params == nil || got.DaemonName != "" {
		t.Fatalf("GetTrigger(trg-b) = %+v, want no daemon, the owner identity and empty (non-nil) maps", got)
	}
	if _, err := store.GetTrigger(ctx, env.ORM, "missing"); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Fatalf("GetTrigger(missing) err = %v, want ErrTriggerNotFound", err)
	}

	// Another project's trigger, for the project filter.
	if err := env.Repo.CreateProject(ctx, &db.Project{ID: "proj-trg-2", Name: "second", Path: "/tmp/second", UserID: storetest.SeedUserID}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c := newStoreTrigger("trg-c", "c", base.Add(2*time.Minute))
	c.ProjectID = "proj-trg-2"
	if err := store.InsertTrigger(ctx, env.ORM, c); err != nil {
		t.Fatalf("InsertTrigger(c): %v", err)
	}
	ids := func(ts []*core.Trigger) []string {
		out := make([]string, len(ts))
		for i, tr := range ts {
			out[i] = tr.ID
		}
		return out
	}
	project := storetest.SeedProjectID
	for _, f := range []core.TriggerFilters{{UserID: storetest.SeedUserID}, {UserID: storetest.SeedUserID, ProjectID: &project}, {UserID: "nobody"}} {
		port, err := store.ListTriggersForUser(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("ListTriggersForUser(%+v): %v", f, err)
		}
		twin, err := env.Repo.ListTriggers(ctx, f)
		if err != nil {
			t.Fatalf("Repo.ListTriggers(%+v): %v", f, err)
		}
		sameValue(t, "ListTriggersForUser", port, twin)
	}
	if mine, _ := store.ListTriggersForUser(ctx, env.ORM, core.TriggerFilters{UserID: storetest.SeedUserID, ProjectID: &project}); !reflect.DeepEqual(ids(mine), []string{"trg-b", "trg-a"}) {
		t.Fatalf("ListTriggersForUser(project) = %v, want [trg-b trg-a], newest first", ids(mine))
	}
	all, err := store.ListAllTriggers(ctx, env.ORM)
	if err != nil {
		t.Fatalf("ListAllTriggers: %v", err)
	}
	twinAll, err := env.Repo.ListAllTriggers(ctx)
	if err != nil {
		t.Fatalf("Repo.ListAllTriggers: %v", err)
	}
	sameValue(t, "ListAllTriggers", all, twinAll)
	if g := ids(all); !reflect.DeepEqual(g, []string{"trg-c", "trg-b", "trg-a"}) {
		t.Fatalf("ListAllTriggers = %v, want newest first", g)
	}

	// LockTrigger holds inside a transaction; a missing trigger is not found.
	if err := env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		return store.LockTrigger(ctx, env.ORM, "trg-a")
	}); err != nil {
		t.Fatalf("LockTrigger: %v", err)
	}
	if err := store.LockTrigger(ctx, env.ORM, "missing"); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Fatalf("LockTrigger(missing) err = %v, want ErrTriggerNotFound", err)
	}

	// ReplaceTrigger writes the definition and stamps updated_at, and never
	// the identity columns.
	edited, _ := env.Repo.GetTrigger(ctx, "trg-a")
	edited.Name, edited.Enabled, edited.DaemonID, edited.NoMachine = "a-edited", false, "", true
	edited.Kind, edited.UserID = core.TriggerKindWebhook, "someone-else"
	edited.UpdatedAt = base.Add(30 * time.Minute)
	if err := store.ReplaceTrigger(ctx, env.ORM, edited); err != nil {
		t.Fatalf("ReplaceTrigger: %v", err)
	}
	twin, _ := env.Repo.GetTrigger(ctx, "trg-a")
	if twin.Name != "a-edited" || twin.Enabled || twin.DaemonID != "" || !twin.NoMachine || !twin.UpdatedAt.After(edited.UpdatedAt) || twin.Kind != core.TriggerKindSchedule || twin.UserID != storetest.SeedUserID {
		t.Fatalf("after ReplaceTrigger Repo.GetTrigger = %+v, want the edit, updated_at stamped, kind and owner kept", twin)
	}
	twin.Name = "a-twin"
	if err := env.Repo.UpdateTrigger(ctx, twin); err != nil {
		t.Fatalf("Repo.UpdateTrigger: %v", err)
	}
	if got, _ := store.GetTrigger(ctx, env.ORM, "trg-a"); got.Name != "a-twin" {
		t.Fatalf("GetTrigger after Repo.UpdateTrigger name = %q", got.Name)
	}
	if err := store.ReplaceTrigger(ctx, env.ORM, newStoreTrigger("missing", "m", base)); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Fatalf("ReplaceTrigger(missing) err = %v, want ErrTriggerNotFound", err)
	}

	// SetTriggerEnabled stamps updated_at.
	replaced, _ := env.Repo.GetTrigger(ctx, "trg-a")
	if err := store.SetTriggerEnabled(ctx, env.ORM, "trg-a", true); err != nil {
		t.Fatalf("SetTriggerEnabled: %v", err)
	}
	if twin, _ := env.Repo.GetTrigger(ctx, "trg-a"); !twin.Enabled || !twin.UpdatedAt.After(replaced.UpdatedAt) {
		t.Fatalf("after SetTriggerEnabled = %+v, want enabled and updated_at stamped", twin)
	}
	if err := store.SetTriggerEnabled(ctx, env.ORM, "missing", true); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Fatalf("SetTriggerEnabled(missing) err = %v, want ErrTriggerNotFound", err)
	}

	// Webhook credentials: their own writes, read by both layers; nil clears.
	if err := store.SetTriggerWebhookTokenHash(ctx, env.ORM, "trg-b", []byte("hash")); err != nil {
		t.Fatalf("SetTriggerWebhookTokenHash: %v", err)
	}
	if err := store.SetTriggerWebhookSecret(ctx, env.ORM, "trg-b", []byte("sealed")); err != nil {
		t.Fatalf("SetTriggerWebhookSecret: %v", err)
	}
	creds, err := store.GetTriggerWebhookCredentials(ctx, env.ORM, "trg-b")
	if err != nil {
		t.Fatalf("GetTriggerWebhookCredentials: %v", err)
	}
	twinCreds, err := env.Repo.GetTriggerWebhookCredentials(ctx, "trg-b")
	if err != nil {
		t.Fatalf("Repo.GetTriggerWebhookCredentials: %v", err)
	}
	sameValue(t, "GetTriggerWebhookCredentials", creds, twinCreds)
	if string(creds.TokenHash) != "hash" || string(creds.SecretSealed) != "sealed" {
		t.Fatalf("credentials = %+v", creds)
	}
	if err := env.Repo.SetTriggerWebhookSecret(ctx, "trg-b", nil); err != nil {
		t.Fatalf("Repo.SetTriggerWebhookSecret(nil): %v", err)
	}
	if err := store.SetTriggerWebhookSecret(ctx, env.ORM, "trg-b", nil); err != nil {
		t.Fatalf("SetTriggerWebhookSecret(nil): %v", err)
	}
	if creds, _ := store.GetTriggerWebhookCredentials(ctx, env.ORM, "trg-b"); creds.SecretSealed != nil || string(creds.TokenHash) != "hash" {
		t.Fatalf("after clearing the secret = %+v, want hash kept and secret NULL", creds)
	}
	if def, _ := store.GetTrigger(ctx, env.ORM, "trg-b"); def.Name != "b" {
		t.Fatalf("a credential write touched the definition: %+v", def)
	}
	for _, write := range []func() error{
		func() error { return store.SetTriggerWebhookTokenHash(ctx, env.ORM, "missing", []byte("h")) },
		func() error { return store.SetTriggerWebhookSecret(ctx, env.ORM, "missing", nil) },
		func() error { _, err := store.GetTriggerWebhookCredentials(ctx, env.ORM, "missing"); return err },
	} {
		if err := write(); !errors.Is(err, core.ErrTriggerNotFound) {
			t.Fatalf("webhook credentials on a missing trigger err = %v, want ErrTriggerNotFound", err)
		}
	}

	// Activations: only a row naming a declared trigger takes a projection.
	if err := store.SetTriggerProjection(ctx, env.ORM, "trg-c", core.TriggerProjection{Filter: "x"}); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Fatalf("SetTriggerProjection(ad hoc) err = %v, want ErrTriggerNotFound", err)
	}
	act := newStoreTrigger("trg-act", "act", base.Add(3*time.Minute))
	act.WorkflowTrigger = strp("nightly")
	if err := store.InsertTrigger(ctx, env.ORM, act); err != nil {
		t.Fatalf("InsertTrigger(activation): %v", err)
	}
	if err := env.Repo.CreateTrigger(ctx, func() *core.Trigger {
		tr := newStoreTrigger("trg-act2", "act2", base.Add(4*time.Minute))
		tr.WorkflowTrigger = strp("weekly")
		return tr
	}()); err != nil {
		t.Fatalf("Repo.CreateTrigger(activation): %v", err)
	}
	if err := store.SetTriggerProjection(ctx, env.ORM, "trg-act", core.TriggerProjection{Config: json.RawMessage(`{"cron":["0 * * * *"]}`), Filter: "trigger.ok"}); err != nil {
		t.Fatalf("SetTriggerProjection: %v", err)
	}
	if err := env.Repo.SetTriggerProjection(ctx, "trg-act2", core.TriggerProjection{}); err != nil {
		t.Fatalf("Repo.SetTriggerProjection: %v", err)
	}
	if twin, _ := env.Repo.GetTrigger(ctx, "trg-act"); twin.Filter != "trigger.ok" || string(twin.Config) != `{"cron": ["0 * * * *"]}` || !twin.UpdatedAt.After(act.UpdatedAt) {
		t.Fatalf("after SetTriggerProjection = %+v (config %s)", twin, twin.Config)
	}
	if got, _ := store.GetTrigger(ctx, env.ORM, "trg-act2"); got.Filter != "" || string(got.Config) != `{}` {
		t.Fatalf("after Repo.SetTriggerProjection(empty) = %+v (config %s), want an empty object", got, got.Config)
	}
	acts, err := store.ListAllWorkflowTriggerActivations(ctx, env.ORM)
	if err != nil {
		t.Fatalf("ListAllWorkflowTriggerActivations: %v", err)
	}
	twinActs, err := env.Repo.ListAllWorkflowTriggerActivations(ctx)
	if err != nil {
		t.Fatalf("Repo.ListAllWorkflowTriggerActivations: %v", err)
	}
	sameValue(t, "ListAllWorkflowTriggerActivations", acts, twinActs)
	if g := ids(acts); !reflect.DeepEqual(g, []string{"trg-act", "trg-act2"}) {
		t.Fatalf("ListAllWorkflowTriggerActivations = %v", g)
	}

	// Integration routes: through the owner's live connection only.
	for _, conn := range []struct{ id, user, status, account string }{
		{"conn-live", storetest.SeedUserID, "active", "acct-1"},
		{"conn-reauth", storetest.SeedUserID, "needs_reauth", ""},
		{"conn-other", "someone-else", "active", "acct-2"},
	} {
		var account any
		if conn.account != "" {
			account = conn.account
		}
		if _, err := env.SQL.ExecContext(ctx, `INSERT INTO connections (id, user_id, integration_id, auth_kind, name, status, external_account_id)
			VALUES ($1, $2, 'github', 'oauth2', $1, $3, $4)`, conn.id, conn.user, conn.status, account); err != nil {
			t.Fatalf("insert connection %s: %v", conn.id, err)
		}
	}
	integration := func(id, conn string, enabled bool, which string) *core.Trigger {
		tr := newStoreTrigger(id, id, base.Add(5*time.Minute))
		tr.Kind, tr.Enabled, tr.ConnectionID = core.TriggerKindIntegration, enabled, &conn
		tr.Config = json.RawMessage(`{"integration":"` + which + `"}`)
		return tr
	}
	for _, tr := range []*core.Trigger{
		integration("trg-int-live", "conn-live", true, "github"),
		integration("trg-int-reauth", "conn-reauth", true, "github"),
		integration("trg-int-off", "conn-live", false, "github"),
		integration("trg-int-slack", "conn-live", true, "slack"),
		integration("trg-int-foreign", "conn-other", true, "github"),
	} {
		if err := store.InsertTrigger(ctx, env.ORM, tr); err != nil {
			t.Fatalf("InsertTrigger(%s): %v", tr.ID, err)
		}
	}
	routes, err := store.ListIntegrationTriggers(ctx, env.ORM, "github")
	if err != nil {
		t.Fatalf("ListIntegrationTriggers: %v", err)
	}
	twinRoutes, err := env.Repo.ListIntegrationTriggers(ctx, "github")
	if err != nil {
		t.Fatalf("Repo.ListIntegrationTriggers: %v", err)
	}
	sameValue(t, "ListIntegrationTriggers", routes, twinRoutes)
	if len(routes) != 2 || routes[0].Trigger.ID != "trg-int-live" || routes[0].ConnectionAccount != "acct-1" || routes[0].ConnectionStatus != "active" ||
		routes[1].Trigger.ID != "trg-int-reauth" || routes[1].ConnectionAccount != "" || routes[1].ConnectionStatus != "needs_reauth" {
		t.Fatalf("ListIntegrationTriggers(github) = %+v, want live and reauth through the owner's connections", routes)
	}

	// RemoveTrigger: gone from both layers; a missing one is not an error.
	if err := store.RemoveTrigger(ctx, env.ORM, "trg-c"); err != nil {
		t.Fatalf("RemoveTrigger: %v", err)
	}
	if _, err := env.Repo.GetTrigger(ctx, "trg-c"); !errors.Is(err, core.ErrTriggerNotFound) {
		t.Fatalf("Repo.GetTrigger after RemoveTrigger err = %v", err)
	}
	if err := store.RemoveTrigger(ctx, env.ORM, "trg-c"); err != nil {
		t.Fatalf("RemoveTrigger(missing): %v", err)
	}
	if err := env.Repo.DeleteTrigger(ctx, "trg-c"); err != nil {
		t.Fatalf("Repo.DeleteTrigger(missing): %v", err)
	}
}

// TestTriggerRegistrationQueries ports the trigger_registrations queries of
// triggers.sql, interleaved with the Repository.
func TestTriggerRegistrationQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, id := range []string{"trg-reg-a", "trg-reg-b"} {
		if err := store.InsertTrigger(ctx, env.ORM, newStoreTrigger(id, id, base)); err != nil {
			t.Fatalf("InsertTrigger(%s): %v", id, err)
		}
	}
	foreign := newStoreTrigger("trg-reg-foreign", "foreign", base)
	foreign.UserID = "someone-else"
	if err := store.InsertTrigger(ctx, env.ORM, foreign); err != nil {
		t.Fatalf("InsertTrigger(foreign): %v", err)
	}

	if _, err := store.GetTriggerRegistration(ctx, env.ORM, "trg-reg-a"); !errors.Is(err, core.ErrTriggerRegistrationNotFound) {
		t.Fatalf("GetTriggerRegistration(none) err = %v, want ErrTriggerRegistrationNotFound", err)
	}
	polled := base.Add(time.Minute)
	if err := store.UpsertTriggerRegistration(ctx, env.ORM, &core.TriggerRegistration{
		TriggerID: "trg-reg-a", Provider: "gmail", Cursor: "c1", LastPolledAt: &polled, LastGapDetail: "",
	}); err != nil {
		t.Fatalf("UpsertTriggerRegistration: %v", err)
	}
	twin, err := env.Repo.GetTriggerRegistration(ctx, "trg-reg-a")
	if err != nil {
		t.Fatalf("Repo.GetTriggerRegistration: %v", err)
	}
	port, err := store.GetTriggerRegistration(ctx, env.ORM, "trg-reg-a")
	if err != nil {
		t.Fatalf("GetTriggerRegistration: %v", err)
	}
	sameValue(t, "GetTriggerRegistration", port, twin)
	if port.Status != core.TriggerRegistrationActive || port.Cursor != "c1" || !port.LastPolledAt.Equal(polled) || port.LastGapAt != nil {
		t.Fatalf("registration = %+v, want active with the cursor", port)
	}

	// status_since moves only when the status changes.
	old := base.Add(-24 * time.Hour)
	if _, err := env.SQL.ExecContext(ctx, `UPDATE trigger_registrations SET status_since = $1 WHERE trigger_id = 'trg-reg-a'`, old); err != nil {
		t.Fatalf("backdate status_since: %v", err)
	}
	gap := base.Add(2 * time.Minute)
	if err := env.Repo.UpsertTriggerRegistration(ctx, &core.TriggerRegistration{
		TriggerID: "trg-reg-a", Provider: "gmail", Cursor: "c2", Status: core.TriggerRegistrationActive, LastGapAt: &gap, LastGapDetail: "rebaselined",
	}); err != nil {
		t.Fatalf("Repo.UpsertTriggerRegistration(same status): %v", err)
	}
	port, _ = store.GetTriggerRegistration(ctx, env.ORM, "trg-reg-a")
	if !port.StatusSince.Equal(old) || port.Cursor != "c2" || port.LastPolledAt != nil || !port.LastGapAt.Equal(gap) || port.LastGapDetail != "rebaselined" {
		t.Fatalf("after a same-status upsert = %+v, want status_since kept and the rest replaced", port)
	}
	if err := store.UpsertTriggerRegistration(ctx, env.ORM, &core.TriggerRegistration{
		TriggerID: "trg-reg-a", Provider: "gmail", Cursor: "c2", Status: core.TriggerRegistrationNeedsReauth, StatusDetail: "token revoked",
	}); err != nil {
		t.Fatalf("UpsertTriggerRegistration(new status): %v", err)
	}
	twin, _ = env.Repo.GetTriggerRegistration(ctx, "trg-reg-a")
	if twin.StatusSince.Equal(old) || twin.Status != core.TriggerRegistrationNeedsReauth || twin.StatusDetail != "token revoked" {
		t.Fatalf("after a status change = %+v, want status_since moved", twin)
	}

	for _, id := range []string{"trg-reg-b", "trg-reg-foreign"} {
		if err := env.Repo.UpsertTriggerRegistration(ctx, &core.TriggerRegistration{TriggerID: id, Provider: "github", RegistrationID: "hook-" + id}); err != nil {
			t.Fatalf("Repo.UpsertTriggerRegistration(%s): %v", id, err)
		}
	}
	for _, ids := range [][]string{{"trg-reg-a", "trg-reg-b", "trg-reg-foreign", "missing"}, {}} {
		port, err := store.ListTriggerRegistrationsForUser(ctx, env.ORM, storetest.SeedUserID, ids)
		if err != nil {
			t.Fatalf("ListTriggerRegistrationsForUser(%v): %v", ids, err)
		}
		twin, err := env.Repo.ListTriggerRegistrations(ctx, storetest.SeedUserID, ids)
		if err != nil {
			t.Fatalf("Repo.ListTriggerRegistrations(%v): %v", ids, err)
		}
		sameValue(t, "ListTriggerRegistrationsForUser", port, twin)
	}
	mine, _ := store.ListTriggerRegistrationsForUser(ctx, env.ORM, storetest.SeedUserID, []string{"trg-reg-a", "trg-reg-b", "trg-reg-foreign"})
	if len(mine) != 2 || mine["trg-reg-b"].RegistrationID != "hook-trg-reg-b" || mine["trg-reg-foreign"] != nil {
		t.Fatalf("ListTriggerRegistrationsForUser = %+v, want only the caller's two", mine)
	}
}

// TestListAccessRoutedTriggers ports integration_access.sql's
// ListAccessRoutedTriggers, interleaved with the Repository.
func TestListAccessRoutedTriggers(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, conn := range []struct{ id, user, status string }{
		{"conn-ok", storetest.SeedUserID, "active"},
		{"conn-reauth", storetest.SeedUserID, "needs_reauth"},
		{"conn-foreign", "someone-else", "active"},
	} {
		if _, err := env.SQL.ExecContext(ctx, `INSERT INTO connections (id, user_id, integration_id, auth_kind, name, status)
			VALUES ($1, $2, 'github', 'oauth2', $1, $3)`, conn.id, conn.user, conn.status); err != nil {
			t.Fatalf("insert connection %s: %v", conn.id, err)
		}
	}
	routed := func(id string, conn *string, enabled bool, which string) {
		t.Helper()
		tr := newStoreTrigger(id, id, now)
		tr.Kind, tr.Enabled, tr.ConnectionID = core.TriggerKindIntegration, enabled, conn
		tr.Config = json.RawMessage(`{"integration":"` + which + `"}`)
		if err := store.InsertTrigger(ctx, env.ORM, tr); err != nil {
			t.Fatalf("InsertTrigger(%s): %v", id, err)
		}
	}
	routed("trg-acc-none", nil, true, "github")
	routed("trg-acc-ok", strp("conn-ok"), true, "github")
	routed("trg-acc-reauth", strp("conn-reauth"), true, "github")
	routed("trg-acc-foreign-conn", strp("conn-foreign"), true, "github")
	routed("trg-acc-off", nil, false, "github")
	routed("trg-acc-slack", nil, true, "slack")
	if err := env.Repo.ReplaceIntegrationAccess(ctx, storetest.SeedUserID, "github", "11", now, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1"},
	}); err != nil {
		t.Fatalf("ReplaceIntegrationAccess: %v", err)
	}

	for _, c := range []struct {
		account, resource string
		fresh             time.Time
		want              []string
	}{
		{"100", "1", now.Add(-time.Minute), []string{"trg-acc-none", "trg-acc-ok"}},
		{"100", "2", now.Add(-time.Minute), nil},
		{"200", "1", now.Add(-time.Minute), nil},
		{"100", "1", now.Add(time.Minute), nil},
	} {
		port, err := store.ListAccessRoutedTriggers(ctx, env.ORM, "github", c.account, c.resource, c.fresh)
		if err != nil {
			t.Fatalf("ListAccessRoutedTriggers(%s/%s): %v", c.account, c.resource, err)
		}
		twin, err := env.Repo.ListAccessRoutedTriggers(ctx, "github", c.account, c.resource, c.fresh)
		if err != nil {
			t.Fatalf("Repo.ListAccessRoutedTriggers(%s/%s): %v", c.account, c.resource, err)
		}
		sameValue(t, "ListAccessRoutedTriggers", port, twin)
		var got []string
		for _, tr := range port {
			got = append(got, tr.ID)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("ListAccessRoutedTriggers(%s/%s, fresh %v) = %v, want %v", c.account, c.resource, c.fresh, got, c.want)
		}
	}
}
