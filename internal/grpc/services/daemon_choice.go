// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// validateOwnedProjectDaemon checks that a daemon a caller chose exists, is
// theirs, and can run the project. Shared by trigger creation and StartChat so
// "which machine may this run on" has one answer.
//
// Another user's daemon is NotFound, identical to a missing one, so a caller
// cannot probe which daemon ids exist. A project with no project_daemons rows
// has no recorded install anywhere, so any owned daemon is accepted; once it is
// installed somewhere, the daemon must be one that has it.
func validateOwnedProjectDaemon(ctx context.Context, repo db.Repository, userID, projectID, daemonID string) error {
	daemon, err := repo.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil || daemon.UserID != userID {
		return connect.NewError(connect.CodeNotFound, errors.New("daemon not found"))
	}

	installs, err := repo.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		logging.Error("daemon choice database error", "op", "list project daemons", "error", err)
		return connect.NewError(connect.CodeInternal, errors.New("database error"))
	}
	if len(installs) == 0 {
		return nil
	}
	for _, install := range installs {
		if install.DaemonID == daemonID && install.InstallState == core.ProjectInstallInstalled {
			return nil
		}
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("project is not installed on that daemon"))
}
