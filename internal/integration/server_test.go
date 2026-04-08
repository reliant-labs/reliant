package integration

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachIntegrationServerExecutor_BindsLocalMCPContextBinder(t *testing.T) {
	remote := toolexec.NewRemoteExecutor(nil)
	toolsFactory := tools.NewToolsFactory(&tools.ToolsOptions{})
	mcpManager := mcp.NewManager()
	defer func() { _ = mcpManager.Close() }()

	attachIntegrationServerExecutor(remote, toolsFactory, mcpManager)

	require.NotNil(t, remote)

	remoteValue := reflect.ValueOf(remote).Elem()
	serverExecutorField := remoteValue.FieldByName("serverExecutor")
	require.True(t, serverExecutorField.IsValid(), "remote executor should expose serverExecutor field")
	require.False(t, serverExecutorField.IsNil(), "integration remote executor should have a server executor attached")

	execValue := serverExecutorField.Elem()
	mcpBinderField := execValue.FieldByName("mcpBinder")
	require.True(t, mcpBinderField.IsValid(), "local executor should expose mcpBinder field")
	assert.False(t, mcpBinderField.IsNil(), "integration server executor should have an MCP binder attached")
}
