package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// listingRuntime is a runtime whose listing the test scripts: per scope, a
// listing or an error, standing in for a daemon that is up or suspended.
type listingRuntime struct {
	identity string // "" = does not implement MCPListIdentity semantics

	mu       sync.Mutex
	listing  map[string]map[string][]mcp.Tool // scope -> server -> tools
	err      error
	callErr  error
	listings int
}

func (r *listingRuntime) MCPListIdentity() string { return r.identity }

func (r *listingRuntime) set(scope string, l map[string][]mcp.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listing == nil {
		r.listing = map[string]map[string][]mcp.Tool{}
	}
	r.listing[scope] = l
	r.err = nil
}

func (r *listingRuntime) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *listingRuntime) EnsureProjectServersLoaded(context.Context, string) *mcp.ProjectServerLoadResult {
	return &mcp.ProjectServerLoadResult{}
}

func (r *listingRuntime) ListProjectTools(scope string) (map[string][]mcp.Tool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listings++
	if r.err != nil {
		return nil, r.err
	}
	return r.listing[scope], nil
}

func (r *listingRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return r.ListProjectTools("")
}

func (r *listingRuntime) ProjectCallTool(_, _, _, _ string, _ map[string]interface{}) (*mcp.ToolResult, error) {
	return nil, r.callErr
}

func (r *listingRuntime) CallTool(_, _, _ string, _ map[string]interface{}) (*mcp.ToolResult, error) {
	return nil, r.callErr
}

func mcpTool(name string) mcp.Tool {
	return mcp.Tool{
		Name:        name,
		Description: "does " + name,
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"arg": map[string]interface{}{"type": "string"}},
		},
	}
}

func listing(servers map[string][]string) map[string][]mcp.Tool {
	out := map[string][]mcp.Tool{}
	for server, names := range servers {
		for _, n := range names {
			out[server] = append(out[server], mcpTool(n))
		}
	}
	return out
}

// fingerprint is everything a provider sees of the tools, in order — the same
// inputs the call_llm prompt-shape hash covers.
func fingerprint(t *testing.T, ts []Tool) string {
	t.Helper()
	var b strings.Builder
	for _, tool := range ts {
		raw, err := json.Marshal(tool.ParamSchema())
		require.NoError(t, err)
		fmt.Fprintf(&b, "%s\x00%s\x00%s\x01", tool.Name(), tool.Description(), raw)
	}
	return b.String()
}

func toolNamesOf(ts []Tool) []string {
	out := make([]string, 0, len(ts))
	for _, tool := range ts {
		out = append(out, tool.Name())
	}
	return out
}

var errSuspended = errors.New("daemon is suspended; not waking it")

func TestMCPToolProvider_ListingErrorServesLastKnownGood(t *testing.T) {
	cache := NewMCPToolCache()
	provider := NewProjectMCPToolProvider("/work/a").WithCache(cache)
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue", "list_prs"}, "chrome": {"new_page"}}))

	live := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsLive, live.Source)
	require.Equal(t,
		[]string{"mcp__chrome__new_page", "mcp__github__create_issue", "mcp__github__list_prs"}, toolNamesOf(live.Tools))

	rt.fail(errSuspended)
	stale := provider.GetToolSet(rt)

	require.Equal(t, MCPToolsStale, stale.Source)
	require.Equal(t, toolNamesOf(live.Tools), toolNamesOf(stale.Tools), "same names in the same order")
	require.Equal(t, fingerprint(t, live.Tools), fingerprint(t, stale.Tools), "byte-identical tools array")
}

func TestMCPToolProvider_SuccessfulRefreshRemovesGoneTool(t *testing.T) {
	provider := NewProjectMCPToolProvider("/work/a").WithCache(NewMCPToolCache())
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue", "list_prs"}}))
	require.Len(t, provider.GetToolSet(rt).Tools, 2)

	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	after := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsLive, after.Source)
	require.Equal(t, []string{"mcp__github__create_issue"}, toolNamesOf(after.Tools))

	// The shrunken listing, not the original, is now the last known good.
	rt.fail(errSuspended)
	stale := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsStale, stale.Source)
	require.Equal(t, []string{"mcp__github__create_issue"}, toolNamesOf(stale.Tools))
}

