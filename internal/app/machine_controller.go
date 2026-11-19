// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
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

// machinePoolAdmin is the DaemonRegistryService rpcs the app labels machines
// and defines pools with (*daemon_registry.Service): the label and pool tools
// write through them, as the user, so an agent's change follows exactly the
// rules, side effects (pool members' idle timeouts) and refusals the web's
// does. The caller's id is on the ctx, as the auth middleware puts it. The
// writes carry no request credential here, so the registry refuses them
// unless the tool marked the ctx with the card a person approved for exactly
// that write (auth.WithApprovedCard, which asUser keeps).
type machinePoolAdmin interface {
	SetMachineLabels(context.Context, *connect.Request[pb.SetMachineLabelsRequest]) (*connect.Response[pb.SetMachineLabelsResponse], error)
	ListMachinePools(context.Context, *connect.Request[pb.ListMachinePoolsRequest]) (*connect.Response[pb.ListMachinePoolsResponse], error)
	PutMachinePool(context.Context, *connect.Request[pb.PutMachinePoolRequest]) (*connect.Response[pb.PutMachinePoolResponse], error)
	DeleteMachinePool(context.Context, *connect.Request[pb.DeleteMachinePoolRequest]) (*connect.Response[pb.DeleteMachinePoolResponse], error)
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
	pools   machinePoolAdmin
}

// newMachineController is nil when self-hosted: there is no control plane
// to act on a machine with, and the tools say so.
func newMachineController(repo machineRegistry, router *WorkerDaemonRouter, pools machinePoolAdmin) tools.MachineController {
	if router == nil || router.NATSDaemonRouter == nil || router.ControlPlane == nil {
		return nil
	}
	return &machineController{repo: repo, waker: router.NATSDaemonRouter, actions: router.ControlPlane, pools: pools}
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

func (m *machineController) ListMachinePools(ctx context.Context, userID string) ([]tools.MachinePoolSummary, error) {
	if m.pools == nil {
		return nil, errNoPoolAdmin
	}
	resp, err := m.pools.ListMachinePools(asUser(ctx, userID), connect.NewRequest(&pb.ListMachinePoolsRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]tools.MachinePoolSummary, 0, len(resp.Msg.GetPools()))
	for _, p := range resp.Msg.GetPools() {
		out = append(out, poolSummary(p))
	}
	return out, nil
}

func (m *machineController) SetMachineLabels(ctx context.Context, userID, daemonID string, labels map[string]string) error {
	if m.pools == nil {
		return errNoPoolAdmin
	}
	if err := m.owned(ctx, userID, daemonID); err != nil {
		return err
	}
	_, err := m.pools.SetMachineLabels(asUser(ctx, userID), connect.NewRequest(&pb.SetMachineLabelsRequest{DaemonId: daemonID, Labels: labels}))
	return err
}

func (m *machineController) PutMachinePool(ctx context.Context, userID string, spec tools.MachinePoolSpec) (tools.MachinePoolSummary, error) {
	if m.pools == nil {
		return tools.MachinePoolSummary{}, errNoPoolAdmin
	}
	resp, err := m.pools.PutMachinePool(asUser(ctx, userID), connect.NewRequest(&pb.PutMachinePoolRequest{Pool: &pb.MachinePoolSpec{
		Name: spec.Name, Selector: spec.Selector,
		MinMembers: uint32(max(spec.MinMembers, 0)), MaxMembers: uint32(max(spec.MaxMembers, 0)),
		MemberSize: spec.MemberSize, MaxRunsPerMember: uint32(max(spec.MaxRunsPerMember, 0)),
		MemberIdleTimeout: spec.MemberIdleTimeout,
	}}))
	if err != nil {
		return tools.MachinePoolSummary{}, err
	}
	return poolSummary(resp.Msg.GetPool()), nil
}

func (m *machineController) DeleteMachinePool(ctx context.Context, userID, name string) error {
	if m.pools == nil {
		return errNoPoolAdmin
	}
	_, err := m.pools.DeleteMachinePool(asUser(ctx, userID), connect.NewRequest(&pb.DeleteMachinePoolRequest{Name: name}))
	return err
}

// errNoPoolAdmin: the worker was wired without the registry service.
var errNoPoolAdmin = errors.New("machine labels and pools are not available in this runtime")

// asUser is ctx carrying userID as the caller, as the auth middleware sets
// it for the registry rpcs.
func asUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, auth.UserIDContextKey, userID)
}

// poolSummary is a registry pool as the tools show it.
func poolSummary(p *pb.MachinePool) tools.MachinePoolSummary {
	spec := p.GetSpec()
	out := tools.MachinePoolSummary{ID: p.GetId(), Warnings: p.GetWarnings(), Spec: tools.MachinePoolSpec{
		Name: spec.GetName(), Selector: spec.GetSelector(),
		MinMembers: int(spec.GetMinMembers()), MaxMembers: int(spec.GetMaxMembers()),
		MemberSize: spec.GetMemberSize(), MaxRunsPerMember: int(spec.GetMaxRunsPerMember()),
		MemberIdleTimeout: spec.GetMemberIdleTimeout(),
	}}
	for _, mem := range p.GetMembers() {
		out.Members = append(out.Members, tools.MachinePoolMember{ID: mem.GetDaemonId(), Name: mem.GetName(), State: mem.GetState(), LiveRuns: int(mem.GetLiveRuns())})
	}
	return out
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
	if err != nil || !authz.CanWriteMachine(ctx, userID, d) {
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
