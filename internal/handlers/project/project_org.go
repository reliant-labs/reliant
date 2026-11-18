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

// PROJECTS ARE KEYED BY (ORG, USER, PATH) AND (ORG, USER, REPOSITORY).
//
// The same folder or repository may be a project in two of a person's orgs
// (once the old per-user keys are dropped, a later release). So every "is
// this already a project?" question a create or an attach asks is asked in
// one org: the request's (orgScope), or, when the request has none, the
// machine's (machineScope). A machine holds only its own org's projects —
// its owner's default org's when its org is not known
// (authz.MachineScopeFor): an attach or a create across orgs is refused
// (requireMachineInOrg, requireMachineHolds), never recorded.

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

// machineScope is the scope of the projects machine (of userID's; nil, no
// registry row, is a machine of no known org) may hold: its org's, or its
// owner's default org's when its org is not known, which is logged
// (authz.MachineScopeFor). nil only in a deployment with no orgs.
func (s *Service) machineScope(ctx context.Context, userID string, machine *db.Daemon) (*core.OrgScope, error) {
	scope, known, err := authz.MachineProjectScope(ctx, userID, machine)
	if err != nil {
		return nil, orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
	}
	if !known && scope != nil {
		id := ""
		if machine != nil {
			id = machine.ID
		}
		logging.InfoContext(ctx, "[project] machine has no known organization; it holds only its owner's default organization's projects",
			"daemonID", id, "defaultOrgID", scope.OrgID)
	}
	return scope, nil
}

// machineOrgSentence names the org a machine holds projects of, for a
// refusal: its own, or its owner's default when its own is not known.
func machineOrgSentence(machine *db.Daemon, scope *core.OrgScope) string {
	if org := authz.MachineOrg(machine); org != "" {
		return "belongs to organization " + org
	}
	return "has no organization recorded, so it holds only your default organization's (" + scope.OrgID + ") projects"
}

// requireMachineInOrg refuses a request acting in org that names (or
// resolved) a machine holding another org's projects: what it would record
// there is a checkout across orgs. A request acting in no org, or a
// deployment with no orgs, refuses nothing.
func (s *Service) requireMachineInOrg(ctx context.Context, userID string, machine *db.Daemon, org *string) error {
	if org == nil || *org == "" {
		return nil
	}
	scope, err := s.machineScope(ctx, userID, machine)
	if err != nil || scope == nil || scope.OrgID == *org {
		return err
	}
	name := "the machine"
	if machine != nil {
		name = s.machineName(ctx, machine.ID)
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%s %s, not organization %s: a machine holds only its organization's projects; pick a machine of organization %s",
		name, machineOrgSentence(machine, scope), *org, *org))
}

// requireMachineHolds refuses recording project on machine when the machine
// holds another org's projects.
func (s *Service) requireMachineHolds(ctx context.Context, userID string, machine *db.Daemon, project *db.Project) error {
	scope, err := s.machineScope(ctx, userID, machine)
	if err != nil || scope.Includes(project.OrgID) {
		return err
	}
	in := "no organization"
	if project.OrgID != nil {
		in = "organization " + *project.OrgID
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%q is in %s and %s %s: a machine holds only its organization's projects",
		project.Name, in, s.machineName(ctx, machine.ID), machineOrgSentence(machine, scope)))
}

// inOrg is how a sentence names the org a found project is in: " in
// organization <id>", or nothing for a project with no org.
func inOrg(p *db.Project) string {
	if p == nil || p.OrgID == nil || *p.OrgID == "" {
		return ""
	}
	return " in organization " + *p.OrgID
}
