// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// PoolReader reads a gate pool and its members. store.MachinePoolExtStore
// implements it.
type PoolReader interface {
	machinepool.MemberReader
	GetMachinePoolByName(ctx context.Context, userID, name string) (*core.MachinePool, error)
}

// DaemonCommander sends one command to one daemon. The worker's
// toolexec.NATSDaemonRouter implements it.
type DaemonCommander interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// GateRepo is what a gate needs of an enrolled repository (pipeline_repos).
type GateRepo struct {
	// Repository is "owner/name", fetched from GitHub.
	Repository string
	// ProjectID is the project a pool member must have installed.
	ProjectID string
	// OwnerUserID owns the machine pool the gate runs on: the user who
	// enrolled the repository (created_by_user_id).
	OwnerUserID string
	Mode        core.PipelineRepoMode
}

// RepoReader reads an enrolled repository; the store layer (M1) implements
// it over pipeline_repos.
type RepoReader interface {
	GateRepo(ctx context.Context, repoID string) (GateRepo, error)
}

// Deps are a Dispatcher's collaborators.
type Deps struct {
	Repos   RepoReader
	Pools   PoolReader
	Daemons DaemonCommander
	// PollInterval is how often a running gate is asked for its status; 0
	// is 15s.
	PollInterval time.Duration
}

// The time a gate gets beyond its own timeout, for the fetch before it and
// the report after it.
const gateSlack = 15 * time.Minute

const (
	startTimeout  = 2 * time.Minute // Select runs the backends' preflight
	statusTimeout = 30 * time.Second
)

type dispatcher struct{ deps Deps }

// NewDispatcher builds the Dispatcher.
func NewDispatcher(deps Deps) Dispatcher {
	if deps.PollInterval <= 0 {
		deps.PollInterval = 15 * time.Second
	}
	return &dispatcher{deps: deps}
}

// Dispatch places the gate on the first connected, eligible member of the
// pool (by name) that takes it, then waits for its verdict.
func (d *dispatcher) Dispatch(ctx context.Context, req GateRequest) (GateResult, error) {
	if req.RepoID == "" || req.BatchID == "" {
		return GateResult{}, fmt.Errorf("%w: repo and batch are required", ErrInvalidRequest)
	}
	repo, err := d.deps.Repos.GateRepo(ctx, req.RepoID)
	if err != nil {
		return GateResult{}, fmt.Errorf("read repository %s: %w", req.RepoID, err)
	}
	start, err := startRequest(repo, req)
	if err != nil {
		return GateResult{}, err
	}
	members, maxPer, err := d.members(ctx, repo, req)
	if err != nil {
		return GateResult{}, err
	}
	members = PreferMember(members, req.PreferMachine)
	start.MaxConcurrent = maxPer
	payload, err := json.Marshal(start)
	if err != nil {
		return GateResult{}, fmt.Errorf("marshal the gate request: %w", err)
	}

	var refused, busy, failed []string
	for _, m := range members {
		var reply StartReply
		if err := d.send(ctx, repo.OwnerUserID, m.DaemonID, CommandStart, payload, startTimeout, &reply); err != nil {
			if ctx.Err() != nil {
				return GateResult{}, fmt.Errorf("%w: %w", errCanceled, ctx.Err())
			}
			// The start may have happened with its reply lost: stop it, and
			// try the next member.
			d.cancel(ctx, repo.OwnerUserID, m.DaemonID, start.GateID)
			failed = append(failed, memberName(m)+": "+err.Error())
			continue
		}
		switch reply.State {
		case JobRunning, JobDone:
			res, err := d.wait(ctx, repo.OwnerUserID, m.DaemonID, start.GateID, start.Timeout()+gateSlack)
			if err == nil {
				res.Machine = m.DaemonID
				res.InfraMatches = append(res.InfraMatches, infraMatches(req.Gate, res)...)
			}
			return res, err
		case JobBusy:
			busy = append(busy, memberName(m))
		case JobRefused:
			refused = append(refused, memberName(m)+": "+reply.Reason)
		default:
			failed = append(failed, fmt.Sprintf("%s: unexpected start state %q", memberName(m), reply.State))
		}
	}
	switch {
	case len(refused) > 0 && len(busy) == 0 && len(failed) == 0:
		return GateResult{}, fmt.Errorf("%w: %s", ErrNoIsolatedGateMachine, strings.Join(refused, "; "))
	case len(busy) > 0 && len(failed) == 0 && len(refused) == 0:
		return GateResult{}, fmt.Errorf("%w: %s", ErrGatePoolBusy, strings.Join(busy, ", "))
	}
	return GateResult{}, fmt.Errorf("%w: busy [%s]; refused [%s]; failed [%s]", ErrNoGateMember,
		strings.Join(busy, ", "), strings.Join(refused, "; "), strings.Join(failed, "; "))
}

