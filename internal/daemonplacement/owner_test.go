// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ownerFixture is a user, their machines, and which of them are connected and
// hold which directories.
type ownerFixture struct {
	mu sync.Mutex

	projectOwner string
	installs     []*core.ProjectDaemon
	daemons      map[string]*db.Daemon
	online       map[string]bool
	dirs         map[string]map[string]bool // daemon -> path -> present
	failing      map[string]bool            // daemon -> its stat errors
	hanging      map[string]bool            // daemon -> its stat never answers

	adopted map[string]string // worktree -> daemon
	stats   map[string]int    // daemon -> stats asked
	// staleReads makes GetWorktree miss the recorded owner, as a read that
	// raced another resolver's commit does.
	staleReads bool
}

// GetWorktree reads the row as the store would: with the owner recorded so far.
func (f *ownerFixture) GetWorktree(_ context.Context, id string) (*db.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wt := unattributed()
	wt.ID = id
	if owner, ok := f.adopted[id]; ok && !f.staleReads {
		wt.DaemonID = &owner
	}
	return wt, nil
}

func newOwnerFixture() *ownerFixture {
	return &ownerFixture{
		projectOwner: "u1",
		daemons:      map[string]*db.Daemon{},
		online:       map[string]bool{},
		dirs:         map[string]map[string]bool{},
		failing:      map[string]bool{},
		hanging:      map[string]bool{},
		adopted:      map[string]string{},
		stats:        map[string]int{},
	}
}

// machine registers one of u1's machines with the project installed on it.
func (f *ownerFixture) machine(id string, online bool, paths ...string) {
	f.daemons[id] = &db.Daemon{ID: id, UserID: "u1"}
	f.online[id] = online
	f.installs = append(f.installs, &core.ProjectDaemon{ProjectID: "p1", DaemonID: id, InstallState: core.ProjectInstallInstalled})
	f.dirs[id] = map[string]bool{}
	for _, p := range paths {
		f.dirs[id][p] = true
	}
}

func (f *ownerFixture) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := f.daemons[id]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

func (f *ownerFixture) ListProjectDaemonsForProject(context.Context, string) ([]*core.ProjectDaemon, error) {
	return f.installs, nil
}

func (f *ownerFixture) GetProject(_ context.Context, id string) (*db.Project, error) {
	return &db.Project{ID: id, UserID: f.projectOwner}, nil
}

