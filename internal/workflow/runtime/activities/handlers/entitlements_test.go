// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// No real tool is gated yet, so these gate websearch for the length of each
// test (tools.GateToolForTest), which swaps process-wide state: none of them
// runs in parallel.

const (
	entitlementOwner = "user-1"
	entitlementOrg   = "55949942-3ee9-4cfc-bb7c-817bdba75186"
)

func entitlementChat() *db.Chat {
	org := entitlementOrg
	return &db.Chat{ID: "chat-1", UserID: entitlementOwner, OrgID: &org}
}

func reachesEverything() tools.ToolAccess {
	return tools.ResolveToolAccess([]string{tools.ToolView}, []string{tools.LoadableWildcard}, nil)
}

func TestEntitledFeatures(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	ctx := context.Background()

	t.Run("asks the gate for the run's owner and org", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, userID, orgID string) (bool, error) {
			return f == featuregate.GTM && userID == entitlementOwner && orgID == entitlementOrg, nil
		}}
		assert.Equal(t, map[string]bool{"gtm": true}, entitledFeatures(ctx, gate, entitlementChat(), reachesEverything()))
	})
	t.Run("a refusal entitles nothing", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return false, nil }}
		assert.Empty(t, entitledFeatures(ctx, gate, entitlementChat(), reachesEverything()))
	})
	t.Run("an error fails closed", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) {
			return true, errors.New("membership outage")
		}}
		assert.Empty(t, entitledFeatures(ctx, gate, entitlementChat(), reachesEverything()))
	})
	t.Run("the ask is bounded by a deadline", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(ctx context.Context, _ featuregate.Feature, _, _ string) (bool, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.LessOrEqual(t, time.Until(deadline), entitlementTimeout)
			return true, nil
		}}
		assert.NotEmpty(t, entitledFeatures(ctx, gate, entitlementChat(), reachesEverything()))
	})
	t.Run("no gate entitles nothing", func(t *testing.T) {
		assert.Empty(t, entitledFeatures(ctx, nil, entitlementChat(), reachesEverything()))
	})
	t.Run("no chat entitles nothing", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return true, nil }}
		assert.Empty(t, entitledFeatures(ctx, gate, nil, reachesEverything()))
	})
	t.Run("a declaration that reaches no gated tool never asks", func(t *testing.T) {
		gate := &featuregate.MockGate{}
		access := tools.ResolveToolAccess([]string{tools.ToolView}, []string{tools.ToolEdit}, nil)
		assert.Empty(t, entitledFeatures(ctx, gate, entitlementChat(), access))
		assert.Zero(t, gate.CallCount("Allowed"))
	})
}

// A call by name to a gated tool the owner is not entitled to is refused at
// execution with exactly the unknown tool's text, on a recorded set and on a
// batch that predates the set.
func TestCapabilityRefusal_NotEntitledReadsAsUnknown(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	const unknown = "zz_no_such_tool"
	call := func(name string) message.ToolCall { return message.ToolCall{ID: "c1", Name: name, Input: "{}"} }

	caps := tools.ResolveCapabilities(tools.CapabilityInputs{
		Access:     tools.ResolveToolAccess([]string{tools.ToolView, tools.ToolWebSearch}, []string{tools.LoadableWildcard}, nil),
		Permission: tools.PermissionMutating,
	})
	// The set travels to execute_tools on the wire.
	caps = tools.CapabilitiesFromProto(caps.Proto())
	want := strings.ReplaceAll(capabilityRefusal(caps, call(unknown)), unknown, tools.ToolWebSearch)
	require.NotEmpty(t, want)
	assert.Equal(t, want, capabilityRefusal(caps, call(tools.ToolWebSearch)))

	legacy := capabilityRefusal(nil, call(tools.ToolWebSearch))
	assert.Equal(t, strings.ReplaceAll(tools.LegacyCapabilities(false).Explain(unknown), unknown, tools.ToolWebSearch), legacy)

	entitled := tools.ResolveCapabilities(tools.CapabilityInputs{
		Access:           tools.ResolveToolAccess([]string{tools.ToolView, tools.ToolWebSearch}, nil, nil),
		Permission:       tools.PermissionMutating,
		EntitledFeatures: map[string]bool{string(featuregate.GTM): true},
	})
	assert.Empty(t, capabilityRefusal(tools.CapabilitiesFromProto(entitled.Proto()), call(tools.ToolWebSearch)))
}