// startRequest is req as the daemon receives it.
func startRequest(repo GateRepo, req GateRequest) (StartRequest, error) {
	if repo.OwnerUserID == "" || repo.ProjectID == "" {
		return StartRequest{}, fmt.Errorf("%w: repository has no owner or project", ErrInvalidRequest)
	}
	if repo.Mode != core.PipelineRepoModeShadow && repo.Mode != core.PipelineRepoModeOn {
		return StartRequest{}, fmt.Errorf("%w: repository %s is %q; only shadow and on gate", ErrInvalidRequest, repo.Repository, repo.Mode)
	}
	plan, err := PlanFromGate(req.Gate)
	if err != nil {
		return StartRequest{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = req.Gate.Timeout
	}
	if timeout <= 0 {
		timeout = config.DefaultGateTimeout
	}
	env := make(map[string]string, len(req.Gate.Env)+len(req.Env))
	for k, v := range req.Gate.Env {
		env[k] = v
	}
	for k, v := range req.Env {
		env[k] = v
	}
	s := StartRequest{
		GateID: GateID(req.BatchID, req.Attempt), Repo: repo.Repository, Ref: req.CandidateRef, SHA: req.CandidateSHA,
		BaseSHA: req.BaseSHA, Stages: plan, Env: env, TimeoutMs: timeout.Milliseconds(),
		// Not isolated is allowed when both say shadow (the caller's
		// request, and the repository's mode as stored), or when the
		// repository opted in to unisolated gates.
		AllowNotIsolated: (req.Shadow && repo.Mode == core.PipelineRepoModeShadow) || req.AllowUnisolated,
		// The warm cache is the same opt-in: only a repository that set
		// gate.allow_unisolated accepts a cache its gates share (owner,
		// 2026-11-26). A shadow gate of a shadow repository runs on runc
		// cold.
		WarmCache: req.AllowUnisolated,
	}
	if err := s.Validate(); err != nil {
		return StartRequest{}, err
	}
	return s, nil
}

// infraMatches are the gate's infra_patterns that match the log tail of the
// stage that did not pass.
func infraMatches(g config.ResolvedGate, res GateResult) []string {
	var out []string
	for _, st := range res.Stages {
		if st.Verdict != StageFail && st.Verdict != StageUnproven {
			continue
		}
		for _, re := range g.InfraPatterns {
			if re.MatchString(st.LogTail) {
				out = append(out, re.String())
			}
		}
	}
	return out
}

// members are the pool's connected, eligible members, by name, and the
// pool's per-member gate bound.
func (d *dispatcher) members(ctx context.Context, repo GateRepo, req GateRequest) ([]machinepool.Member, int, error) {
	return PoolMembers(ctx, d.deps.Pools, repo, req.Gate.Pool)
}

// PoolMembers are the connected, eligible members of repo's owner's machine
// pool named pool ("" is config.DefaultGatePool) that have repo's project,
// sorted by name then daemon id, and the pool's per-member run bound
// (max_runs_per_member, at least 1). No member is an error wrapping
// [ErrNoGateMember] that says why.
func PoolMembers(ctx context.Context, pools PoolReader, repo GateRepo, pool string) ([]machinepool.Member, int, error) {
	name := pool
	if name == "" {
		name = config.DefaultGatePool
	}
	row, err := pools.GetMachinePoolByName(ctx, repo.OwnerUserID, name)
	if err != nil {
		if errors.Is(err, core.ErrMachinePoolNotFound) {
			return nil, 0, fmt.Errorf("%w: no machine pool %q (label gate machines pool=%s and create the pool)", ErrNoGateMember, name, name)
		}
		return nil, 0, fmt.Errorf("read machine pool %s: %w", name, err)
	}
	all, err := machinepool.MembersOf(ctx, pools, row, repo.ProjectID)
	if err != nil {
		return nil, 0, fmt.Errorf("read machine pool %s's members: %w", name, err)
	}
	var out []machinepool.Member
	for _, m := range machinepool.Eligible(all) {
		if m.Phase == machinepool.PhaseConnected {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("%w: %s", ErrNoGateMember, machinepool.Decide(machinepool.PoolFromCore(row), all).Reason(name))
	}
	sortMembers(out)
	return out, max(row.MaxRunsPerMember, 1), nil
}

// wait polls the member until the gate is done, the member loses it, or
// budget passes. A status the member cannot answer (a reconnect) is asked
// again at the next poll.
func (d *dispatcher) wait(ctx context.Context, userID, daemonID, gateID string, budget time.Duration) (GateResult, error) {
	payload, _ := json.Marshal(StatusRequest{GateID: gateID})
	deadline := time.Now().Add(budget)
	tick := time.NewTicker(d.deps.PollInterval)
	defer tick.Stop()
	var lastErr error
	for {
		var reply StatusReply
		err := d.send(ctx, userID, daemonID, CommandStatus, payload, statusTimeout, &reply)
		switch {
		case ctx.Err() != nil:
			d.cancel(ctx, userID, daemonID, gateID)
			return GateResult{}, fmt.Errorf("%w: %w", errCanceled, ctx.Err())
		case err != nil:
			lastErr = err
		case reply.State == JobDone && reply.Result != nil:
			return *reply.Result, nil
		case reply.State == JobUnknown:
			return GateResult{}, fmt.Errorf("%w: machine %s no longer knows gate %s (it restarted?)", ErrGateLost, daemonID, gateID)
		}
		if time.Now().After(deadline) {
			d.cancel(ctx, userID, daemonID, gateID)
			return GateResult{}, fmt.Errorf("%w: no result from machine %s within %s (last error: %v)", ErrGateLost, daemonID, budget, lastErr)
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

func (d *dispatcher) send(ctx context.Context, userID, daemonID, command string, payload []byte, timeout time.Duration, reply any) error {
	raw, err := d.deps.Daemons.SendDaemonCommandToDaemon(ctx, userID, daemonID, command, payload, int32(timeout.Milliseconds()))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, reply); err != nil {
		return fmt.Errorf("decode %s reply: %w", command, err)
	}
	return nil
}

// cancel asks the member to stop the gate, best effort, even when ctx is
// done.
func (d *dispatcher) cancel(ctx context.Context, userID, daemonID, gateID string) {
	cctx, done := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
	defer done()
	payload, _ := json.Marshal(StatusRequest{GateID: gateID})
	_, _ = d.deps.Daemons.SendDaemonCommandToDaemon(cctx, userID, daemonID, CommandCancel, payload, int32(statusTimeout.Milliseconds()))
}

// PreferMember is members with the one whose daemon id is daemonID moved to
// the front, the others in their order. "" or an id not among them returns
// members unchanged.
func PreferMember(members []machinepool.Member, daemonID string) []machinepool.Member {
	if daemonID == "" {
		return members
	}
	i := slices.IndexFunc(members, func(m machinepool.Member) bool { return m.DaemonID == daemonID })
	if i <= 0 {
		return members
	}
	out := make([]machinepool.Member, 0, len(members))
	out = append(out, members[i])
	out = append(out, members[:i]...)
	return append(out, members[i+1:]...)
}

func memberName(m machinepool.Member) string {
	if m.Name != "" {
		return m.Name
	}
	return m.DaemonID
}

func sortMembers(ms []machinepool.Member) {
	slices.SortFunc(ms, func(a, b machinepool.Member) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.DaemonID, b.DaemonID))
	})
}
