// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// fakeRegistry is the daemon rows, project installs, worktrees and chat pins
// the placement rules read, in memory.
type fakeRegistry struct {
	mu        sync.Mutex
	daemons   map[string]*db.Daemon
	installs  []*core.ProjectDaemon
	worktrees map[string]*db.Worktree
	pins      map[string]string
	noMachine map[string]bool
	pinWrites int
	pinErr    error
}

func newRegistry() *fakeRegistry {
	return &fakeRegistry{
		daemons:   map[string]*db.Daemon{},
		worktrees: map[string]*db.Worktree{},
		pins:      map[string]string{},
		noMachine: map[string]bool{},
	}
}

func (r *fakeRegistry) addDaemon(id, owner string) { r.daemons[id] = &db.Daemon{ID: id, UserID: owner} }

func (r *fakeRegistry) install(project, daemon string, state core.ProjectInstallState) {
	r.installs = append(r.installs, &core.ProjectDaemon{ProjectID: project, DaemonID: daemon, InstallState: state})
}

func (r *fakeRegistry) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := r.daemons[id]; ok {
		return d, nil
	}
	return nil, svcerr.NotFound("daemon")
}

func (r *fakeRegistry) ListProjectDaemonsForProject(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	var out []*core.ProjectDaemon
	for _, row := range r.installs {
		if row.ProjectID == projectID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *fakeRegistry) GetWorktree(_ context.Context, id string) (*db.Worktree, error) {
	if w, ok := r.worktrees[id]; ok {
		return w, nil
	}
	return nil, core.ErrWorktreeNotFound
}

func (r *fakeRegistry) PinChatDaemonIfUnset(_ context.Context, chatID, daemonID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pinErr != nil {
		return "", r.pinErr
	}
	if r.noMachine[chatID] {
		return "", nil
	}
	if existing := r.pins[chatID]; existing != "" {
		return existing, nil
	}
	r.pins[chatID] = daemonID
	r.pinWrites++
	return daemonID, nil
}

// fakeMachines answers PlacementDaemonID the way the router does: default
// resolution prefers the first id in order (the connected local machine), an
// id selector resolves that machine if the user owns it.
type fakeMachines struct {
	registry *fakeRegistry
	owner    string
	// order is default resolution's preference, most preferred first.
	order []string
	// unroutable ids answer ErrNoDaemon when named.
	unroutable map[string]bool
	calls      []string
}

func (m *fakeMachines) PlacementDaemonID(_ context.Context, userID, daemonID string) (string, error) {
	m.calls = append(m.calls, daemonID)
	if daemonID == "" {
		for _, id := range m.order {
			if d := m.registry.daemons[id]; d != nil && d.UserID == userID {
				return id, nil
			}
		}
		return "", toolexec.ErrNoDaemon
	}
	if d := m.registry.daemons[daemonID]; d == nil || d.UserID != userID || m.unroutable[daemonID] {
		return "", toolexec.ErrNoDaemon
	}
	return daemonID, nil
}

func ptr(s string) *string { return &s }

const (
	owner   = "user-1"
	project = "proj-1"
)

// The reported shape: a cloud VM with the project installed, then a laptop
// that is local and connected (default resolution prefers it) but never had
// the project cloned onto it.
func vmAndLaptop() (*fakeRegistry, *fakeMachines) {
	reg := newRegistry()
	reg.addDaemon("vm", owner)
	reg.addDaemon("laptop", owner)
	reg.install(project, "vm", core.ProjectInstallInstalled)
	return reg, &fakeMachines{registry: reg, owner: owner, order: []string{"laptop", "vm"}}
}

func TestProjectDefaultDaemonID(t *testing.T) {
	ctx := context.Background()

	t.Run("the project filter excludes a connected laptop without the project", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "vm", id, "plain default resolution would have said laptop")
	})

	t.Run("the router's default wins when the project is installed on it too", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.install(project, "laptop", core.ProjectInstallInstalled)
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "laptop", id)
	})

	t.Run("no installs recorded falls to plain default resolution", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "laptop", id)
	})

	t.Run("a clone still in flight or failed is not an install", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		reg.install(project, "vm", core.ProjectInstallInstalling)
		reg.install(project, "laptop", core.ProjectInstallFailed)
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "laptop", id, "nothing installed, so nothing to narrow to")
	})

	t.Run("an install on another user's machine is not the user's", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.addDaemon("stranger", "someone-else")
		reg.install(project, "stranger", core.ProjectInstallInstalled)
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "vm", id)
	})

	t.Run("a dangling install row is skipped", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.install(project, "deleted-daemon", core.ProjectInstallInstalled)
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "vm", id)
	})

	t.Run("an installed machine the router cannot place is still named", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		machines.unroutable = map[string]bool{"vm": true}
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "vm", id, "the record is known; the run's preflight waits for it")
	})

	t.Run("the first routable install is taken when the default is not one", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.addDaemon("vm2", owner)
		reg.installs = nil
		reg.install(project, "vm2", core.ProjectInstallInstalled)
		reg.install(project, "vm", core.ProjectInstallInstalled)
		machines.unroutable = map[string]bool{"vm2": true}
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, "vm", id)
	})

	t.Run("no machine at all is the router's no-daemon error", func(t *testing.T) {
		reg := newRegistry()
		machines := &fakeMachines{registry: reg, owner: owner}
		_, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		assert.True(t, errors.Is(err, toolexec.ErrNoDaemon))
	})
}

