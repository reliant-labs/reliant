// Copyright (c) 2025 Reliant Labs
package app

import (
	"log/slog"

	"github.com/reliant-labs/reliant/internal/daemonmirror"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/machinetools"
	"github.com/reliant-labs/reliant/internal/rowreads"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// machineStores is the machine tools' registry over the generated stores:
// each embedded store answers the methods the registry names from it.
type machineStores struct {
	store.DaemonExtStore
	store.TriggerEventExtStore
	store.TriggerExtStore
	store.ChatsWithActivityExtStore
}

// MachineControllerDeps are what NewMachineController reads.
type MachineControllerDeps struct {
	DB     *db.Repo
	Router *WorkerDaemonRouter
	Mirror daemonmirror.Service
	// Logger and Config build the daemon registry service the machine label
	// and pool tools write through (machinePoolAdmin).
	Logger *slog.Logger
	Config *config.Config
}

// NewMachineController is nil when self-hosted: there is no control plane
// to act on a machine with, and the tools say so.
//
//forge:optional-component: a self-hosted worker has no control plane to act on a machine with
//forge:root-only worker
func NewMachineController(d MachineControllerDeps) (tools.MachineController, error) {
	repo := d.DB
	// The registry service is a library here, not a mount: the label and
	// pool tools call its rpcs in-process, as the run's owner. Its
	// IdleTimeouts is the worker's control plane (nil when self-hosted).
	registryDeps := daemon_registry.Deps{Logger: d.Logger, Config: d.Config, Repo: rowreads.WithRepo(repo), Pools: store.NewMachinePoolExtStore(repo.ORM()), Mirror: d.Mirror}
	if d.Router != nil && d.Router.ControlPlane != nil {
		registryDeps.IdleTimeouts = d.Router.ControlPlane
	}
	registry, err := daemon_registry.New(registryDeps)
	if err != nil {
		return nil, err
	}
	return newMachineController(repo, d.Router, registry), nil
}

// newMachineController is nil when self-hosted (an untyped nil interface).
func newMachineController(repo *db.Repo, router *WorkerDaemonRouter, pools *daemon_registry.Service) tools.MachineController {
	if router == nil || router.NATSDaemonRouter == nil || router.ControlPlane == nil {
		return nil
	}
	orm := repo.ORM()
	return machinetools.New(machinetools.Deps{
		Registry: machineStores{
			DaemonExtStore:            store.NewDaemonExtStore(orm),
			TriggerEventExtStore:      store.NewTriggerEventExtStore(orm),
			TriggerExtStore:           store.NewTriggerExtStore(orm),
			ChatsWithActivityExtStore: store.NewChatsWithActivityExtStore(orm),
		},
		PoolRows: store.NewMachinePoolExtStore(orm),
		Waker:    router.NATSDaemonRouter,
		Actions:  router.ControlPlane,
		Pools:    pools,
	})
}
