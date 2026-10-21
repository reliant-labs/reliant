package toolexec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Two callers with the same project path but a different user or daemon see
// different MCP servers, so the identity the tool-list cache keys on must
// differ between them and agree for the same caller.
func TestDaemonMCPRuntime_ListIdentity(t *testing.T) {
	id := func(user string, sel *DaemonSelector) string {
		return (&daemonMCPRuntime{userID: user, selector: sel}).MCPListIdentity()
	}

	require.Equal(t, id("u1", nil), id("u1", nil))
	require.NotEqual(t, id("u1", nil), id("u2", nil), "users must not share a list")
	require.NotEqual(t, id("u1", nil), id("u1", &DaemonSelector{ID: "d1"}), "default resolution is its own identity")
	require.NotEqual(t, id("u1", &DaemonSelector{ID: "d1"}), id("u1", &DaemonSelector{ID: "d2"}), "daemons must not share a list")
	require.NotEqual(t, id("u1", &DaemonSelector{Name: "a"}), id("u1", &DaemonSelector{Type: "a"}))
	require.Equal(t,
		id("u1", &DaemonSelector{Labels: map[string]string{"a": "1", "b": "2"}}),
		id("u1", &DaemonSelector{Labels: map[string]string{"b": "2", "a": "1"}}),
		"label order must not matter")
	require.NotEqual(t,
		id("u1", &DaemonSelector{Labels: map[string]string{"a": "1"}}),
		id("u1", &DaemonSelector{Labels: map[string]string{"a": "2"}}))
}
