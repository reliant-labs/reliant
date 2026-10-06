// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeWithAccess is a tool context for a turn whose node declared the given
// preloaded and loadable sets, resolved the way call_llm resolves them, and
// that set itself.
func scopeWithAccess(t *testing.T, preloaded, loadable []string) (*rctx.ToolContext, *Capabilities) {
	t.Helper()
	caps := declaredCaps(PermissionMutating, preloaded, loadable, nil)
	return toolCtxWithCaps(t, caps), caps
}

// TestDefaultAgentCanStillLoadGenerateImage is the regression that shipped.
//
// The default coding agent preloads `tag:coding:default`, and generate_image is
// deliberately NOT TagCodingDefault — it spends real money on a provider the user may
// not have configured. No builtin or preset names it anywhere, so load_tool is
// its ONLY route.
//
// Enforcing the preloaded bundle at the load site made that route impossible
// and broke a feature that had already shipped. The bundle says what the agent
// is HANDED; it is not a statement about what it may reach.
func TestDefaultAgentCanStillLoadGenerateImage(t *testing.T) {
	t.Parallel()

	defaultTools := ExpandToolFilter([]string{"tag:coding:default"}, nil)
	require.NotContains(t, defaultTools, ToolGenerateImage,
		"precondition: generate_image is deliberately not in tag:coding:default")

	// The default agent declares no loadable_tools, which means unrestricted.
	ctx, _ := scopeWithAccess(t, []string{"tag:coding:default"}, []string{LoadableWildcard})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.False(t, resp.IsError,
		"the default agent must be able to opt into generate_image via load_tool: %s", resp.Content)
}

// TestUndeclaredLoadableMeansNothing is the inversion of what this file used
// to assert, and the inversion is the point.
//
// Declaring nothing used to mean EVERYTHING: a node that configured two
// preloaded tools silently granted load access to the whole registry,
// generate_image included — a tool kept out of every bundle precisely because
// calling it spends the user's money. "Say nothing, get everything" is the
// wrong direction for a default, and it could not be stated without a reader
// asking "wait, really?".
//
// Reaching everything is still one word: loadable_tools: ["*"].
func TestUndeclaredLoadableMeansNothing(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, []string{ToolView}, nil)

	tool := &loadToolTool{}
	for _, name := range []string{ToolWrite, ToolEdit, ToolGenerateImage} {
		resp, err := tool.Execute(ctx, LoadToolParams{Name: name})
		require.NoError(t, err)
		assert.True(t, resp.IsError,
			"a node that declared no loadable tools must not reach %q", name)
	}
}

// TestWildcardLoadableMeansEverything — what the coding workflows write
// explicitly, so the intent is visible rather than resting on a default.
func TestWildcardLoadableMeansEverything(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, []string{ToolView}, []string{LoadableWildcard})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "\"*\" must allow any registry tool: %s", resp.Content)
}

// TestDeclaredLoadableRestricts is the capability this split buys: a workflow
// that genuinely wants a closed set can say so, and it holds.
func TestDeclaredLoadableRestricts(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, []string{ToolView}, []string{ToolEdit})

	tool := &loadToolTool{}

	allowed, err := tool.Execute(ctx, LoadToolParams{Name: ToolEdit})
	require.NoError(t, err)
	assert.False(t, allowed.IsError, "a named loadable tool must load: %s", allowed.Content)

	refused, err := tool.Execute(ctx, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.True(t, refused.IsError,
		"a tool outside a DECLARED loadable set must be refused")
}

// TestAbsentAndEmptyLoadableAgree replaces a test that pinned them as
// DIFFERENT — absent meaning unrestricted, empty meaning nothing.
//
// That distinction could not be carried by the slice, since both arrive as
// len 0, so it needed a companion bool threaded through every caller; and it
// was the one place in tool resolution where saying less got you more.
// Collapsing the two is what lets the rule be one sentence — a workflow gets
// what it declares — and lets the "was it declared?" flag disappear from four
// signatures.
func TestAbsentAndEmptyLoadableAgree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		loadable []string
	}{
		{"absent", nil},
		{"empty", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := scopeWithAccess(t, []string{ToolView}, tc.loadable)

			tool := &loadToolTool{}
			resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
			require.NoError(t, err)
			assert.True(t, resp.IsError,
				"%s loadable must allow nothing beyond the preloaded bundle", tc.name)
		})
	}
}

// TestPreloadedToolIsAlwaysLoadable — refusing to "load" a tool the agent is
// already holding would be incoherent, and a workflow that lists a tool in both
// places should not be punished for it.
func TestPreloadedToolIsAlwaysLoadable(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, []string{ToolWrite}, []string{ToolEdit})

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.False(t, resp.IsError,
		"a preloaded tool must remain loadable: %s", resp.Content)
}

// TestDiscoveryMatchesEnforcement is the invariant whose violation produced the
// visible symptom: load_tool's description lists deferred tools by name, which
// the model reads as a promise. Advertising a tool that is then refused teaches
// it to retry something that cannot work, and it cannot tell a policy refusal
// from a malfunction.
func TestDiscoveryMatchesEnforcement(t *testing.T) {
	t.Parallel()

	ctx, caps := scopeWithAccess(t, []string{ToolView}, []string{ToolEdit, ToolLoadTool})

	advertised := caps.Deferred()
	require.NotEmpty(t, advertised, "precondition: something must be advertised")

	tool := &loadToolTool{}
	for _, name := range advertised {
		resp, err := tool.Execute(ctx, LoadToolParams{Name: name})
		require.NoError(t, err)
		assert.False(t, resp.IsError,
			"load_tool advertises %q but refuses it: %s", name, resp.Content)
	}
}

// TestDiscoveryUnrestrictedAdvertisesBeyondThePreloadedSet is the other half:
// discovery must not narrow to the preloaded bundle when the scope is
// unrestricted, or the agent can never find the tools it is allowed to load.
func TestDiscoveryUnrestrictedAdvertisesBeyondThePreloadedSet(t *testing.T) {
	t.Parallel()

	_, caps := scopeWithAccess(t, []string{ToolView}, []string{LoadableWildcard})

	advertised := caps.Deferred()
	assert.Contains(t, advertised, ToolGenerateImage,
		"an unrestricted scope must advertise tools outside the preloaded bundle")
}
