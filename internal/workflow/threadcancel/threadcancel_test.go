// Copyright (c) 2025 Reliant Labs
package threadcancel

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The signal name is not a label, it is a wire contract already bound in every
// live workflow execution's signal channel. A rename is a silent failure: the
// sender succeeds, nothing is listening, and the spawn the user asked to stop
// keeps running while the UI says cancelled.
func TestSignalName_IsStable(t *testing.T) {
	t.Parallel()
	require.Equal(t, "cancel_thread", SignalName)
}

// Both ids must serialize under the names the receiver has always read, and
// an unset id must stay absent rather than arriving as "" — the receiver
// records whichever ids are present, and a blank one would mark a spawn
// nothing can match.
func TestSignal_JSONTags(t *testing.T) {
	t.Parallel()

	both, err := json.Marshal(Signal{Thread: "t-1", ToolCallID: "toolu_1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"thread":"t-1","tool_call_id":"toolu_1"}`, string(both))

	threadOnly, err := json.Marshal(Signal{Thread: "t-1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"thread":"t-1"}`, string(threadOnly))

	var decoded Signal
	require.NoError(t, json.Unmarshal([]byte(`{"thread":"t-2","tool_call_id":"toolu_2"}`), &decoded))
	require.Equal(t, Signal{Thread: "t-2", ToolCallID: "toolu_2"}, decoded)
}
