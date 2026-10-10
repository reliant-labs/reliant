package config

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// versionedStore is a StoredConfigStore + StoredConfigVersions over an
// in-memory record per project. It counts full-record reads, which is also
// the parse count: the provider parses every record it reads.
type versionedStore struct {
	mu       sync.Mutex
	records  map[string]*StoredProjectConfigRecord
	versions map[string]string

	recordReads  atomic.Int64
	versionReads atomic.Int64
	// gate, when set, blocks every record read until it is closed.
	gate chan struct{}
}

func newVersionedStore() *versionedStore {
	return &versionedStore{records: map[string]*StoredProjectConfigRecord{}, versions: map[string]string{}}
}

func (s *versionedStore) put(projectID, version string, skills ...string) {
	var parts []string
	for _, name := range skills {
		parts = append(parts, fmt.Sprintf(`{"skill_path":%q,"name":%q,"body":"body of %s"}`, name, name, name))
	}
	blob := "[" + strings.Join(parts, ",") + "]"
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[projectID] = &StoredProjectConfigRecord{ProjectID: projectID, DaemonID: "daemon-1", ProjectSkillsJSON: &blob}
	s.versions[projectID] = version
}

func (s *versionedStore) GetProjectConfigRecord(_ context.Context, projectID string) (*StoredProjectConfigRecord, error) {
	s.recordReads.Add(1)
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[projectID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return r, nil
}

func (s *versionedStore) GetProjectConfigVersion(_ context.Context, projectID string) (string, error) {
	s.versionReads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions[projectID]
	if !ok {
		return "", sql.ErrNoRows
	}
	return v, nil
}

func skillPaths(cfg *Config) []string {
	var out []string
	for _, s := range cfg.Skills {
		out = append(out, s.SkillPath)
	}
	return out
}

// The worker resolves the project config on every LLM call. 50 calls against
// an unchanged record read and parse it once; a rewrite (new version) is seen
// on the very next call — there is no TTL to wait out.
func TestCachedProvider_ParsesOncePerVersion(t *testing.T) {
	store := newVersionedStore()
	store.put("p1", "v1", "alpha")
	provider := NewCachedStoredConfigProvider(store, store, 0)
	ctx := context.Background()

	for range 50 {
		cfg, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
		require.NoError(t, err)
		require.Equal(t, []string{"alpha"}, skillPaths(cfg))
		require.True(t, cfg.SnapshotSynced)
	}
	require.EqualValues(t, 1, store.recordReads.Load(), "an unchanged record is read and parsed once")
	require.EqualValues(t, 50, store.versionReads.Load(), "every call checks the version")

	store.put("p1", "v2", "alpha", "beta")
	cfg, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, skillPaths(cfg), "a rewrite is visible on the next call")
	require.EqualValues(t, 2, store.recordReads.Load())
}

// A fan-out of agents on one project misses together right after a daemon
// push. They share one read and parse.
func TestCachedProvider_ConcurrentMissesShareOneParse(t *testing.T) {
	store := newVersionedStore()
	store.put("p1", "v1", "alpha")
	store.gate = make(chan struct{})
	provider := NewCachedStoredConfigProvider(store, store, 0)

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
			if err == nil && len(cfg.Skills) != 1 {
				err = fmt.Errorf("got %d skills", len(cfg.Skills))
			}
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return store.versionReads.Load() == callers }, 5*time.Second, time.Millisecond)
	close(store.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, store.recordReads.Load())
}

// A caller that gives up does not take the shared fill down with it.
func TestCachedProvider_CanceledCallerDoesNotPoisonTheFill(t *testing.T) {
	store := newVersionedStore()
	store.put("p1", "v1", "alpha")
	store.gate = make(chan struct{})
	provider := NewCachedStoredConfigProvider(store, store, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
		done <- err
	}()
	require.Eventually(t, func() bool { return store.recordReads.Load() == 1 }, 5*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	close(store.gate)
	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, skillPaths(cfg))
	require.EqualValues(t, 1, store.recordReads.Load(), "the abandoned fill still completed and was kept")
}

// No record yet answers the default empty config without reading anything,
// and the record is picked up as soon as it exists.
func TestCachedProvider_NotSyncedThenSynced(t *testing.T) {
	store := newVersionedStore()
	provider := NewCachedStoredConfigProvider(store, store, 0)
	ctx := context.Background()

	cfg, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Empty(t, cfg.Skills)
	require.False(t, cfg.SnapshotSynced)
	require.EqualValues(t, 0, store.recordReads.Load())

	store.put("p1", "v1", "alpha")
	cfg, err = provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, skillPaths(cfg))
}

