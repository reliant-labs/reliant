// Copyright (c) 2025 Reliant Labs
package jobrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// Configs resolves a repository's recorded pipeline.yaml, for the name of
// its gate pool. mergequeue.StoredConfig implements it.
type Configs interface {
	Resolved(ctx context.Context, repo *core.PipelineRepo) (config.Resolved, error)
}

// Clients is the per-repository GitHub client a build's report is checked
// through (refs and commits: metadata, never contents). queuetoken.Clients
// implements it.
type Clients interface {
	Client(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, error)
}

// Repos finds the enrolments of a repository, for a config read, which
// names the repository and the user it acts for but not the enrolment.
// *db.Repo implements it.
type Repos interface {
	ListPipelineReposByRepository(ctx context.Context, provider, repository string) ([]*core.PipelineRepo, error)
}

// Deps are the collaborators of a [Builder] and a [ConfigReader].
type Deps struct {
	// Pools reads the gate pool and its members. store.MachinePoolExtStore
	// implements it.
	Pools gaterun.PoolReader
	// Daemons sends the job commands. The worker's
	// toolexec.NATSDaemonRouter implements it.
	Daemons gaterun.DaemonCommander
	// Configs names the repository's gate pool (gate.pool). nil reads
	// pipeline_repos.config_json as mergequeue.StoredConfig does.
	//
	// forge:optional-dep
	Configs Configs
	// GitHub checks a build's report. NewBuilder requires it; the
	// ConfigReader does not use it.
	//
	// forge:optional-dep
	GitHub Clients
	// Repos finds a config read's enrolment. NewConfigReader requires it;
	// the Builder does not use it.
	//
	// forge:optional-dep
	Repos Repos
	// PollInterval is how often a running job is asked for its status; 0
	// is 5s.
	PollInterval time.Duration
}

// How long each kind of job may take before it is given up as lost: a
// build fetches, squashes, may run regen (candidate.DefaultRegenTimeout)
// and pushes; the others fetch and read.
var budgets = map[buildjob.Kind]time.Duration{
	buildjob.KindBuild:  time.Hour,
	buildjob.KindPaths:  15 * time.Minute,
	buildjob.KindConfig: 10 * time.Minute,
}

const (
	startTimeout  = time.Minute
	statusTimeout = 30 * time.Second
)

// Errors a job returns when it reached no answer. Each is safe to retry with
// the same request.
var (
	// ErrNoMember: the pool has no connected, eligible member.
	ErrNoMember = errors.New("jobrun: no gate pool member is available")
	// ErrPoolBusy: every eligible member runs all the jobs it takes.
	ErrPoolBusy = errors.New("jobrun: every gate pool member is busy")
	// ErrRefused: every member refused the job (an invalid request, or a
	// member whose Reliant predates pipeline jobs).
	ErrRefused = errors.New("jobrun: every gate pool member refused the job")
	// ErrLost: the member stopped knowing the job (it restarted), or never
	// answered before the job's budget passed.
	ErrLost = errors.New("jobrun: the job was lost")
	// ErrJobFailed: the job ran and reached no answer (a git or GitHub
	// failure on the member).
	ErrJobFailed = errors.New("jobrun: the job failed")
	// ErrUnverified: the member's report does not hold up against the
	// request or against GitHub.
	ErrUnverified = errors.New("jobrun: the job's report did not verify")
	// ErrInvalidRequest: the job cannot be dispatched as asked.
	ErrInvalidRequest = errors.New("jobrun: invalid job request")
)

type dispatcher struct{ deps Deps }

func newDispatcher(deps Deps) *dispatcher {
	if deps.PollInterval <= 0 {
		deps.PollInterval = 5 * time.Second
	}
	return &dispatcher{deps: deps}
}

// target is where a job runs: the repository as a gate sees it and its gate
// pool's name.
type target struct {
	repo gaterun.GateRepo
	pool string
	// skip are members (daemon ids) not to place the job on: ones that
	// already answered a job of this request in a shape too old to use.
	skip []string
}

func (d *dispatcher) target(ctx context.Context, repo *core.PipelineRepo) (target, error) {
	if repo == nil {
		return target{}, fmt.Errorf("%w: no repository", ErrInvalidRequest)
	}
	if repo.CreatedByUserID == "" || repo.ProjectID == "" {
		return target{}, fmt.Errorf("%w: repository %s has no owner or project", ErrInvalidRequest, repo.Repository)
	}
	var resolved config.Resolved
	var err error
	if d.deps.Configs != nil {
		resolved, err = d.deps.Configs.Resolved(ctx, repo)
	} else {
		resolved, err = storedConfig(repo)
	}
	if err != nil {
		return target{}, fmt.Errorf("jobrun: resolve %s's pipeline.yaml: %w", repo.Repository, err)
	}
	return target{
		repo: gaterun.GateRepo{Repository: repo.Repository, ProjectID: repo.ProjectID, OwnerUserID: repo.CreatedByUserID, Mode: repo.Mode},
		pool: resolved.Gate.Pool,
	}, nil
}

// storedConfig is mergequeue.StoredConfig's reading of config_json.
func storedConfig(repo *core.PipelineRepo) (config.Resolved, error) {
	if len(repo.ConfigJSON) == 0 || string(repo.ConfigJSON) == "null" {
		return config.Defaults(), nil
	}
	return config.Load(repo.ConfigJSON)
}

