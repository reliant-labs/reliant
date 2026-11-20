package toolexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listingRouter answers a machine's MCP listing: two servers, one of which
// cannot list its tools.
type listingRouter struct {
	routerStub
	failing map[string]bool
}

func (r *listingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	switch commandType {
	case "mcp.server_status":
		return []byte(`{"servers":[{"name":"tickets"},{"name":"docs"}]}`), nil
	case "mcp.list_tools":
		var req struct {
			ServerName string `json:"server_name"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		if r.failing[req.ServerName] {
			return []byte(`{"error":"MCP server \"` + req.ServerName + `\" exited before completing the handshake"}`), nil
		}
		return []byte(`{"tools":[{"name":"` + req.ServerName + `_tool"}]}`), nil
	}
	return nil, nil
}

// One server that cannot list its tools leaves out only its own. It used to
// fail the whole listing, so the turn lost every other server's tools too.
func TestDaemonMCPRuntime_OneFailingServerDoesNotHideTheOthers(t *testing.T) {
	router := &listingRouter{failing: map[string]bool{"tickets": true}}
	turn := bindSlowMCPRuntime(t, NewDaemonMCPContextBinder(router))

	tools, err := turn.MCP.ListProjectTools("/proj")
	require.NoError(t, err)
	assert.NotContains(t, tools, "tickets")
	require.Len(t, tools["docs"], 1)
	assert.Equal(t, "docs_tool", tools["docs"][0].Name)

	router.failing["docs"] = true
	_, err = turn.MCP.ListProjectTools("/proj")
	require.Error(t, err, "with every server failing the listing fails, and the caller keeps its last good one")
	assert.Contains(t, err.Error(), "tickets")
	assert.Contains(t, err.Error(), "docs")
}