func TestPinTarget(t *testing.T) {
	ctx := context.Background()
	chat := func() *db.Chat { return &db.Chat{ID: "c1", UserID: owner, ProjectID: project} }

	t.Run("the chat's own pin wins over every default", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		c := chat()
		c.ActiveDaemonID = ptr("laptop")
		id, err := PinTarget(ctx, reg, machines, c)
		require.NoError(t, err)
		assert.Equal(t, "laptop", id)
		assert.Empty(t, machines.calls, "an existing pin asks the router nothing")
	})

	t.Run("the worktree's owner wins over the project default", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.worktrees["wt"] = &db.Worktree{ID: "wt", DaemonID: ptr("laptop")}
		c := chat()
		c.WorktreeID = ptr("wt")
		id, err := PinTarget(ctx, reg, machines, c)
		require.NoError(t, err)
		assert.Equal(t, "laptop", id)
	})

	t.Run("a main-checkout chat gets the project default", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.worktrees["main"] = &db.Worktree{ID: "main", IsMain: true}
		c := chat()
		c.WorktreeID = ptr("main")
		id, err := PinTarget(ctx, reg, machines, c)
		require.NoError(t, err)
		assert.Equal(t, "vm", id)
	})

	t.Run("a chat with no machine by design is never placed", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		c := chat()
		c.NoMachine = true
		id, err := PinTarget(ctx, reg, machines, c)
		require.NoError(t, err)
		assert.Empty(t, id)
		assert.Empty(t, machines.calls)
	})

	t.Run("a user with no machine leaves the chat unpinned", func(t *testing.T) {
		reg := newRegistry()
		machines := &fakeMachines{registry: reg, owner: owner}
		id, err := PinTarget(ctx, reg, machines, chat())
		require.NoError(t, err)
		assert.Empty(t, id)
	})

	t.Run("no router leaves only the pin and the worktree owner to place a chat", func(t *testing.T) {
		reg, _ := vmAndLaptop()
		id, err := PinTarget(ctx, reg, nil, chat())
		require.NoError(t, err)
		assert.Empty(t, id)
	})

	t.Run("any other router failure is returned, not guessed around", func(t *testing.T) {
		reg := newRegistry() // no installs: the router's default is the only evidence
		boom := errors.New("registry down")
		id, err := PinTarget(ctx, reg, failingMachines{boom}, chat())
		assert.ErrorIs(t, err, boom)
		assert.Empty(t, id)
	})

	t.Run("a failing router still yields a machine the project is installed on", func(t *testing.T) {
		reg, _ := vmAndLaptop()
		id, err := PinTarget(ctx, reg, failingMachines{errors.New("registry down")}, chat())
		require.NoError(t, err)
		assert.Equal(t, "vm", id, "the install is evidence enough; the record is the user's own")
	})
}

type failingMachines struct{ err error }

func (f failingMachines) PlacementDaemonID(context.Context, string, string) (string, error) {
	return "", f.err
}

