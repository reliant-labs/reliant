// Copyright (c) 2025 Reliant Labs
package prreviewwait

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

type memory struct {
	sub                Subscription
	outcome            Outcome
	notified, consumed int
	failAck            bool
}

func (m *memory) Binding(context.Context, string, int64, int64) (prhandoff.Binding, error) {
	return prhandoff.Binding{}, nil
}
func (m *memory) Arm(context.Context, Request, runcontinuity.Result) (Subscription, error) {
	return m.sub, nil
}
func (m *memory) Ready(context.Context, Subscription) error            { return nil }
func (m *memory) Read(context.Context, Subscription) (Outcome, error)  { return m.outcome, nil }
func (m *memory) Consume(context.Context, Subscription, Outcome) error { m.consumed++; return nil }
func (m *memory) Cancel(context.Context, Subscription) error           { m.sub.State = "cancelled"; return nil }
func (m *memory) Put(_ context.Context, o Outcome) (Subscription, error) {
	m.outcome = o
	return m.sub, nil
}
func (m *memory) Advance(_ context.Context, s Subscription, p runcontinuity.Result) (Subscription, error) {
	if s.CurrentRunID != p.Current.RunID {
		m.sub.CurrentRunID = p.Current.RunID
		m.sub.NotifiedSequence = 0
	}
	return m.sub, nil
}
func (m *memory) Notified(_ context.Context, _ Subscription, seq int64) error {
	if m.failAck {
		m.failAck = false
		return errors.New("crash after signal")
	}
	m.notified++
	m.sub.NotifiedSequence = seq
	return nil
}
func (m *memory) Unavailable(context.Context, Subscription) error {
	m.sub.State = "needs_human"
	return nil
}

type continuity struct {
	current runcontinuity.Execution
	live    bool
}

func (c *continuity) Resolve(_ context.Context, e runcontinuity.Execution) (runcontinuity.Result, error) {
	return runcontinuity.Result{Original: e, Current: c.current, Live: c.live}, nil
}

type signal struct {
	c        *continuity
	attempts []runcontinuity.Execution
	race     bool
}

func (s *signal) SignalReview(_ context.Context, e runcontinuity.Execution, _ string) error {
	s.attempts = append(s.attempts, e)
	if s.race {
		s.race = false
		s.c.current.RunID = "successor"
		return errors.New("old execution closed")
	}
	return nil
}

func TestPublishRolloverCrashAndAckNeverConsumes(t *testing.T) {
	ctx := context.Background()
	original := runcontinuity.Execution{WorkflowID: "wf", RunID: "original"}
	m := &memory{sub: Subscription{ID: "s", BindingID: "b", HeadSHA: strings.Repeat("a", 40), WorkflowID: "wf", OriginalRunID: "original", CurrentRunID: "original", State: "armed"}, failAck: true}
	c := &continuity{current: original, live: true}
	signals := &signal{c: c, race: true}
	svc, err := New(Deps{Store: m, Continuity: c, Signals: signals})
	if err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{BindingID: "b", HeadSHA: m.sub.HeadSHA, ProviderRef: "review:1", Sequence: 1, State: "changes"}
	if err := svc.Publish(ctx, outcome); err == nil {
		t.Fatal("crash must return retryable error")
	}
	if m.consumed != 0 || m.sub.NotifiedSequence != 0 {
		t.Fatal("signal was claimed consumed/acked")
	}
	if err := svc.Publish(ctx, outcome); err != nil {
		t.Fatal(err)
	}
	if m.sub.CurrentRunID != "successor" || m.consumed != 0 || m.sub.NotifiedSequence != 1 {
		t.Fatalf("wrong cursors %+v", m)
	}
	before := len(signals.attempts)
	if err := svc.Publish(ctx, outcome); err != nil {
		t.Fatal(err)
	}
	if len(signals.attempts) != before {
		t.Fatal("duplicate signal after acknowledged delivery")
	}
	c.live = false
	outcome.Sequence = 2
	outcome.ProviderRef = "review:2"
	if err := svc.Publish(ctx, outcome); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if m.sub.State != "needs_human" {
		t.Fatal("terminal author not explicit")
	}
}
