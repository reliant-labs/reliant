// Package gitcredentialclient asks control-plane for a user's CURRENT git
// provider token.
//
// forge:outbound-io
//
// It is the outbound adapter to control-plane's GitCredentialInternalService:
// it owns the wire format, the internal-service bearer and the vendor→domain
// mapping, and nothing calls in.
//
// control-plane is the GitHub token authority (one GitHub App, control-plane's).
// Its git_credentials table holds each user's App user token and renews it.
// Reliant never stores that token: the worker asks for it at call time, for
// the owner of the run, authenticated with an internal-service JWT
// (INTERNAL_SERVICE_SECRET).
//
// The service is deliberately NOT in the exported public contract
// (control-plane proto/public-api.txt withholds it), so this package speaks the
// Connect unary protocol as plain HTTP+JSON rather than a generated client —
// exactly as internal/accesstokenclient does. The procedure path, the JSON
// field names and the reason codes in client.go ARE the contract; control-
// plane pins them in internal/handlers/git_credential_internal/wire_json_test.go.
//
// User ids are EXTERNAL (the IdP subject) — the id reliant already holds for
// every user. control-plane translates at its boundary.
//
// Built only when hosted, with the internal-service signer, by internal/app's
// gitTokens and gitHubTokenSource providers (providers_api.go) and
// internal/integrations/credwire; self-hosted has none (nil), so it is not a
// node compose.go constructs from Deps.
//
//forge:provided
package gitcredentialclient

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// Token is a provider access token and when it stops working.
//
// It holds plaintext. It is a value the caller applies to ONE outbound request
// and drops; String/GoString/Format never print it.
type Token struct {
	accessToken string
	// ExpiresAt is nil for a token the provider never said expires.
	ExpiresAt *time.Time
}

// NewToken builds a Token (tests and fakes).
func NewToken(accessToken string, expiresAt *time.Time) Token {
	return Token{accessToken: accessToken, ExpiresAt: expiresAt}
}

// Use exposes the plaintext to fn and nowhere else.
func (t Token) Use(fn func(accessToken string) error) error { return fn(t.accessToken) }

// Empty reports whether the token carries no credential.
func (t Token) Empty() bool { return t.accessToken == "" }

// Service is the adapter's surface. *Client implements it.
type Service interface {
	// UserAccessToken returns the user's current token for provider (empty
	// means "github"), renewed by control-plane first when it is near expiry.
	//
	// Errors: ErrNotConnected and ErrNeedsReconnect are permanent until the
	// user acts (never retry). Anything else — transport, a control-plane
	// outage, the provider unreachable during renewal — is transient.
	UserAccessToken(ctx context.Context, externalUserID, provider string) (Token, error)

	// MintInstallationToken returns a GitHub App installation token scoped to
	// ONE repository, so an automation can write as the App's bot
	// (`reliant-labs[bot]`) instead of as the user. control-plane first checks
	// that the named user's own token has push access to the repository, so
	// the bot is never a way past the user's own permissions.
	//
	// Errors, all permanent until someone acts unless noted: ErrAppNotConfigured
	// (control-plane holds no App key: the caller may fall back to the user's
	// own token), ErrAppNotInstalled, ErrAppPermissions, ErrOwnerCannotPush,
	// ErrNotConnected / ErrNeedsReconnect (the user has no usable GitHub token
	// to prove push access with). ErrRateLimited (a *RateLimitedError) is
	// transient with a known end: retry after its RetryAfter. Anything else is
	// transient.
	MintInstallationToken(ctx context.Context, req InstallationRequest) (InstallationToken, error)

	// MintQueueInstallationToken returns a Reliant Queue App installation
	// token scoped to ONE repository: the merge queue's own identity, the
	// only actor a repository's ruleset lets update its default branch
	// (pipeline redesign §3.1, §5, owner decision D2). control-plane mints it
	// only for the reliant worker, proven by req.CallerAssertion
	// (SignQueueCallerAssertion), and for no user.
	//
	// Errors: ErrQueueAppNotConfigured (control-plane holds no Queue App) and
	// ErrQueueAppNotInstalled are the queue's cue to land as the Reliant App
	// instead, unenforced; ErrQueueCallerNotWorker (the assertion did not
	// verify, or control-plane holds no caller secret) and
	// ErrQueueAppPermissions are permanent until an operator acts.
	// ErrRateLimited is transient with a known end; anything else is
	// transient.
	MintQueueInstallationToken(ctx context.Context, req QueueInstallationRequest) (InstallationToken, error)

	// ListOrgGitHubInstallations lists the GitHub App installations an org
	// has linked (control-plane OrganizationService.LinkGitHubAppInstallation):
	// ids and account names, no credential. The caller decides who may see
	// them (a live member of the org). A control plane from before org
	// installations answers Unimplemented, which is an *RPCError.
	ListOrgGitHubInstallations(ctx context.Context, orgID string) ([]OrgInstallation, error)
}

