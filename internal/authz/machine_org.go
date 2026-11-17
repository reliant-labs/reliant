package authz

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A MACHINE HOLDS ITS ORG'S PROJECTS ONLY.
//
// A machine belongs to one organization (control-plane's daemons.org_id,
// mirrored onto core.Daemon.OrgID when it connects). Its project checkouts —
// what the connect reconcile records, what an attach or a create records on
// it — are projects of that org. The same folder on two machines of two orgs
// is two projects, one per org (projects are unique per (org, user, path)).
//
// A machine whose org reliant has not been told (an older daemon credential
// or control plane, a machine that only ever connected with an unbound login
// token) keeps the behaviour of before: it matches its owner's projects of
// every org. Callers log that, so an unscoped machine is visible.

// ErrDefaultOrgUnavailable is OrgScopeFor's answer when the scope depends on
// the user's default org and it could not be read (an outage, or a process
// with no membership wired). The caller fails closed on it: it records
// nothing, and a create refuses rather than risk a duplicate.
var ErrDefaultOrgUnavailable = errors.New("authz: could not read the default organization")

// OrgScopeFor is the scope of userID's rows in org: the rows of org, and,
// when org is userID's default org, the rows with no org yet (they are its,
// lazily: internal/orgs ListScope stamps them). nil when org is "" — no org
// to keep to, every org's rows, as before orgs.
//
// The default org is the process's (or ctx's) membership's answer, cached
// like every membership answer. A deployment with no orgs names none, so an
// unassigned row is never in a named org's scope there; such a deployment
// never names an org either.
func OrgScopeFor(ctx context.Context, userID, org string) (*core.OrgScope, error) {
	if org == "" {
		return nil, nil
	}
	defaults, ok := membershipOf(ctx).(defaultOrgs)
	if !ok {
		return nil, ErrDefaultOrgUnavailable
	}
	def, err := defaults.DefaultOrg(ctx, userID)
	if err != nil {
		return nil, errors.Join(ErrDefaultOrgUnavailable, err)
	}
	return &core.OrgScope{OrgID: org, IncludeUnassigned: def == org}, nil
}

// MachineProjectScope is the scope of the projects machine may hold:
// OrgScopeFor its owner and its org. nil — every org's projects — when the
// machine's org is not known (MachineOrg).
func MachineProjectScope(ctx context.Context, machine *core.Daemon) (*core.OrgScope, error) {
	if machine == nil {
		return nil, nil
	}
	return OrgScopeFor(ctx, machine.UserID, MachineOrg(machine))
}

// MachineOrg is the org machine belongs to, "" when it is not known.
func MachineOrg(machine *core.Daemon) string {
	if machine == nil || machine.OrgID == nil {
		return ""
	}
	return *machine.OrgID
}
