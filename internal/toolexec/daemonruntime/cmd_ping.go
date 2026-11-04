// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

func init() {
	RegisterCommand(toolexec.LinkProbeCommand, handlePing)
}

// handlePing answers the gateway's link probe. It does nothing on purpose: the
// probe measures the round trip a command makes — the stream both ways and this
// daemon's send backlog — and any work here would only blur that.
func handlePing(context.Context, []byte) ([]byte, error) {
	return []byte("{}"), nil
}
