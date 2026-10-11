// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/mcp"
)

// mcpReadRepo is the repository the MCP read RPCs run against, in memory,
// counting reads of the whole project config record.
type mcpReadRepo struct {
	db.Repository

	project     *db.Project
	mcpConfigs  string
	recordReads atomic.Int32
	columnReads atomic.Int32
}

func (r *mcpReadRepo) GetProjectWithUserCheck(_ context.Context, id, userID string) (*db.Project, error) {
	if r.project.ID != id || r.project.UserID != userID {
		return nil, fmt.Errorf("project not found")
	}
	return r.project, nil
}

func (r *mcpReadRepo) GetProjectConfigRecord(_ context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	r.recordReads.Add(1)
	if projectID != r.project.ID {
		return nil, sql.ErrNoRows
	}
	cfg := r.mcpConfigs
	return &db.ProjectConfigRecord{ProjectID: projectID, MCPConfigs: &cfg}, nil
}

func (r *mcpReadRepo) GetProjectMCPConfigsJSON(_ context.Context, projectID string) (*string, error) {
	r.columnReads.Add(1)
	if projectID != r.project.ID {
		return nil, sql.ErrNoRows
	}
	cfg := r.mcpConfigs
	return &cfg, nil
}

// countingMCPRouter answers daemon commands from a fake MCP manager, counting
// them; with hang set, every command blocks until its context ends — a
// machine that is connected but not answering.
type countingMCPRouter struct {
	*fakeMCPDaemonRouter

	hang     atomic.Bool
	mu       sync.Mutex
	commands map[string]int
}

func (r *countingMCPRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.mu.Lock()
	if r.commands == nil {
		r.commands = map[string]int{}
	}
	r.commands[commandType]++
	r.mu.Unlock()
	if r.hang.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.fakeMCPDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *countingMCPRouter) count(commandType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commands[commandType]
}

func (r *countingMCPRouter) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.commands {
		n += c
	}
	return n
}

// newMCPReadFixture is a project with n enabled MCP servers, all running and
// healthy on the user's (fake) machine.
func newMCPReadFixture(t *testing.T, n int) (context.Context, *MCPService, *mcpReadRepo, *countingMCPRouter) {
	t.Helper()
	const userID = "mcp-read-user"
	repo := &mcpReadRepo{project: &db.Project{ID: "mcp-read-project", UserID: userID, Path: "/work/mcp-read"}}

	servers := ""
	mgr := newFakeMCPManagerRuntime()
	mgr.projectServers[repo.project.Path] = map[string]bool{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("server-%d", i)
		if servers != "" {
			servers += ","
		}
		servers += fmt.Sprintf(`\"%s\":{\"command\":\"run-%d\",\"enabled\":true}`, name, i)
		mgr.clientByName[name] = &fakeManagerClient{
			connected: true,
			tools:     []mcp.Tool{{Name: "a"}, {Name: "b"}},
			info:      &mcp.ServerInfo{Name: name, Version: "1.0.0", Capabilities: mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}}},
		}
		mgr.healthByName[name] = true
		mgr.projectServers[repo.project.Path][name] = true
	}
	repo.mcpConfigs = fmt.Sprintf(`{"project":"{\"mcpServers\":{%s}}"}`, servers)

	router := &countingMCPRouter{fakeMCPDaemonRouter: newFakeMCPDaemonRouter(mgr)}
	svc := NewMCPService(repo, router)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return ctx, svc, repo, router
}

func listMCPServers(t *testing.T, ctx context.Context, svc *MCPService, projectID string) []*reliantv1.MCPServer {
	t.Helper()
	resp, err := svc.ListServers(ctx, connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: projectID}))
	require.NoError(t, err)
	return resp.Msg.GetServers()
}

// Listing used to make 2+2N daemon round trips in a row — a status call each
// for the clients, their health and every server's last error, plus a
// tools/list per server — and read the whole project config record once per
// scope. It now reads the record once and asks the daemon once, whatever the
// number of servers, and everything the page shows comes from that answer.
func TestMCPListServers_OneDaemonRoundTripAndOneRecordRead(t *testing.T) {
	ctx, svc, repo, router := newMCPReadFixture(t, 5)

	servers := listMCPServers(t, ctx, svc, repo.project.ID)
	require.Len(t, servers, 5)
	for _, s := range servers {
		assert.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, s.GetStatus(), s.GetName())
		assert.True(t, s.GetConnected())
		assert.EqualValues(t, 2, s.GetToolCount(), "the tool count comes with the status")
		require.NotNil(t, s.GetServerInfo())
		assert.Equal(t, s.GetName(), s.GetServerInfo().GetName())
	}

	assert.Equal(t, 1, router.total(), "one daemon round trip for the whole list: %v", router.commands)
	assert.Equal(t, 1, router.count("mcp.server_status"))
	assert.Zero(t, router.count("mcp.list_tools"), "tool counts are in the status answer")
	assert.EqualValues(t, 1, repo.columnReads.Load(), "the mcp_configs column is read once, not once per scope")
	assert.Zero(t, repo.recordReads.Load(), "the full config row (18 MB in prod) is not read on the list path")
}

// The status is the daemon's, the configuration is the database's. A page
// load must not wait on the daemon for what it already has: within
// mcpStatusFreshFor the last answer is served as is.
func TestMCPListServers_ServesTheLastStatusWithoutAskingAgain(t *testing.T) {
	ctx, svc, repo, router := newMCPReadFixture(t, 3)

	listMCPServers(t, ctx, svc, repo.project.ID)
	listMCPServers(t, ctx, svc, repo.project.ID)
	_, err := svc.GetServer(ctx, connect.NewRequest(&reliantv1.GetServerRequest{ProjectId: repo.project.ID, Name: "server-1"}))
	require.NoError(t, err)

	assert.Equal(t, 1, router.count("mcp.server_status"), "a fresh status is not fetched again")
}

