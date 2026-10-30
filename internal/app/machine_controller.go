// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// machineRegistry is what the machine tools read: the user's daemon records
// and the trigger activation that launched a run.
type machineRegistry interface {
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	// A pool-bound activation's run is on the member its chat is pinned to.
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	// The owner's machine pools, to show each machine's pools.
	ListMachinePoolsForUser(ctx context.Context, userID string) ([]*core.MachinePool, error)
	ListMachinePoolMembers(ctx context.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error)
}

// machineWaker wakes one machine; *toolexec.NATSDaemonRouter is it.
type machineWaker interface {
	Wake(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (toolexec.WakeResult, error)
}

// machineActions are the control-plane rpcs Settings -> Machines stops and
// resizes with.
type machineActions interface {
	SuspendDaemon(ctx context.Context, jwt, daemonID string) error
	ResizeDaemon(ctx context.Context, jwt, daemonID, size string) error
}

// machineController is tools.MachineController for the worker.
//
// Every action presents the run owner's own session token, so the control
// plane applies exactly the ownership, plan and funding gates the web gets.
// The one exception is an unattended start, which goes through the router's
// Wake with the ctx automationcred-marked: that falls back to the
// daemon:resume token bound to exactly the machine named, which only the
// activation that targets it holds. manage_machine decides who may ask.
type machineController struct {
	repo    machineRegistry
	waker   machineWaker
	actions machineActions
}

// newMachineController is nil when self-hosted: there is no control plane
// to act on a machine with, and the tools say so.
func newMachineController(repo machineRegistry, router *WorkerDaemonRouter) tools.MachineController {
	if router == nil || router.NATSDaemonRouter == nil || router.ControlPlane == nil {
		return nil
	}
	return &machineController{repo: repo, waker: router.NATSDaemonRouter, actions: router.ControlPlane}
}

func (m *machineController) ListMachines(ctx context.Context, userID string) ([]tools.MachineSummary, error) {
	daemons, err := m.repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	pools, live := m.poolMembership(ctx, userID)
	out := make([]tools.MachineSummary, 0, len(daemons))
	for _, d := range daemons {
		s := tools.MachineSummary{ID: d.ID, Name: d.Name, Type: deref(d.DaemonType), State: deref(d.LifecyclePhase), Size: deref(d.Size),
			Labels: d.Labels, Pools: pools[d.ID], LiveRuns: live[d.ID]}
		if s.Name == "" {
			s.Name = deref(d.Hostname)
		}
		out = append(out, s)
	}
	return out, nil
}

// poolMembership is, per machine, the names of the pools it is a member of
// and the live runs each placed on it. Best effort: a failed read shows no
// pools rather than failing the listing.
func (m *machineController) poolMembership(ctx context.Context, userID string) (map[string][]string, map[string]map[string]int) {
	names, live := map[string][]string{}, map[string]map[string]int{}
	pools, err := m.repo.ListMachinePoolsForUser(ctx, userID)
	if err != nil {
		return names, live
	}
	for _, p := range pools {
		members, err := m.repo.ListMachinePoolMembers(ctx, p, "")
		if err != nil {
			continue
		}
		for _, mem := range members {
			names[mem.DaemonID] = append(names[mem.DaemonID], p.Name)
			if live[mem.DaemonID] == nil {
				live[mem.DaemonID] = map[string]int{}
			}
			live[mem.DaemonID][p.Name] = mem.LiveRuns
		}
	}
	return names, live
}

func (m *machineController) StartMachine(ctx context.Context, userID, daemonID string, unattended bool) error {
	if err := m.owned(ctx, userID, daemonID); err != nil {
		return err
	}
	if unattended {
		ctx = automationcred.Allow(ctx)
	}
	_, err := m.waker.Wake(ctx, userID, &toolexec.DaemonSelector{ID: daemonID})
	if err == nil || toolexec.IsDaemonPending(err) {
		return nil
	}
	if errors.Is(err, toolexec.ErrAutomationAccessNotGranted) {
		return errors.New("no live sign-in to act as the user: they can start it in Settings -> Machines, or send a message in this chat to refresh the session")
	}
	return err
}

func (m *machineController) StopMachine(ctx context.Context, userID, daemonID string) error {
	jwt, err := m.ownerSession(ctx, userID, daemonID)
	if err != nil {
		return err
	}
	return m.actions.SuspendDaemon(ctx, jwt, daemonID)
}

func (m *machineController) ResizeMachine(ctx context.Context, userID, daemonID, size string) error {
	jwt, err := m.ownerSession(ctx, userID, daemonID)
	if err != nil {
		return err
	}
	return m.actions.ResizeDaemon(ctx, jwt, daemonID, size)
}

func (m *machineController) ActivationMachine(ctx context.Context, chatID string) (string, error) {
	ev, err := m.repo.GetTriggerEventByChatID(ctx, chatID)
	if errors.Is(err, core.ErrTriggerEventNotFound) || (err == nil && (ev == nil || ev.TriggerID == nil || !ev.Kind.Unattended())) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	trigger, err := m.repo.GetTrigger(ctx, *ev.TriggerID)
	if err != nil {
		return "", err
	}
	if trigger.MachinePoolID != "" {
		// The pool placed this run on one member and pinned its chat there.
		chat, err := m.repo.GetChat(ctx, chatID)
		if err != nil || chat == nil || chat.ActiveDaemonID == nil {
			return "", err
		}
		return *chat.ActiveDaemonID, nil
	}
	return trigger.DaemonID, nil
}

// owned refuses a machine that is not the user's. The control plane checks
// ownership too; this keeps the refusal ahead of any credential choice.
func (m *machineController) owned(ctx context.Context, userID, daemonID string) error {
	d, err := m.repo.GetDaemon(ctx, daemonID)
	if err != nil || d == nil || d.UserID != userID {
		return fmt.Errorf("machine %s is not one of the user's machines", daemonID)
	}
	return nil
}

// ownerSession is the user's live session token, after the ownership check.
func (m *machineController) ownerSession(ctx context.Context, userID, daemonID string) (string, error) {
	if err := m.owned(ctx, userID, daemonID); err != nil {
		return "", err
	}
	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return "", errors.New("no live sign-in to act as the user: they can do this in Settings -> Machines, or send a message in this chat to refresh the session")
	}
	return jwt, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
