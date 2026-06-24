// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// getCELCompletions is a test helper that calls GetCELCompletions on a fresh service instance.
func getCELCompletions(t *testing.T) *reliantv1.GetCELCompletionsResponse {
	t.Helper()
	svc := NewCatalogService(nil) // GetCELCompletions uses no external deps
	resp, err := svc.GetCELCompletions(context.Background(), connect.NewRequest(&reliantv1.GetCELCompletionsRequest{}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	return resp.Msg
}

func TestGetCELCompletions_ReturnsNonEmptyData(t *testing.T) {
	msg := getCELCompletions(t)

	assert.GreaterOrEqual(t, len(msg.Namespaces), 6, "should have at least 6 namespaces")
	assert.GreaterOrEqual(t, len(msg.Functions), 10, "should have at least 10 functions")
	assert.Greater(t, len(msg.NodeOutputSchemas), 0, "should have node output schemas")
	assert.Greater(t, len(msg.HelperTypes), 0, "should have helper types")
}

func TestGetCELCompletions_KnownNamespacesPresent(t *testing.T) {
	msg := getCELCompletions(t)

	nsByName := make(map[string]*reliantv1.CELNamespaceInfo, len(msg.Namespaces))
	for _, ns := range msg.Namespaces {
		nsByName[ns.Name] = ns
	}

	t.Run("workflow namespace", func(t *testing.T) {
		ns, ok := nsByName["workflow"]
		require.True(t, ok, "workflow namespace must exist")
		assert.False(t, ns.IsDynamic)

		fieldNames := fieldNamesFromCELFields(ns.Fields)
		for _, expected := range []string{"id", "name", "path", "branch", "mode", "run_id", "session_id", "worktree_path"} {
			assert.Contains(t, fieldNames, expected, "workflow namespace should have field %q", expected)
		}
	})

	t.Run("iter namespace", func(t *testing.T) {
		ns, ok := nsByName["iter"]
		require.True(t, ok, "iter namespace must exist")
		assert.False(t, ns.IsDynamic)

		fieldNames := fieldNamesFromCELFields(ns.Fields)
		assert.Contains(t, fieldNames, "iteration")
	})

	t.Run("inputs namespace is dynamic", func(t *testing.T) {
		ns, ok := nsByName["inputs"]
		require.True(t, ok, "inputs namespace must exist")
		assert.True(t, ns.IsDynamic)
	})

	t.Run("nodes namespace is dynamic", func(t *testing.T) {
		ns, ok := nsByName["nodes"]
		require.True(t, ok, "nodes namespace must exist")
		assert.True(t, ns.IsDynamic)
	})
}

func TestGetCELCompletions_KnownFunctionsPresent(t *testing.T) {
	msg := getCELCompletions(t)

	fnNames := make(map[string]bool, len(msg.Functions))
	for _, fn := range msg.Functions {
		fnNames[fn.Name] = true
	}

	expected := []string{
		"parseJson",
		"toJson",
		"coalesce",
		"getOrDefault",
		"now",
		"spawn",
		"parseDuration",
	}
	for _, name := range expected {
		assert.True(t, fnNames[name], "function %q should be present", name)
	}
}

func TestGetCELCompletions_NodeOutputSchemas(t *testing.T) {
	msg := getCELCompletions(t)

	schemaByType := make(map[string]*reliantv1.CELNodeOutputSchema, len(msg.NodeOutputSchemas))
	for _, s := range msg.NodeOutputSchemas {
		schemaByType[s.NodeType] = s
	}

	t.Run("call_llm output fields", func(t *testing.T) {
		s, ok := schemaByType["call_llm"]
		require.True(t, ok, "call_llm schema must exist")

		fieldNames := fieldNamesFromCELFields(s.Fields)
		for _, expected := range []string{"response_text", "tool_calls", "token_count", "thinking"} {
			assert.Contains(t, fieldNames, expected, "call_llm output should have field %q", expected)
		}
	})

	t.Run("run output fields", func(t *testing.T) {
		s, ok := schemaByType["run"]
		require.True(t, ok, "run schema must exist")

		fieldNames := fieldNamesFromCELFields(s.Fields)
		for _, expected := range []string{"stdout", "stderr", "exit_code"} {
			assert.Contains(t, fieldNames, expected, "run output should have field %q", expected)
		}
	})
}

func TestGetCELCompletions_Idempotent(t *testing.T) {
	svc := NewCatalogService(nil)
	ctx := context.Background()
	req := connect.NewRequest(&reliantv1.GetCELCompletionsRequest{})

	resp1, err := svc.GetCELCompletions(ctx, req)
	require.NoError(t, err)

	resp2, err := svc.GetCELCompletions(ctx, connect.NewRequest(&reliantv1.GetCELCompletionsRequest{}))
	require.NoError(t, err)

	// Same pointer — sync.Once caching guarantees identical object.
	assert.Same(t, resp1.Msg, resp2.Msg, "cached response should return the same pointer")
}

// fieldNamesFromCELFields extracts field names from a slice of CELFieldInfo.
func fieldNamesFromCELFields(fields []*reliantv1.CELFieldInfo) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}
