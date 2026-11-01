// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The connected banner names the account the gateway says the credential acts
// as. A bare "connected" over another person's saved credential read as a
// success to the new user it was not connected for.
func TestConnectedNoticeNamesTheAccount(t *testing.T) {
	// By email when the gateway named one: a person recognises their email,
	// not their user id.
	require.Equal(t,
		"\n  ✓ Connected as alice@example.com — head back to Reliant to start working.\n\n    Press Ctrl+C to disconnect.\n\n",
		connectedNotice("user-42", "alice@example.com"))
	// No email (self-hosted, or an older control plane): by user id.
	require.Equal(t,
		"\n  ✓ Connected as account user-42 — head back to Reliant to start working.\n\n    Press Ctrl+C to disconnect.\n\n",
		connectedNotice("user-42", ""))
	// No account from the gateway: no account claimed.
	require.Equal(t,
		"\n  ✓ Daemon connected — you can head back to Reliant and start working.\n\n    Press Ctrl+C to disconnect.\n\n",
		connectedNotice("", ""))
}

// A registration ack replaces the account's email along with its id, so a
// reconnect as another account never names the previous account's email.
func TestRegistrationAckReplacesTheAccountEmail(t *testing.T) {
	d := &daemonClient{}
	d.recordAccount(&reliantv1.RegistrationAck{UserId: "user-1", AccountEmail: "one@example.com"})
	userID, email := d.currentAccount()
	require.Equal(t, "user-1", userID)
	require.Equal(t, "one@example.com", email)

	// Another account, from a gateway that names no email: no email.
	d.recordAccount(&reliantv1.RegistrationAck{UserId: "user-2"})
	userID, email = d.currentAccount()
	require.Equal(t, "user-2", userID)
	require.Empty(t, email)

	// An ack naming no account keeps the one it has.
	d.recordAccount(&reliantv1.RegistrationAck{})
	userID, _ = d.currentAccount()
	require.Equal(t, "user-2", userID)
}
