// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// membership is an org's live membership, changed by the test mid-run.
type membership struct {
	mu      sync.Mutex
	members map[string]bool // "user/org"
	err     error
	asked   int
}

func (m *membership) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked++
	if m.err != nil {
		return false, true, m.err
	}
	return m.members[userID+"/"+orgID], true, nil
}

func (m *membership) set(userID, orgID string, member bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members == nil {
		m.members = map[string]bool{}
	}
	m.members[userID+"/"+orgID] = member
	m.err = err
}

type botCred struct{}

func (botCred) ConnectionID() string      { return "controlplane:github-app" }
func (botCred) Apply(*http.Request) error { return nil }
func (botCred) Scrub(s string) string     { return s }
func (botCred) Params() map[string]string { return nil }

// fakeBroker records what it was asked to mint.
type fakeBroker struct {
	owners   []string
	requests []httpaction.CredentialRequest
	err      error
}

func (b *fakeBroker) ActingCredential(_ context.Context, owner string, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	b.owners = append(b.owners, owner)
	b.requests = append(b.requests, req)
	if b.err != nil {
		return nil, b.err
	}
	return botCred{}, nil
}

// fakeRunner stands in for GitHub behind the catalog's check_run actions: it
// records each action, its params and the credential it was given.
type fakeRunner struct {
	calls  []runnerCall
	status map[int64]string
	next   int64
}

type runnerCall struct {
	action string
	params map[string]any
	cred   httpaction.Credential
}

func (r *fakeRunner) RunAuthenticated(ctx context.Context, _ *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error) {
	cred, err := src.Credential(ctx, httpaction.CredentialRequest{ActionID: a.GetId(), ActingIdentity: site.ActingIdentity})
	if err != nil {
		return nil, err
	}
	r.calls = append(r.calls, runnerCall{action: a.GetId(), params: params, cred: cred})
	if r.status == nil {
		r.status = map[int64]string{}
	}
	switch a.GetId() {
	case "check_run.list":
		return &httpaction.Result{Data: map[string]any{"items": []any{}}}, nil
	case "check_run.create":
		r.next++
		id := 500 + r.next
		r.status[id], _ = params["status"].(string)
		return &httpaction.Result{Data: map[string]any{"id": float64(id)}}, nil
	default:
		id := params["check_run_id"].(int64)
		if s, ok := params["status"].(string); ok {
			r.status[id] = s
		}
		return &httpaction.Result{Data: map[string]any{"id": float64(id)}}, nil
	}
}

func (r *fakeRunner) actions() []string {
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = c.action
	}
	return out
}

var orgIdentity = Identity{UserID: "u1", OrgID: "org-1", ActingIdentity: httpaction.IdentityApp, Automation: "reliant-ci"}

func TestGitHubWritesThroughTheCatalogAsTheAppForOneRepository(t *testing.T) {
	m := &membership{}
	m.set("u1", "org-1", true, nil)
	ctx := authz.WithMembership(context.Background(), m)
	broker, runner := &fakeBroker{}, &fakeRunner{}
	gh := NewGitHub(GitHubDeps{Runner: runner, Broker: broker})

	runs, err := gh.CheckRuns(ctx, orgIdentity, "reliant-labs", "reliant")
	require.NoError(t, err)
	require.Equal(t, []string{"u1"}, broker.owners, "minted for the trigger's owner")
	req := broker.requests[0]
	require.Equal(t, httpaction.IdentityApp, req.ActingIdentity)
	require.Equal(t, "check_run.create", req.ActionID, "checks:write, the least a check needs")
	require.True(t, req.Mutates)
	require.Equal(t, map[string]any{"owner": "reliant-labs", "repo": "reliant"}, req.Params, "scoped to the one repository")
	require.Equal(t, "github", req.IntegrationID)

	id, err := runs.Find(ctx, headSHA, "reliant/ci", "row-1")
	require.NoError(t, err)
	require.Zero(t, id)
	started := t0
	id, err = runs.Create(ctx, Write{Name: "reliant/ci", HeadSHA: headSHA, Status: core.CheckStatusQueued, ExternalID: "row-1",
		DetailsURL: HostedRunURL("chat-1"), Output: &Output{Title: "Queued", Summary: "s"}})
	require.NoError(t, err)
	require.NoError(t, runs.Update(ctx, id, Write{Status: core.CheckStatusInProgress, StartedAt: &started}))

	require.Equal(t, []string{"check_run.list", "check_run.create", "check_run.update"}, runner.actions())
	for _, c := range runner.calls {
		require.Equal(t, botCred{}, c.cred, "every write carries the credential minted for this sync")
		require.Equal(t, "reliant-labs", c.params["owner"])
		require.Equal(t, "reliant", c.params["repo"])
	}
	require.Equal(t, "row-1", runner.calls[1].params["external_id"])
	require.Equal(t, "https://app.reliantlabs.io/workflows/runs/chat-1", runner.calls[1].params["details_url"])
	require.Equal(t, id, runner.calls[2].params["check_run_id"])
	require.Equal(t, "2026-11-18T02:00:00Z", runner.calls[2].params["started_at"])
}

