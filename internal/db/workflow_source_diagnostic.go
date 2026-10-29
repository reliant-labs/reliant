package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// WorkflowSourceDiagnostic says where a project workflow that failed to
// resolve lives, read only after resolution misses. daemonID is the machine
// the resolution was for ("" for the project's union of every machine):
//
//	machine "cicd" has no workflow "release"; it is defined on machine "default" at control-plane/.reliant/workflows/release.yaml
//
// When no machine carries it, it names the file and machine it was last
// synced from, which each machine's catalog retains when a file leaves it.
func (r *Repo) WorkflowSourceDiagnostic(ctx context.Context, projectID, daemonID, ref string) (string, error) {
	if projectID == "" {
		return "", nil
	}
	catalogs, err := r.projectMachineCatalogs(ctx, projectID)
	if err != nil {
		return "", err
	}
	if len(catalogs) == 0 {
		return "no machine has synced this project's workflows yet; connect a machine that has the project checked out", nil
	}
	slug := workflowref.ProjectSlug(ref)
	names := r.machineNames(ctx, catalogs)

	var providers []string
	for _, c := range catalogs {
		if c.DaemonID == daemonID {
			continue
		}
		for _, w := range c.Workflows {
			if storedWorkflowAnswers(w, slug) {
				providers = append(providers, fmt.Sprintf("machine %q at %s", names[c.DaemonID], w.RelativePath))
				break
			}
		}
	}
	sort.Strings(providers)

	removals, err := r.projectMachineRemovals(ctx, projectID)
	if err != nil {
		return "", err
	}
	removedFrom := lastRemoval(removals, names, slug)

	var b strings.Builder
	if daemonID != "" {
		fmt.Fprintf(&b, "machine %q has no workflow %q", r.machineName(ctx, names, daemonID), slug)
	} else {
		fmt.Fprintf(&b, "no machine provides a workflow %q", slug)
	}
	switch {
	case len(providers) > 0:
		fmt.Fprintf(&b, "; it is defined on %s", strings.Join(providers, " and on "))
		if daemonID != "" {
			b.WriteString("; check that repository out on this machine, or point the trigger at a machine that has it")
		}
	case removedFrom != "":
		fmt.Fprintf(&b, "; it was last synced from %s, which no machine provides any more", removedFrom)
	default:
		b.WriteString("; no machine has ever synced a workflow by that name for this project")
	}
	return b.String(), nil
}

// storedWorkflowAnswers reports whether a synced workflow is the one a
// project ref's slug names: by its name:, bare or qualified by its repo.
func storedWorkflowAnswers(w config.StoredWorkflow, slug string) bool {
	repo, _, _ := workflowref.SplitRelativePath(w.RelativePath)
	return workflowref.Slug(w.Name) == slug || (repo != "" && workflowref.QualifiedSlug(repo+"/"+w.Name) == slug)
}

// lastRemoval is the most recent "<path> on machine <name> (removed <when>)"
// among the machines' removal histories for a workflow named slug.
func lastRemoval(removals map[string][]config.RemovedCatalogFile, names map[string]string, slug string) string {
	var best *config.RemovedCatalogFile
	bestMachine := ""
	for daemonID, files := range removals {
		for i := range files {
			f := &files[i]
			if f.Kind != config.CatalogKindWorkflow {
				continue
			}
			if !storedWorkflowAnswers(config.StoredWorkflow{Name: f.Name, RelativePath: f.RelativePath}, slug) {
				continue
			}
			if best == nil || f.RemovedAt.After(best.RemovedAt) {
				best, bestMachine = f, daemonID
			}
		}
	}
	if best == nil {
		return ""
	}
	name := names[bestMachine]
	if name == "" {
		name = bestMachine
	}
	return fmt.Sprintf("%s on machine %q (removed %s)", best.RelativePath, name, best.RemovedAt.UTC().Format("2006-01-02 15:04 UTC"))
}

// machineNames is each catalog's machine's display name.
func (r *Repo) machineNames(ctx context.Context, catalogs []config.MachineCatalog) map[string]string {
	names := make(map[string]string, len(catalogs))
	for _, c := range catalogs {
		names[c.DaemonID] = r.machineName(ctx, names, c.DaemonID)
	}
	return names
}

// machineName is a machine's name, then hostname, then id. A diagnostic is
// best effort: a failed read names the machine by id rather than failing.
func (r *Repo) machineName(ctx context.Context, known map[string]string, daemonID string) string {
	if n := known[daemonID]; n != "" {
		return n
	}
	daemon, err := r.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil {
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !svcerr.IsNotFound(err) {
			return daemonID
		}
		return daemonID + " (removed)"
	}
	if daemon.Name != "" {
		return daemon.Name
	}
	if daemon.Hostname != nil && *daemon.Hostname != "" {
		return *daemon.Hostname
	}
	return daemonID
}
