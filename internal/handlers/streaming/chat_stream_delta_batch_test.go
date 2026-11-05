package streaming

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/streaming"
)

func textDelta(thread, msg string, block int, text string) streaming.StreamingDelta {
	return streaming.StreamingDelta{DeltaType: streaming.DeltaTypeContentBlockDelta, Thread: thread, MessageID: msg, BlockIndex: block, Delta: text}
}

// A chat open finds the chat's recent deltas already waiting: they leave as
// one frame, text merged per block, structural deltas kept in order.
func TestDrainDeltas_BacklogIsOneOrderedBatch(t *testing.T) {
	ch := make(chan streaming.StreamingDelta, 16)
	ch <- textDelta("t1", "m1", 0, "lo")
	ch <- textDelta("t1", "m1", 0, " world")
	ch <- streaming.StreamingDelta{DeltaType: streaming.DeltaTypeContentBlockStop, Thread: "t1", MessageID: "m1"}
	ch <- textDelta("t2", "m2", 0, "a")
	ch <- textDelta("t2", "m2", 0, "b")

	batch, open := drainDeltas(textDelta("t1", "m1", 0, "hel"), ch)

	require.True(t, open)
	require.Len(t, batch, 3)
	require.Equal(t, "hello world", batch[0].Delta)
	require.Equal(t, streaming.DeltaTypeContentBlockStop, batch[1].DeltaType)
	require.Equal(t, "ab", batch[2].Delta)
	require.Empty(t, ch)
}

// A delta that arrives on an idle channel goes out alone, at once.
func TestDrainDeltas_LiveDeltaIsNotHeld(t *testing.T) {
	ch := make(chan streaming.StreamingDelta)
	batch, open := drainDeltas(textDelta("t1", "m1", 0, "x"), ch)
	require.True(t, open)
	require.Equal(t, []streaming.StreamingDelta{textDelta("t1", "m1", 0, "x")}, batch)
}

func TestDrainDeltas_BoundedAndReportsClose(t *testing.T) {
	ch := make(chan streaming.StreamingDelta, maxDeltaBatch+10)
	for i := 0; i < maxDeltaBatch+10; i++ {
		ch <- streaming.StreamingDelta{DeltaType: streaming.DeltaTypeToolUse, Thread: "t1"}
	}
	batch, open := drainDeltas(streaming.StreamingDelta{DeltaType: streaming.DeltaTypeToolUse}, ch)
	require.True(t, open)
	require.Len(t, batch, maxDeltaBatch)
	require.Len(t, ch, 11)

	closed := make(chan streaming.StreamingDelta, 1)
	closed <- textDelta("t1", "m1", 0, "y")
	close(closed)
	batch, open = drainDeltas(textDelta("t1", "m1", 0, "x"), closed)
	require.False(t, open)
	require.Len(t, batch, 1)
	require.Equal(t, "xy", batch[0].Delta)
}
