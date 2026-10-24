// Copyright (c) 2025 Reliant Labs
package netguard

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
