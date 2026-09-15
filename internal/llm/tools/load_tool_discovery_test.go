// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeWithAccess records a workflow's declared access and returns a tool
// context bound to the same scope.
func scopeWithAccess(t *testing.T, name string, preloaded, loadable []string, declaredLoadable bool) (*rctx.ToolContext, string) {
	t.Helper()

	chatID := name
	const thread = "0"
	scopeKey := Scope(chatID, thread)

	store := GetLoadedToolsStore()
	store.Clear(scopeKey)
	store.SetPermission(scopeKey, PermissionMutating)
	store.SetToolAccess(scopeKey, ResolveToolAccess(preloaded, loadable, declaredLoadable, nil))
	t.Cleanup(func() { store.Clear(scopeKey) })

	worktree := &rctx.WorktreeInfo{ID: "test", Path: t.TempDir()}
	return rctx.NewToolContext(context.Background(), chatID, thread, nil, worktree), scopeKey
}

// TestDefaultAgentCanStillLoadGenerateImage is the regression that shipped.
//
// The default coding agent preloads `tag:default`, and generate_image is
// deliberately NOT TagDefault — it spends real money on a provider the user may
// not have configured. No builtin or preset names it anywhere, so load_tool is
// its ONLY route.
//
// Enforcing the preloaded bundle at the load site made that route impossible
// and broke a feature that had already shipped. The bundle says what the agent
// is HANDED; it is not a statement about what it may reach.
func TestDefaultAgentCanStillLoadGenerateImage(t *testing.T) {
	t.Parallel()

	defaultTools := ExpandToolFilter([]string{"tag:default"}, nil)
	require.NotContains(t, defaultTools, ToolGenerateImage,
		"precondition: generate_image is deliberately not in tag:default")

	// The default agent declares no loadable_tools, which means unrestricted.
	ctx, _ := scopeWithAccess(t, "default-agent-"+t.Name(), []string{"tag:default"}, nil, false)

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.False(t, resp.IsError,
		"the default agent must be able to opt into generate_image via load_tool: %s", resp.Content)
}

// TestUndeclaredLoadableMeansEverything states the default directly. Every
// workflow written before loadable_tools existed relies on it.
func TestUndeclaredLoadableMeansEverything(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, "undeclared-"+t.Name(), []string{ToolView}, nil, false)

	tool := &loadToolTool{}
	for _, name := range []string{ToolWrite, ToolEdit, ToolGenerateImage} {
		resp, err := tool.Execute(ctx, LoadToolParams{Name: name})
		require.NoError(t, err)
		assert.False(t, resp.IsError,
			"an undeclared loadable set must not restrict %q: %s", name, resp.Content)
	}
}

// TestWildcardLoadableMeansEverything — what the coding workflows write
// explicitly, so the intent is visible rather than resting on a default.
func TestWildcardLoadableMeansEverything(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, "wildcard-"+t.Name(),
		[]string{ToolView}, []string{LoadableWildcard}, true)

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "\"*\" must allow any registry tool: %s", resp.Content)
}

// TestDeclaredLoadableRestricts is the capability this split buys: a workflow
// that genuinely wants a closed set can say so, and it holds.
func TestDeclaredLoadableRestricts(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, "restricted-"+t.Name(),
		[]string{ToolView}, []string{ToolEdit}, true)

	tool := &loadToolTool{}

	allowed, err := tool.Execute(ctx, LoadToolParams{Name: ToolEdit})
	require.NoError(t, err)
	assert.False(t, allowed.IsError, "a named loadable tool must load: %s", allowed.Content)

	refused, err := tool.Execute(ctx, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.True(t, refused.IsError,
		"a tool outside a DECLARED loadable set must be refused")
}

// TestEmptyLoadableIsNotUnset pins the distinction the API depends on: absent
// means unrestricted, present-but-empty means nothing. Both arrive as len 0, so
// only the declared flag separates them — and collapsing them would either
// break every existing workflow or make "exactly what I preloaded"
// inexpressible.
func TestEmptyLoadableIsNotUnset(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, "empty-"+t.Name(), []string{ToolView}, []string{}, true)

	tool := &loadToolTool{}
	resp, err := tool.Execute(ctx, LoadToolParams{Name: ToolWrite})
	require.NoError(t, err)
	assert.True(t, resp.IsError,
		"an empty-but-declared loadable set must allow nothing beyond the preloaded bundle")
}

// TestPreloadedToolIsAlwaysLoadable — refusing to "load" a tool the agent is
// already holding would be incoherent, and a workflow that lists a tool in both
// places should not be punished for it.
func TestPreloadedToolIsAlwaysLoadable(t *testing.T) {
	t.Parallel()

	ctx, _ := scopeWithAccess(t, "preloaded-"+t.Name(),
		[]string{ToolWrite}, []string{ToolEdit}, true)

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

	ctx, scopeKey := scopeWithAccess(t, "discovery-"+t.Name(),
		[]string{ToolView}, []string{ToolEdit, ToolLoadTool}, true)

	advertised := DeferredToolNames(scopeKey, PermissionMutating, []string{ToolView, ToolLoadTool}, nil)
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

	_, scopeKey := scopeWithAccess(t, "wide-discovery-"+t.Name(), []string{ToolView}, nil, false)

	advertised := DeferredToolNames(scopeKey, PermissionMutating, []string{ToolView}, nil)
	assert.Contains(t, advertised, ToolGenerateImage,
		"an unrestricted scope must advertise tools outside the preloaded bundle")
}
