package project

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitremote"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// A REQUEST THAT NAMES A MACHINE RUNS ON THAT MACHINE AND NOWHERE ELSE.
//
// CreateProject (with daemon_id) and AttachProjectToDaemon act on a folder the
// user picked on one of their machines. Every command they send is pinned to
// it — a path is true only on the disk it was read from — and the machine must
// answer: the user just browsed it, so an offline machine is reported, never
// worked around by queueing for whichever machine connects next.

// ownedMachine returns the caller's machine daemonID. Another user's machine
// is NotFound, the same as a missing one, so the answer never reveals that it
// exists.
func (s *Service) ownedMachine(ctx context.Context, userID, daemonID string) (*db.Daemon, error) {
	d, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	if err != nil || !authz.CanWriteMachine(ctx, userID, d) {
		return nil, svcerr.NotFound("machine")
	}
	return d, nil
}

// machineName is how a sentence names a machine (daemonplacement.MachineName).
func (s *Service) machineName(ctx context.Context, daemonID string) string {
	return daemonplacement.MachineName(ctx, s.deps.Repo, daemonID)
}

// requireFolder asks the machine whether path is a directory on its disk, and
// refuses — in a sentence naming the machine — when it is missing or is a
// file. A machine that does not answer is daemonerr's classification
// (offline is Unavailable, saying so).
func (s *Service) requireFolder(ctx context.Context, userID, daemonID, path string) error {
	var stat struct {
		Exists bool `json:"exists"`
		IsDir  bool `json:"is_dir"`
	}
	if err := s.sendToDaemon(ctx, userID, daemonID, "fs.stat", map[string]string{"path": path}, &stat); err != nil {
		return daemonerr.Error("check the folder on "+s.machineName(ctx, daemonID), err)
	}
	switch {
	case !stat.Exists:
		return svcerr.FailedPrecondition(fmt.Sprintf("%s does not exist on %s", path, s.machineName(ctx, daemonID)))
	case !stat.IsDir:
		return svcerr.FailedPrecondition(fmt.Sprintf("%s on %s is a file, not a folder", path, s.machineName(ctx, daemonID)))
	}
	return nil
}

// discoveredRepo is one git repository repo.discover found under a path.
type discoveredRepo struct {
	RelativePath string `json:"relative_path"`
	Name         string `json:"name"`
	RemoteURL    string `json:"remote_url,omitempty"`
}

// rootRepo is the repository at the root of discovered (relative path "" or
// "."), if any.
func rootRepo(discovered []discoveredRepo) (discoveredRepo, bool) {
	for _, found := range discovered {
		if rel := strings.TrimSpace(found.RelativePath); rel == "" || rel == "." {
			return found, true
		}
	}
	return discoveredRepo{}, false
}

// discoverOn runs repo.discover for path on one machine, one level deep: the
// root repository and its remote are all an attach reads.
func (s *Service) discoverOn(ctx context.Context, userID, daemonID, path string) ([]discoveredRepo, error) {
	var resp struct {
		Discovered []discoveredRepo `json:"discovered"`
	}
	if err := s.sendToDaemon(ctx, userID, daemonID, "repo.discover", map[string]any{"path": path, "max_depth": 1}, &resp); err != nil {
		return nil, err
	}
	return resp.Discovered, nil
}

// checkoutWarnings says, in sentences for the user, how the folder at path
// differs from a checkout of project: not a git repository, or one of another
// remote. Neither stops an attach — a plain folder of a git project is the
// user's call — and a project with no remote has nothing to compare. A
// discovery that fails is logged and warns nothing: the folder exists
// (requireFolder), and git is not why it is being attached.
func (s *Service) checkoutWarnings(ctx context.Context, userID, daemonID string, project *db.Project, path string) []string {
	want := ""
	if project.RemoteURL != nil {
		want = strings.TrimSpace(*project.RemoteURL)
	}
	if want == "" {
		return nil
	}
	discovered, err := s.discoverOn(ctx, userID, daemonID, path)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the attached folder's git remote; attaching without the check",
			"error", err, "projectID", project.ID, "daemonID", daemonID, "path", path)
		return nil
	}
	root, ok := rootRepo(discovered)
	switch {
	case !ok:
		return []string{fmt.Sprintf("%s is not a git repository; %q is cloned from %s", path, project.Name, gitremote.Redacted(want))}
	case strings.TrimSpace(root.RemoteURL) == "":
		return []string{fmt.Sprintf("%s has no git remote; %q is cloned from %s", path, project.Name, gitremote.Redacted(want))}
	case !sameRemote(root.RemoteURL, want):
		return []string{fmt.Sprintf("%s is a checkout of %s, not of %s", path, gitremote.Redacted(root.RemoteURL), gitremote.Redacted(want))}
	}
	return nil
}

