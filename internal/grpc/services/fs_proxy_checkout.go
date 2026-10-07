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
// not exist". The daemon reports errors as text across the wire, so the text is
// what there is to match: Go's ENOENT on Unix, and the two Win32 messages for a
// missing file and a missing directory.
var missingDirMarkers = []string{
	"no such file or directory",
	"cannot find the file specified",
	"cannot find the path specified",
}

func isMissingPathError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
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
// Only the ROOT of the MAIN checkout qualifies. A missing subdirectory is an
// ordinary race with the user's own edits (the tree already handles it), and a
// worktree's directory is not described by the clone record this consults.
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
	if worktreeID != nil && *worktreeID != "" {
		return nil
	}
	if filepath.Clean(resolvedPath) != filepath.Clean(scope.basePath) || !isMissingPathError(err) {
		return nil
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