// OrgInstallation is one GitHub App installation linked to an org.
type OrgInstallation struct {
	InstallationID int64
	AccountLogin   string
	AccountType    string
	// Suspended: GitHub reports it suspended, so nothing is minted through it.
	Suspended bool
}

// InstallationRequest names what an installation token is for.
type InstallationRequest struct {
	// ExternalUserID is the EXTERNAL id of the user the automation acts for
	// (the activation's owner). Their push access gates the mint.
	ExternalUserID string
	// Owner and Repo name the one repository the token is scoped to.
	Owner string
	Repo  string
	// Permissions narrows the token (issues, pull_requests, contents, actions,
	// checks -> read|write). Empty means control-plane's default: issues,
	// pull_requests and contents at write.
	Permissions map[string]string
	// OrgID, when set, mints through the org's own linked installation only:
	// the repository's installation must be linked to the org
	// (ErrAppNotLinked) and the user a live member of it (ErrNotOrgMember).
	// The user's push access gates it exactly as without it. Empty is any
	// installation of the App, as before org installations.
	OrgID string
}

// QueueInstallationRequest names what a Queue App token is for.
type QueueInstallationRequest struct {
	// Owner and Repo name the one repository the token is scoped to.
	Owner string
	Repo  string
	// Permissions narrows the token (contents, pull_requests, checks ->
	// read|write). Empty means control-plane's default: all three at write.
	Permissions map[string]string
	// CallerAssertion proves the caller is the reliant worker: the
	// X-Reliant-Queue-Caller JWT SignQueueCallerAssertion signs.
	CallerAssertion string
}

// InstallationToken is a minted installation token and who it speaks as.
//
// It holds plaintext; String/GoString/Format never print it.
type InstallationToken struct {
	Token Token
	// ExpiresAt is when the token stops working (GitHub issues one-hour tokens).
	ExpiresAt time.Time
	// BotLogin is the login GitHub shows for writes made with the token,
	// e.g. "reliant-labs[bot]".
	BotLogin string
	// ActingForLogin is the GitHub login of the user the token was minted for,
	// so what the bot writes can name its owner.
	ActingForLogin string
	// Repository is "owner/name" as the token is scoped.
	Repository string
	// InstallationID is the App installation the token was minted through.
	// Zero from a control plane that does not report it.
	InstallationID int64
}

// Signer mints the internal-service bearer for each call.
type Signer func() (string, error)

// Deps are the adapter's collaborators.
type Deps struct {
	// ControlPlane is where the control plane serving
	// GitCredentialInternalService is: its url (RELIANT_CONTROL_PLANE_URL).
	ControlPlane *configv1.ControlPlaneConfig
	// Sign supplies a fresh internal-service token per call.
	Sign Signer
	// HTTPClient performs the calls. nil uses a client with a 10s timeout.
	HTTPClient *http.Client
}

