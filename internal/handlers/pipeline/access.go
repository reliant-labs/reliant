package pipeline

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// Reasons (x-forge-error-reason) this service refuses with, beyond
// internal/orgs' (org_admin_required, not_org_member).
const (
	// ReasonPipelineDisabled: the server's pipeline.enabled switch is off.
	ReasonPipelineDisabled = "pipeline_disabled"
	// ReasonPersonalCredentialRequired: a write came with a credential that
	// is not a person's own: a token bound to a machine, an org automation
	// token, an OAuth client's token, another user's token, or one no
	// validator classified.
	ReasonPersonalCredentialRequired = "personal_credential_required"
	// ReasonNoRepositoryAccess: the caller's own GitHub access does not
	// reach the repository (recently enough to trust).
	ReasonNoRepositoryAccess = "no_repository_access"
	// ReasonEnrolledElsewhere: another project enrolled the repository.
	ReasonEnrolledElsewhere = "enrolled_elsewhere"
)

// providerGitHub is the only provider enrolment supports: pr_records' and
// the access grants' (prrecords.ProviderGitHub, ghaccess.IntegrationID).
const providerGitHub = "github"

// enabled refuses every rpc while the pipeline.enabled switch is off.
func (s *Service) enabled() error {
	if s.deps.Pipeline.GetEnabled() {
		return nil
	}
	return svcerr.WithReason(svcerr.FailedPrecondition(
		"Reliant's pipeline is off on this server ("+serveraction.SwitchName+" is false)"), ReasonPipelineDisabled)
}

// callerID is the signed-in caller, or Unauthenticated.
func callerID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

// repositoryArg is a request's owner/name, or InvalidArgument.
func repositoryArg(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", svcerr.InvalidArgument("repository must be owner/name")
	}
	return repo, nil
}

// writer is who made a write, for its log line.
type writer struct {
	userID string
	// credential is the credential's metric label (auth.Credential.Label).
	credential string
	// tokenID is the access token's id; empty for a session.
	tokenID string
}

// requirePipelineAdmin is the gate on the pipeline's writes (enrolling a
// repository, setting its mode). It admits an owner or admin of orgID, by a
// fresh read of their role, who is either
//
//   - a person signed in to the app: a browser session (the credential and
//     forge's authn method both say so), a Supabase app session, or the
//     self-host shared key (auth.RequireApprovalDecider's person class); or
//   - holding a personal access token: an `rlat_` token acting as this very
//     user, bound to no machine or resource, not an OAuth client's and not
//     ephemeral. The CLI login and `reliant auth token create` mint these.
//
// Refused: a token bound to a machine (a daemon), an org automation token
// (no acting user), an OAuth client's token, an identity token held by
// software, and an unclassified context.
//
// This is wider than RequireApprovalDecider on purpose: the MVP has no UI,
// so the CLI must be able to enrol and change modes (pipeline lead decision,
// 2026-11-26, pending owner review). Approval decisions keep their stricter
// rule, and the pipeline's later decision kinds (pipeline_decisions) need
// the browser session.
func requirePipelineAdmin(ctx context.Context, userID, orgID, action string) (writer, error) {
	w := writer{userID: userID, credential: auth.CredentialFromContext(ctx).Label()}
	refuse := func(why string) (writer, error) {
		return w, svcerr.WithReason(svcerr.PermissionDenied(
			"only an organization admin, signed in to the Reliant app or with a personal access token "+
				"(the CLI login, or `reliant auth token create`), can "+action+"; "+why), ReasonPersonalCredentialRequired)
	}
	switch auth.CredentialFromContext(ctx) {
	case auth.CredentialBrowserSession:
		if m, ok := authn.AuthMethod(ctx); !ok || !m.IsBrowserSession() {
			return refuse("this request's session was not read from the app's sign-in cookie")
		}
	case auth.CredentialSession, auth.CredentialSharedKey:
	case auth.CredentialAccessToken:
		p, ok := auth.MachineTokenFromContext(ctx)
		switch {
		case !ok:
			return refuse("this access token carried no principal")
		case auth.MachineBound(p):
			return refuse("this token belongs to a machine (a daemon)")
		case p.ActingUserID == "":
			return refuse("this is an organization automation token, which acts as nobody")
		case p.ActingUserID != userID:
			return refuse("this token acts as another user")
		case p.ClientID != "":
			return refuse("this token was issued to an OAuth client")
		case p.Ephemeral:
			return refuse("this token belongs to a session of a machine")
		}
		w.tokenID = p.TokenID
	default:
		return refuse("this request was made with " + auth.CredentialFromContext(ctx).String())
	}
	switch err := authz.RequireOrgAdminRole(ctx, userID, orgID); {
	case err == nil:
		return w, nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return w, orgs.LookupFailed(ctx, "could not confirm your role in this organization; try again", err)
	default:
		// ErrNotOrgAdmin, or ErrDenied (a credential bound to another org).
		return w, svcerr.WithCause(svcerr.WithReason(
			svcerr.PermissionDenied("only an owner or admin of this organization can "+action), orgs.ReasonOrgAdminRequired), err)
	}
}

// readRepo returns the enrolled repository with id when the caller is a
// member of its organization; NotFound for an id that does not exist or
// that the caller cannot read, so ids are not probed.
func (s *Service) readRepo(ctx context.Context, userID, id string) (*core.PipelineRepo, error) {
	repo, err := s.deps.Store.GetPipelineRepo(ctx, id)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, svcerr.NotFound("pipeline repository")
	}
	if err := orgs.RequireMember(ctx, userID, repo.OrgID); err != nil {
		if svcerr.Code(err) == connect.CodeUnavailable {
			return nil, err
		}
		return nil, svcerr.NotFound("pipeline repository")
	}
	return repo, nil
}
