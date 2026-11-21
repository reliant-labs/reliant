package authz

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A MACHINE HOLDS ITS ORG'S PROJECTS ONLY.
//
// A machine belongs to one organization (control-plane's daemons.org_id,
// mirrored onto core.Daemon.OrgID when it connects). Its project checkouts —
// what the connect reconcile records, what an attach or a create records on
// it — are projects of that org.
//
// A machine whose org reliant has not been told (an older daemon credential
// or control plane, a machine that only ever connected with an unbound login
// token) is held to its OWNER'S DEFAULT org, never to every org: before orgs
// every machine was its owner's, the default org is where a person's
// pre-org rows land (the lazy backfill), and a managed machine's credential
// is minted in it. So the unknown case can never record or attach another
// org's project; at worst it leaves a non-default org's project unrecorded on
// a machine of that org until control-plane names the machine's org. Only a
// KNOWN org is authoritative enough to delete anything (the reconcile's
// cleanup); the default-org fallback only narrows what is matched.

// ErrDefaultOrgUnavailable is OrgScopeFor's answer when the scope depends on
// the user's default org and it could not be read (an outage, or a process
// with no membership wired). The caller fails closed on it: it records
// nothing, and a create refuses rather than risk a duplicate.
var ErrDefaultOrgUnavailable = errors.New("authz: could not read the default organization")

// ErrMachineOwnerNotMember is MachineScopeFor's answer for a machine of a
// known org whose owner is no longer a live member of it (or acts with a
// credential bound to another org): the machine holds none of that org's
// projects, and none of anyone else's.
var ErrMachineOwnerNotMember = errors.New("authz: the machine's owner is not a live member of the machine's organization")

// OrgScopeFor is the scope of userID's rows in org: the rows of org, and,
// when org is userID's default org, the rows with no org yet (they are its,
// lazily: internal/orgs ListScope stamps them). nil when org is "" — no org
// to keep to, every org's rows, as before orgs.
//
// The default org is the process's (or ctx's) membership's answer, cached
// like every membership answer.
func OrgScopeFor(ctx context.Context, userID, org string) (*core.OrgScope, error) {
	if org == "" {
		return nil, nil
	}
	def, err := defaultOrg(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &core.OrgScope{OrgID: org, IncludeUnassigned: def == org}, nil
}

// MachineScopeFor is the scope of the projects a machine of userID's, of
// machineOrg ("" when not known), may hold, and whether that org is known.
//
//   - A known org: OrgScopeFor it; known is true — but only while the
//     machine's owner is a live member of that org, decided by the rule
//     every org resource is held to (deny: the owner, while a live member).
//     A machine stays connected after its owner leaves its org, and nothing
//     it reports may then create, find or record that org's projects: a
//     non-member is ErrMachineOwnerNotMember (wrapping ErrNotOrgMember), an
//     unanswerable membership ErrMembershipUnavailable. Either way the
//     caller acts as for a machine whose org it cannot tell: nothing
//     org-scoped.
//   - An unknown org: the owner's default org's scope (its rows and the
//     unassigned ones); known is false. In a deployment with no orgs (no
//     default org) it is nil, every row, which there carries no org anyway.
//
// An unreadable default org is ErrDefaultOrgUnavailable: the caller records
// nothing.
//
// Membership is asked on every call, never kept from the connect: a caller
// about to write asks it inside the write's transaction.
func MachineScopeFor(ctx context.Context, userID, machineOrg string) (scope *core.OrgScope, known bool, err error) {
	if machineOrg != "" {
		if err := deny(ctx, userID, resource{present: true, ownerUserID: userID, orgID: machineOrg}); err != nil {
			if errors.Is(err, ErrNotOrgMember) || errors.Is(err, ErrDenied) {
				logging.WarnContext(ctx, "[authz] the machine's owner is not a live member of the machine's organization; the machine holds none of its projects",
					"user_id", userID, "org_id", machineOrg, "reason", err)
				return nil, false, errors.Join(ErrMachineOwnerNotMember, err)
			}
			return nil, false, err
		}
		scope, err = OrgScopeFor(ctx, userID, machineOrg)
		return scope, err == nil, err
	}
	def, err := defaultOrg(ctx, userID)
	if err != nil || def == "" {
		return nil, false, err
	}
	return &core.OrgScope{OrgID: def, IncludeUnassigned: true}, false, nil
}

// MachineProjectScope is MachineScopeFor the machine's owner and org. A nil
// machine (no registry row) is a machine of no known org.
func MachineProjectScope(ctx context.Context, userID string, machine *core.Daemon) (*core.OrgScope, bool, error) {
	return MachineScopeFor(ctx, userID, MachineOrg(machine))
}

// MachineOrg is the org machine belongs to, "" when it is not known.
func MachineOrg(machine *core.Daemon) string {
	if machine == nil || machine.OrgID == nil {
		return ""
	}
	return *machine.OrgID
}

// defaultOrg is userID's default org, "" in a deployment with none. A
// process that wired no membership at all (every server process wires one:
// app.NewOrgResolver) is read as one with no orgs, and logged at ERROR as
// authz.deny logs it; a membership that cannot answer is
// ErrDefaultOrgUnavailable.
func defaultOrg(ctx context.Context, userID string) (string, error) {
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership wired; reading the default organization as none (a path was never wired with authz.WithMembership)",
			"user_id", userID)
		return "", nil
	}
	defaults, ok := m.(defaultOrgs)
	if !ok {
		return "", ErrDefaultOrgUnavailable
	}
	def, err := defaults.DefaultOrg(ctx, userID)
	if err != nil {
		return "", errors.Join(ErrDefaultOrgUnavailable, err)
	}
	return def, nil
}
