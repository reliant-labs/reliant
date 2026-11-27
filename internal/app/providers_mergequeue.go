// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/mergequeue"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/lander"
	"github.com/reliant-labs/reliant/internal/mergequeue/queuetoken"
	"github.com/reliant-labs/reliant/internal/store"
)

// MergeQueueDeps is what the worker's merge queue is built from.
type MergeQueueDeps struct {
	DB *db.Repo
	// Router sends the gate commands to the gate pool's daemons.
	Router *WorkerDaemonRouter
	// Pipeline is ServerProcessConfig.pipeline: the enabled switch, and the
	// Queue App's caller secret.
	Pipeline *configv1.PipelineConfig
	// ControlPlane is where the queue's GitHub tokens are minted.
	ControlPlane *configv1.ControlPlaneConfig
	// DataDir holds the queue's git workdirs (<data_dir>/mergequeue).
	DataDir string
}

// MergeQueue is the Reliant merge queue on this worker: the scheduler,
// driving every enrolled repository it leads (internal/mergequeue). nil
// unless pipeline.enabled is on and a control plane mints GitHub tokens.
type MergeQueue struct {
	scheduler *mergequeue.Scheduler
	repos     mergequeue.Enrolled
}

// NewMergeQueue builds the worker's merge queue: the scheduler over the
// gate runner (gaterun, the repository's gate pool), the lander (as the
// Queue App, else the Reliant App, unenforced) and the queue's git workdirs.
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
	gate := mergequeue.DispatchGate{Dispatcher: gaterun.NewDispatcher(gaterun.Deps{
		Repos: mergequeue.GateRepos{Store: d.DB}, Pools: store.NewMachinePoolExtStore(d.DB.ORM()), Daemons: d.Router,
		PollInterval: 0, // gaterun's default, 15s
	})}
	land, err := lander.New(lander.Deps{
		Store: d.DB, Credentials: clients, Verifier: mergequeue.GateVerifier{Gate: gate}, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	locks, err := dblock.NewInstrumented(dblock.Deps{Sessions: d.DB.SessionPool()})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	root := d.DataDir
	if root == "" {
		root = os.TempDir()
	}
	token := func(ctx context.Context, repo *core.PipelineRepo) (string, error) {
		t, err := source.Token(ctx, repo)
		if err != nil {
			return "", err
		}
		var plain string
		if err := t.Token.Use(func(s string) error { plain = s; return nil }); err != nil {
			return "", err
		}
		if plain == "" {
			return "", errors.New("merge queue: an empty GitHub token")
		}
		return plain, nil
	}
	scheduler, err := mergequeue.New(mergequeue.Deps{
		Store: d.DB, Leader: mergequeue.AdvisoryLeader{Locks: locks},
		Workdirs: mergequeue.GitWorkdirs{Root: filepath.Join(root, "mergequeue"), Access: mergequeue.AccessOf(clients, token)},
		Gate:     gate, Lander: mergequeue.LanderAdapter{Lander: land}, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("merge queue: %w", err)
	}
	logger.Info("the Reliant merge queue is ON (pipeline.enabled): every enrolled repository not off is driven")
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