func (f *ownerFixture) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	var out []*db.Daemon
	for _, d := range f.daemons {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

// AdoptWorktreeDaemon is a conditional write, as the store's is: the first
// owner recorded for a row wins, and every call returns the recorded one.
func (f *ownerFixture) AdoptWorktreeDaemon(_ context.Context, id, daemonID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if recorded, ok := f.adopted[id]; ok {
		return recorded, nil
	}
	f.adopted[id] = daemonID
	return daemonID, nil
}

func (f *ownerFixture) IsDaemonOnline(_ context.Context, _ string, selector *toolexec.DaemonSelector) (bool, error) {
	return f.online[selector.ID], nil
}

func (f *ownerFixture) SendDaemonCommandToDaemon(ctx context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType != "fs.stat" {
		return nil, errors.New("unexpected command " + commandType)
	}
	f.mu.Lock()
	f.stats[daemonID]++
	f.mu.Unlock()
	if f.hanging[daemonID] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.failing[daemonID] {
		return nil, errors.New("stat failed")
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(payload, &req)
	present := f.dirs[daemonID][req.Path]
	return json.Marshal(map[string]bool{"exists": present, "is_dir": present})
}

func (f *ownerFixture) statsAsked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.stats {
		n += c
	}
	return n
}

const wtPath = "/home/workspace/.reliant/worktrees/wt-1"

func unattributed() *db.Worktree {
	return &db.Worktree{ID: "wt-1", ProjectID: "p1", Path: wtPath}
}

func TestResolver_RecordsTheOneConnectedMachineThatHasIt(t *testing.T) {
	f := newOwnerFixture()
	f.machine("laptop", true)
	f.machine("gpu-box", true, wtPath)

	owner := NewResolver(f, f).Owner(context.Background(), "u1", unattributed())

	assert.Equal(t, "gpu-box", owner)
	assert.Equal(t, map[string]string{"wt-1": "gpu-box"}, f.adopted, "the owner is recorded on the row")
}

// Another resolver (a second service or API replica, whose probes saw other
// machines) recorded the row first: route only to the recorded owner, never
// to this resolver's own nominee, or file and git mutations land on a machine
// other than the one the row names.
func TestResolver_RoutesToTheOwnerAnotherResolverRecordedFirst(t *testing.T) {
	f := newOwnerFixture()
	f.machine("gpu-box", true, wtPath)
	f.adopted["wt-1"] = "laptop" // the other resolver's write, committed first
	f.staleReads = true          // ...after this resolver re-read the row

	owner := NewResolver(f, f).Owner(context.Background(), "u1", unattributed())

	assert.Equal(t, "laptop", owner, "the persisted owner wins over this resolver's probe")
	assert.Equal(t, map[string]string{"wt-1": "laptop"}, f.adopted, "the recorded owner is not overwritten")
}

// A request validates a workspace (settling its owner) and then routes with
// the same row it read before. The second use must not probe again: the
// owner may have dropped off, and an unknown answer would send the request
// to the default machine, a different disk.
func TestResolver_AStaleCopyRoutesToTheOwnerAlreadyRecorded(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	f.machine("b", true)
	r := NewResolver(f, f)

	row := unattributed() // the request's copy, never updated
	require.Equal(t, "a", r.Owner(context.Background(), "u1", row), "validation settles the owner")
	asked := f.statsAsked()

	f.failing["a"] = true // the owner stops answering between the two calls
	assert.Equal(t, "a", r.Owner(context.Background(), "u1", row), "the recorded owner, not a fresh probe")
	assert.Equal(t, asked, f.statsAsked(), "no machine is asked again")

	f.online["a"] = false // or disconnects altogether
	assert.Equal(t, "a", NewResolver(f, f).Owner(context.Background(), "u1", row), "another resolver (a replica) reads it too")
}

// An asleep machine is not woken or waited for: it is skipped.
func TestResolver_SkipsAsleepMachines(t *testing.T) {
	f := newOwnerFixture()
	f.machine("cloud", false, wtPath)
	f.machine("laptop", true)

	owner := NewResolver(f, f).Owner(context.Background(), "u1", unattributed())

	assert.Empty(t, owner, "the only connected machine does not have it")
	assert.Empty(t, f.adopted, "the row stays unassigned")
	assert.Zero(t, f.stats["cloud"], "an asleep machine is never asked")
}

func TestResolver_SettlesWhileAnotherMachineIsAsleep(t *testing.T) {
	f := newOwnerFixture()
	f.machine("cloud", false)
	f.machine("laptop", true, wtPath)

	owner := NewResolver(f, f).Owner(context.Background(), "u1", unattributed())

	assert.Equal(t, "laptop", owner)
	assert.Equal(t, "laptop", f.adopted["wt-1"])
}

func TestResolver_LeavesAmbiguousRowsUnassigned(t *testing.T) {
	cases := map[string]func(f *ownerFixture){
		"two machines have the path": func(f *ownerFixture) {
			f.machine("a", true, wtPath)
			f.machine("b", true, wtPath)
		},
		"a connected machine could not answer": func(f *ownerFixture) {
			f.machine("a", true, wtPath)
			f.machine("b", true)
			f.failing["b"] = true
		},
		"no connected machine has it": func(f *ownerFixture) {
			f.machine("a", true)
		},
		"no machine is connected": func(f *ownerFixture) {
			f.machine("a", false, wtPath)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture()
			setup(f)
			assert.Empty(t, NewResolver(f, f).Owner(context.Background(), "u1", unattributed()))
			assert.Empty(t, f.adopted)
		})
	}
}

