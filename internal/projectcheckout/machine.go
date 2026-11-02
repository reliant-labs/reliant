// Copyright (c) 2025 Reliant Labs
package projectcheckout

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// On returns the project's checkout on the named machine — the one a chat or
// workspace was already placed on, so the path a command carries is true
// where it runs. A machine that is not the user's, or that has no installed
// checkout, is NotOnError's answer: which machines DO have it, and how to
// recover (switch to one of them, or clone it onto this one).
func On(ctx context.Context, r Reader, userID string, project *db.Project, daemonID, purpose string) (Target, error) {
	rows, err := r.ListProjectDaemonsForProject(ctx, project.ID)
	if err != nil {
		return Target{}, svcerr.WithCause(svcerr.Internal("failed to read the project's checkouts"), err)
	}
	t := Target{Project: project, DaemonID: daemonID, roots: installedRoots(rows)}
	var mine *core.ProjectDaemon
	var elsewhere []string
	for _, row := range rows {
		switch {
		case row == nil:
		case row.DaemonID == daemonID:
			mine = row
		case row.InstallState == core.ProjectInstallInstalled && ownedBy(ctx, r, row.DaemonID, userID):
			elsewhere = append(elsewhere, row.DaemonID)
		}
	}
	if mine != nil && mine.InstallState == core.ProjectInstallInstalled && strings.TrimSpace(mine.Path) != "" && ownedBy(ctx, r, daemonID, userID) {
		t.Path = mine.Path
		return t, nil
	}
	return Target{}, NotOnError(ctx, r, project, daemonID, mine, elsewhere, purpose)
}

// ForMachine is On for a named machine and Resolve for "" (no machine chosen
// yet: the one the project's work is placed on).
func ForMachine(ctx context.Context, r Reader, machines daemonplacement.Machines, userID string, project *db.Project, daemonID, purpose string) (Target, error) {
	if daemonID == "" {
		return Resolve(ctx, r, machines, userID, project, purpose)
	}
	return On(ctx, r, userID, project, daemonID, purpose)
}

// Rebase moves path onto this target's checkout when it lies in one of the
// project's checkouts — a run path computed for the machine a chat ran on
// before it moved, or a node's working directory under the checkout — keeping
// it relative to the checkout it was in. "" is the checkout itself. A path in
// none of them (a branch worktree, a directory outside the project) is
// returned as it is.
//
// The project's creation path counts as one of them for RECOGNITION only: a
// run started before checkouts were resolved per machine carries it as its
// path, and it is moved onto this machine's checkout like any other. It is
// never the answer; the result is always under t.Path.
//
// A branch worktree is never rebased, even when it lies under a recognised
// root: a project opened at the home directory has every Reliant worktree
// (~/.reliant/worktrees/<id>) under its creation path, and moving one into
// the main checkout would run a branch chat on the main checkout.
func (t Target) Rebase(path string) string {
	if path == "" {
		return t.Path
	}
	clean := filepath.Clean(path)
	best := ""
	roots := append([]string{t.Path}, t.roots...)
	if t.Project != nil {
		roots = append(roots, t.Project.Path)
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		root = filepath.Clean(root)
		if len(root) <= len(best) {
			continue
		}
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			best = root
		}
	}
	if best == "" {
		return path
	}
	rel, err := filepath.Rel(best, clean)
	if err != nil || inWorktreesDir(rel) {
		return path
	}
	return t.Join(rel)
}

// inWorktreesDir reports whether rel, a path relative to a checkout root,
// enters a .reliant/worktrees directory: where the daemon makes branch
// worktrees (<HOME>/.reliant/worktrees/<id>), never part of a checkout.
func inWorktreesDir(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == ".reliant" && parts[i+1] == "worktrees" {
			return true
		}
	}
	return false
}

// notOnMachine is NotOnError's cause: errors.Is(err, ErrNoCheckout) holds, so
// IsMissing reads it like the no-checkout-anywhere answer.
type notOnMachine struct{ project, daemon string }

func (e notOnMachine) Error() string {
	return fmt.Sprintf("project %s is not checked out on machine %s", e.project, e.daemon)
}

func (notOnMachine) Is(target error) bool { return target == daemonplacement.ErrNoCheckout }

// NotOnError is the answer when the machine a chat or workspace is on has no
// checkout of the project: FailedPrecondition, the ProjectCheckoutMissing
// detail for THAT machine, and a sentence with the way out — the machines
// that do have it (switch to one), cloning it onto this one, or connecting
// the machine whose disk holds a plain folder.
func NotOnError(ctx context.Context, names daemonplacement.DaemonReader, project *db.Project, daemonID string, row *core.ProjectDaemon, elsewhere []string, purpose string) error {
	machine := daemonplacement.MachineName(ctx, names, daemonID)
	name := strconv.Quote(project.Name)
	detail := &reliantv1.ProjectCheckoutMissing{
		ProjectId: project.ID,
		DaemonId:  daemonID,
		State:     reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT,
		RemoteUrl: remoteURL(project),
	}
	if row != nil {
		detail.Path = row.Path
	}
	var msg string
	switch {
	case row != nil && row.InstallState == core.ProjectInstallInstalling:
		detail.State = reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING
		msg = fmt.Sprintf("%s is still being cloned onto %s; try again once the clone finishes", name, machine)
	case row != nil && row.InstallState == core.ProjectInstallFailed:
		detail.State = reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED
		detail.InstallError = row.InstallError
		msg = fmt.Sprintf("cloning %s onto %s failed", name, machine)
		if reason := strings.TrimSpace(row.InstallError); reason != "" {
			msg += " (" + reason + ")"
		}
	default:
		msg = fmt.Sprintf("%s isn't checked out on %s", name, machine)
	}
	var ways []string
	if len(elsewhere) > 0 {
		others := make([]string, 0, len(elsewhere))
		for _, id := range elsewhere {
			others = append(others, daemonplacement.MachineName(ctx, names, id))
		}
		ways = append(ways, fmt.Sprintf("switch to %s, which has it", strings.Join(others, " or ")))
	}
	switch {
	case detail.State == reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING:
	case detail.RemoteUrl != "":
		ways = append(ways, fmt.Sprintf("clone it onto %s", machine))
	case len(elsewhere) == 0:
		ways = append(ways, "connect the machine that holds its folder")
	}
	if len(ways) > 0 {
		msg += ": " + strings.Join(ways, ", or ") + " to " + purpose
	}
	return svcerr.WithDetail(svcerr.WithCause(svcerr.FailedPrecondition(msg), notOnMachine{project.ID, daemonID}), detail)
}

// ownedBy reports whether daemonID is a machine of userID's.
func ownedBy(ctx context.Context, r Reader, daemonID, userID string) bool {
	d, err := r.GetDaemon(ctx, daemonID)
	return err == nil && d != nil && d.UserID == userID
}

// installedRoots is every installed checkout path among rows, for Rebase.
func installedRoots(rows []*core.ProjectDaemon) []string {
	var roots []string
	for _, row := range rows {
		if row != nil && row.InstallState == core.ProjectInstallInstalled && strings.TrimSpace(row.Path) != "" {
			roots = append(roots, row.Path)
		}
	}
	return roots
}
