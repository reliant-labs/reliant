// Copyright (c) 2025 Reliant Labs
package netguard

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Forge's refused set adds the Private Google Access VIPs and refuses the NAT64
// well-known prefix whole; the opt-in still opens all of it for a self-hosted
// deployment.
func TestForDeployment_ForgeRefusals(t *testing.T) {
	closed, open := ForDeployment(false), ForDeployment(true)
	for _, host := range []string{"199.36.153.4", "199.36.153.11", "64:ff9b::8.8.8.8"} {
		assert.ErrorIs(t, closed.CheckHost(context.Background(), host), ErrBlockedAddress, host)
		require.NoError(t, open.CheckHost(context.Background(), host), host)
	}
}

// A server role dials from a shared process, so ForDeployment refuses private
// targets unless an operator explicitly opted in. Nothing implied by the
// deployment's shape (such as an unset control-plane URL) opens them.
func TestForDeployment_FailsClosedUnlessOptedIn(t *testing.T) {
	closed := ForDeployment(false)
	assert.False(t, closed.AllowPrivate)
	assert.Equal(t, Policy{}, closed, "the closed policy is the zero Policy")
	for _, host := range []string{"127.0.0.1", "10.0.0.8", "169.254.169.254", "localhost", "64:ff9b::a00:1"} {
		assert.ErrorIs(t, closed.CheckHost(context.Background(), host), ErrBlockedAddress, host)
	}
	assert.False(t, closed.IPAllowed(net.ParseIP("169.254.169.254")))

	open := ForDeployment(true)
	assert.True(t, open.AllowPrivate)
	for _, host := range []string{"127.0.0.1", "10.0.0.8", "169.254.169.254", "localhost"} {
		require.NoError(t, open.CheckHost(context.Background(), host), host)
	}
}