// Typed outcomes. Both are FailedPrecondition at control-plane, told apart by
// the X-Forge-Error-Reason header.
//
// Each one a user (or the repository's owner) acts on is a user error
// (svcerr.ClassUser), so the component log middleware writes it at INFO and it
// never becomes an issue: on 2026-10-26..28 "GitHub is not connected" alone was
// four issues and ~530 ERROR records. ErrAppNotConfigured is ours to fix and
// stays a server fault.
var (
	// ErrAppNotConfigured: control-plane holds no GitHub App private key (or
	// it is unusable), so it cannot mint installation tokens.
	ErrAppNotConfigured = errors.New("gitcredentialclient: the GitHub App is not configured on the control plane")
	// ErrAppNotInstalled: the App is not installed on the repository, or its
	// installation is suspended.
	ErrAppNotInstalled = svcerr.WithClass(errors.New("gitcredentialclient: the GitHub App is not installed on the repository"), svcerr.ClassUser)
	// ErrAppPermissions: the App has not been granted a permission the token
	// needs; its owner must accept the updated permissions.
	ErrAppPermissions = svcerr.WithClass(errors.New("gitcredentialclient: the GitHub App lacks a permission the token needs"), svcerr.ClassUser)
	// ErrOwnerCannotPush: the user the automation acts for does not have push
	// access to the repository.
	ErrOwnerCannotPush = svcerr.WithClass(errors.New("gitcredentialclient: the owner cannot push to the repository"), svcerr.ClassUser)
	// ErrAppNotLinked: a mint for an org found the repository's installation
	// not linked to that org (or suspended); an org admin links it.
	ErrAppNotLinked = svcerr.WithClass(errors.New("gitcredentialclient: the repository's GitHub App installation is not linked to the organization"), svcerr.ClassUser)
	// ErrNotOrgMember: a mint for an org whose user is not a live member.
	ErrNotOrgMember = svcerr.WithClass(errors.New("gitcredentialclient: the user is not a member of the organization"), svcerr.ClassUser)
)

// The Queue App's outcomes (MintQueueInstallationToken). Each is an
// operator's to fix, never a user's, so none is a user error.
var (
	// ErrQueueAppNotConfigured: control-plane holds no Queue App id or key.
	ErrQueueAppNotConfigured = errors.New("gitcredentialclient: the Reliant Queue App is not configured on the control plane")
	// ErrQueueAppNotInstalled: the Queue App is not installed on the
	// repository.
	ErrQueueAppNotInstalled = errors.New("gitcredentialclient: the Reliant Queue App is not installed on the repository")
	// ErrQueueAppPermissions: the Queue App lacks a permission the token
	// needs.
	ErrQueueAppPermissions = errors.New("gitcredentialclient: the Reliant Queue App lacks a permission the token needs")
	// ErrQueueCallerNotWorker: control-plane did not accept the caller as the
	// reliant worker (a bad assertion, or no caller secret there).
	ErrQueueCallerNotWorker = errors.New("gitcredentialclient: control-plane did not accept this caller as the reliant worker")
)

// User errors too, for the same reason as the App outcomes above.
var (
	// ErrNotConnected: the user has no credential for the provider at
	// control-plane (or control-plane does not know the user).
	ErrNotConnected = svcerr.WithClass(errors.New("gitcredentialclient: no credential is connected"), svcerr.ClassUser)
	// ErrNeedsReconnect: the credential exists but the user must re-authorize.
	ErrNeedsReconnect = svcerr.WithClass(errors.New("gitcredentialclient: the connection needs to be reconnected"), svcerr.ClassUser)
)

// ErrRateLimited: GitHub's API rate limit on the token control-plane checks
// with is exhausted (control-plane#2964: resource_exhausted, reason
// github_rate_limited). It is waited out, not fixed: a user error, so no log
// layer reports it as our outage. The error returned is a *RateLimitedError,
// which says when to retry; errors.Is matches it against this sentinel.
//
// control-plane answers its App's OWN exhausted quota the same way on the
// wire and pages on it there; to this client it is the same "wait until",
// so it is not paged twice.
var ErrRateLimited = svcerr.WithClass(errors.New("gitcredentialclient: GitHub's API rate limit is exhausted"), svcerr.ClassUser)

// RateLimitedError is ErrRateLimited with control-plane's client-safe message
// (it names whose limit and until when) and its Retry-After.
type RateLimitedError struct {
	// Message is control-plane's text, e.g. "GitHub's API rate limit for your
	// GitHub account is exhausted until 2026-11-18T15:00:00Z; try again then".
	Message string
	// RetryAfter is how long to wait before trying again; zero when
	// control-plane did not say.
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	if e.Message == "" {
		return ErrRateLimited.Error()
	}
	return "gitcredentialclient: " + e.Message
}

// Unwrap makes errors.Is(err, ErrRateLimited) hold, and carries its class.
func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

var _ Service = (*Client)(nil)