func TestMCPToolProvider_SuccessfulEmptyListingIsNotAnError(t *testing.T) {
	provider := NewProjectMCPToolProvider("/work/a").WithCache(NewMCPToolCache())
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	provider.GetToolSet(rt)

	rt.set("/work/a", map[string][]mcp.Tool{}) // every server genuinely removed
	after := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsLive, after.Source)
	require.Empty(t, after.Tools, "a successful listing that is empty removes everything")
}

func TestMCPToolProvider_FirstEverErrorIsEmptyNotPanic(t *testing.T) {
	provider := NewProjectMCPToolProvider("/work/a").WithCache(NewMCPToolCache())
	rt := &listingRuntime{identity: "user=u1"}
	rt.fail(errSuspended)

	require.NotPanics(t, func() {
		got := provider.GetToolSet(rt)
		require.Equal(t, MCPToolsNone, got.Source)
		require.Empty(t, got.Tools)
		require.NotNil(t, got.Tools)
	})
}

func TestMCPToolProvider_NoCacheKeepsOldBehaviour(t *testing.T) {
	provider := NewProjectMCPToolProvider("/work/a")
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	require.Len(t, provider.GetTools(rt), 1)

	rt.fail(errSuspended)
	require.Empty(t, provider.GetTools(rt))
}

func TestMCPToolProvider_RuntimeWithoutIdentityIsNeverCached(t *testing.T) {
	provider := NewProjectMCPToolProvider("/work/a").WithCache(NewMCPToolCache())
	rt := &listingRuntime{identity: ""}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	require.Len(t, provider.GetTools(rt), 1)

	rt.fail(errSuspended)
	got := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsNone, got.Source, "a list that cannot be attributed to a caller must not be shared")
	require.Empty(t, got.Tools)
}

func TestMCPToolProvider_ScopeAndIdentityIsolateLists(t *testing.T) {
	cache := NewMCPToolCache()
	rtA := &listingRuntime{identity: "user=alice|id=d1"}
	rtB := &listingRuntime{identity: "user=bob|id=d2"}
	rtA2 := &listingRuntime{identity: "user=alice|id=d3"}
	for _, rt := range []*listingRuntime{rtA, rtB, rtA2} {
		rt.set("/work/a", listing(map[string][]string{"srv": {strings.ReplaceAll(rt.identity, "|", "_") + "x"}}))
		rt.set("/work/b", listing(map[string][]string{"other": {"only_b"}}))
	}
	for _, rt := range []*listingRuntime{rtA, rtB, rtA2} {
		NewProjectMCPToolProvider("/work/a").WithCache(cache).GetTools(rt)
	}
	NewProjectMCPToolProvider("/work/b").WithCache(cache).GetTools(rtA)

	want := map[string][]string{}
	for _, rt := range []*listingRuntime{rtA, rtB, rtA2} {
		want[rt.identity] = toolNamesOf(NewProjectMCPToolProvider("/work/a").WithCache(NewMCPToolCache()).GetTools(rt))
	}

	// Every runtime now fails; each must see only its own last list.
	for _, rt := range []*listingRuntime{rtA, rtB, rtA2} {
		rt.fail(errSuspended)
		got := NewProjectMCPToolProvider("/work/a").WithCache(cache).GetToolSet(rt)
		require.Equal(t, MCPToolsStale, got.Source)
		require.Equal(t, want[rt.identity], toolNamesOf(got.Tools), "identity %s", rt.identity)
	}

	// Same user and daemon, other scope: its own list, not /work/a's.
	other := NewProjectMCPToolProvider("/work/b").WithCache(cache).GetToolSet(rtA)
	require.Equal(t, []string{"mcp__other__only_b"}, toolNamesOf(other.Tools))

	// A scope nobody has listed has nothing to fall back on.
	unseen := NewProjectMCPToolProvider("/work/c").WithCache(cache).GetToolSet(rtA)
	require.Equal(t, MCPToolsNone, unseen.Source)
	require.Empty(t, unseen.Tools)
}

func TestMCPToolCache_TTLExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newMCPToolCache(time.Hour, 8, func() time.Time { return now })
	provider := NewProjectMCPToolProvider("/work/a").WithCache(cache)
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	provider.GetTools(rt)
	rt.fail(errSuspended)

	now = now.Add(59 * time.Minute)
	within := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsStale, within.Source)
	require.Equal(t, 59*time.Minute, within.StaleAge)

	now = now.Add(2 * time.Minute)
	expired := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsNone, expired.Source)
	require.Empty(t, expired.Tools)
	require.Equal(t, 0, cache.len(), "an expired entry is dropped")
}

