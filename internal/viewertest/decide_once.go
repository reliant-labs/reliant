// Copyright (c) 2025 Reliant Labs

package viewertest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// SeededAt is the fixed time of every row a viewertest helper seeds after New.
var SeededAt = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// FirstAnswerOnly is a membership that answers its first question as First
// does and every later one with After: a denial (After nil: "not a member") or
// an outage (After set). It is how a test shows a read site decides access
// once: a site that asks again gets a different answer than its first.
type FirstAnswerOnly struct {
	First authz.Membership
	After error
	asked atomic.Int32
}

// IsMember implements authz.Membership.
func (m *FirstAnswerOnly) IsMember(ctx context.Context, userID, orgID string) (bool, bool, error) {
	if m.asked.Add(1) == 1 {
		return m.First.IsMember(ctx, userID, orgID)
	}
	if m.After != nil {
		return false, false, m.After
	}
	return false, true, nil
}

// Asked is how many questions it was asked.
func (m *FirstAnswerOnly) Asked() int { return int(m.asked.Load()) }

// ContextWith is Context(userID) asking membership m instead of the
// fixture's.
func (f *Fixture) ContextWith(userID string, m authz.Membership) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return auth.WithCredential(authz.WithMembership(ctx, m), auth.CredentialSession)
}

// ResolvedStep is a step of the shared chat whose resolved input carries an
// earlier tool's result (Secret), as a template like `{{ steps.read.output }}`
// resolves to, and whose output and error carry it too.
type ResolvedStep struct {
	WorkflowID, StepID string
	Secret             string
}

// SeedResolvedStep writes a workflow of the shared chat and one step of it
// (ResolvedStep), at SeededAt.
func (f *Fixture) SeedResolvedStep(t *testing.T) ResolvedStep {
	t.Helper()
	ctx := context.Background()
	s := ResolvedStep{
		WorkflowID: "resolved-wf-" + f.ChatID,
		StepID:     "resolved-step-" + f.ChatID,
		Secret:     "SECRET_FROM_AN_EARLIER_TOOL_RESULT=hunter2",
	}
	if err := f.Repo.CreateWorkflow(ctx, &db.Workflow{
		ID: s.WorkflowID, ChatID: f.ChatID, WorkflowName: "builtin://agent", Thread: s.WorkflowID,
		Status: db.Active(), CreatedAt: SeededAt,
	}); err != nil {
		t.Fatalf("viewertest: workflow: %v", err)
	}
	input := `{"command":"deploy --token ` + s.Secret + `"}`
	output := `{"stdout":"` + s.Secret + `"}`
	failure := "exit 1: " + s.Secret
	nodePath := "agent.deploy"
	attempt := int32(1)
	success := false
	if err := f.Repo.CreateStepExecution(ctx, &db.StepExecution{
		ID: s.StepID, WorkflowID: s.WorkflowID, StepID: "deploy", ActivityName: "run_command",
		InputJSON: &input, OutputJSON: &output, ErrorMessage: &failure, NodePath: &nodePath,
		Attempt: &attempt, Success: &success, CreatedAt: SeededAt,
	}); err != nil {
		t.Fatalf("viewertest: step: %v", err)
	}
	return s
}
