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
	"github.com/reliant-labs/reliant/internal/store"
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
		assert.Equal(t, map[string]bool{"gtm": true}, entitledFeatures(ctx, gate, nil, entitlementChat(), reachesEverything()))
	})
	t.Run("a refusal entitles nothing", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return false, nil }}
		assert.Empty(t, entitledFeatures(ctx, gate, nil, entitlementChat(), reachesEverything()))
	})
	t.Run("an error fails closed", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) {
			return true, errors.New("membership outage")
		}}
		assert.Empty(t, entitledFeatures(ctx, gate, nil, entitlementChat(), reachesEverything()))
	})
	t.Run("the ask is bounded by a deadline", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(ctx context.Context, _ featuregate.Feature, _, _ string) (bool, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.LessOrEqual(t, time.Until(deadline), entitlementTimeout)
			return true, nil
		}}
		assert.NotEmpty(t, entitledFeatures(ctx, gate, nil, entitlementChat(), reachesEverything()))
	})
	t.Run("no gate entitles nothing", func(t *testing.T) {
		assert.Empty(t, entitledFeatures(ctx, nil, nil, entitlementChat(), reachesEverything()))
	})
	t.Run("no chat entitles nothing", func(t *testing.T) {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return true, nil }}
		assert.Empty(t, entitledFeatures(ctx, gate, nil, nil, reachesEverything()))
	})
	t.Run("a declaration that reaches no gated tool never asks", func(t *testing.T) {
		gate := &featuregate.MockGate{}
		access := tools.ResolveToolAccess([]string{tools.ToolView}, []string{tools.ToolEdit}, nil)
		assert.Empty(t, entitledFeatures(ctx, gate, nil, entitlementChat(), access))
		assert.Zero(t, gate.CallCount("Allowed"))
	})
}

// A call by name to a gated tool the owner is not entitled to is refused at
// execution with exactly the unknown tool's text on a recorded set. A batch
// that predates the set judges it exactly as a name outside the registry: it
// passes the same tier check, and the executor answers TOOL_NOT_FOUND
// (toolexec's TestLocalToolExecutor_HiddenGatedToolIsNotFound).
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

	assert.Equal(t, strings.ReplaceAll(capabilityRefusal(nil, call(unknown)), unknown, tools.ToolWebSearch),
		capabilityRefusal(nil, call(tools.ToolWebSearch)), "no set: judged as a name outside the registry")

	entitled := tools.ResolveCapabilities(tools.CapabilityInputs{
		Access:           tools.ResolveToolAccess([]string{tools.ToolView, tools.ToolWebSearch}, nil, nil),
		Permission:       tools.PermissionMutating,
		EntitledFeatures: map[string]bool{string(featuregate.GTM): true},
	})
	assert.Empty(t, capabilityRefusal(tools.CapabilitiesFromProto(entitled.Proto()), call(tools.ToolWebSearch)))
}

// The gate's answers are kept per (user, org, feature): an answer for a
// minute, an error, as "no", for 5s.
func TestEntitledFeatures_Cache(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	cache := newEntitlementCache(func() time.Time { return now })

	answer, fail := true, false
	gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) {
		if fail {
			return false, errors.New("membership outage")
		}
		return answer, nil
	}}
	ask := func() map[string]bool {
		return entitledFeatures(ctx, gate, cache, entitlementChat(), reachesEverything())
	}

	assert.Equal(t, map[string]bool{"gtm": true}, ask())
	answer = false
	now = now.Add(entitlementAnswerTTL - time.Second)
	assert.Equal(t, map[string]bool{"gtm": true}, ask(), "a yes is kept for a minute")
	assert.Equal(t, 1, gate.CallCount("Allowed"))

	now = now.Add(2 * time.Second)
	assert.Empty(t, ask(), "then asked again")
	assert.Equal(t, 2, gate.CallCount("Allowed"))

	fail = true
	now = now.Add(entitlementAnswerTTL)
	assert.Empty(t, ask(), "an error fails closed")
	fail, answer = false, true
	now = now.Add(entitlementErrorTTL - time.Second)
	assert.Empty(t, ask(), "and is kept as no for 5s")
	assert.Equal(t, 3, gate.CallCount("Allowed"))
	now = now.Add(2 * time.Second)
	assert.Equal(t, map[string]bool{"gtm": true}, ask(), "then asked again")

	other := entitlementChat()
	other.UserID = "user-2"
	assert.Equal(t, map[string]bool{"gtm": true}, entitledFeatures(ctx, gate, cache, other, reachesEverything()))
	assert.Equal(t, 5, gate.CallCount("Allowed"), "answers are per user")
}

// call_llm hands the gate's answer to the capability resolver: the seam that
// fails if getAvailableTools stops passing EntitledFeatures.
func TestGetAvailableTools_OffersGatedToolsOnlyWhenEntitled(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	req := toolRequest{
		Preloaded:  []string{tools.ToolFetch, tools.ToolWebSearch},
		Loadable:   []string{tools.LoadableWildcard},
		Permission: tools.PermissionMutating,
		Thread:     "thread-1",
	}
	resolve := func(allowed bool) *tools.Capabilities {
		gate := &featuregate.MockGate{AllowedFunc: func(context.Context, featuregate.Feature, string, string) (bool, error) { return allowed, nil }}
		a := (&CallLLMActivity{}).WithFeatureGate(gate)
		return a.getAvailableTools(context.Background(), entitlementChat(), "", "", nil, req).Capabilities
	}

	entitled := resolve(true)
	assert.True(t, entitled.Offers(tools.ToolWebSearch))
	assert.Equal(t, []string{"gtm"}, entitled.EntitledFeatures)

	denied := resolve(false)
	assert.False(t, denied.Offers(tools.ToolWebSearch))
	assert.True(t, denied.Offers(tools.ToolFetch))
	assert.Empty(t, denied.EntitledFeatures)

	none := (&CallLLMActivity{}).getAvailableTools(context.Background(), entitlementChat(), "", "", nil, req).Capabilities
	assert.False(t, none.Offers(tools.ToolWebSearch), "no gate entitles nothing")
}

// The usage ledger records a call to a gated tool its caller is not entitled
// to as it records a name outside the registry: unknown, and unnamed.
func TestToolUsageKind_HiddenGatedTool(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	unknownKind, unknownName := toolUsageKind("zz_no_such_tool", false, nil)

	for name, caps := range map[string]*tools.Capabilities{
		"no set":       nil,
		"not entitled": {Permission: tools.PermissionMutating},
	} {
		kind, recorded := toolUsageKind(tools.ToolWebSearch, false, caps)
		assert.Equal(t, unknownKind, kind, name)
		assert.Equal(t, unknownName, recorded, name)
	}
	kind, recorded := toolUsageKind(tools.ToolWebSearch, false, &tools.Capabilities{Permission: tools.PermissionMutating, EntitledFeatures: []string{"gtm"}})
	assert.Equal(t, store.ToolKindBuiltin, kind)
	assert.Equal(t, tools.ToolWebSearch, recorded)
}
