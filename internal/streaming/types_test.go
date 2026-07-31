package streaming

import "testing"

// Delta identity: deltas carry the message they belong to (MessageID) and a
// monotonically increasing StreamSeq. The coalescer must never merge deltas
// across message boundaries — a retry re-stream allocates a new message id and
// merging its text into the previous attempt's tail corrupts the rendered
// message. When merging within one message, the LATER StreamSeq must win so
// downstream consumers see the high-water mark.

func identifiedDelta(msgID string, seq int64, thread string, block int, text string) StreamingDelta {
	return StreamingDelta{
		DeltaType:  DeltaTypeContentBlockDelta,
		MessageID:  msgID,
		StreamSeq:  seq,
		Thread:     thread,
		BlockIndex: block,
		Delta:      text,
	}
}

func TestCanCoalesceWith_MessageID(t *testing.T) {
	tests := []struct {
		name string
		a, b StreamingDelta
		want bool
	}{
		{
			name: "same message id coalesces",
			a:    identifiedDelta("m1", 1, "t", 0, "a"),
			b:    identifiedDelta("m1", 2, "t", 0, "b"),
			want: true,
		},
		{
			name: "different message ids never coalesce",
			a:    identifiedDelta("m1", 1, "t", 0, "a"),
			b:    identifiedDelta("m2", 2, "t", 0, "b"),
			want: false,
		},
		{
			name: "id-less delta does not coalesce with identified delta",
			a:    identifiedDelta("", 0, "t", 0, "a"),
			b:    identifiedDelta("m1", 1, "t", 0, "b"),
			want: false,
		},
		{
			name: "identified delta does not coalesce with id-less delta",
			a:    identifiedDelta("m1", 1, "t", 0, "a"),
			b:    identifiedDelta("", 0, "t", 0, "b"),
			want: false,
		},
		{
			name: "both id-less still coalesce (legacy streams)",
			a:    identifiedDelta("", 0, "t", 0, "a"),
			b:    identifiedDelta("", 0, "t", 0, "b"),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.canCoalesceWith(tt.b); got != tt.want {
				t.Errorf("canCoalesceWith() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCoalesce_KeepsLaterStreamSeq(t *testing.T) {
	a := identifiedDelta("m1", 3, "t", 0, "hello ")
	b := identifiedDelta("m1", 7, "t", 0, "world")

	merged := a.coalesce(b)
	if merged.Delta != "hello world" {
		t.Errorf("Delta = %q, want %q", merged.Delta, "hello world")
	}
	if merged.StreamSeq != 7 {
		t.Errorf("StreamSeq = %d, want 7 (the later delta's seq)", merged.StreamSeq)
	}
	if merged.MessageID != "m1" {
		t.Errorf("MessageID = %q, want m1", merged.MessageID)
	}
}

func TestCoalesce_ZeroSeqDoesNotClobber(t *testing.T) {
	// Legacy id-less deltas carry StreamSeq 0; merging them must not reset an
	// existing seq (can only happen when both are id-less, but pin it anyway).
	a := identifiedDelta("", 5, "t", 0, "x")
	b := identifiedDelta("", 0, "t", 0, "y")
	merged := a.coalesce(b)
	if merged.StreamSeq != 5 {
		t.Errorf("StreamSeq = %d, want 5 (zero must not clobber)", merged.StreamSeq)
	}
}
