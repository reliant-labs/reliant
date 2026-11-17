// Copyright (c) 2025 Reliant Labs

package project

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// actingOrg is the organization a project create acts in (orgs.ActingOrg):
// nil when the deployment has none, or none could be resolved for a request
// that named none.
func (s *Service) actingOrg(ctx context.Context, userID, requested string) (*string, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	orgID, err := s.deps.Orgs.ActingOrg(ctx, userID, requested)
	if err != nil || orgID == "" {
		return nil, err
	}
	return &orgID, nil
}

// listScope is the org filter of a project list (orgs.ListScope): nil when
// the request names no org.
func (s *Service) listScope(ctx context.Context, userID, requested string) (*core.OrgScope, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	return s.deps.Orgs.ListScope(ctx, userID, requested, s.deps.Repo.AssignUnassignedOrg)
}

// PROJECTS ARE UNIQUE PER (ORG, USER, PATH) AND (ORG, USER, REPOSITORY).
//
// The same folder or repository may be a project in two of a person's orgs.
// So every "is this already a project?" question a create or an attach asks
// is asked in one org: the request's (orgScope), or, when the request has
// none, the machine's (machineScope). A machine holds only its own org's
// projects (authz.MachineProjectScope): an attach or a pinned create across
// orgs is refused (requireMachineInOrg, requireMachineHolds), never recorded.

// orgScope is the scope of the caller's projects in org (authz.OrgScopeFor):
// nil when org is nil, a request acting in no org (no orgs here, or none
// could be resolved), which keeps the check of before orgs: every org's.
func (s *Service) orgScope(ctx context.Context, userID string, org *string) (*core.OrgScope, error) {
	if org == nil || *org == "" {
		return nil, nil
	}
	scope, err := authz.OrgScopeFor(ctx, userID, *org)
	if err != nil {
		return nil, orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
	}
	return scope, nil
}

// machineScope is the scope of the projects machine may hold
// (authz.MachineProjectScope): nil, every org's, when its org is not known,
// which is logged.
func (s *Service) machineScope(ctx context.Context, machine *db.Daemon) (*core.OrgScope, error) {
	if machine == nil {
		return nil, nil
	}
	if authz.MachineOrg(machine) == "" {
		logging.InfoContext(ctx, "[project] machine has no known organization; matching the owner's projects of every organization, as before machines had orgs",
			"daemonID", machine.ID)
		return nil, nil
	}
	scope, err := authz.MachineProjectScope(ctx, machine)
	if err != nil {
		return nil, orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
	}
	return scope, nil
}

// requireMachineInOrg refuses a request acting in org that names a machine of
// another org: what it would record there is a checkout across orgs. Either
// org unknown refuses nothing.
func (s *Service) requireMachineInOrg(ctx context.Context, machine *db.Daemon, org *string) error {
	machineOrg := authz.MachineOrg(machine)
	if machineOrg == "" || org == nil || *org == "" || machineOrg == *org {
		return nil
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%s belongs to organization %s, not %s: a machine holds only its organization's projects; pick a machine of organization %s",
		s.machineName(ctx, machine.ID), machineOrg, *org, *org))
}

// requireMachineHolds refuses recording project on machine when the machine
// belongs to an org the project is not in.
func (s *Service) requireMachineHolds(ctx context.Context, machine *db.Daemon, project *db.Project) error {
	scope, err := s.machineScope(ctx, machine)
	if err != nil || scope.Includes(project.OrgID) {
		return err
	}
	in := "no organization"
	if project.OrgID != nil {
		in = "organization " + *project.OrgID
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%q is in %s and %s belongs to organization %s: a machine holds only its organization's projects",
		project.Name, in, s.machineName(ctx, machine.ID), authz.MachineOrg(machine)))
}

// inOrg is how a sentence names the org a found project is in: " in
// organization <id>", or nothing for a project with no org.
func inOrg(p *db.Project) string {
	if p == nil || p.OrgID == nil || *p.OrgID == "" {
		return ""
	}
	return " in organization " + *p.OrgID
}
