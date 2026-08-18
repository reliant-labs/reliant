// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE REGRESSION, end to end through the real activity and a real database.
//
// A sub-agent's opening instruction ("## Get It Right — Attempt 1 of 4") is
// written by the SaveMessage activity when its thread is forked. The user
// paused mid-attempt and resumed; the resumed run wrote the seed a SECOND time,
// byte-identical, and the agent restarted work already in flight.
//
// The cause was the idempotency key: it embedded the Temporal RunID, and a
// resumed run has a new RunID, so the second write computed a different key and
// missed the dedup that already existed in threads.SaveMessage.
//
// These tests drive the activity twice with DIFFERENT RunIDs — the whole point,
// since that is the only thing resume changes — and assert on rows in the
// database rather than on return values, because the bug was a duplicate ROW.
//
// The Temporal test environment fixes its RunID at "default-test-run-id" and
// exposes no setter, so the two runs are simulated the way the activity
// actually consumes a RunID: through the key it derives. runInject() passes an
// explicit MessageIdempotencyKey (the production inject path) and
// runInjectRunScoped() passes the per-run key the old code built, so the two
// spellings can be compared directly against the same database.

// injectFixture is one chat with a forked child thread, ready to be seeded.
type injectFixture struct {
	h        *IdempotencyTestHelper
	chatID   string
	threadID string
	activity *SaveMessageActivity
}

func newInjectFixture(t *testing.T) *injectFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)

	ctx := context.Background()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, uuid.New().String(), uuid.New().String())
	h.CreateTestChat(ctx, chatID, uuid.New().String(), uuid.New().String())

	// The child thread the inject seeds, as ForChild would have created it.
	threadID := "thread-implement-" + uuid.New().String()[:8]
	h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	return &injectFixture{
		h:        h,
		chatID:   chatID,
		threadID: threadID,
		activity: NewSaveMessageActivity(h.Repo()),
	}
}

const injectSeedText = "## Get It Right — Attempt 1 of 4"

// runInject seeds the thread the way the production inject path does: with an
// explicit, run-independent idempotency key.
func (f *injectFixture) runInject(t *testing.T, workflowID, key string) {
	t.Helper()
	input := (&SaveMessageInput{
		ChatID:     f.chatID,
		Thread:     f.threadID,
		Role:       "user",
		Content:    injectSeedText,
		WorkflowID: workflowID,
	}).V3()
	input.Runtime.MessageIdempotencyKey = key

	var out SaveMessageOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, input, &out))
}

// runInjectRunScoped seeds the thread with the key the OLD code derived:
// workflowID + RunID + activityID. Taking runID as a parameter is what lets one
// test express "the same injection, in two different runs".
func (f *injectFixture) runInjectRunScoped(t *testing.T, workflowID, runID string) {
	t.Helper()
	input := (&SaveMessageInput{
		ChatID:     f.chatID,
		Thread:     f.threadID,
		Role:       "user",
		Content:    injectSeedText,
		WorkflowID: workflowID,
	}).V3()
	input.Runtime.MessageIdempotencyKey = workflowID + "-" + runID + "-0"

	var out SaveMessageOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, input, &out))
}

// seedCount counts how many times the opening instruction is present in the
// child's transcript. Two is the bug the user saw.
func (f *injectFixture) seedCount(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	messages, err := f.h.Repo().ListMessages(ctx, f.chatID, db.MessageListOptions{})
	require.NoError(t, err)

	count := 0
	for _, msg := range messages {
		if msg.ThreadID != f.threadID {
			continue
		}
		blocks, err := f.h.Repo().ListContentBlocks(ctx, msg.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT &&
				b.Content != nil && *b.Content == injectSeedText {
				count++
			}
		}
	}
	return count
}

// The fix: the same logical injection, under two different runs, writes ONE
// message. The key is derived from the graph position, so the resumed run
// recognizes the seed it already wrote.
func TestInject_SameFrameAcrossRuns_WritesOneMessage(t *testing.T) {
	f := newInjectFixture(t)

	// The stable key the production path supplies. Identical in both runs
	// precisely because it contains nothing about the run.
	const stableKey = "inject|7:wf-root|16:thread-implement|9:implement|6:iter:0"

	f.runInject(t, "wf-root", stableKey) // original run
	f.runInject(t, "wf-root", stableKey) // resumed run: NEW RunID, same frame

	assert.Equal(t, 1, f.seedCount(t),
		"the resumed run must recognize the seed it already wrote; a second copy "+
			"tells the sub-agent to restart work it had already started")
}

// THE CHARACTERIZATION OF THE BUG. Same fixture, same logical injection, but
// keyed the way the old code keyed it — scoped to the RunID. The duplicate
// appears. This is what the production transcript showed, and it is why the key
// had to stop naming the run.
//
// If this test ever reports 1, the RunID has stopped varying across runs and
// this whole fix is unnecessary — read that as a signal, not a pass.
func TestInject_RunScopedKey_DuplicatesAcrossRuns(t *testing.T) {
	f := newInjectFixture(t)

	f.runInjectRunScoped(t, "wf-root", "run-aaa") // original run
	f.runInjectRunScoped(t, "wf-root", "run-bbb") // resume mints a new RunID

	assert.Equal(t, 2, f.seedCount(t),
		"a RunID-scoped key cannot dedupe across a resume — this is the bug, "+
			"pinned so the fix cannot be quietly reverted")
}

// The negative half: genuinely different frames must EACH inject. A key that
// over-dedupes loses a real instruction, and a sub-agent that is never told
// what to do simply stalls — a worse failure than the duplicate, because
// nothing in the transcript shows what went wrong.
func TestInject_DifferentFrames_EachInject(t *testing.T) {
	f := newInjectFixture(t)

	// Four iterations of a retry loop: "Attempt 1 of 4" .. "Attempt 4 of 4".
	// Same node, same thread, different iteration.
	for _, key := range []string{
		"inject|7:wf-root|16:thread-implement|9:implement|6:iter:0",
		"inject|7:wf-root|16:thread-implement|9:implement|6:iter:1",
		"inject|7:wf-root|16:thread-implement|9:implement|6:iter:2",
		"inject|7:wf-root|16:thread-implement|9:implement|6:iter:3",
	} {
		f.runInject(t, "wf-root", key)
	}

	assert.Equal(t, 4, f.seedCount(t),
		"each loop iteration is a new instruction and must be delivered; "+
			"over-deduping strands the agent waiting on a message that was swallowed")
}

// A non-inject caller (an ordinary assistant message) supplies no explicit key
// and keeps the RunID-scoped derivation, which is correct for it: the message
// belongs to the run that produced it. This pins that the fix did not widen
// beyond the inject path.
func TestInject_NonInjectCallersKeepRunScopedKey(t *testing.T) {
	f := newInjectFixture(t)

	input := (&SaveMessageInput{
		ChatID:     f.chatID,
		Thread:     f.threadID,
		Role:       "assistant",
		Content:    "ordinary assistant reply",
		WorkflowID: "wf-root",
	}).V3()
	require.Empty(t, input.Runtime.MessageIdempotencyKey,
		"an ordinary SaveMessage caller supplies no explicit key")

	var out SaveMessageOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, input, &out))

	ctx := context.Background()
	msg, err := f.h.Repo().GetMessage(ctx, out.MessageId)
	require.NoError(t, err)
	require.NotNil(t, msg.ActivityID)
	assert.Contains(t, *msg.ActivityID, "wf-root",
		"the default key is still workflow+run scoped for non-inject callers")
	assert.NotContains(t, *msg.ActivityID, "inject|",
		"only the inject path uses the stable graph-position key")
}
