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

	it := c.Runs.List(ctx, reliant.ListRuns{
		Workflow:      []string{"builtin://agent"},
		DisplayStates: []reliant.RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED},
		StartedAfter:  time.Now().Add(-24 * time.Hour),
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
			var answers []reliant.Answer
			for _, p := range g.Prompts {
				a := reliant.Answer{Question: p.Question}
				if len(p.Options) > 0 {
					a.Selected = p.Options[:1] // the first option's exact label
				} else {
					a.Freetext = "yes"
				}
				answers = append(answers, a)
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

	// The firing is asynchronous: look for its event until it settles.
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		events := c.Triggers.Events(ctx, fired.TriggerID, reliant.EventFilter{Limit: 10})
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
			log.Fatal(err)
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
