// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/sdk/go/reliant"
)

// These examples are the code the SDK guide in the Reliant docs
// (docs/sdk/go.mdx) shows. They have no `// Output:` comment, so `go test`
// compiles and vets them without running them: they need a live server and a
// token. Keep the guide and this file in step.

func ExampleFromEnv() {
	// RELIANT_TOKEN is required; RELIANT_SERVER_URL defaults to
	// https://api.reliantapi.com.
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleWithTokenSource() {
	// A token source is called for every request, so a token that is rotated
	// in place (a mounted secret, say) is picked up without a restart.
	c, err := reliant.New(reliant.WithTokenSource(func(context.Context) (string, error) {
		b, err := os.ReadFile("/run/secrets/reliant_token")
		return strings.TrimSpace(string(b)), err
	}))
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleRunsClient_Follow() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	run, err := c.Runs.Start(ctx, reliant.StartRun{
		Workflow: "builtin://agent",
		Message:  "fix the failing test in ./internal/billing",
	})
	if err != nil {
		log.Fatal(err)
	}

	f, err := c.Runs.Follow(ctx, run.GetId(), reliant.FollowOptions{Timeout: 30 * time.Minute})
	if err != nil {
		log.Fatal(err)
	}
	for ev := range f.Events() {
		fmt.Println(ev.Event, ev.NodeID)
	}

	res, err := f.Wait()
	if err != nil {
		log.Fatal(err)
	}
	switch {
	case res.Succeeded:
		fmt.Println("passed")
	case res.TimedOut:
		fmt.Println("still running; follow it again later")
	default:
		log.Fatalf("did not pass: status=%s outcome=%s", res.Status, res.Outcome)
	}
}

func ExampleRunsClient_Status() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	st, err := c.Runs.Status(ctx, "run-id")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: status=%s outcome=%q succeeded=%v\n", st.Workflow, st.Status, st.Outcome, st.Succeeded)
	for _, g := range st.Gates {
		fmt.Println("waiting on", g.Kind, g.ID)
	}
}

func ExampleRunsClient_List() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	it := c.Runs.List(ctx, reliant.ListRuns{
		Workflow:      []string{"builtin://agent"},
		DisplayStates: []reliant.RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED},
		StartedAfter:  since,
		Limit:         20,
	})
	for it.Next() {
		run := it.Value()
		fmt.Println(run.GetId(), run.GetTitle(), run.GetFailureDetail())
	}
	if err := it.Err(); err != nil {
		log.Fatal(err)
	}
}

func ExampleIter_Seq() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	for trigger, err := range c.Triggers.List(ctx, reliant.ListTriggers{}).Seq() {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(trigger.GetName(), trigger.GetEnabled())
	}
}

func ExampleRunsClient_Cancel() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	delivered, err := c.Runs.Signal(ctx, "run-id", "", reliant.UserMessage("skip the migration step"))
	if err != nil {
		log.Fatal(err)
	}
	if !delivered {
		fmt.Println("the run was not listening; nothing was delivered")
	}

	if err := c.Runs.Cancel(ctx, "run-id"); err != nil {
		log.Fatal(err)
	}
}

// askOperator puts a question's prompts to a person (a chat message, a pager,
// a form) and returns what they chose. The SDK supplies no default answer:
// answering unblocks the run, so the decision is the operator's.
func askOperator(prompts []reliant.Prompt) ([]reliant.Answer, error) {
	return nil, errors.New("deliver the prompts to a person and return their choices")
}

func ExampleGatesClient_Answer() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	const runID = "run-id"

	// Block until the run needs input, then look at what it is waiting on.
	f, err := c.Runs.Follow(ctx, runID, reliant.FollowOptions{StopOnGate: true})
	if err != nil {
		log.Fatal(err)
	}
	res, err := f.Wait()
	if err != nil {
		log.Fatal(err)
	}
	if !res.StoppedOnGate {
		return // it finished, or timed out, without asking
	}

	gates, err := c.Gates.Pending(ctx, runID)
	if err != nil {
		log.Fatal(err)
	}
	for _, g := range gates {
		switch g.Kind {
		case reliant.GateQuestion:
			// Answer only with what a person chose. Never fill in a default.
			answers, err := askOperator(g.Prompts)
			if err != nil {
				log.Fatal(err)
			}
			if err := c.Gates.Answer(ctx, g.ID, answers); err != nil {
				log.Fatal(err)
			}
		case reliant.GateApproval:
			// Approving takes a person; the API cannot decide it.
			fmt.Printf("approval %q is waiting for someone to decide\n", g.Title)
		}
	}
}

func ExampleTriggersClient_Fire() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	fired, err := c.Triggers.Fire(ctx, "nightly-report") // an id or a name
	if err != nil {
		log.Fatal(err)
	}

	// The firing is asynchronous: look for its event until it settles, and
	// treat running out of time as a failure, not as success.
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		events := c.Triggers.Events(waitCtx, fired.TriggerID, reliant.EventFilter{Limit: 10})
		for events.Next() {
			ev := events.Value()
			if ev.GetFireWorkflowId() != fired.FireWorkflowID {
				continue
			}
			switch ev.GetOutcome() {
			case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED:
				f, err := c.Runs.FollowSession(ctx, ev.GetChatId(), reliant.FollowOptions{})
				if err != nil {
					log.Fatal(err)
				}
				res, err := f.Wait()
				fmt.Println(res.Succeeded, err)
				return
			case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_PENDING:
				// not launched yet; look again
			default:
				log.Fatalf("not launched: %s: %s", ev.GetOutcome(), ev.GetOutcomeDetail())
			}
		}
		if err := events.Err(); err != nil {
			log.Fatalf("the firing did not settle: %v", err) // includes the deadline
		}
		select {
		case <-tick.C:
		case <-waitCtx.Done():
			log.Fatalf("the firing did not settle within a minute: %v", waitCtx.Err())
		}
	}
}

