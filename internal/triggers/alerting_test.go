// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// fakeReceivers is the control plane's receiver set.
type fakeReceivers struct {
	held      map[string]controlplane.AutopilotReceiver
	upsertErr error
	calls     []string // "list", "upsert:<id>", "delete:<id>"
}

func (f *fakeReceivers) UpsertAutopilotReceiver(_ context.Context, r controlplane.AutopilotReceiver) error {
	f.calls = append(f.calls, "upsert:"+r.ActivationID)
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.held[r.ActivationID] = r
	return nil
}

func (f *fakeReceivers) DeleteAutopilotReceiver(_ context.Context, id string) error {
	f.calls = append(f.calls, "delete:"+id)
	delete(f.held, id)
	return nil
}

func (f *fakeReceivers) ListAutopilotReceivers(context.Context) ([]string, error) {
	f.calls = append(f.calls, "list")
	var ids []string
	for id := range f.held {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

type fakeAlertingStore struct {
	hashes      map[string][]byte
	activations []*core.Trigger
	project     *core.Project
	repos       []*core.Repo
	locked      bool
	listed      *[]string // the receivers' call log, to order the two listings
}

func (s *fakeAlertingStore) SetTriggerWebhookTokenHash(_ context.Context, id string, h []byte) error {
	s.hashes[id] = h
	return nil
}
func (s *fakeAlertingStore) GetProject(context.Context, string) (*core.Project, error) {
	return s.project, nil
}
func (s *fakeAlertingStore) ListReposByProject(context.Context, string) ([]*core.Repo, error) {
	return s.repos, nil
}
func (s *fakeAlertingStore) ListAllWorkflowTriggerActivations(context.Context) ([]*core.Trigger, error) {
	*s.listed = append(*s.listed, "activations")
	return s.activations, nil
}
func (s *fakeAlertingStore) TryAdvisoryLock(context.Context, int64) (func(), bool, error) {
	return func() {}, !s.locked, nil
}

func alertingTrigger(id string, enabled bool, updated time.Time) *core.Trigger {
	return &core.Trigger{ID: id, UserID: "u-" + id, ProjectID: "p", Kind: core.TriggerKindWebhook, Enabled: enabled,
		Config: []byte(`{"source":"alerting"}`), WorkflowTrigger: ptr.Of("alerts"), UpdatedAt: updated}
}

func newAlertingFixture() (*Alerting, *fakeReceivers, *fakeAlertingStore) {
	rec := &fakeReceivers{held: map[string]controlplane.AutopilotReceiver{}}
	st := &fakeAlertingStore{hashes: map[string][]byte{}, listed: &rec.calls,
		project: &core.Project{ID: "p", ForgeProjectName: ptr.Of("shop"), RemoteURL: ptr.Of("git@github.com:acme/shop.git")},
		repos:   []*core.Repo{{RemoteURL: ptr.Of("git@github.com:acme/shop.git")}, {RemoteURL: ptr.Of("https://github.com/acme/infra")}, {}},
	}
	a := NewAlerting(rec, st, "https://api.example.com/")
	a.now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	return a, rec, st
}

func TestIsAlertingWebhook(t *testing.T) {
	assert.True(t, IsAlertingWebhook(alertingTrigger("t", true, time.Time{})))
	assert.False(t, IsAlertingWebhook(&core.Trigger{Kind: core.TriggerKindWebhook, Config: []byte(`{}`)}))
	assert.False(t, IsAlertingWebhook(&core.Trigger{Kind: core.TriggerKindSchedule, Config: []byte(`{"source":"alerting"}`)}))
	assert.False(t, (*Alerting)(nil).Manages(alertingTrigger("t", true, time.Time{})), "no control plane manages nothing")
}

// Provision hands the control plane the trigger's URL, a fresh token and its
// project's scope, and stores the hash of exactly that token.
func TestProvisionHandsTheTokenToTheControlPlaneAndStoresItsHash(t *testing.T) {
	a, rec, st := newAlertingFixture()
	require.NoError(t, a.Provision(context.Background(), alertingTrigger("t1", true, time.Time{})))
	got := rec.held["t1"]
	assert.Equal(t, "u-t1", got.UserID)
	assert.Equal(t, "https://api.example.com/hooks/t1", got.URL, "no token in the URL")
	assert.Equal(t, []string{"shop"}, got.Projects)
	assert.Equal(t, []string{"git@github.com:acme/shop.git", "https://github.com/acme/infra"}, got.Repos, "each remote once")
	assert.True(t, WebhookTokenMatches(st.hashes["t1"], got.Token), "the stored hash opens the token the control plane holds")
}

// A refused provision leaves the stored token alone, so a receiver the
// control plane already had keeps working.
func TestARefusedProvisionKeepsTheCurrentToken(t *testing.T) {
	a, rec, st := newAlertingFixture()
	rec.upsertErr = errors.New("down")
	st.hashes["t1"] = []byte("current")
	require.Error(t, a.Provision(context.Background(), alertingTrigger("t1", true, time.Time{})))
	assert.Equal(t, []byte("current"), st.hashes["t1"])
}

func TestSweepConvergesReceiversOntoEnabledAlertingActivations(t *testing.T) {
	a, rec, st := newAlertingFixture()
	old := a.now().Add(-time.Hour)
	recent := a.now().Add(-time.Minute)
	rec.held = map[string]controlplane.AutopilotReceiver{"kept": {}, "gone": {}, "paused": {}, "plain-now": {}}
	plain := alertingTrigger("plain-now", true, old)
	plain.Config = []byte(`{}`) // its declaration no longer says source: alerting
	st.activations = []*core.Trigger{
		alertingTrigger("kept", true, old),
		alertingTrigger("paused", false, old),
		plain,
		alertingTrigger("missing", true, old),
		alertingTrigger("just-enabled", true, recent),
	}

	rep, err := a.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, rep.Removed, "gone, paused and plain-now")
	assert.Equal(t, 1, rep.Provisioned, "missing")
	held, _ := rec.ListAutopilotReceivers(context.Background())
	assert.Equal(t, []string{"kept", "missing"}, held, "just-enabled is inside the grace its own enable is still writing in")
	assert.Equal(t, "list", rec.calls[0], "receivers are listed before the activations")
	assert.Equal(t, "activations", rec.calls[1])
}

func TestSweepSkipsWhenAnotherReplicaHoldsIt(t *testing.T) {
	a, rec, st := newAlertingFixture()
	st.locked = true
	rep, err := a.Sweep(context.Background())
	require.NoError(t, err)
	assert.True(t, rep.Skipped)
	assert.Empty(t, rec.calls)
}

// An owner with no organization yet is not an error the sweep repeats every
// minute: there is nothing to receive.
func TestSweepLeavesAnOrglessOwnerQuietly(t *testing.T) {
	a, rec, st := newAlertingFixture()
	rec.upsertErr = &controlplane.AutopilotError{Code: "failed_precondition", Reason: controlplane.ReasonAutopilotNoOrg}
	st.activations = []*core.Trigger{alertingTrigger("t", true, a.now().Add(-time.Hour))}
	rep, err := a.Sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, rep.Provisioned)
	assert.Empty(t, st.hashes, "a refused provision stores no token")
}
