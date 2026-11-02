// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A PROJECT'S PATH IS PER MACHINE.
//
// projects.path is the folder the project was created or cloned at, ON THE
// MACHINE THAT DID IT. A project the owner created on their Mac carries
// /Users/<them>/src/<repo>; the same project cloned onto a cloud machine lives
// at /home/workspace/projects/<repo>. The per-machine truth is project_daemons:
// one row per machine holding a checkout, each with its own path.
//
// So a server-side caller that sends a project-scoped command must choose the
// machine and the path TOGETHER, from one row. Sending projects.path to
// whichever machine default resolution picks is the bug this exists to end:
// the forge console asked a cloud machine about a Mac path and reported
// "forge project dir does not exist: /Users/...".

// Checkout is a project's main checkout on one machine.
type Checkout struct {
	// DaemonID is the machine holding it.
	DaemonID string
	// Path is where it is on THAT machine.
	Path string
}

// ErrNoCheckout is what NoCheckoutError matches with errors.Is.
var ErrNoCheckout = errors.New("project is not checked out on any of the user's machines")

// NoCheckoutError says why a project has no checkout to run against: none
// of the user's machines holds one. Cloning and Failed are the user's own
// machines a clone is still landing on, or failed on — what a caller needs
// to say something more useful than "not found".
type NoCheckoutError struct {
	ProjectID string
	// Cloning are installs still queued or running.
	Cloning []*core.ProjectDaemon
	// Failed are installs that ran and failed (InstallError says why).
	Failed []*core.ProjectDaemon
}

func (e *NoCheckoutError) Error() string {
	return fmt.Sprintf("project %s is not checked out on any of the user's machines (%d cloning, %d failed)",
		e.ProjectID, len(e.Cloning), len(e.Failed))
}

// Is makes errors.Is(err, ErrNoCheckout) hold.
func (e *NoCheckoutError) Is(target error) bool { return target == ErrNoCheckout }

// ProjectCheckout returns the machine a project-scoped command should run on
// AND the project's path on that machine, from the same project_daemons row.
//
// The machine is chosen exactly as ProjectDefaultDaemonID chooses it among
// the user's installed machines (the router's default when it has the
// project, else the first it can place, else the first), so a command sent
// here lands where the project's chats and package commands do.
//
// Unlike ProjectDefaultDaemonID there is no fallback to project-blind default
// resolution: with no installed checkout there is no path that is true on any
// machine, and guessing one is the bug. The error is then a *NoCheckoutError.
func ProjectCheckout(ctx context.Context, repo Installs, machines Machines, userID, projectID string) (Checkout, error) {
	installs, err := userInstalls(ctx, repo, userID, projectID)
	if err != nil {
		return Checkout{}, err
	}
	var ready []*core.ProjectDaemon
	missing := &NoCheckoutError{ProjectID: projectID}
	for _, row := range installs {
		switch {
		case row.InstallState == core.ProjectInstallInstalled && strings.TrimSpace(row.Path) != "":
			ready = append(ready, row)
		case row.InstallState == core.ProjectInstallInstalling:
			missing.Cloning = append(missing.Cloning, row)
		case row.InstallState == core.ProjectInstallFailed:
			missing.Failed = append(missing.Failed, row)
		}
	}
	if len(ready) == 0 {
		return Checkout{}, missing
	}
	row := preferredInstall(ctx, machines, userID, ready)
	return Checkout{DaemonID: row.DaemonID, Path: row.Path}, nil
}