func TestMCPToolCache_SuccessRefreshesTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newMCPToolCache(time.Hour, 8, func() time.Time { return now })
	provider := NewProjectMCPToolProvider("/work/a").WithCache(cache)
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	provider.GetTools(rt)

	now = now.Add(50 * time.Minute)
	provider.GetTools(rt) // success restarts the clock
	now = now.Add(50 * time.Minute)
	rt.fail(errSuspended)
	require.Equal(t, MCPToolsStale, provider.GetToolSet(rt).Source)
}

func TestMCPToolCache_SizeBoundEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newMCPToolCache(time.Hour, 2, time.Now)
	rt := &listingRuntime{identity: "user=u1"}
	for _, scope := range []string{"/a", "/b"} {
		rt.set(scope, listing(map[string][]string{"s": {"t"}}))
		NewProjectMCPToolProvider(scope).WithCache(cache).GetTools(rt)
	}
	rt.set("/a", listing(map[string][]string{"s": {"t"}}))
	NewProjectMCPToolProvider("/a").WithCache(cache).GetTools(rt) // /a is now most recent
	rt.set("/c", listing(map[string][]string{"s": {"t"}}))
	NewProjectMCPToolProvider("/c").WithCache(cache).GetTools(rt)

	require.Equal(t, 2, cache.len())
	rt.fail(errSuspended)
	require.Equal(t, MCPToolsStale, NewProjectMCPToolProvider("/a").WithCache(cache).GetToolSet(rt).Source)
	require.Equal(t, MCPToolsStale, NewProjectMCPToolProvider("/c").WithCache(cache).GetToolSet(rt).Source)
	require.Equal(t, MCPToolsNone, NewProjectMCPToolProvider("/b").WithCache(cache).GetToolSet(rt).Source, "/b was least recently used")
}

func TestMCPToolCache_ConcurrentChats(t *testing.T) {
	cache := NewMCPToolCache()
	const chats = 16
	var wg sync.WaitGroup
	for i := 0; i < chats; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			scope := fmt.Sprintf("/work/%d", i%4)
			rt := &listingRuntime{identity: fmt.Sprintf("user=%d", i%4)}
			own := fmt.Sprintf("tool%d", i%4)
			rt.set(scope, listing(map[string][]string{"srv": {own}}))
			provider := NewProjectMCPToolProvider(scope).WithCache(cache)
			for j := 0; j < 50; j++ {
				if j%2 == 1 {
					rt.fail(errSuspended)
				} else {
					rt.set(scope, listing(map[string][]string{"srv": {own}}))
				}
				got := provider.GetToolSet(rt)
				if j == 0 {
					continue
				}
				if len(got.Tools) != 1 || got.Tools[0].Name() != "mcp__srv__"+own {
					t.Errorf("chat %d iteration %d: got %v (%s)", i, j, toolNamesOf(got.Tools), got.Source)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// A tool kept in the list while its server is down must fail at execution with
// an error the model reads — never succeed, never surface as a Go error that
// aborts the batch.
func TestMCPToolAdapter_DownServerFailsCleanlyAtExecution(t *testing.T) {
	provider := NewProjectMCPToolProvider("/work/a").WithCache(NewMCPToolCache())
	rt := &listingRuntime{identity: "user=u1"}
	rt.set("/work/a", listing(map[string][]string{"github": {"create_issue"}}))
	provider.GetTools(rt)

	rt.fail(errSuspended)
	rt.callErr = errors.New("your machine is still starting: no daemon connected")
	stale := provider.GetToolSet(rt)
	require.Equal(t, MCPToolsStale, stale.Source)
	require.Len(t, stale.Tools, 1)

	resp, err := stale.Tools[0].Run(rctx.NewToolContext(context.Background(), "", "", nil, nil).WithMCP(rt),
		ToolCall{ID: "c1", Name: stale.Tools[0].Name(), Input: `{"arg":"x"}`})
	require.NoError(t, err, "a down server is a tool error the model can read, not an infrastructure failure")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "MCP tool error")
	require.Contains(t, resp.Content, "still starting")
}