func ExampleWebhook_SendJSON() {
	ctx := context.Background()
	hook, err := reliant.NewWebhook(
		"", // the hosted API; pass your own server's URL otherwise
		os.Getenv("TRIGGER_ID"),
		reliant.WebhookBearer(os.Getenv("TRIGGER_TOKEN")),
	)
	if err != nil {
		log.Fatal(err)
	}

	d, err := hook.SendJSON(ctx,
		map[string]any{"status": "failed", "service": "billing"},
		reliant.WithIdempotencyKey("deploy-4812"),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(d.EventID, d.Outcome, d.Duplicate)
}

func ExampleWebhookHMAC() {
	hook, err := reliant.NewWebhook("", os.Getenv("TRIGGER_ID"),
		// The same header, algorithm, prefix and encoding as the trigger's
		// webhook.hmac block.
		reliant.WebhookHMAC([]byte(os.Getenv("WEBHOOK_SECRET")), reliant.WebhookHMACConfig{
			Header: "X-Hub-Signature-256",
			Prefix: "sha256=",
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = hook
}

func Example_errors() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	_, err = c.Runs.Get(ctx, "run-id")

	var rerr *reliant.Error
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Your own context ended the call. These are never wrapped.
	case errors.Is(err, reliant.ErrNotFound):
		fmt.Println("no such run, or it is not yours")
	case errors.Is(err, reliant.ErrUnauthenticated):
		fmt.Println("the token is missing, expired or revoked: mint a new one")
	case errors.Is(err, reliant.ErrPermissionDenied):
		fmt.Println("the token may not do this")
	case errors.As(err, &rerr):
		fmt.Println(rerr.Op, rerr.Code, rerr.Message)
	}
}

// withRetry runs a read until it succeeds, the error is not worth repeating,
// or the attempts are used up. Only ErrUnavailable is retried: that is the
// SDK's word for "the server could not be reached or was not ready".
func withRetry[T any](ctx context.Context, attempts int, fn func() (T, error)) (T, error) {
	delay := 500 * time.Millisecond
	for i := 1; ; i++ {
		v, err := fn()
		if err == nil || !errors.Is(err, reliant.ErrUnavailable) || i >= attempts {
			return v, err
		}
		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

func Example_retry() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	run, err := withRetry(ctx, 4, func() (*reliant.Run, error) {
		return c.Runs.Get(ctx, "run-id")
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(run.GetDisplayState())
}

func ExampleClient_HTTPClient() {
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// A generated Connect client for a public procedure the wrappers do not
	// cover, on the same authenticated HTTP client.
	runs := reliantv1connect.NewRunServiceClient(c.HTTPClient(), c.BaseURL())
	_ = runs
}

func ExampleWithInsecureHTTP() {
	// A self-hosted server on a network you trust, reached over plain http.
	// The token is readable by anyone on the path, so the default refuses it.
	c, err := reliant.New(
		reliant.WithBaseURL("http://reliant.internal:8080"),
		reliant.WithToken(os.Getenv("RELIANT_TOKEN")),
		reliant.WithInsecureHTTP(),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleFollowOptions() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// Log receives the follow's own diagnostics (a poll that failed and will
	// be retried); the run's events still arrive on Events.
	f, err := c.Runs.Follow(ctx, "run-id", reliant.FollowOptions{Log: os.Stderr})
	if err != nil {
		log.Fatal(err)
	}
	res, err := f.Wait()
	fmt.Println(res.Succeeded, err)
}

func ExampleRunsClient_ResumeWithResult() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	res, err := c.Runs.ResumeWithResult(ctx, "run-id")
	if err != nil {
		log.Fatal(err) // the call itself failed
	}
	if res.Declined {
		// The server answered but did not resume the run. Show its message;
		// do not infer a reason from it.
		fmt.Println("not resumed:", res.Message)
		return
	}
	fmt.Println("resumed as", res.Run.GetId()) // may differ from the id you passed

	msg, err := c.Runs.CancelWithResult(ctx, res.Run.GetId())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(msg.Message)
}

func ExampleInboxClient_List() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// The cheap call, for a badge: counts only, no items.
	n, err := c.Inbox.Counts(ctx, "")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n.Blocking, "items are holding a run")

	var seen []string
	it := c.Inbox.List(ctx, reliant.ListInbox{
		Kinds: []reliant.InboxItemKind{reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING},
		Limit: 50,
	})
	for it.Next() {
		item := it.Value()
		fmt.Println(item.GetKind(), item.GetTriggerName(), item.GetWaitingSince())
		seen = append(seen, item.GetItemId())
	}
	if err := it.Err(); errors.Is(err, reliant.ErrTruncated) {
		fmt.Println("the inbox is longer than the client will fetch; narrow the filter")
	} else if err != nil {
		log.Fatal(err)
	}

	// Dismissing hides an item. It does not resolve an approval or a question:
	// the run keeps waiting.
	if err := c.Inbox.Dismiss(ctx, seen...); err != nil {
		log.Fatal(err)
	}
	if err := c.Inbox.Restore(ctx, seen...); err != nil {
		log.Fatal(err)
	}
}

func ExampleApprovalsClient_ListForRun() {
	ctx := context.Background()
	c, err := reliant.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	approvals, err := c.Approvals.ListForRun(ctx, "run-id")
	if err != nil {
		log.Fatal(err)
	}
	for _, a := range approvals {
		fmt.Println(a.GetId(), a.GetTitle(), a.GetStatus())
	}
}