// run places the job on the first member of t's pool that takes it, prefer
// first, and waits for its reply. It returns the reply of a job that is done
// with no error, and the member (daemon id) that ran it.
func (d *dispatcher) run(ctx context.Context, t target, start buildjob.StartRequest, prefer string) (buildjob.StatusReply, string, error) {
	if start.JobID == "" {
		start.JobID = newJobID(start.Kind)
	}
	if err := start.Validate(); err != nil {
		return buildjob.StatusReply{}, "", fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	members, maxPer, err := gaterun.PoolMembers(ctx, d.deps.Pools, t.repo, t.pool)
	if err != nil {
		if errors.Is(err, gaterun.ErrNoGateMember) {
			return buildjob.StatusReply{}, "", fmt.Errorf("%w: %w", ErrNoMember, err)
		}
		return buildjob.StatusReply{}, "", err
	}
	members = slices.DeleteFunc(members, func(m machinepool.Member) bool { return slices.Contains(t.skip, m.DaemonID) })
	if len(members) == 0 {
		return buildjob.StatusReply{}, "", fmt.Errorf("%w: every connected member of pool %s was tried", ErrNoMember, t.pool)
	}
	members = gaterun.PreferMember(members, prefer)
	start.MaxConcurrent = maxPer
	payload, err := json.Marshal(start)
	if err != nil {
		return buildjob.StatusReply{}, "", fmt.Errorf("jobrun: marshal the job: %w", err)
	}
	owner := t.repo.OwnerUserID
	var refused, busy, failed []string
	for _, m := range members {
		var reply buildjob.StartReply
		if err := d.send(ctx, owner, m.DaemonID, buildjob.CommandStart, payload, startTimeout, &reply); err != nil {
			if ctx.Err() != nil {
				return buildjob.StatusReply{}, "", ctx.Err()
			}
			if unsupported(err) {
				refused = append(refused, memberName(m)+": its Reliant predates pipeline jobs")
				continue
			}
			// The start may have happened with its reply lost: stop it,
			// and try the next member.
			d.cancel(ctx, owner, m.DaemonID, start.JobID)
			failed = append(failed, memberName(m)+": "+err.Error())
			continue
		}
		switch reply.State {
		case buildjob.JobRunning, buildjob.JobDone:
			res, err := d.wait(ctx, owner, m.DaemonID, start.JobID, budgets[start.Kind])
			return res, m.DaemonID, err
		case buildjob.JobBusy:
			busy = append(busy, memberName(m))
		case buildjob.JobRefused:
			refused = append(refused, memberName(m)+": "+reply.Reason)
		default:
			failed = append(failed, fmt.Sprintf("%s: unexpected start state %q", memberName(m), reply.State))
		}
	}
	switch {
	case len(refused) > 0 && len(busy) == 0 && len(failed) == 0:
		return buildjob.StatusReply{}, "", fmt.Errorf("%w: %s", ErrRefused, strings.Join(refused, "; "))
	case len(busy) > 0 && len(failed) == 0 && len(refused) == 0:
		return buildjob.StatusReply{}, "", fmt.Errorf("%w: %s", ErrPoolBusy, strings.Join(busy, ", "))
	}
	return buildjob.StatusReply{}, "", fmt.Errorf("%w: busy [%s]; refused [%s]; failed [%s]", ErrNoMember,
		strings.Join(busy, ", "), strings.Join(refused, "; "), strings.Join(failed, "; "))
}

// wait polls the member until the job is done, the member loses it, or
// budget passes. A status the member cannot answer (a reconnect) is asked
// again at the next poll.
func (d *dispatcher) wait(ctx context.Context, userID, daemonID, jobID string, budget time.Duration) (buildjob.StatusReply, error) {
	payload, _ := json.Marshal(buildjob.StatusRequest{JobID: jobID})
	deadline := time.Now().Add(budget)
	tick := time.NewTicker(d.deps.PollInterval)
	defer tick.Stop()
	var lastErr error
	for {
		var reply buildjob.StatusReply
		err := d.send(ctx, userID, daemonID, buildjob.CommandStatus, payload, statusTimeout, &reply)
		switch {
		case ctx.Err() != nil:
			d.cancel(ctx, userID, daemonID, jobID)
			return buildjob.StatusReply{}, ctx.Err()
		case err != nil:
			lastErr = err
		case reply.State == buildjob.JobDone && reply.Error != "":
			return buildjob.StatusReply{}, fmt.Errorf("%w on machine %s: %s", ErrJobFailed, daemonID, reply.Error)
		case reply.State == buildjob.JobDone:
			return reply, nil
		case reply.State == buildjob.JobUnknown:
			return buildjob.StatusReply{}, fmt.Errorf("%w: machine %s no longer knows job %s (it restarted?)", ErrLost, daemonID, jobID)
		}
		if time.Now().After(deadline) {
			d.cancel(ctx, userID, daemonID, jobID)
			return buildjob.StatusReply{}, fmt.Errorf("%w: no result from machine %s within %s (last error: %v)", ErrLost, daemonID, budget, lastErr)
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

// cancel asks the member to stop the job, best effort, even when ctx is
// done.
func (d *dispatcher) cancel(ctx context.Context, userID, daemonID, jobID string) {
	cctx, done := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
	defer done()
	payload, _ := json.Marshal(buildjob.StatusRequest{JobID: jobID})
	_, _ = d.deps.Daemons.SendDaemonCommandToDaemon(cctx, userID, daemonID, buildjob.CommandCancel, payload, int32(statusTimeout.Milliseconds()))
}

// unsupported reports whether err is a daemon's answer to a command it does
// not have: a Reliant older than pipeline jobs (daemonruntime's
// unknownCommandError, or the older registry's text).
func unsupported(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "pipeline.job.") &&
		(strings.Contains(msg, "doesn't support") || strings.Contains(msg, "unknown daemon command type"))
}

func newJobID(kind buildjob.Kind) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return string(kind) + "-" + hex.EncodeToString(b[:])
}

func memberName(m machinepool.Member) string {
	if m.Name != "" {
		return m.Name
	}
	return m.DaemonID
}