// A machine that is connected but not answering held ListServers for 8.7s in
// prod. The list now waits at most mcpStatusWait for a status it has never
// had, answers with the configuration (status unknown) past it, and the next
// load does not wait at all.
func TestMCPListServers_DoesNotWaitOnAHungDaemon(t *testing.T) {
	ctx, svc, repo, router := newMCPReadFixture(t, 4)
	router.hang.Store(true)
	svc.statuses.wait = 200 * time.Millisecond
	svc.statuses.fetchTimeout = 5 * time.Second

	start := time.Now()
	servers := listMCPServers(t, ctx, svc, repo.project.ID)
	first := time.Since(start)
	require.Len(t, servers, 4, "the configured servers are listed regardless")
	for _, s := range servers {
		assert.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNSPECIFIED, s.GetStatus(), "status unknown, not an error")
		assert.False(t, s.GetConnected())
		assert.True(t, s.GetEnabled())
	}
	assert.Less(t, first, time.Second, "the first load waits for the daemon at most mcpStatusWait")

	start = time.Now()
	listMCPServers(t, ctx, svc, repo.project.ID)
	assert.Less(t, time.Since(start), 100*time.Millisecond, "a later load does not wait on the same hung fetch")
	assert.Equal(t, 1, router.count("mcp.server_status"), "one fetch in flight per project, not one per load")
}

// The request's own deadline bounds the wait too.
func TestMCPListServers_RespectsTheRequestDeadline(t *testing.T) {
	ctx, svc, repo, router := newMCPReadFixture(t, 1)
	router.hang.Store(true)
	svc.statuses.fetchTimeout = 5 * time.Second

	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	resp, err := svc.ListServers(ctx, connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: repo.project.ID}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetServers(), 1)
	assert.Less(t, time.Since(start), time.Second)
}

// Starting, stopping or reconfiguring a server changes its status, so the
// read that follows must ask the daemon rather than serve the cached answer.
func TestMCPListServers_RefetchesAfterAServerChange(t *testing.T) {
	ctx, svc, repo, router := newMCPReadFixture(t, 1)
	servers := listMCPServers(t, ctx, svc, repo.project.ID)
	require.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY, servers[0].GetStatus())

	require.NoError(t, svc.mcpManagerForUser("mcp-read-user").RemoveProjectServer(repo.project.Path, "server-0"))

	servers = listMCPServers(t, ctx, svc, repo.project.ID)
	assert.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED, servers[0].GetStatus(),
		"the stopped server is not reported healthy from the cache")
	assert.Equal(t, 2, router.count("mcp.server_status"))
}

// Disabled servers have no runtime status, so a list of only disabled servers
// does not ask the daemon at all.
func TestMCPListServers_AllDisabledDoesNotAskTheDaemon(t *testing.T) {
	ctx, svc, repo, router := newMCPReadFixture(t, 0)
	repo.mcpConfigs = `{"project":"{\"mcpServers\":{\"off\":{\"command\":\"x\",\"enabled\":false}}}"}`

	servers := listMCPServers(t, ctx, svc, repo.project.ID)
	require.Len(t, servers, 1)
	assert.Equal(t, reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED, servers[0].GetStatus())
	assert.Zero(t, router.total())
}

// Past mcpStatusFreshFor a read refreshes: a machine that answers promptly
// gives the page its current status, and one that does not costs it no more
// than refreshWait before the previous answer is served — with one refresh
// in flight however many reads arrive.
func TestMCPStatusCache_RefreshesBoundedByRefreshWait(t *testing.T) {
	var fetches atomic.Int32
	release := make(chan struct{})
	hangSecond := atomic.Bool{}
	cache := newMCPStatusCache(func(ctx context.Context, _, _ string) (*daemonMCPServerStatus, error) {
		n := fetches.Add(1)
		if n == 3 && hangSecond.Load() {
			<-release
		}
		return &daemonMCPServerStatus{Servers: []daemonMCPServerStatusEntry{{Name: fmt.Sprintf("v%d", n)}}}, nil
	})
	cache.refreshWait = 50 * time.Millisecond
	now := time.Unix(1_000_000, 0)
	var nowMu sync.Mutex
	cache.now = func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }
	advance := func(d time.Duration) { nowMu.Lock(); now = now.Add(d); nowMu.Unlock() }
	get := func() string {
		s := cache.get(context.Background(), "u", "/p")
		require.NotNil(t, s)
		return s.Servers[0].Name
	}

	assert.Equal(t, "v1", get())
	assert.Equal(t, "v1", get(), "fresh: not fetched again")

	// A prompt machine: the stale read waits for the refresh and gets it.
	advance(mcpStatusFreshFor + time.Second)
	assert.Equal(t, "v2", get(), "a prompt refresh is served, not the stale answer")

	// A wedged machine: the stale read gives up after refreshWait.
	hangSecond.Store(true)
	advance(mcpStatusFreshFor + time.Second)
	start := time.Now()
	assert.Equal(t, "v2", get(), "the previous answer when the refresh is slow")
	assert.Less(t, time.Since(start), 500*time.Millisecond)
	assert.Equal(t, "v2", get())
	assert.EqualValues(t, 3, fetches.Load(), "one refresh in flight, not one per read")

	close(release)
	require.Eventually(t, func() bool { return get() == "v3" }, time.Second, 5*time.Millisecond,
		"the slow refresh still lands in the cache")
}
