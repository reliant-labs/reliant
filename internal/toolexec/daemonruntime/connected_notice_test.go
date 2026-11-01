// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The connected banner names the account the gateway says the credential acts
// as. A bare "connected" over another person's saved credential read as a
// success to the new user it was not connected for.
func TestConnectedNoticeNamesTheAccount(t *testing.T) {
	require.Equal(t,
		"\n  ✓ Connected as account user-42 — head back to Reliant to start working.\n\n    Press Ctrl+C to disconnect.\n\n",
		connectedNotice("user-42"))
	// No account from the gateway: no account claimed.
	require.Equal(t,
		"\n  ✓ Daemon connected — you can head back to Reliant and start working.\n\n    Press Ctrl+C to disconnect.\n\n",
		connectedNotice(""))
}
