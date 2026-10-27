// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"time"

	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// PRReviewWaitInput names the node subject. Author/execution identity is derived
// by activities from their actual Temporal context and persisted enrollment.
type PRReviewWaitInput = prreviewwait.NodeInput

// executePRReviewWait executes the durable node. Channel collection starts
// BEFORE Arm, and a generation counter prevents read→Await lost wakes. Signals
// carry no verdict; every transition is read/consumed through the durable inbox.
func executePRReviewWait(ctx workflow.Context, input PRReviewWaitInput) (map[string]interface{}, error) {
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5}})
	var subscription prreviewwait.Subscription
	generation := uint64(0)
	channel := workflow.GetSignalChannel(ctx, prreviewwait.SignalName)
	listenerCtx, cancelListener := workflow.WithCancel(ctx)
	defer cancelListener()
	workflow.Go(listenerCtx, func(gctx workflow.Context) {
		for {
			var id string
			channel.Receive(gctx, &id)
			if gctx.Err() != nil {
				return
			}
			if subscription.ID == "" || id == subscription.ID {
				generation++
			}
		}
	})
	if err := workflow.ExecuteActivity(activityCtx, "PRReviewWaitArm", input).Get(ctx, &subscription); err != nil {
		return nil, err
	}
	if subscription.State == "needs_human" {
		return waitOutput(subscription, prreviewwait.Outcome{State: "unavailable"}), nil
	}
	defer func() {
		if ctx.Err() != nil {
			cleanup, _ := workflow.NewDisconnectedContext(ctx)
			cleanup = workflow.WithActivityOptions(cleanup, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
			_ = workflow.ExecuteActivity(cleanup, "PRReviewWaitCancel", subscription).Get(cleanup, nil)
		}
	}()
	// Register cleanup before the first post-arm activity so cancellation while
	// readiness is being exposed cannot strand an armed subscription.
	if err := workflow.ExecuteActivity(activityCtx, "PRReviewWaitReady", subscription).Get(ctx, nil); err != nil {
		return nil, err
	}
	for {
		observed := generation
		var outcome prreviewwait.Outcome
		if err := workflow.ExecuteActivity(activityCtx, "PRReviewWaitRead", subscription).Get(ctx, &outcome); err != nil {
			return nil, err
		}
		if generation != observed {
			continue
		} // newer doorbell raced the durable read
		if outcome.State != "" {
			if err := workflow.ExecuteActivity(activityCtx, "PRReviewWaitConsume", prreviewwait.ConsumeInput{Subscription: subscription, Outcome: outcome}).Get(ctx, nil); err != nil {
				var application *temporal.ApplicationError
				if errors.As(err, &application) && application.Type() == "PRReviewWaitStaleRead" {
					continue
				}
				return nil, err
			}
			return waitOutput(subscription, outcome), nil
		}
		if err := workflow.Await(ctx, func() bool { return generation != observed }); err != nil {
			return nil, err
		}
	}
}

func waitOutput(s prreviewwait.Subscription, o prreviewwait.Outcome) map[string]interface{} {
	return map[string]interface{}{"state": o.State, "subscription_id": s.ID, "provider_ref": o.ProviderRef, "head_sha": s.HeadSHA, "repo_id": s.RepoID, "pr": s.PR}
}

func (e *StepExecutor) startPRReviewWait(_ *reliantv1.Node, evalResult *reliantv1.Node) (workflow.Future, string) {
	future, settable := workflow.NewFuture(e.ctx)
	args := evalResult.GetAwaitPrReview()
	workflow.Go(e.ctx, func(ctx workflow.Context) {
		if args == nil {
			settable.SetError(temporal.NewNonRetryableApplicationError("await_pr_review arguments missing", "PRReviewWaitInvalid", nil))
			return
		}
		output, err := executePRReviewWait(ctx, PRReviewWaitInput{Repo: model.CelStringValue(args.GetRepo()), PR: model.CelIntValue(args.GetPr()), HeadSHA: model.CelStringValue(args.GetExpectedHead())})
		if err != nil {
			settable.SetError(err)
			return
		}
		settable.SetValue(output)
	})
	return future, "AwaitPRReview"
}
