// Copyright (c) 2025 Reliant Labs
package app

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/machinetools"
	"github.com/reliant-labs/reliant/internal/store"
)

// machineStores is the machine tools' registry over the generated stores:
// each embedded store answers the methods the registry names from it.
type machineStores struct {
	store.DaemonExtStore
	store.TriggerEventExtStore
	store.TriggerExtStore
	store.ChatsWithActivityExtStore
}

// newMachineController is nil when self-hosted: there is no control plane
// to act on a machine with, and the tools say so.
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
