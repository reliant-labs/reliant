// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

type waitIdentity struct{}

func (waitIdentity) GetWorkflow(_ context.Context, id string) (*core.Workflow, error) {
	return &core.Workflow{ID: id, ChatID: "chat", Status: core.Active()}, nil
}
func (waitIdentity) GetChat(context.Context, string) (*core.Chat, error) {
	return &core.Chat{ID: "chat", UserID: "owner"}, nil
}

type waitRepo struct{}

func (waitRepo) Repository(context.Context, string, string) (prhandoff.Repository, error) {
	return prhandoff.Repository{ID: 42, Name: "org/repo"}, nil
}

type waitContinuity struct{ current runcontinuity.Execution }

func (c *waitContinuity) Resolve(_ context.Context, original runcontinuity.Execution) (runcontinuity.Result, error) {
	return runcontinuity.Result{Original: original, Current: c.current, Live: true}, nil
}

type waitSignals struct {
	env               *testsuite.TestWorkflowEnvironment
	sent              int
	workflowID, runID string
}

func (s *waitSignals) SignalReview(_ context.Context, e runcontinuity.Execution, id string) error {
	s.sent++
	s.workflowID = e.WorkflowID
	s.runID = e.RunID
	s.env.SignalWorkflow(prreviewwait.SignalName, id)
	return nil
}

func reviewWaitTestWorkflow(ctx workflow.Context, in PRReviewWaitInput) (map[string]interface{}, error) {
	return executePRReviewWait(ctx, in)
}

func TestPRReviewWaitRealActivitiesAndInboxRaces(t *testing.T) {
	for _, when := range []string{"before-arm", "after-arm-before-await", "while-parked", "cancelled"} {
		t.Run(when, func(t *testing.T) {
			database := storetest.New(t)
			ctx := context.Background()
			sha := strings.Repeat("a", 40)
			original := runcontinuity.Execution{WorkflowID: "default-test-workflow-id", RunID: "default-test-run-id"}
			author := prhandoff.Author{OwnerID: "owner", RootID: original.WorkflowID, ExecutionID: original.RunID, ChatID: "chat", WorktreeID: "wt"}
			repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
			publications := store.PRHandoffStore{DB: database.ORM}
			require.NoError(t, publications.RecordPublication(ctx, prhandoff.Publication{ID: "pub", Author: author, Repo: repo, Branch: "fix", HeadSHA: sha}))
			require.NoError(t, publications.Enroll(ctx, prhandoff.Binding{ID: "binding", Author: author, PublicationID: "pub", Pull: prhandoff.Pull{Repo: repo, Number: 5, ObjectID: 55, HeadRepoID: 42, Branch: "fix", HeadSHA: sha, Open: true}}))
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			continuity := &waitContinuity{current: original}
			signals := &waitSignals{env: env}
			persistent := store.PRReviewWaitStore{DB: database.ORM}
			service, err := prreviewwait.New(prreviewwait.Deps{Store: persistent, Continuity: continuity, Signals: signals})
			require.NoError(t, err)
			activities := prreviewwait.Activities{Service: service, Identity: waitIdentity{}, GitHub: waitRepo{}}
			activities.Register(env)
			publish := func() {
				require.NoError(t, service.Publish(ctx, prreviewwait.Outcome{BindingID: "binding", HeadSHA: sha, ProviderRef: "review:77", Sequence: 77, State: "changes"}))
			}
			if when == "before-arm" {
				publish()
			} else if when == "cancelled" {
				env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
			} else {
				delay := time.Millisecond
				if when == "while-parked" {
					delay = time.Second
				}
				env.RegisterDelayedCallback(publish, delay)
			}
			env.ExecuteWorkflow(reviewWaitTestWorkflow, PRReviewWaitInput{Repo: "org/repo", PR: 5, HeadSHA: sha})
			require.True(t, env.IsWorkflowCompleted())
			if when == "cancelled" {
				require.Error(t, env.GetWorkflowError())
				var state string
				require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT state FROM pr_review_wait_subscriptions").Scan(&state))
				require.Equal(t, "cancelled", state)
				require.Zero(t, signals.sent)
				return
			}
			require.NoError(t, env.GetWorkflowError())
			var output map[string]interface{}
			require.NoError(t, env.GetWorkflowResult(&output))
			require.Equal(t, "changes", output["state"])
			require.Equal(t, "review:77", output["provider_ref"])
			var consumed, notified int64
			var ready bool
			require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT consumed_sequence,notified_sequence,ready FROM pr_review_wait_subscriptions").Scan(&consumed, &notified, &ready))
			require.EqualValues(t, 77, consumed)
			require.True(t, ready)
			if when == "before-arm" {
				require.Zero(t, signals.sent)
				require.Zero(t, notified)
			} else {
				require.Equal(t, original.WorkflowID, signals.workflowID)
				require.Equal(t, original.RunID, signals.runID)
			}
		})
	}
}
