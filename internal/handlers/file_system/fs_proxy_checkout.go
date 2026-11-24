package file_system

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/checkoutstate"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// isMissingPathError reports whether a daemon fs command failed because the
// path it was asked about does not exist. The daemon reports errors as text
// across the wire, so the text is what there is to match; the markers (and the
// fork/exec exclusion: a program the daemon could not start is its install,
// not the user's files) live with the router's daemon-reported failure, which
// classifies the same text as a user error (toolexec.IsMissingPathText).
func isMissingPathError(err error) bool {
	return err != nil && toolexec.IsMissingPathText(err.Error())
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
//
// err is sendCommand's failure, already classified: a path the daemon could
// not find is its NotFound (fsProxyDaemonError).
func (s *Service) checkoutMissingError(
	ctx context.Context, userID, projectID string, worktreeID *string, scope workspaceScope, resolvedPath string, err error,
) error {
	if filepath.Clean(resolvedPath) != filepath.Clean(scope.basePath) || !svcerr.IsNotFound(err) {
		return nil
	}
	// The client names the main worktree when the user is on the project's
	// own checkout, which is still the clone this describes. Only a real
	// worktree is out of scope.
	if worktreeID != nil && *worktreeID != "" {
		wt, werr := s.deps.Placement.GetWorktree(ctx, *worktreeID)
		if werr != nil || wt == nil || !wt.IsMain {
			return nil
		}
	}

	detail := &reliantv1.ProjectCheckoutMissing{
		ProjectId: projectID,
		DaemonId:  scope.target.DaemonID,
		Path:      scope.basePath,
		State:     reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT,
	}
	if detail.DaemonId == "" {
		// resolveScope pins every request whose main checkout resolved, so
		// this is a request no machine was named for. Describe the machine
		// the project's work is placed on — among those that have it — not
		// project-blind default resolution's, which may be one without it.
		detail.DaemonId = s.projectMachine(ctx, userID, projectID)
	}
	if project, perr := s.deps.Placement.GetProject(ctx, projectID); perr == nil && project != nil && project.RemoteURL != nil {
		detail.RemoteUrl = strings.TrimSpace(*project.RemoteURL)
	}
	if rows, lerr := s.deps.Placement.ListProjectDaemonsForProject(ctx, projectID); lerr != nil {
		logging.WarnContext(ctx, "[FSProxy] Could not read clone records for a missing project directory",
			"error", lerr, "projectID", projectID)
	} else {
		detail.State, detail.InstallError = checkoutstate.On(rows, detail.DaemonId)
		detail.AvailableDaemonIds = projectcheckout.InstalledElsewhere(ctx, s.deps.Placement, userID, rows, detail.DaemonId)
	}

	missing := svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("project directory %s does not exist on this machine", scope.basePath))
	return svcerr.WithDetail(svcerr.WithCause(missing, err), detail)
}

// projectMachine is the machine a project's work goes to when nothing names
// one (daemonplacement.ProjectDefaultDaemonID: the default when it has the
// project, else another of the user's machines that does), or "" when it
// cannot be told.
func (s *Service) projectMachine(ctx context.Context, userID, projectID string) string {
	if machines, ok := s.deps.Router.(daemonplacement.Machines); ok {
		if id, err := daemonplacement.ProjectDefaultDaemonID(ctx, s.deps.Placement, machines, userID, projectID); err == nil {
			return id
		}
		return ""
	}
	if s.deps.Router == nil {
		return ""
	}
	id, _ := s.deps.Router.ResolveDaemonID(ctx, userID)
	return id
}
