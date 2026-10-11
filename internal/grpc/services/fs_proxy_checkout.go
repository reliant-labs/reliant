package services

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// missingDirMarkers are how a daemon's filesystem error says "that path does
// not exist". The daemon reports errors as text across the wire — a
// DaemonCommandResponse carries error_message and no error kind — so the text
// is what there is to match: Go's ENOENT on Unix, and the two Win32 messages
// for a missing file and a missing directory.
var missingDirMarkers = []string{
	"no such file or directory",
	"cannot find the file specified",
	"cannot find the path specified",
}

// isMissingPathError reports whether a daemon fs command failed because the
// path it was asked about does not exist.
//
// A program the daemon could not start fails with ENOENT too ("fork/exec
// /usr/bin/rg: no such file or directory"). That is the daemon's install, not
// the user's files, so it is excluded and stays a server error.
func isMissingPathError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "fork/exec") {
		return false
	}
	for _, marker := range missingDirMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// checkoutMissingError turns "the project's root does not exist on this
// machine" into NOT_FOUND carrying a ProjectCheckoutMissing detail, or returns
// nil when err is anything else.
//
// Only the ROOT of the MAIN checkout qualifies — requested with no worktree or
// with the project's main worktree. A missing subdirectory is an ordinary race
// with the user's own edits, and a worktree's directory is not described by the
// clone record this consults; both are plain NOT_FOUND (see sendCommand).
//
// The state comes from project_daemons for the machine the request reached:
// a clone still installing there is CLONING (the window between "Clone from
// GitHub" and the queued clone landing, during which the UI used to show a
// raw "no such file or directory" 500), a failed one is CLONE_FAILED, and no
// record at all is ABSENT — the project was cloned onto another machine, as
// houndersclub was on 2026-10-07 when the owner's new machine came up empty.
func (s *FileSystemProxyService) checkoutMissingError(
	ctx context.Context, userID, projectID string, worktreeID *string, scope workspaceScope, resolvedPath string, err error,
) error {
	if filepath.Clean(resolvedPath) != filepath.Clean(scope.basePath) || !isMissingPathError(err) {
		return nil
	}
	// The client names the main worktree when the user is on the project's
	// own checkout, which is still the clone this describes. Only a real
	// worktree is out of scope.
	if worktreeID != nil && *worktreeID != "" {
		wt, werr := s.database.GetWorktree(ctx, *worktreeID)
		if werr != nil || wt == nil || !wt.IsMain {
			return nil
		}
	}

	detail := &reliantv1.ProjectCheckoutMissing{
		ProjectId: projectID,
		DaemonId:  scope.target.daemonID,
		Path:      scope.basePath,
		State:     reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT,
	}
	if detail.DaemonId == "" && s.router != nil {
		// Default resolution picked the machine. Ask the router which one,
		// so the state describes THAT machine rather than any machine the
		// project was ever cloned onto.
		if id, rerr := s.router.ResolveDaemonID(ctx, userID); rerr == nil {
			detail.DaemonId = id
		}
	}
	if project, perr := s.database.GetProject(ctx, projectID); perr == nil && project != nil && project.RemoteURL != nil {
		detail.RemoteUrl = strings.TrimSpace(*project.RemoteURL)
	}
	if rows, lerr := s.database.ListProjectDaemonsForProject(ctx, projectID); lerr != nil {
		logging.Warn("[FSProxy] Could not read clone records for a missing project directory",
			"error", lerr, "projectID", projectID)
	} else {
		detail.State, detail.InstallError = checkoutStateOn(rows, detail.DaemonId)
	}

	cerr := connect.NewError(connect.CodeNotFound,
		fmt.Errorf("project directory %s does not exist on this machine", scope.basePath))
	if d, derr := connect.NewErrorDetail(detail); derr == nil {
		cerr.AddDetail(d)
	}
	return cerr
}

// checkoutStateOn reads one machine's clone record for a project. With no
// machine to match (the router could not say), only an in-flight clone counts:
// claiming CLONE_FAILED from some other machine's record would be wrong, and
// CLONING is the one state that resolves itself.
func checkoutStateOn(rows []*core.ProjectDaemon, daemonID string) (reliantv1.ProjectCheckoutState, string) {
	for _, row := range rows {
		if row == nil {
			continue
		}
		if daemonID != "" && row.DaemonID != daemonID {
			continue
		}
		switch row.InstallState {
		case core.ProjectInstallInstalling:
			return reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, ""
		case core.ProjectInstallFailed:
			if daemonID != "" {
				return reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED, row.InstallError
			}
		}
	}
	return reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, ""
}
