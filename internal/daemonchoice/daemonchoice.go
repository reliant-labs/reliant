// Copyright (c) 2025 Reliant Labs

// Package daemonchoice answers "which machine may this run on?" for a daemon a
// caller chose explicitly. Trigger creation and StartChat (and a chat's
// machine switch) share it, so the question has one answer.
//
//forge:exclude-contract: pure ownership and install check over daemon rows; it reads only through the Reader its caller passes, and holds no state
package daemonchoice

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Reader is what Validate reads: the chosen daemon, and the project's installs.
type Reader interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
}

// Validate checks that a daemon a caller chose exists, is
// theirs, and can run the project. Shared by trigger creation and StartChat so
// "which machine may this run on" has one answer.
//
// Another user's daemon is NotFound, identical to a missing one, so a caller
// cannot probe which daemon ids exist. A project with no project_daemons rows
// has no recorded install anywhere, so any owned daemon is accepted; once it is
// installed somewhere, the daemon must be one that has it.
func Validate(ctx context.Context, repo Reader, userID, projectID, daemonID string) error {
	daemon, err := repo.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil || daemon.UserID != userID {
		return svcerr.ToConnect(svcerr.NotFound("daemon"))
	}

	installs, err := repo.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		logging.ErrorContext(ctx, "daemon choice database error", "op", "list project daemons", "error", err)
		return svcerr.ToConnect(svcerr.Internal("database error"))
	}
	if len(installs) == 0 {
		return nil
	}
	for _, install := range installs {
		if install.DaemonID == daemonID && install.InstallState == core.ProjectInstallInstalled {
			return nil
		}
	}
	return svcerr.ToConnect(svcerr.FailedPrecondition("project is not installed on that daemon"))
}
