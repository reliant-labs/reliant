// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListServers and GetServer return each server's config.headers and
// config.env as the user stored them, literal API keys and bearer tokens
// included (mcpServerFromConfig). So they must not declare
// `idempotency_level = NO_SIDE_EFFECTS`: that lets Connect serve them over
// HTTP GET, whose responses a cache or proxy may store and replay. And the two
// fields are debug_redact, so forge's rpc forensics never reads them from
// InstallServer / UpdateServerConfig requests.
func TestMCPServerSecretsAreNotGETableAndAreDebugRedacted(t *testing.T) {
	sd := reliantv1.File_reliant_v1_mcp_proto.Services().ByName("MCPService")
	for _, name := range []string{"ListServers", "GetServer"} {
		md := sd.Methods().ByName(protoreflect.Name(name))
		require.NotNil(t, md, name)
		opts, _ := md.Options().(*descriptorpb.MethodOptions)
		require.NotEqual(t, descriptorpb.MethodOptions_NO_SIDE_EFFECTS, opts.GetIdempotencyLevel(),
			"%s returns stored MCP secrets: it must not be NO_SIDE_EFFECTS (servable over GET)", name)
	}

	cfg := (&reliantv1.MCPServerConfig{}).ProtoReflect().Descriptor()
	for _, field := range []string{"headers", "env"} {
		fd := cfg.Fields().ByName(protoreflect.Name(field))
		require.NotNil(t, fd, field)
		opts, _ := fd.Options().(*descriptorpb.FieldOptions)
		require.True(t, opts.GetDebugRedact(), "MCPServerConfig.%s carries user secrets: it must be debug_redact", field)
	}
}