func TestPinner(t *testing.T) {
	ctx := context.Background()

	t.Run("pins an unpinned chat and carries the pin onto the chat it was given", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project}
		NewPinner(reg, machines).Pin(ctx, chat)
		require.NotNil(t, chat.ActiveDaemonID)
		assert.Equal(t, "vm", *chat.ActiveDaemonID)
		assert.Equal(t, "vm", reg.pins["c1"])
	})

	t.Run("an existing pin is never overwritten", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project, ActiveDaemonID: ptr("laptop")}
		NewPinner(reg, machines).Pin(ctx, chat)
		assert.Equal(t, "laptop", *chat.ActiveDaemonID)
		assert.Zero(t, reg.pinWrites)
	})

	t.Run("a no-machine chat is left alone", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project, NoMachine: true}
		NewPinner(reg, machines).Pin(ctx, chat)
		assert.Nil(t, chat.ActiveDaemonID)
		assert.Zero(t, reg.pinWrites)
	})

	t.Run("a failed write leaves the chat unpinned and the run going", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.pinErr = errors.New("serialization failure")
		chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project}
		NewPinner(reg, machines).Pin(ctx, chat)
		assert.Nil(t, chat.ActiveDaemonID, "best effort: proceed unpinned")
	})

	t.Run("a failed placement leaves the chat unpinned", func(t *testing.T) {
		reg := newRegistry()
		chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project}
		NewPinner(reg, failingMachines{errors.New("down")}).Pin(ctx, chat)
		assert.Nil(t, chat.ActiveDaemonID)
		assert.Zero(t, reg.pinWrites)
	})

	t.Run("a chat that lost the race adopts the winner's machine", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.pins["c1"] = "laptop" // a concurrent send pinned it between our read and our write
		chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project}
		NewPinner(reg, machines).Pin(ctx, chat)
		assert.Equal(t, "laptop", *chat.ActiveDaemonID)
		assert.Zero(t, reg.pinWrites)
	})

	t.Run("Target writes nothing", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		got := NewPinner(reg, machines).Target(ctx, &db.Chat{ID: "c1", UserID: owner, ProjectID: project})
		assert.Equal(t, "vm", got)
		assert.Zero(t, reg.pinWrites)
	})
}

// The reported incident, end to end through the rule: an unpinned chat runs on
// the VM; the laptop then connects as the router's preferred default; the
// chat's next run is still on the VM, because it was pinned.
func TestAChatStaysOnItsMachineWhenAnotherConnects(t *testing.T) {
	ctx := context.Background()
	reg := newRegistry()
	reg.addDaemon("vm", owner)
	reg.install(project, "vm", core.ProjectInstallInstalled)
	machines := &fakeMachines{registry: reg, owner: owner, order: []string{"vm"}}
	pinner := NewPinner(reg, machines)

	chat := &db.Chat{ID: "c1", UserID: owner, ProjectID: project}
	pinner.Pin(ctx, chat)
	require.Equal(t, "vm", *chat.ActiveDaemonID, "first run")

	reg.addDaemon("laptop", owner)
	machines.order = []string{"laptop", "vm"}
	def, err := machines.PlacementDaemonID(ctx, owner, "")
	require.NoError(t, err)
	require.Equal(t, "laptop", def, "precondition: default resolution now prefers the laptop")

	// The next run starts from the stored row.
	stored := &db.Chat{ID: "c1", UserID: owner, ProjectID: project}
	if pin := reg.pins["c1"]; pin != "" {
		stored.ActiveDaemonID = &pin
	}
	pinner.Pin(ctx, stored)
	require.NotNil(t, stored.ActiveDaemonID)
	assert.Equal(t, "vm", *stored.ActiveDaemonID, "second run, after the laptop appeared")
	assert.Equal(t, 1, reg.pinWrites, "pinned once")

	// A chat that first runs after the laptop appeared also avoids it: the
	// laptop does not have the project.
	late := &db.Chat{ID: "c2", UserID: owner, ProjectID: project}
	pinner.Pin(ctx, late)
	require.NotNil(t, late.ActiveDaemonID)
	assert.Equal(t, "vm", *late.ActiveDaemonID, fmt.Sprintf("a new chat, laptop %s the project", "without"))
}
