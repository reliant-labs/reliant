package streaming

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoalesceDeltas_MergesOnlyAdjacentTextOfOneBlock(t *testing.T) {
	text := func(msg string, block int, s string, seq int64) StreamingDelta {
		return StreamingDelta{DeltaType: DeltaTypeContentBlockDelta, Thread: "t", MessageID: msg, BlockIndex: block, Delta: s, StreamSeq: seq}
	}
	in := []StreamingDelta{
		text("m1", 0, "a", 1),
		text("m1", 0, "b", 2),
		text("m1", 1, "c", 3), // another block: not merged
		{DeltaType: DeltaTypeToolUse, Thread: "t", MessageID: "m1", StreamSeq: 4},
		text("m1", 1, "d", 5), // not adjacent to "c" any more
		text("m2", 1, "e", 1), // another message: not merged
	}
	out := CoalesceDeltas(in)
	require.Len(t, out, 5)
	require.Equal(t, "ab", out[0].Delta)
	require.EqualValues(t, 2, out[0].StreamSeq)
	require.Equal(t, "c", out[1].Delta)
	require.Equal(t, DeltaTypeToolUse, out[2].DeltaType)
	require.Equal(t, "d", out[3].Delta)
	require.Equal(t, "e", out[4].Delta)
}