// Every refusal is final (ErrNotAuthorized) and asks GitHub nothing; an
// outage is retried; none of them writes.
func TestGitHubRefusesWithoutAuthority(t *testing.T) {
	type tc struct {
		id         Identity
		member     bool
		memberErr  error
		noBroker   bool
		mintErr    error
		refused    bool
		mintsAsked bool
	}
	for name, c := range map[string]tc{
		"owner removed from the trigger's org": {id: orgIdentity, member: false, refused: true},
		"membership cannot be read":            {id: orgIdentity, memberErr: errors.New("control plane down")},
		"acts as its owner":                    {id: Identity{UserID: "u1", OrgID: "org-1", ActingIdentity: "owner"}, member: true, refused: true},
		"no GitHub App here":                   {id: orgIdentity, member: true, noBroker: true, refused: true},
		"owner cannot push / App not installed": {id: orgIdentity, member: true, mintsAsked: true, refused: true,
			mintErr: &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "the Reliant App is not installed on reliant-labs/reliant"}},
		"mint failed in transit": {id: orgIdentity, member: true, mintsAsked: true,
			mintErr: &httpaction.CredentialError{Code: httpaction.CodeUnavailable, Message: "control plane unavailable"}},
	} {
		t.Run(name, func(t *testing.T) {
			m := &membership{}
			m.set("u1", "org-1", c.member, c.memberErr)
			ctx := authz.WithMembership(context.Background(), m)
			broker, runner := &fakeBroker{err: c.mintErr}, &fakeRunner{}
			deps := GitHubDeps{Runner: runner, Broker: broker}
			if c.noBroker {
				deps.Broker = nil
			}
			runs, err := NewGitHub(deps).CheckRuns(ctx, c.id, "reliant-labs", "reliant")
			require.Error(t, err)
			require.Nil(t, runs)
			require.Equal(t, c.refused, errors.Is(err, ErrNotAuthorized), "%v", err)
			require.Equal(t, c.mintsAsked, len(broker.owners) > 0, "a credential is minted only for an owner who may still write")
			require.Empty(t, runner.calls)
		})
	}
}

// A trigger of no org is its owner's alone: no membership is asked, and the
// mint still decides the repository.
func TestGitHubPersonalTriggerNeedsNoMembership(t *testing.T) {
	m := &membership{}
	ctx := authz.WithMembership(context.Background(), m)
	broker := &fakeBroker{}
	_, err := NewGitHub(GitHubDeps{Runner: &fakeRunner{}, Broker: broker}).CheckRuns(ctx,
		Identity{UserID: "u1", ActingIdentity: httpaction.IdentityApp}, "octo", "hello")
	require.NoError(t, err)
	require.Zero(t, m.asked)
	require.Len(t, broker.owners, 1)
}

// The owner is removed from the trigger's org partway through a run: the
// check was created queued and moved to in_progress as a member; after the
// removal the next sync mints nothing and writes nothing, and the check is
// abandoned at once, not retried. On GitHub it stays in_progress, which a
// required check never counts as passing.
func TestMembershipRevokedPartwayThroughARun(t *testing.T) {
	m := &membership{}
	m.set("u1", "org-1", true, nil)
	broker, runner := &fakeBroker{}, &fakeRunner{}
	h := newHarness()
	h.svc.github = NewGitHub(GitHubDeps{Runner: runner, Broker: broker})
	ctx := authz.WithMembership(context.Background(), m)
	syncOnce := func() error { _, err := h.svc.SyncOnce(ctx); return err }

	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(launched(status(core.Active())))
	require.NoError(t, syncOnce())
	require.NoError(t, h.svc.Started(ctx, "chat-1"))
	require.NoError(t, syncOnce())
	require.Equal(t, core.CheckStatusInProgress, row.GitHubStatus)
	id := *row.GitHubCheckRunID
	require.Equal(t, core.CheckStatusInProgress, runner.status[id])
	writes, mints := len(runner.calls), len(broker.owners)

	// Removed from the org; the run then finishes with its own output.
	m.set("u1", "org-1", false, nil)
	require.NoError(t, h.svc.Ended(ctx, "chat-1", core.WorkflowCheckEnding{
		Status: core.TriggerRunCompleted, Output: map[string]any{"title": "Passed", "summary": "posted by a former member"},
	}))
	require.NoError(t, syncOnce(), "an abandoned check is an outcome, not a failed sync")

	require.Len(t, broker.owners, mints, "no credential is minted for a removed owner")
	require.Len(t, runner.calls, writes, "nothing is written to GitHub after the removal")
	require.Equal(t, core.CheckStatusInProgress, runner.status[id], "the check never passes on the removed owner's say")
	require.NotNil(t, row.FinishedAt, "abandoned at once")
	require.Contains(t, row.LastError, "abandoned, not authorized")
	require.Contains(t, row.LastError, "no longer a member")

	// Never retried, even long after.
	h.clock.advance(24 * time.Hour)
	m.set("u1", "org-1", true, nil)
	require.NoError(t, syncOnce())
	require.Len(t, runner.calls, writes)
}

// A membership outage mid-run fails closed (nothing is written) and is
// retried with backoff; once membership answers again the check is written.
func TestMembershipOutageFailsClosedAndRetries(t *testing.T) {
	m := &membership{}
	m.set("u1", "org-1", true, nil)
	broker, runner := &fakeBroker{}, &fakeRunner{}
	h := newHarness()
	h.svc.github = NewGitHub(GitHubDeps{Runner: runner, Broker: broker})
	ctx := authz.WithMembership(context.Background(), m)

	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	m.set("u1", "org-1", true, errors.New("control plane down"))
	_, err := h.svc.SyncOnce(ctx)
	require.Error(t, err)
	require.Empty(t, broker.owners)
	require.Empty(t, runner.calls)
	require.Nil(t, row.FinishedAt)
	require.Equal(t, 1, row.Attempts)

	m.set("u1", "org-1", true, nil)
	h.clock.advance(time.Minute)
	_, err = h.svc.SyncOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"check_run.list", "check_run.create"}, runner.actions())
	require.Equal(t, core.CheckStatusQueued, row.GitHubStatus)
}
