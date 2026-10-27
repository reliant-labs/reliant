// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
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

type waitAuthorityPolicy struct{}

func (waitAuthorityPolicy) Resolve(context.Context, prreviewwait.AuthoritySubject) (prreviewwait.Authority, error) {
	return prreviewwait.Authority{ID: "server", PolicyRevision: 1}, nil
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
	s.env.SignalWorkflow(prreviewwait.SubscriptionSignal(id), id)
	return nil
}

func reviewWaitTestWorkflow(ctx workflow.Context, in PRReviewWaitInput) (map[string]interface{}, error) {
	return executePRReviewWait(ctx, in)
}

func parallelReviewWaitWorkflow(ctx workflow.Context, inputs []PRReviewWaitInput) ([]map[string]interface{}, error) {
	results := make([]map[string]interface{}, len(inputs))
	remaining := len(inputs)
	var firstErr error
	for i, in := range inputs {
		index, input := i, in
		workflow.Go(ctx, func(ctx workflow.Context) {
			result, err := executePRReviewWait(ctx, input)
			results[index] = result
			if err != nil && firstErr == nil {
				firstErr = err
			}
			remaining--
		})
	}
	if err := workflow.Await(ctx, func() bool { return remaining == 0 }); err != nil {
		return nil, err
	}
	return results, firstErr
}

func seedReviewBinding(t *testing.T, database *storetest.Env, number int64, sha string) {
	t.Helper()
	ctx := context.Background()
	original := runcontinuity.Execution{WorkflowID: "default-test-workflow-id", RunID: "default-test-run-id"}
	author := prhandoff.Author{OwnerID: "owner", RootID: original.WorkflowID, ExecutionID: original.RunID, ChatID: "chat", WorktreeID: "wt"}
	repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
	persistent := store.PRHandoffStore{DB: database.ORM}
	publicationID := fmt.Sprintf("pub-%d", number)
	branch := fmt.Sprintf("branch-%d", number)
	require.NoError(t, persistent.RecordPublication(ctx, prhandoff.Publication{ID: publicationID, Author: author, Repo: repo, Branch: branch, HeadSHA: sha}))
	require.NoError(t, persistent.Enroll(ctx, prhandoff.Binding{ID: fmt.Sprintf("b-%d", number), Author: author, PublicationID: publicationID, Pull: prhandoff.Pull{Repo: repo, Number: number, ObjectID: number + 100, HeadRepoID: 42, Branch: branch, HeadSHA: sha, Open: true}}))
}

func TestPRReviewWaitParallelDedicatedChannels(t *testing.T) {
	database := storetest.New(t)
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	seedReviewBinding(t, database, 1, sha)
	seedReviewBinding(t, database, 2, sha)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	continuity := &waitContinuity{current: runcontinuity.Execution{WorkflowID: "default-test-workflow-id", RunID: "default-test-run-id"}}
	signals := &waitSignals{env: env}
	persistent := store.PRReviewWaitStore{DB: database.ORM}
	svc, err := prreviewwait.New(prreviewwait.Deps{Policy: waitAuthorityPolicy{}, Store: persistent, Continuity: continuity, Signals: signals})
	require.NoError(t, err)
	(prreviewwait.Activities{Service: svc, Identity: waitIdentity{}, GitHub: waitRepo{}}).Register(env)
	publish := func(number int64) {
		require.NoError(t, svc.Publish(ctx, prreviewwait.Outcome{OwnerID: "owner", AuthorityID: "server", BindingID: fmt.Sprintf("b-%d", number), HeadSHA: sha, ProviderRef: fmt.Sprintf("review:%d", number), Sequence: number, State: "changes"}))
	}
	// Reverse completion order. Each independently parked goroutine must wake.
	env.RegisterDelayedCallback(func() { publish(2) }, time.Second)
	env.RegisterDelayedCallback(func() { publish(1) }, 2*time.Second)
	env.ExecuteWorkflow(parallelReviewWaitWorkflow, []PRReviewWaitInput{{Occurrence: "node-a", Repo: "org/repo", PR: 1, HeadSHA: sha}, {Occurrence: "node-b", Repo: "org/repo", PR: 2, HeadSHA: sha}})
	require.NoError(t, env.GetWorkflowError())
	var output []map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&output))
	require.Equal(t, "review:1", output[0]["provider_ref"])
	require.Equal(t, "review:2", output[1]["provider_ref"])
}

