// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// PREEMPTION (ToolDefinition.Preemptable)
//
// A thread blocked in shell_wait or spawn_status(wait) cannot read its mailbox:
// mail is delivered only by the next call_llm, and the next call_llm waits for
// the whole tool batch. So a message that arrived mid-wait — the user steering,
// a parent's instruction, a sub-agent's result — sat unread for up to twenty
// minutes. Preemption ends the wait as soon as mail arrives, and the thread's
// next turn reads it.
//
// Why here, and why poll:
//   - Every way a message reaches a thread lands in ONE place, a queued
//     agent_messages row — SendMessage to a live run, SendAgentMessage,
//     spawn_send in either direction, a sub-agent's completion report. Watching
//     that row covers every sender, including ones added later, without any of
//     them knowing preemption exists.
//   - It is activity code, so nothing here touches workflow determinism.
//   - A wait already polls every 500ms; a one-second check costs less than the
//     wait it ends early, and only runs while a preemptable call is in flight.
//
// The rest is the tool's half of the contract: a preemptable tool answers its
// context ending with a real result. The executor delivers the preemption by
// placement (toolexec.cancelOnPreemption / relayPreemptionToDaemon).

// preemptionPollInterval is how often a preemptable call checks for mail.
const preemptionPollInterval = time.Second

// preemptionStore is the one write preemption needs.
type preemptionStore interface {
	MarkQueuedAgentMessagesPreempting(ctx context.Context, toThreadID string, preemptedAt time.Time) (int64, error)
}

// batchPreemption is shared by every call in one ExecuteTools batch. Mail
// preempts the BATCH, not one call: the thread's next turn waits for every
// call in it, so a sibling wait left running would hold the message back just
// as long. Each message marks itself preempting once (see
// MarkQueuedAgentMessagesPreempting), so whichever call sees it first must end
// all of them.
type batchPreemption struct {
	store   preemptionStore
	thread  string
	preempt chan struct{}
	once    sync.Once
}

func newBatchPreemption(store preemptionStore, thread string) *batchPreemption {
	if store == nil || thread == "" {
		return nil
	}
	return &batchPreemption{store: store, thread: thread, preempt: make(chan struct{})}
}

func (b *batchPreemption) fire() {
	b.once.Do(func() { close(b.preempt) })
}

// fired reports whether mail has preempted the batch.
func (b *batchPreemption) fired() bool {
	select {
	case <-b.preempt:
		return true
	default:
		return false
	}
}

// arm starts watching for mail on behalf of one preemptable call, and returns
// the context to execute it under and a release to call when it returns.
// release reports whether the batch was preempted while the call ran; once it
// returns, the watcher has stopped, so the answer cannot change under the
// caller.
//
// The first check is one interval in, not immediately. A call that is not
// actually waiting (spawn_status without wait) finishes long before then and
// is never touched; a wait that started with mail already queued ends a second
// later than it could have, which is nothing against the twenty minutes it
// would otherwise have held the message.
func (b *batchPreemption) arm(ctx context.Context) (context.Context, func() bool) {
	callDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(preemptionPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-callDone:
				return
			case <-b.preempt:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			marked, err := b.store.MarkQueuedAgentMessagesPreempting(ctx, b.thread, time.Now().UTC())
			if err != nil {
				// A failed check is not a reason to stop checking: the wait
				// may run for many minutes yet.
				logging.Debug("[ExecuteTools] Preemption check failed; will retry",
					"thread", b.thread, "error", err)
				continue
			}
			if marked > 0 {
				logging.Info("[ExecuteTools] A message arrived for the thread; preempting its waiting calls",
					"thread", b.thread, "messages", marked)
				b.fire()
				return
			}
		}
	}()

	return tools.WithPreemption(ctx, b.preempt), func() bool {
		close(callDone)
		<-watcherDone
		return b.fired()
	}
}

type batchPreemptionContextKey struct{}

func withBatchPreemption(ctx context.Context, b *batchPreemption) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, batchPreemptionContextKey{}, b)
}

func batchPreemptionFrom(ctx context.Context) *batchPreemption {
	b, _ := ctx.Value(batchPreemptionContextKey{}).(*batchPreemption)
	return b
}

// preemptedNote is appended to the answer of a call that was running when mail
// arrived. It states only what is certain: the tool's own text says how far it
// got, and this says why the agent is hearing it now.
const preemptedNote = "\n\n[A message arrived for you while this call was running, so it returned early. The message follows this result.]"

// preemptedResultContent stands in for a preempted call whose answer was lost:
// a daemon too old to send it, or one that never answered in time. It says
// nothing about the state of what was being waited on, because nothing reported
// it.
const preemptedResultContent = "Stopped early: a message arrived for you while this call was running, and it follows this result. " +
	"Nothing was stopped or changed — whatever this call was waiting on is untouched. Repeat the call if you still need to wait."

// preemptedOutcome is what a preempted call reports. A clean answer from the
// tool is kept, with the note saying why it came early. Anything else — a
// cancellation, an abandoned wait, a transport error — is the preemption's
// doing, not the tool's, and is replaced with a SUCCESSFUL result: the call
// did exactly what it promised, and an error would tell the agent its wait
// failed when it was only interrupted by news.
func preemptedOutcome(res *toolexec.ToolResult, execErr error) *toolexec.ToolResult {
	if execErr == nil && res != nil && res.Success && !res.IsError {
		answered := *res
		answered.Content += preemptedNote
		answered.Metadata = withPreemptedMetadata(res.Metadata)
		return &answered
	}

	now := time.Now()
	stood := &toolexec.ToolResult{
		Success:   true,
		Content:   preemptedResultContent,
		Metadata:  withPreemptedMetadata(""),
		StartTime: now,
		EndTime:   now,
	}
	if res != nil {
		stood.StartTime = res.StartTime
	}
	return stood
}

// withPreemptedMetadata adds "preempted": true to a tool's JSON metadata, so
// the UI can say why the call ended early. Metadata that is not a JSON object
// is left alone rather than replaced: other readers (response_data) may
// depend on it.
func withPreemptedMetadata(metadata string) string {
	fields := map[string]any{}
	if metadata != "" {
		if err := json.Unmarshal([]byte(metadata), &fields); err != nil {
			return metadata
		}
	}
	fields["preempted"] = true
	encoded, err := json.Marshal(fields)
	if err != nil {
		return metadata
	}
	return string(encoded)
}