// The cache is bounded by the bytes of the records it holds, so it cannot
// itself become the worker's next memory problem.
func TestCachedProvider_EvictsLeastRecentlyUsedPastTheByteBudget(t *testing.T) {
	store := newVersionedStore()
	for _, p := range []string{"p1", "p2", "p3"} {
		store.put(p, "v1", "skill-"+p)
	}
	size := storedRecordBytes(store.records["p1"])
	provider := NewCachedStoredConfigProvider(store, store, 2*size+size/2) // room for two
	ctx := context.Background()
	get := func(p string) {
		t.Helper()
		_, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: p})
		require.NoError(t, err)
	}

	get("p1")
	get("p2")
	get("p1") // p1 is now most recent; p2 is the eviction candidate
	get("p3") // evicts p2
	require.EqualValues(t, 3, store.recordReads.Load())
	get("p1")
	get("p3")
	require.EqualValues(t, 3, store.recordReads.Load(), "p1 and p3 were retained")
	get("p2")
	require.EqualValues(t, 4, store.recordReads.Load(), "p2 was evicted")
	require.LessOrEqual(t, provider.cache.bytes, provider.cache.maxBytes)

	// A record bigger than the whole budget is served but never retained.
	small := NewCachedStoredConfigProvider(store, store, size-1)
	before := store.recordReads.Load()
	for range 3 {
		_, err := small.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
		require.NoError(t, err)
	}
	require.EqualValues(t, before+3, store.recordReads.Load())
	require.Zero(t, small.cache.lru.Len())
}

// Callers share one parsed Config. Reshaping the copy a caller got — append,
// sort, overwrite, add a server — must not reach the next caller.
func TestCachedProvider_CallerMutationDoesNotLeakIntoTheCache(t *testing.T) {
	store := newVersionedStore()
	store.put("p1", "v1", "alpha", "beta")
	provider := NewCachedStoredConfigProvider(store, store, 0)
	ctx := context.Background()

	first, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	first.Skills[0] = StoredSkill{SkillPath: "clobbered"}
	first.Skills = append(first.Skills, StoredSkill{SkillPath: "appended"})
	first.MCPServers = map[string]MCPServer{"x": {}}
	first.ProjectMemoryMD = "changed"

	second, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, skillPaths(second))
	require.Empty(t, second.MCPServers)
	require.Empty(t, second.ProjectMemoryMD)
	require.EqualValues(t, 1, store.recordReads.Load())
}

// The uncached provider (daemon, rare readers) is unchanged: every call
// reads, and no record is the default empty config.
func TestStoredConfigProvider_UncachedReadsEveryCall(t *testing.T) {
	store := newVersionedStore()
	provider := NewStoredConfigProvider(store)
	ctx := context.Background()

	cfg, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.False(t, cfg.SnapshotSynced)

	store.put("p1", "v1", "alpha")
	for range 3 {
		_, err := provider.GetProjectConfig(ctx, ProjectRef{ProjectID: "p1"})
		require.NoError(t, err)
	}
	require.EqualValues(t, 4, store.recordReads.Load())
	require.EqualValues(t, 0, store.versionReads.Load())
}

// The MCP servers' own slices and maps are copied too: a caller that edits a
// server's args, env or headers must not change the cached Config another
// caller is holding.
func TestConfigShareableCopy_ClonesNestedMCPFields(t *testing.T) {
	original := &Config{MCPServers: map[string]MCPServer{
		"server": {
			Args:    []string{"one"},
			Env:     []string{"TOKEN=before"},
			Headers: map[string]string{"Authorization": "before"},
		},
	}}

	copied := original.shareableCopy()
	server := copied.MCPServers["server"]
	server.Args[0] = "changed"
	server.Env[0] = "TOKEN=changed"
	server.Headers["Authorization"] = "changed"
	copied.MCPServers["server"] = server

	got := original.MCPServers["server"]
	require.Equal(t, []string{"one"}, got.Args)
	require.Equal(t, []string{"TOKEN=before"}, got.Env)
	require.Equal(t, "before", got.Headers["Authorization"])
}
