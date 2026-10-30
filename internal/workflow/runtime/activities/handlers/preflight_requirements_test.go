// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/workflow/requirements"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// requirementsStore is the chat → project lookup, for a chat on a machine.
type requirementsStore struct{ noMachine bool }

func (s requirementsStore) GetChat(_ context.Context, id string) (*db.Chat, error) {
	return &db.Chat{ID: id, ProjectID: "p1", NoMachine: s.noMachine}, nil
}
func (requirementsStore) GetProject(_ context.Context, id string) (*db.Project, error) {
	return &db.Project{ID: id, UserID: "u1", Path: "/home/workspace/projects/acme", Name: "acme"}, nil
}
func (requirementsStore) GetWorktree(context.Context, string) (*db.Worktree, error) {
	return nil, errors.New("no worktree")
}

// fakeProbeMachine is the run's machine: it answers the probe with out.
type fakeProbeMachine struct {
	out   string
	err   error
	ran   string
	where RunExecutorContext
}

func (m *fakeProbeMachine) Run(_ context.Context, command string) (string, int, error) {
	m.ran = command
	return m.out, 0, m.err
}

// fakeConnections is the connection store: the integrations it holds
// resolve, others fail the way connections does for a missing one.
type fakeConnections struct {
	held map[string]bool
	seen []httpaction.CredentialRequest
}

type heldCredential struct{}

func (heldCredential) ConnectionID() string      { return "c1" }
func (heldCredential) Apply(*http.Request) error { return nil }
func (heldCredential) Scrub(s string) string     { return s }
func (heldCredential) Params() map[string]string { return nil }

func (f *fakeConnections) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	f.seen = append(f.seen, req)
	if f.held[req.IntegrationID] {
		return heldCredential{}, nil
	}
	return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: req.IntegrationID + " is not connected."}
}

func newRequirementsActivity(store RunExecutorContextStore, m *fakeProbeMachine, conns *fakeConnections) *PreflightRequirementsActivity {
	a := NewPreflightRequirementsActivity(store, nil, conns)
	a.machine = func(ec RunExecutorContext) requirements.Machine { m.where = ec; return m }
	return a
}

func TestPreflightRequirementsAllPresent(t *testing.T) {
	m := &fakeProbeMachine{out: "REQUIRES_PROBE_DONE\n"}
	conns := &fakeConnections{held: map[string]bool{"github": true}}
	a := newRequirementsActivity(requirementsStore{}, m, conns)
	out, err := a.Execute(context.Background(), PreflightRequirementsInput{
		ChatID: "chat1", WorkflowID: "wf1", Tools: []string{"gh"}, Repos: []string{"control-plane"},
		Integrations: []string{"github"}, ActingIdentity: "app", Automation: "queued",
	})
	require.NoError(t, err)
	assert.Empty(t, out.Problems)
	assert.Equal(t, "/home/workspace/projects/acme", m.where.ProjectPath, "the probe runs in the project root")
	assert.Contains(t, m.ran, "command -v 'gh'")
	require.Len(t, conns.seen, 1)
	assert.Equal(t, "wf1", conns.seen[0].RunID)
	assert.Equal(t, "app", conns.seen[0].ActingIdentity, "checked as the identity the run's actions use")
	assert.False(t, conns.seen[0].Mutates)
}

// Unmet requirements fail the run, non-retryably, naming every problem.
func TestPreflightRequirementsUnmetFailsWithEveryProblem(t *testing.T) {
	m := &fakeProbeMachine{out: "TOOL_MISSING\ttask\nREPO_TAG_DIVERGED\treliant\tv1.8.13\nREQUIRES_PROBE_DONE\n"}
	a := newRequirementsActivity(requirementsStore{}, m, &fakeConnections{})
	out, err := a.Execute(context.Background(), PreflightRequirementsInput{
		ChatID: "chat1", Tools: []string{"task"}, Repos: []string{"reliant"}, Integrations: []string{"github"},
	})
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, PreflightRequirementsErrType, appErr.Type())
	assert.True(t, appErr.NonRetryable())
	assert.Len(t, out.Problems, 3)
	for _, want := range []string{"tool task", "repo reliant: local tag(s) v1.8.13", "integration github: github is not connected"} {
		assert.Contains(t, err.Error(), want)
	}
}

// A probe the machine dropped is not a verdict: an ordinary (retryable)
// error, never "requirements unmet".
func TestPreflightRequirementsMachineFailureIsRetryable(t *testing.T) {
	m := &fakeProbeMachine{err: errors.New("no result came back from the machine")}
	a := newRequirementsActivity(requirementsStore{}, m, nil)
	_, err := a.Execute(context.Background(), PreflightRequirementsInput{ChatID: "chat1", Tools: []string{"git"}})
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	assert.False(t, errors.As(err, &appErr) && appErr.NonRetryable())
}

// A run with no machine cannot have tools or checkouts.
func TestPreflightRequirementsNoMachine(t *testing.T) {
	m := &fakeProbeMachine{}
	a := newRequirementsActivity(requirementsStore{noMachine: true}, m, nil)
	_, err := a.Execute(context.Background(), PreflightRequirementsInput{ChatID: "chat1", Tools: []string{"git"}})
	require.ErrorContains(t, err, "this run has none")
	assert.Empty(t, m.ran)
}

// The connection check names an unknown integration, and passes a missing
// connection's own message through.
func TestPreflightRequirementsConnectionCheck(t *testing.T) {
	conns := &fakeConnections{}
	check := connectionCheck{src: conns}
	require.ErrorContains(t, check.Check(context.Background(), "not-an-integration"), "not an integration")
	require.ErrorContains(t, check.Check(context.Background(), "github"), "github is not connected")
}