// sameRemote reports whether two git remote URLs name the same repository:
// credentials, a trailing ".git" or "/", the host's case, and the scp-like
// SSH form (git@host:owner/repo) do not make them different.
func sameRemote(a, b string) bool {
	return canonicalRemote(a) == canonicalRemote(b)
}

func canonicalRemote(raw string) string {
	u := strings.TrimSpace(gitremote.StripCredentials(raw))
	// scp-like: [user@]host:path, with no scheme.
	if !strings.Contains(u, "://") {
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		if colon := strings.Index(u, ":"); colon > 0 {
			u = u[:colon] + "/" + u[colon+1:]
		}
	} else {
		u = u[strings.Index(u, "://")+3:]
		if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
			u = u[at+1:]
		}
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	host, rest, _ := strings.Cut(u, "/")
	return strings.ToLower(host) + "/" + rest
}

// recordCheckout records the folder at path as project's checkout on the
// machine — installed and verified now, any detachment of the pair cleared —
// then learns what a fresh checkout teaches (its forge name) and has the
// machine sync the project's config from it. Only the row is the RPC's
// failure; the rest is best effort, and the machine's next connect redoes it.
func (s *Service) recordCheckout(ctx context.Context, userID, daemonID string, project *db.Project, path string, defaultBranch *string) error {
	if err := s.deps.Repo.UpsertProjectDaemon(ctx, project.ID, daemonID, path, defaultBranch); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to record the project on the machine"))
	}
	s.syncConfigOn(ctx, userID, daemonID, project.ID, path)
	return nil
}

// syncConfigOn asks the machine to load and watch the project's config at
// path, its checkout there, when the router can name a machine
// (MachineConfigSync). Best effort: the machine's next connect asks again.
func (s *Service) syncConfigOn(ctx context.Context, userID, daemonID, projectID, path string) {
	sync, ok := s.deps.Router.(MachineConfigSync)
	if !ok {
		return
	}
	if err := sync.SendLoadProjectConfigsToDaemon(ctx, userID, daemonID, path, uuid.NewString()); err != nil {
		logging.WarnContext(ctx, "Failed to request the machine's config load for a project checkout",
			"error", err, "projectID", projectID, "daemonID", daemonID, "path", path)
	}
	if err := sync.SendWatchProjectConfigsToDaemon(ctx, userID, daemonID, path, true); err != nil {
		logging.WarnContext(ctx, "Failed to request the machine's config watch for a project checkout",
			"error", err, "projectID", projectID, "daemonID", daemonID, "path", path)
	}
}

// checkoutOn returns the project's row on the machine, nil when it has none.
func (s *Service) checkoutOn(ctx context.Context, projectID, daemonID string) (*core.ProjectDaemon, error) {
	rows, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the project's machines"))
	}
	for _, row := range rows {
		if row != nil && row.DaemonID == daemonID {
			return row, nil
		}
	}
	return nil, nil
}

// projectAtPathOn returns the caller's project whose checkout on the machine
// is at path, nil when none is.
func (s *Service) projectAtPathOn(ctx context.Context, userID, daemonID, path string) (*db.Project, error) {
	rows, err := s.deps.Repo.ListProjectDaemonsForDaemon(ctx, daemonID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the machine's projects"))
	}
	want := ospath.Clean(path)
	for _, row := range rows {
		if row == nil || ospath.Clean(row.Path) != want {
			continue
		}
		if p, err := s.deps.Repo.GetProjectWithUserCheck(ctx, row.ProjectID, userID); err == nil && p != nil {
			return p, nil
		}
	}
	return nil, nil
}

// checkoutProto is a row as the wire carries it, its machine named.
func (s *Service) checkoutProto(ctx context.Context, row *core.ProjectDaemon) *reliantv1.ProjectDaemon {
	return s.newMachineNames().proto(ctx, row)
}

// justRecorded is the row recordCheckout wrote, for a response that should
// not read it back.
func justRecorded(projectID, daemonID, path string, defaultBranch *string) *core.ProjectDaemon {
	now := time.Now().UTC()
	return &core.ProjectDaemon{
		ProjectID:     projectID,
		DaemonID:      daemonID,
		Path:          path,
		DefaultBranch: defaultBranch,
		ClonedAt:      now,
		InstallState:  core.ProjectInstallInstalled,
		VerifiedAt:    &now,
	}
}