// A machine that does not answer is bounded by the probe timeout, not
// waited on.
func TestResolver_BoundsAMachineThatDoesNotAnswer(t *testing.T) {
	prev := ownerProbeTimeout
	ownerProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ownerProbeTimeout = prev })

	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	f.machine("b", true)
	f.hanging["b"] = true

	start := time.Now()
	owner := NewResolver(f, f).Owner(context.Background(), "u1", unattributed())

	assert.Empty(t, owner, "the silent machine might be the owner")
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestResolver_AnswersFromTheRecordWithoutAsking(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	owned := unattributed()
	recorded := "b"
	owned.DaemonID = &recorded

	assert.Equal(t, "b", NewResolver(f, f).Owner(context.Background(), "u1", owned))
	main := &db.Worktree{ID: "main", ProjectID: "p1", Path: wtPath, IsMain: true}
	assert.Empty(t, NewResolver(f, f).Owner(context.Background(), "u1", main), "the main checkout is on every machine")
	archivedAt := time.Now()
	archived := unattributed()
	archived.DeletedAt = &archivedAt
	assert.Empty(t, NewResolver(f, f).Owner(context.Background(), "u1", archived))
	assert.Zero(t, f.statsAsked())

	var nilResolver *Resolver
	assert.Equal(t, "b", nilResolver.Owner(context.Background(), "u1", owned), "a nil resolver reads the record")
}

// Someone else's workspace is never settled by the caller's machines.
func TestResolver_IgnoresAnotherUsersWorkspace(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	f.projectOwner = "someone-else"

	assert.Empty(t, NewResolver(f, f).Owner(context.Background(), "u1", unattributed()))
	assert.Zero(t, f.statsAsked())
	assert.Empty(t, f.adopted)
}

// A project with no recorded install is asked about on every machine of the
// user's.
func TestResolver_FallsBackToEveryMachineWithoutInstalls(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	f.installs = nil

	assert.Equal(t, "a", NewResolver(f, f).Owner(context.Background(), "u1", unattributed()))
}

// A workspace survives on A after A's install row was removed, while B still
// has one. Install rows prove nothing about a branch checkout, so A is asked
// too, and the workspace is A's, not left to default routing (B).
func TestResolver_AsksAMachineWhoseInstallWasRemoved(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	f.machine("b", true)
	f.installs = f.installs[1:] // only B's install remains

	assert.Equal(t, "a", NewResolver(f, f).Owner(context.Background(), "u1", unattributed()))
	assert.Equal(t, map[string]string{"wt-1": "a"}, f.adopted)
}

// A row the machines could not settle is not asked about again on every
// request, only after the retry window.
func TestResolver_RetriesAnUnsettledRowOnlyAfterTheWindow(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true)
	r := NewResolver(f, f)
	now := time.Now()
	r.now = func() time.Time { return now }

	assert.Empty(t, r.Owner(context.Background(), "u1", unattributed()))
	assert.Empty(t, r.Owner(context.Background(), "u1", unattributed()))
	assert.Equal(t, 1, f.statsAsked(), "the second use is answered from the retry window")

	f.dirs["a"][wtPath] = true
	now = now.Add(ownerRetryAfter + time.Second)
	assert.Equal(t, "a", r.Owner(context.Background(), "u1", unattributed()))
	assert.Equal(t, 2, f.statsAsked())
}

// Concurrent uses of one row probe once.
func TestResolver_ConcurrentUsesProbeOnce(t *testing.T) {
	prev := ownerProbeTimeout
	ownerProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ownerProbeTimeout = prev })

	f := newOwnerFixture()
	f.machine("a", true, wtPath)
	f.machine("b", true)
	f.hanging["b"] = true // holds the first resolution open while the others arrive
	r := NewResolver(f, f)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Owner(context.Background(), "u1", unattributed())
		}()
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, 1, f.stats["a"], "one resolution, shared")
}
