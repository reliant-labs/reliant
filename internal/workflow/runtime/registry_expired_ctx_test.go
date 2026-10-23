// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// deadlineRepo records the context each chat_updates write ran on.
type deadlineRepo struct {
	ActivityStore
	ctxErr      error
	hasDeadline bool
	deadlineIn  time.Duration
	traceValue  any
}

type traceKey struct{}

func (r *deadlineRepo) CreateChatUpdate(ctx context.Context, _ string, _ db.UpdateType, _ string, _ string) error {
	r.ctxErr = ctx.Err()
	d, ok := ctx.Deadline()
	r.hasDeadline, r.deadlineIn = ok, time.Until(d)
	r.traceValue = ctx.Value(traceKey{})
	return nil
}

// An activity whose context already expired still tells the UI it failed: the
// error event is written on a short context of its own, detached from the dead
// one but keeping its values (trace, logger attributes). On 2026-10-23 a
// starved worker's activities all hit their deadline, and the write used to be
// a second failure on top of the first.
func TestWriteErrorEventSurvivesAnExpiredActivityContext(t *testing.T) {
	t.Parallel()
	repo := &deadlineRepo{}
	wrapper := NewActivityWrapper(
		"DrainAgentMessages",
		func(_ context.Context, _ types.DrainAgentMessagesInput) (types.DrainAgentMessagesOutput, error) {
			return types.DrainAgentMessagesOutput{}, nil
		},
		repo,
	)
	ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), traceKey{}, "trace-1"), time.Now().Add(-time.Second))
	defer cancel()

	wrapper.writeErrorEvent(ctx, types.DrainAgentMessagesInput{ChatID: "chat-1"}, "DrainAgentMessages", "activity-1", 1, "workflow-1",
		fmt.Errorf("allocate chat update sequence: %w", context.DeadlineExceeded), 3)

	if repo.ctxErr != nil {
		t.Fatalf("the error event was written on a dead context: %v", repo.ctxErr)
	}
	if !repo.hasDeadline || repo.deadlineIn <= 0 || repo.deadlineIn > errorEventWriteTimeout {
		t.Fatalf("the write must have its own short deadline (≤%v), got %v (set=%v)", errorEventWriteTimeout, repo.deadlineIn, repo.hasDeadline)
	}
	if repo.traceValue != "trace-1" {
		t.Fatalf("the write lost the activity context's values: %v", repo.traceValue)
	}
}

// A failure is reported once, where it arose; the layers that pass it up see
// the mark through any wrapping and log it at DEBUG.
func TestReportedErrorSurvivesWrapping(t *testing.T) {
	t.Parallel()
	base := errors.New("the agent's tool calls failed 3 times in a row")
	if alreadyReported(base) || markReported(nil) != nil {
		t.Fatal("an unmarked error is not reported; nil stays nil")
	}
	marked := markReported(base)
	wrapped := fmt.Errorf("inline workflow review failed: %w", fmt.Errorf("loop agent failed: %w", marked))
	if !alreadyReported(wrapped) {
		t.Fatal("the mark must survive fmt.Errorf wrapping")
	}
	if !errors.Is(wrapped, base) || wrapped.Error() != "inline workflow review failed: loop agent failed: "+base.Error() {
		t.Fatalf("marking must not change the error: %v", wrapped)
	}
	if markReported(marked) != marked {
		t.Fatal("marking twice is a no-op")
	}
}