func TestPRReviewWaitCancelAfterArmCommitBeforeResult(t *testing.T) {
	database := storetest.New(t)
	sha := strings.Repeat("a", 40)
	seedReviewBinding(t, database, 1, sha)
	ctx := context.Background()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	persistent := store.PRReviewWaitStore{DB: database.ORM}
	svc, err := prreviewwait.New(prreviewwait.Deps{Policy: waitAuthorityPolicy{}, Store: persistent, Continuity: &waitContinuity{current: runcontinuity.Execution{WorkflowID: "default-test-workflow-id", RunID: "default-test-run-id"}}, Signals: &waitSignals{env: env}})
	require.NoError(t, err)
	activities := prreviewwait.Activities{Service: svc, Identity: waitIdentity{}, GitHub: waitRepo{}}
	activities.Register(env)
	env.RegisterActivityWithOptions(func(ctx context.Context, in prreviewwait.NodeInput) (prreviewwait.Subscription, error) {
		sub, err := activities.Arm(ctx, in)
		if err != nil {
			return sub, err
		}
		// SQL is committed, but the result never reaches the workflow. Cancel now.
		env.CancelWorkflow()
		<-ctx.Done()
		return prreviewwait.Subscription{}, temporal.NewCanceledError()
	}, activity.RegisterOptions{Name: "PRReviewWaitArm", DisableAlreadyRegisteredCheck: true})
	input := PRReviewWaitInput{Occurrence: "commit-before-result", Repo: "org/repo", PR: 1, HeadSHA: sha}
	env.ExecuteWorkflow(reviewWaitTestWorkflow, input)
	require.Error(t, env.GetWorkflowError())
	var state, requestState string
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT state FROM pr_review_wait_subscriptions").Scan(&state))
	require.Equal(t, "cancelled", state)
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT state FROM pr_review_wait_requests").Scan(&requestState))
	require.Equal(t, "cancelled", requestState)
}

func TestPRReviewWaitSignalBufferedBeforeArmResult(t *testing.T) {
	database := storetest.New(t)
	sha := strings.Repeat("a", 40)
	seedReviewBinding(t, database, 1, sha)
	ctx := context.Background()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	persistent := store.PRReviewWaitStore{DB: database.ORM}
	svc, err := prreviewwait.New(prreviewwait.Deps{Policy: waitAuthorityPolicy{}, Store: persistent, Continuity: &waitContinuity{current: runcontinuity.Execution{WorkflowID: "default-test-workflow-id", RunID: "default-test-run-id"}}, Signals: &waitSignals{env: env}})
	require.NoError(t, err)
	activities := prreviewwait.Activities{Service: svc, Identity: waitIdentity{}, GitHub: waitRepo{}}
	activities.Register(env)
	var once sync.Once
	env.RegisterActivityWithOptions(func(ctx context.Context, in prreviewwait.NodeInput) (prreviewwait.Subscription, error) {
		sub, err := activities.Arm(ctx, in)
		if err == nil {
			once.Do(func() {
				require.NoError(t, svc.Publish(ctx, prreviewwait.Outcome{OwnerID: "owner", AuthorityID: "server", BindingID: "b-1", HeadSHA: sha, ProviderRef: "early", Sequence: 1, State: "approved"}))
			})
		}
		return sub, err
	}, activity.RegisterOptions{Name: "PRReviewWaitArm", DisableAlreadyRegisteredCheck: true})
	env.ExecuteWorkflow(reviewWaitTestWorkflow, PRReviewWaitInput{Occurrence: "early", Repo: "org/repo", PR: 1, HeadSHA: sha})
	require.NoError(t, env.GetWorkflowError())
	var result map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "approved", result["state"])
	digest := sha256.Sum256([]byte("b-1\x00" + sha))
	require.Equal(t, hex.EncodeToString(digest[:]), result["subscription_id"])
	_ = ctx
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
			service, err := prreviewwait.New(prreviewwait.Deps{Policy: waitAuthorityPolicy{}, Store: persistent, Continuity: continuity, Signals: signals})
			require.NoError(t, err)
			activities := prreviewwait.Activities{Service: service, Identity: waitIdentity{}, GitHub: waitRepo{}}
			activities.Register(env)
			publish := func() {
				require.NoError(t, service.Publish(ctx, prreviewwait.Outcome{OwnerID: "owner", AuthorityID: "server", BindingID: "binding", HeadSHA: sha, ProviderRef: "review:77", Sequence: 77, State: "changes"}))
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
