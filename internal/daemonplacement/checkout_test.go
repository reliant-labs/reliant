// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func (r *fakeRegistry) installAt(project, daemon, path string, state core.ProjectInstallState) {
	r.installs = append(r.installs, &core.ProjectDaemon{ProjectID: project, DaemonID: daemon, Path: path, InstallState: state})
}

func TestProjectCheckout(t *testing.T) {
	ctx := context.Background()

	// The prod report: created on the Mac (its path), cloned onto a cloud VM
	// at another path. Whichever machine is chosen, the path is that
	// machine's own.
	macAndVM := func() (*fakeRegistry, *fakeMachines) {
		reg := newRegistry()
		reg.addDaemon("mac", owner)
		reg.addDaemon("vm", owner)
		reg.installAt(project, "mac", "/Users/sean/src/control-plane", core.ProjectInstallInstalled)
		reg.installAt(project, "vm", "/home/workspace/projects/control-plane", core.ProjectInstallInstalled)
		return reg, &fakeMachines{registry: reg, owner: owner, order: []string{"vm"}, unroutable: map[string]bool{"mac": true}}
	}

	t.Run("the chosen machine's own path, never another machine's", func(t *testing.T) {
		reg, machines := macAndVM()
		got, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, Checkout{DaemonID: "vm", Path: "/home/workspace/projects/control-plane"}, got)
	})

	t.Run("the router's default wins when it holds a checkout", func(t *testing.T) {
		reg, machines := macAndVM()
		machines.order = []string{"mac", "vm"}
		machines.unroutable = nil
		got, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, Checkout{DaemonID: "mac", Path: "/Users/sean/src/control-plane"}, got)
	})

	t.Run("it agrees with ProjectDefaultDaemonID on the machine", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		reg.installAt(project, "vm", "/vm/p", core.ProjectInstallInstalled)
		id, err := ProjectDefaultDaemonID(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		got, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, id, got.DaemonID)
		assert.Equal(t, "/vm/p", got.Path)
	})

	t.Run("no install is a NoCheckoutError, never default resolution", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		_, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.ErrorIs(t, err, ErrNoCheckout)
		var missing *NoCheckoutError
		require.True(t, errors.As(err, &missing))
		assert.Empty(t, missing.Cloning)
		assert.Empty(t, missing.Failed)
	})

	t.Run("clones in flight or failed are reported, not used", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		reg.installAt(project, "vm", "/vm/p", core.ProjectInstallInstalling)
		reg.installAt(project, "laptop", "/laptop/p", core.ProjectInstallFailed)
		_, err := ProjectCheckout(ctx, reg, machines, owner, project)
		var missing *NoCheckoutError
		require.True(t, errors.As(err, &missing))
		require.Len(t, missing.Cloning, 1)
		assert.Equal(t, "vm", missing.Cloning[0].DaemonID)
		require.Len(t, missing.Failed, 1)
		assert.Equal(t, "laptop", missing.Failed[0].DaemonID)
	})

	t.Run("another user's machine and a dangling row are not the user's", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		reg.addDaemon("stranger", "someone-else")
		reg.installAt(project, "stranger", "/theirs", core.ProjectInstallInstalled)
		reg.installAt(project, "deleted", "/gone", core.ProjectInstallInstalled)
		_, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.ErrorIs(t, err, ErrNoCheckout)
	})

	t.Run("an install row with no path names no checkout", func(t *testing.T) {
		reg, machines := vmAndLaptop()
		reg.installs = nil
		reg.installAt(project, "vm", "  ", core.ProjectInstallInstalled)
		_, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.ErrorIs(t, err, ErrNoCheckout)
	})

	t.Run("an installed machine the router cannot place is still named", func(t *testing.T) {
		reg, machines := macAndVM()
		machines.order = nil
		machines.unroutable = map[string]bool{"mac": true, "vm": true}
		got, err := ProjectCheckout(ctx, reg, machines, owner, project)
		require.NoError(t, err)
		assert.Equal(t, Checkout{DaemonID: "mac", Path: "/Users/sean/src/control-plane"}, got,
			"the record is known; the dispatch reports the machine's state")
	})
}
