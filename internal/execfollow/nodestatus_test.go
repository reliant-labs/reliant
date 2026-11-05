// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// The SDK cannot name NodeExecutionStatus (streaming.proto is not public), so
// it carries the SKIPPED value as a constant. This is the other end of that
// agreement: a renumbering in the proto must fail here, not turn every skipped
// node back into a green tick.
func TestNodeStatusSkippedMatchesTheProto(t *testing.T) {
	if got, want := int32(follow.NodeStatusSkipped), int32(reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_SKIPPED); got != want {
		t.Fatalf("follow.NodeStatusSkipped = %d, but the proto says SKIPPED is %d", got, want)
	}
}
