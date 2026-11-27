// Copyright (c) 2025 Reliant Labs
package grants

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// Capability is a power a grant gives an activation's runs.
type Capability = core.PipelineCapability

const (
	// Approve: a model review verdict the run writes counts.
	Approve Capability = core.PipelineCapabilityApprove
	// Land: the merge queue may squash-merge what the run's verdict
	// admitted.
	Land Capability = core.PipelineCapabilityLand
)

// Run is the run a capability is asked for, as the server's own records
// name it (serveraction.Principal and Invocation, or a pr_head_verdicts
// row): never what a workflow or a daemon says about itself.
type Run struct {
	// RunID and ChatID are the run and its chat, for the log.
	RunID, ChatID string
	// TriggerID is the activation that launched the run, "" for none.
	TriggerID string
	// DaemonID is the machine the run executes on, "" for none.
	DaemonID string
	// UserID is the run's owner. When set it must be the grant's owner;
	// "" leaves the check to the activation's owner, whose runs every run
	// of the activation is.
	UserID string
	// RepoID is the enrolled repository (pipeline_repos.id) the capability
	// is used on, "" for none: then only a grant for every repository
	// counts.
	RepoID string
}

// Authorizer decides whether a run holds a capability: nil when it does,
// an error that errors.Is ErrNotAuthorized (a *NotAuthorizedError) when it
// does not, any other error when the records could not be read. The merge
// queue's lander takes one, and so does pr.verdict.
type Authorizer interface {
	Authorize(ctx context.Context, run Run, capability Capability) error
}

// Reason is why a run does not hold a capability.
type Reason string

const (
	// ReasonUnknownCapability: the capability is not one this package knows.
	ReasonUnknownCapability Reason = "unknown_capability"
	// ReasonNoActivation: the run is not an activation's (a person started
	// it, or it is a sub-agent's), or its activation is gone.
	ReasonNoActivation Reason = "no_activation"
	// ReasonNoMachine: the run executes on no machine.
	ReasonNoMachine Reason = "no_machine"
	// ReasonNoGrant: the activation holds no live grant of the capability
	// (for the repository).
	ReasonNoGrant Reason = "no_grant"
	// ReasonOwnerMismatch: the grant's owner is not the owner of the
	// activation, of the pool, or of the run.
	ReasonOwnerMismatch Reason = "owner_mismatch"
	// ReasonNotPoolMember: the run's machine is not a member of a granted
	// pool by the labels its owner set.
	ReasonNotPoolMember Reason = "not_pool_member"
)

// ErrNotAuthorized is every refusal: errors.Is(err, ErrNotAuthorized).
var ErrNotAuthorized = errors.New("not authorized")

// NotAuthorizedError is a refusal with its reason. Its message is written
// for the workflow's author and the log; it names no grant id.
type NotAuthorizedError struct {
	Capability Capability
	Reason     Reason
	Detail     string
}

func (e *NotAuthorizedError) Error() string {
	return fmt.Sprintf("the %s capability is not granted: %s", e.Capability, e.Detail)
}

// Is makes every *NotAuthorizedError match ErrNotAuthorized.
func (e *NotAuthorizedError) Is(target error) bool { return target == ErrNotAuthorized }

// ReasonOf is err's refusal reason, "" when err is not a refusal.
func ReasonOf(err error) Reason {
	var e *NotAuthorizedError
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

var _ Authorizer = (*Service)(nil)

// Authorize decides whether run holds capability: a live grant of it to the
// run's activation, made by the owner of the activation and of the pool, on
// a pool whose selector the run's machine matches by the labels its owner
// set. Every fact is read again on each call, so a revoked grant, a
// relabelled machine or a pool that changed hands takes effect at once.
func (s *Service) Authorize(ctx context.Context, run Run, capability Capability) error {
	deny := func(reason Reason, format string, args ...any) *NotAuthorizedError {
		return &NotAuthorizedError{Capability: capability, Reason: reason, Detail: fmt.Sprintf(format, args...)}
	}
	refuse := func(err *NotAuthorizedError) error {
		s.log.InfoContext(ctx, "pipeline capability refused", "capability", string(capability), "reason", string(err.Reason),
			"run_id", run.RunID, "chat_id", run.ChatID, "trigger_id", run.TriggerID, "daemon_id", run.DaemonID, "repo_id", run.RepoID)
		return err
	}
	if !slices.Contains(core.AllPipelineCapabilities, capability) {
		return refuse(deny(ReasonUnknownCapability, "%q is not a pipeline capability", capability))
	}
	if run.TriggerID == "" {
		return refuse(deny(ReasonNoActivation, "the run was not launched by an activation; only an activation's runs hold pipeline capabilities"))
	}
	if run.DaemonID == "" {
		return refuse(deny(ReasonNoMachine, "the run executes on no machine; a capability is granted on a machine pool"))
	}
	grants, err := s.deps.Store.ListLivePipelineCapabilityGrants(ctx, run.TriggerID, capability, run.RepoID)
	if err != nil {
		return err
	}
	if len(grants) == 0 {
		return refuse(deny(ReasonNoGrant, "the run's activation holds no live grant of it; its owner grants it by approving a card in chat"))
	}
	daemon, err := s.deps.Store.PipelineDaemonFactsFor(ctx, run.DaemonID)
	if err != nil {
		return err
	}
	var last *NotAuthorizedError
	for _, g := range grants {
		facts, err := s.deps.Store.PipelineGrantFactsFor(ctx, g.TriggerID, g.MachinePoolID)
		if err != nil {
			return err
		}
		// A grant, its pool and the pool's machines are the owner's personal
		// resources (never shared), so ownership is authz's personal rule.
		switch {
		case !facts.TriggerFound:
			last = deny(ReasonNoActivation, "the run's activation no longer exists")
			continue
		case !facts.PoolFound || !authz.CanAccessPersonal(g.UserID, facts.PoolUserID) || !authz.CanAccessPersonal(g.UserID, facts.TriggerUserID):
			last = deny(ReasonOwnerMismatch, "the grant's owner no longer owns both the activation and the pool")
			continue
		case run.UserID != "" && !authz.CanAccessPersonal(run.UserID, g.UserID):
			last = deny(ReasonOwnerMismatch, "the run acts for another user than the grant's owner")
			continue
		case !daemon.Found || !authz.CanAccessPersonal(g.UserID, daemon.UserID) || !machinepool.Matches(daemon.Labels, facts.PoolSelector):
			last = deny(ReasonNotPoolMember, "the run's machine is not a member of the granted pool %q by the labels its owner set", facts.PoolName)
			continue
		}
		s.log.InfoContext(ctx, "pipeline capability authorized", "capability", string(capability), "grant_id", g.ID,
			"run_id", run.RunID, "chat_id", run.ChatID, "trigger_id", run.TriggerID, "daemon_id", run.DaemonID,
			"machine_pool_id", g.MachinePoolID, "repo_id", run.RepoID)
		return nil
	}
	return refuse(last)
}
