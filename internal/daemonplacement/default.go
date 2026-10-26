// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Machines resolves a machine for a caller that records it rather than
// sends to it. *toolexec.NATSDaemonRouter implements it.
//
// daemonID "" is default resolution; otherwise it names the machine to
// resolve. A machine that is starting or asleep answers with its id — the
// record is known and a placement must not wait for the machine to come up —
// and "the user has no such machine" is toolexec.ErrNoDaemon.
type Machines interface {
	PlacementDaemonID(ctx context.Context, userID, daemonID string) (string, error)
}

// Installs is what the project-aware default reads: where the project is
// installed, and who owns each of those machines.
type Installs interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
}

// ProjectDefaultDaemonID is the machine a project's work goes to when nothing
// names one: default resolution, narrowed to the machines that have the
// project.
//
// Default resolution alone is project-blind and prefers a connected local
// machine, so a user who adds their laptop to a cloud machine's project sends
// everything to a machine with no checkout (daemonchoice.Validate refuses
// exactly that choice when a user makes it). Among the user's machines the
// project is installed on, it picks:
//
//  1. the router's default, when it is one of them;
//  2. else the first of them (oldest install) the router can place;
//  3. else the first of them, whose record the router could not place.
//
// A project with no installed machine — no install recorded at all, or only
// installs still cloning or failed — has nothing to narrow to, so plain
// default resolution answers, as it always did.
//
// The error is the router's: toolexec.ErrNoDaemon when the user has no
// machine at all.
func ProjectDefaultDaemonID(ctx context.Context, repo Installs, machines Machines, userID, projectID string) (string, error) {
	installed, err := installedDaemonIDs(ctx, repo, userID, projectID)
	if err != nil {
		return "", err
	}
	def, defErr := machines.PlacementDaemonID(ctx, userID, "")
	if len(installed) == 0 {
		return def, defErr
	}
	if defErr == nil {
		for _, id := range installed {
			if id == def {
				return def, nil
			}
		}
	}
	for _, id := range installed {
		if placed, err := machines.PlacementDaemonID(ctx, userID, id); err == nil {
			return placed, nil
		}
	}
	return installed[0], nil
}

// installedDaemonIDs lists, oldest install first, the user's machines the
// project is installed on. A machine another user owns, or one that no longer
// exists, is not the user's to run on.
func installedDaemonIDs(ctx context.Context, repo Installs, userID, projectID string) ([]string, error) {
	rows, err := repo.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project installs %s: %w", projectID, err)
	}
	var ids []string
	for _, row := range rows {
		if row.InstallState != core.ProjectInstallInstalled {
			continue
		}
		daemon, err := repo.GetDaemon(ctx, row.DaemonID)
		if err != nil && !svcerr.IsNotFound(err) {
			return nil, fmt.Errorf("load daemon %s: %w", row.DaemonID, err)
		}
		if err != nil || daemon == nil || daemon.UserID != userID {
			continue
		}
		ids = append(ids, row.DaemonID)
	}
	return ids, nil
}

// Registry is what PinTarget reads.
type Registry interface {
	WorktreeReader
	Installs
}

// PinTarget returns the machine an unpinned chat should be pinned to, or ""
// when it should stay unpinned (a chat with no machine by design, or a user
// with no machine at all).
//
// The chat's own pin or its worktree's owner comes first (ChatDaemonID); only
// a chat that names neither falls to ProjectDefaultDaemonID. The error is
// anything else that stopped the answer; the caller decides what that costs.
func PinTarget(ctx context.Context, repo Registry, machines Machines, chat *db.Chat) (string, error) {
	if chat.NoMachine {
		return "", nil
	}
	id, err := ChatDaemonID(ctx, repo, chat)
	if err != nil || id != "" {
		return id, err
	}
	if machines == nil {
		return "", nil
	}
	id, err = ProjectDefaultDaemonID(ctx, repo, machines, chat.UserID, chat.ProjectID)
	if errors.Is(err, toolexec.ErrNoDaemon) {
		return "", nil
	}
	return id, err
}
