// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// The activation tools bind an activation to a machine pool by name or id
// (tools.ActivationParams.MachinePool / MachinePoolID), the agent's
// spelling of TriggerDefinition.machine_pool_id. A pool's members are by
// construction the owner's own machines (store.ListMachinePoolMembers reads
// only the pool owner's daemons), so re-pointing an activation to the
// owner's pool grants it no machine the owner did not already have, and the
// pool adds no capability item (rowCapabilities). That rests on the members
// really being the owner's: ownAgentPool checks it on every agent change and
// refuses one whose pool holds anyone else's machine, rather than treating
// such a pool as merely widening, because the invariant broke and no card
// can say what the activation would then run on.

// hasEdits reports whether p changes anything beyond naming the activation:
// pause, delete and enable accept trigger_id alone.
func hasEdits(p tools.ActivationParams) bool {
	return p.Name != "" || p.Workflow != "" || p.WorkflowTrigger != "" || p.ProjectID != "" ||
		p.MachineID != nil || p.NoMachine != nil || p.MachinePoolID != nil || p.MachinePool != nil ||
		p.ConnectionID != nil || p.Params != nil || p.Presets != nil || p.Message != nil || p.ActingIdentity != nil
}

// poolNotFound is the refusal for a pool that is not one of the caller's: a
// missing pool and another user's read the same, so neither can be probed.
const poolNotFound = "machine pool not found among your pools (list_machines lists them by name)"

// agentPoolChoice is the pool p names, resolved among user's own pools: its
// id, and set when p names a pool at all. An explicit empty name or id is
// set with no id: it unbinds the pool.
func (s *Service) agentPoolChoice(ctx context.Context, user string, p tools.ActivationParams) (string, bool, error) {
	var (
		pool *core.MachinePool
		err  error
	)
	switch {
	case p.MachinePoolID != nil && p.MachinePool != nil:
		return "", false, refused("give machine_pool (a pool name) or machine_pool_id, not both")
	case p.MachinePoolID != nil:
		id := strings.TrimSpace(*p.MachinePoolID)
		if id == "" {
			return "", true, nil
		}
		pool, err = s.deps.Repo.GetMachinePoolForUser(ctx, user, id)
	case p.MachinePool != nil:
		name := strings.TrimSpace(*p.MachinePool)
		if name == "" {
			return "", true, nil
		}
		pool, err = s.deps.Repo.GetMachinePoolByName(ctx, user, name)
	default:
		return "", false, nil
	}
	switch {
	case errors.Is(err, core.ErrMachinePoolNotFound) || (err == nil && (pool == nil || !authz.CanAccessPersonal(user, pool.UserID))):
		return "", false, tools.RefuseActivation(poolNotFound, svcerr.NotFound("machine pool"))
	case err != nil:
		return "", false, tools.RefuseActivation("the machine pool could not be read; try again", triggerDBError("get machine pool", err))
	}
	return pool.ID, true, nil
}

// agentPool is one of the caller's pools with its members, as an agent's
// change and its approval card describe it.
type agentPool struct {
	ID      string
	Name    string
	Members []core.MachinePoolMemberRow
}

// disclosure is the pool as the card's Machine row shows it.
func (p *agentPool) disclosure() string {
	return fmt.Sprintf("Machine pool: %s (id %s), %d member(s)", p.Name, p.ID, len(p.Members))
}

// detail is the pool's technical line: its id and each member's id, so the
// approval names exactly the machines the runs may land on now.
func (p *agentPool) detail() string {
	ids := make([]string, len(p.Members))
	for i, m := range p.Members {
		ids[i] = discloseScope(m.DaemonID)
	}
	members := "none yet"
	if len(ids) > 0 {
		members = strings.Join(ids, ", ")
	}
	return "Machine pool id: " + discloseScope(p.ID) + "; members: " + members
}

// ownAgentPool reads user's pool id and its members, refusing a pool that is
// not user's or holds a machine that is not user's.
func (s *Service) ownAgentPool(ctx context.Context, user, id string) (*agentPool, error) {
	pool, err := s.deps.Repo.GetMachinePoolForUser(ctx, user, id)
	switch {
	case errors.Is(err, core.ErrMachinePoolNotFound) || (err == nil && (pool == nil || !authz.CanAccessPersonal(user, pool.UserID))):
		return nil, tools.RefuseActivation(poolNotFound, svcerr.NotFound("machine pool"))
	case err != nil:
		return nil, tools.RefuseActivation("the machine pool could not be read; try again", triggerDBError("get machine pool", err))
	}
	members, err := s.deps.Repo.ListMachinePoolMembers(ctx, pool, "")
	if err != nil {
		return nil, tools.RefuseActivation("the machine pool's members could not be read; try again", triggerDBError("list machine pool members", err))
	}
	// A machine is personal (never org-shared): every member must be the
	// caller's own, not merely one they may act on.
	for _, m := range members {
		d, err := s.deps.Repo.GetDaemon(ctx, m.DaemonID)
		if err != nil || d == nil || !authz.CanAccessPersonal(user, d.UserID) {
			msg := fmt.Sprintf("machine pool %s has a member that is not one of your machines, so an agent cannot run an activation on it; the owner can fix the pool in Settings -> Machines", pool.Name)
			return nil, tools.RefuseActivation(msg, svcerr.FailedPrecondition(msg))
		}
	}
	return &agentPool{ID: pool.ID, Name: pool.Name, Members: members}, nil
}

// workflowMachine is the machine whose catalog def's workflow resolves on:
// its daemon, or for a pool the member holding the pool's agreed catalog,
// as triggerFromDefinition resolves it. With no agreed catalog yet (no
// member has synced the project) it is "", the union, which is all there is
// to validate against.
func (s *Service) workflowMachine(ctx context.Context, user string, def *pb.TriggerDefinition) string {
	if def.GetMachinePoolId() == "" {
		return def.GetDaemonId()
	}
	if member, _, err := machinepool.AgreedCatalog(ctx, s.deps.Repo, user, def.GetMachinePoolId(), def.GetProjectId()); err == nil {
		return member
	}
	return ""
}
