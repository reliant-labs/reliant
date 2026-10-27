package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// WorkflowSourceDiagnostic is read only after workflow resolution fails. It
// reads the small workflow column, never the whole config/skill payload.
func (r *Repo) WorkflowSourceDiagnostic(ctx context.Context, projectID, ref string) (string, error) {
	if projectID == "" {
		return "", nil
	}
	value, legacyID, err := store.GetProjectWorkflowSnapshot(ctx, r.ORM(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "no project workflow snapshot has been synced; connect the project's machine", nil
	}
	if err != nil {
		return "", err
	}
	workflows, err := config.ParseStoredWorkflows(value)
	if err != nil {
		return "", err
	}
	var relative string
	slug := workflowref.ProjectSlug(ref)
	for _, w := range workflows {
		repo, _, _ := workflowref.SplitRelativePath(w.RelativePath)
		if workflowref.Slug(w.Name) == slug || workflowref.QualifiedSlug(repo+"/"+w.Name) == slug {
			relative = w.RelativePath
			break
		}
	}
	p, e := r.GetProject(ctx, projectID)
	if e != nil {
		return "", e
	}
	name, state := legacyID, "offline"
	daemon, e := r.GetDaemon(ctx, legacyID)
	switch {
	case (e != nil && (errors.Is(e, sql.ErrNoRows) || svcerr.IsNotFound(e))) || (e == nil && daemon == nil):
		state = "removed"
	case e != nil:
		return "", e
	case daemon != nil:
		if daemon.Name != "" {
			name = daemon.Name
		} else if daemon.Hostname != nil {
			name = *daemon.Hostname
		}
		online, e := r.IsDaemonIDAttached(ctx, legacyID, 90*time.Second)
		if e != nil {
			return "", e
		}
		if online {
			state = "online"
		}
	}
	if name == "" {
		name = legacyID
	}
	return workflowSnapshotDiagnostic(name, legacyID, state, p.Path, relative), nil
}

// Do not invent a source filename for a missing definition: workflow names
// need not match filenames. The current schema only remembers the last
// PROJECT snapshot source, not the erased workflow's previous provenance.
func workflowSnapshotDiagnostic(name, daemonID, state, projectPath, relative string) string {
	where := path.Join(projectPath, workflowref.Dir) + " (searched along with each nested repository's .reliant/workflows; missing definition's original path is not retained)"
	if relative != "" {
		where = path.Join(projectPath, relative)
	}
	return fmt.Sprintf("last project snapshot: machine %q (%s, %s), path %s; a snapshot from another machine can replace the workflow catalog, and unchanged files are not re-sent; reconnect the source machine or save the committed workflow again to sync it", name, daemonID, state, where)
}
