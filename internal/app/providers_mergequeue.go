// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/mergequeue"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/jobrun"
	"github.com/reliant-labs/reliant/internal/mergequeue/lander"
	"github.com/reliant-labs/reliant/internal/mergequeue/queuetoken"
	"github.com/reliant-labs/reliant/internal/prrecords"
	"github.com/reliant-labs/reliant/internal/store"
)

// MergeQueueDeps is what the worker's merge queue is built from.
type MergeQueueDeps struct {
	DB *db.Repo
	// Router sends the gate pool's jobs and gates to its daemons.
	Router *WorkerDaemonRouter
	// Pipeline is ServerProcessConfig.pipeline: the enabled switch, and the
	// Queue App's caller secret.
	Pipeline *configv1.PipelineConfig
	// ControlPlane is where the queue's GitHub tokens are minted.
	ControlPlane *configv1.ControlPlaneConfig
}

// MergeQueue is the Reliant merge queue on this worker: the scheduler,
// driving every enrolled repository it leads (internal/mergequeue). nil
// unless pipeline.enabled is on and a control plane mints GitHub tokens.
type MergeQueue struct {
	scheduler *mergequeue.Scheduler
	repos     mergequeue.Enrolled
}

// NewMergeQueue builds the worker's merge queue. The worker coordinates
// only (owner, 2026-11-27): every code operation — a candidate build, a
// head's changed paths, a gate — is a job on a daemon of the repository's
// gate pool (jobrun, gaterun), whose report is checked through the GitHub
// API, and the worker holds no repository bytes. Landing is the one write
// the worker makes itself: a squash merge through the GitHub API as the
// Queue App, pinned to the gated head, and only while the run that
// approved each head holds the land capability (internal/pipeline/grants).
// It is nil, and nothing runs, while pipeline.enabled is off.
//
//forge:root-only worker
func NewMergeQueue(d MergeQueueDeps) (*MergeQueue, error) {
	if !d.Pipeline.GetEnabled() {
		return nil, nil
	}
	logger := slog.Default().With("component", "mergequeue")
	tokens, err := gitTokens(d.ControlPlane)
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	if tokens == nil {
		logger.Warn("the merge queue is OFF: pipeline.enabled is on, but no control plane mints GitHub tokens")
		return nil, nil
	}
	source, err := queuetoken.New(queuetoken.Options{Minter: tokens, CallerSecret: d.Pipeline.GetGithubQueueAppCallerSecret(), Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	clients := &queuetoken.Clients{Source: source}
	pools := store.NewMachinePoolExtStore(d.DB.ORM())
	gate := mergequeue.DispatchGate{Dispatcher: gaterun.NewDispatcher(gaterun.Deps{
		Repos: mergequeue.GateRepos{Store: d.DB}, Pools: pools, Daemons: d.Router,
		PollInterval: 0, // gaterun's default, 15s
	})}
	builder, err := jobrun.NewBuilder(jobrun.Deps{
		Pools: pools, Daemons: d.Router, GitHub: queueGitHub{clients: clients},
		Repos:        nil, // only a ConfigReader finds a read's enrolment
		Configs:      nil, // pipeline_repos.config_json, as mergequeue.StoredConfig reads it
		PollInterval: 0,   // jobrun's default, 5s
	})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	land, err := lander.New(lander.Deps{
		Store: d.DB, Credentials: clients, Verifier: mergequeue.GateVerifier{Gate: gate}, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	authorizer, err := pipelineGrants(d.DB)
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	locks, err := dblock.NewInstrumented(dblock.Deps{Sessions: d.DB.SessionPool()})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	scheduler, err := mergequeue.New(mergequeue.Deps{
		Store: d.DB, Leader: mergequeue.AdvisoryLeader{Locks: locks},
		Refs:       mergequeue.ClientRefs{Clients: queueGitHub{clients: clients}},
		Builder:    builder,
		Gate:       gate,
		Authorizer: authorizer,
		Lander:     mergequeue.LanderAdapter{Lander: land},
		Logger:     logger,
	})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	logger.Info("the Reliant merge queue is ON (pipeline.enabled): every enrolled repository not off is driven, its code operations on its gate pool")
	return &MergeQueue{scheduler: scheduler, repos: d.DB}, nil
}

// Run drives the queue until ctx ends.
func (q *MergeQueue) Run(ctx context.Context) error {
	err := q.scheduler.Run(ctx, q.repos, mergequeue.DefaultTickInterval)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// queueGitHub is the per-repository GitHub client the queue reads refs and
// commits through (jobrun.Clients, mergequeue.ClientRefs): queuetoken's,
// without the identity only the lander lands as.
type queueGitHub struct{ clients *queuetoken.Clients }

// Client returns the repository's client.
func (q queueGitHub) Client(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, error) {
	c, _, err := q.clients.Client(ctx, repo)
	return c, err
}

// PipelineConfigRefreshDeps is what the worker's pipeline.yaml refresh is
// built from.
type PipelineConfigRefreshDeps struct {
	DB *db.Repo
	// Router sends the config jobs to the gate pool's daemons.
	Router *WorkerDaemonRouter
	// Pipeline is ServerProcessConfig.pipeline: the enabled switch.
	Pipeline *configv1.PipelineConfig
}

// PipelineConfigRefresh reads the .reliant/pipeline.yaml each base-branch
// push leaves pending (the PR record projector records the commit, inside
// webhook delivery, which never waits on a daemon) on a daemon of the
// repository's gate pool, and records it (prrecords.ConfigRefresher). nil,
// and nothing runs, while pipeline.enabled is off.
type PipelineConfigRefresh struct {
	refresher *prrecords.ConfigRefresher
}

// pipelineConfigRefreshInterval is how often the worker looks for a due
// pipeline.yaml read.
const pipelineConfigRefreshInterval = 15 * time.Second

// NewPipelineConfigRefresh builds the worker's pipeline.yaml refresh: the
// file is read as a config job on the repository's gate pool (jobrun's
// ConfigReader), whose bytes the worker validates; a read that finds no
// member is recorded as the config error and retried later.
//
//forge:root-only worker
func NewPipelineConfigRefresh(d PipelineConfigRefreshDeps) (*PipelineConfigRefresh, error) {
	if !d.Pipeline.GetEnabled() {
		return nil, nil
	}
	reader, err := jobrun.NewConfigReader(jobrun.Deps{
		Pools: store.NewMachinePoolExtStore(d.DB.ORM()), Daemons: d.Router, Repos: d.DB,
		GitHub:       nil, // only a Builder checks a report through GitHub
		Configs:      nil, // pipeline_repos.config_json, as mergequeue.StoredConfig reads it
		PollInterval: 0,   // jobrun's default, 5s
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline config refresh: %w", err)
	}
	refresher, err := prrecords.NewConfigRefresher(prrecords.ConfigRefresherDeps{
		Store: d.DB, Contents: reader, Logger: slog.Default().With("component", "pipeline_config_refresh"),
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline config refresh: %w", err)
	}
	return &PipelineConfigRefresh{refresher: refresher}, nil
}

// Run reads the due pipeline.yaml files until ctx ends.
func (r *PipelineConfigRefresh) Run(ctx context.Context) error {
	return r.refresher.Run(ctx, pipelineConfigRefreshInterval)
}
