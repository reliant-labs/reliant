package project

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// nestedRepoIDs is the project's repositories below its root that a set-up
// on a machine clones after the root: each one with a remote. One with no
// remote cannot be cloned anywhere (PreviewProjectMachineMove lists it as
// staying behind). Best-effort: a failed read clones the root alone, as
// before nested repositories were cloned.
func (s *Service) nestedRepoIDs(ctx context.Context, projectID string) []string {
	repos, err := s.deps.Repo.ListReposByProject(ctx, projectID)
	if err != nil {
		logging.WarnContext(ctx, "CreateProjectFromRepo: could not list the project's repositories; cloning the root alone",
			"error", err, "project_id", projectID)
		return nil
	}
	var ids []string
	for _, r := range repos {
		if r == nil || isRootRepo(r) || r.RemoteURL == nil || strings.TrimSpace(*r.RemoteURL) == "" {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids
}

// isRootRepo reports whether the repo is the project's root checkout.
func isRootRepo(r *core.Repo) bool {
	rel := strings.Trim(strings.TrimSpace(r.RelativePath), "/")
	return rel == "" || rel == "."
}

// defaultClonePath is where a clone that names no path lands on machine.
//
// A managed machine (and one this server knows nothing of) keeps its
// projects in /home/workspace/projects. Any other machine reports the
// directory its daemon runs in (daemons.working_dir), which is where its
// owner keeps projects: the clone goes beside them, not into a
// /home/workspace that a laptop or a build host does not have. A working
// directory that is itself a checkout on the machine (the daemon was started
// inside a project) is not a projects directory: its parent is.
func (s *Service) defaultClonePath(ctx context.Context, machine *db.Daemon, name string) string {
	if dir := s.machineProjectsDir(ctx, machine); dir != "" {
		return ospath.Join(dir, name)
	}
	return cloudProjectPath(name)
}

func (s *Service) machineProjectsDir(ctx context.Context, machine *db.Daemon) string {
	if machine == nil || machine.WorkingDir == nil {
		return ""
	}
	if machine.DaemonType != nil && *machine.DaemonType == "managed" {
		return ""
	}
	dir := ospath.Clean(strings.TrimSpace(*machine.WorkingDir))
	if !ospath.IsAbs(dir) || isFilesystemRoot(dir) {
		return ""
	}
	rows, err := s.deps.Installs.ListProjectDaemonsForDaemon(ctx, machine.ID)
	if err != nil {
		// Unknown whether it is a checkout: the managed default is wrong on
		// such a machine, so the working directory is still the better guess.
		logging.WarnContext(ctx, "CreateProjectFromRepo: could not read the machine's checkouts", "error", err, "daemon_id", machine.ID)
		return dir
	}
	for _, row := range rows {
		checkout := ospath.Clean(row.Path)
		if checkout != "" && pathWithin(dir, checkout) {
			parent := parentDir(checkout)
			if parent == "" || isFilesystemRoot(parent) {
				return ""
			}
			return parent
		}
	}
	return dir
}

// pathWithin reports whether child is dir or inside it. Both are cleaned.
func pathWithin(child, dir string) bool {
	if child == dir {
		return true
	}
	sep := "/"
	if strings.Contains(dir, `\`) {
		sep = `\`
	}
	return strings.HasPrefix(child, strings.TrimRight(dir, sep)+sep)
}

// parentDir is p's directory, in p's own convention; "" when p has none.
func parentDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	switch {
	case i < 0:
		return ""
	case i == 0:
		return p[:1]
	case i == 2 && p[1] == ':':
		return p[:3] // C:\
	}
	return p[:i]
}

// isFilesystemRoot reports whether p is "/" or a Windows drive root.
func isFilesystemRoot(p string) bool {
	p = strings.TrimRight(p, `\/`)
	return p == "" || (len(p) == 2 && p[1] == ':')
}
