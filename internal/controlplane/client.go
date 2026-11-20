package controlplane

import (
	"context"
	"net/http"
	"strings"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/reliant-labs/forge/pkg/observe"
	accesstokenv1 "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1"
	accesstokenv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1/controlplanev1connect"
	daemonv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	gitcredentialv1 "github.com/reliant-labs/reliant/gen/controlplane/services/git_credential/v1"
	gitcredentialv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/git_credential/v1/controlplanev1connect"
	organizationv1 "github.com/reliant-labs/reliant/gen/controlplane/services/organization/v1"
	organizationv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/organization/v1/controlplanev1connect"
	userv1 "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1"
	userv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

const defaultBaseURL = "http://localhost:8090"

// AccountDeletionBlocker is one reason the control plane refused to delete the
// account. Reason is machine-readable ("paid_subscription", "wallet_balance");
// Detail is a sentence safe to show the user.
type AccountDeletionBlocker struct {
	Reason string
	Detail string
}

// RefundDestination is wallet money going back to one card on deletion.
type RefundDestination struct {
	CardBrand   string
	CardLast4   string
	AmountCents int64
}

// AccountDeletionWalletQuote is what deleting the account would do to the
// caller's wallet, as quoted by the control plane: paid credit refunded to the
// card, promotional credit forfeited.
type AccountDeletionWalletQuote struct {
	RefundCents         int64
	Destinations        []RefundDestination
	UnrefundableCents   int64
	ForfeitedPromoCents int64
}

// AccountDeletionResult is the control plane's answer to a deletion.
type AccountDeletionResult struct {
	// Blockers, when non-empty, mean the control plane REFUSED and destroyed
	// nothing.
	Blockers []AccountDeletionBlocker

	RefundedCents       int64
	RefundDestinations  []RefundDestination
	RefundOwedCents     int64
	ForfeitedPromoCents int64
	// RefundPending: the account is deleted, but RefundOwedCents could not be
	// refunded automatically and support will refund it by hand.
	RefundPending bool
}

// ReliantProviderKeyName is the device name of the LLM gateway key reliant
// holds on a user's behalf (persisted as the "reliant" provider credential).
// One holder per user, so one name: re-syncing rotates THIS key and leaves the
// user's other devices' keys alone.
const ReliantProviderKeyName = "reliant-provider"

// ReliantOrgProviderKeyName is the name of the managed key minted in one org
// (the per-(person, org) key, org-provider-accounts P10). control-plane rotates
// a person's tokens per (org, name), so one name serves every org; it differs
// from ReliantProviderKeyName so that minting in the person's default org never
// revokes the org-less key, which a chat of no org (and an older release) still
// spends.
const ReliantOrgProviderKeyName = "reliant-provider-org"

// LLMKey is a freshly minted LLM gateway key: an `rlat_` access token with
// llm:invoke acting as the caller. Returned exactly once.
type LLMKey struct {
	Plaintext string
	// Rotated reports that a previous key for the same device was replaced
	// (and revoked) by this mint — "rotated" versus "created". Every mint is a
	// new plaintext, so this cannot be inferred by comparing plaintexts.
	Rotated bool
}

// DaemonResumeToken is a freshly minted daemon-bound `daemon:resume` token.
// Returned exactly once.
type DaemonResumeToken struct {
	Plaintext string
}

// daemonResumeScope and daemonResourceKind are the wire spellings of forge's
// accesstoken.ScopeDaemonResume and accesstoken.ResourceDaemon.
const (
	daemonResumeScope  = "daemon:resume"
	daemonResourceKind = "daemon"
)

// CloneRepoRequest asks the control plane to clone a repo onto a daemon.
type CloneRepoRequest struct {
	DaemonID string
	CloneURL string
	Branch   string
	Path     string
	// RequestID is stamped on the queued command and echoed by the daemon's
	// failure announcement, so the outcome can be matched to what the caller
	// recorded for this clone.
	RequestID string
}

// CloneRepoResult is what the control plane reports back about the clone.
type CloneRepoResult struct {
	// ClonedPath is where the checkout will live. Echoed from the request;
	// not confirmation that the directory exists.
	ClonedPath string
	// Queued is true when the command was enqueued and has not run yet.
	Queued bool
	// DaemonName names the machine the clone is waiting on, so the caller
	// can say which without a second lookup.
	DaemonName string
}

// client is the Service over the control plane at baseURL.
type client struct {
	httpClient *http.Client
	baseURL    string
	sign       func() (string, error)

	accessTokens   accesstokenv1connect.AccessTokenServiceClient
	users          userv1connect.UserServiceClient
	gitCredentials gitcredentialv1connect.GitCredentialServiceClient
	daemons        daemonv1connect.DaemonServiceClient
	organizations  organizationv1connect.OrganizationServiceClient
}

// New returns the adapter for the control plane deps.ControlPlane names.
//
// forge:constructor
func New(deps Deps) Service {
	baseURL := strings.TrimRight(EndpointFrom(deps.ControlPlane).URL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// Two clients by default, because the two kinds of call are traced at
	// different layers: the plain POST (run_resume.go) by the transport, the
	// Connect rpcs by the otelconnect interceptor. An otelhttp transport under
	// the Connect clients would nest a duplicate span under every rpc.
	httpClient, rpcClient := deps.HTTPClient, deps.HTTPClient
	var (
		rpcHTTPOpts     []connecthttp.Option
		rpcInterceptors []connect.ClientInterceptor
	)
	if httpClient == nil {
		httpClient = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second})
		rpcClient = httpClient
		if stack, err := observe.NewClientStackV2(observe.ClientStackDepsV2{Timeout: 30 * time.Second}); err == nil {
			rpcClient, rpcHTTPOpts, rpcInterceptors = stack.HTTPClient, stack.HTTPOptions, stack.Interceptors
		}
	}
	rpc := rpcclient.New(rpcClient, baseURL, rpcHTTPOpts, rpcInterceptors...)
	return &client{
		httpClient:     httpClient,
		baseURL:        baseURL,
		sign:           deps.Sign,
		accessTokens:   accesstokenv1connect.NewAccessTokenServiceClient(rpc),
		users:          userv1connect.NewUserServiceClient(rpc),
		gitCredentials: gitcredentialv1connect.NewGitCredentialServiceClient(rpc),
		daemons:        daemonv1connect.NewDaemonServiceClient(rpc),
		organizations:  organizationv1connect.NewOrganizationServiceClient(rpc),
	}
}

// Endpoint is how a server process reaches the control plane hosting its
// deployment: ServerProcessConfig's ControlPlaneConfig
// (proto/reliant/config/v1/control_plane_config.proto), loaded once by the entrypoint
// and handed down. The zero Endpoint is a self-hosted deployment, with no
// control plane — never a localhost default, which would turn "not hosted"
// into a connection error.
type Endpoint struct {
	// URL is the control plane's base URL (RELIANT_CONTROL_PLANE_URL).
	URL string
	// InternalServiceSecret signs internal-service calls to it
	// (INTERNAL_SERVICE_SECRET). Every consumer refuses a URL without one.
	InternalServiceSecret string
}

// EndpointFrom is cfg (ServerProcessConfig's ControlPlaneConfig) as an
// Endpoint, trimmed as every reader of these variables trimmed them. A nil cfg
// is the zero Endpoint: self-hosted.
func EndpointFrom(cfg *configv1.ControlPlaneConfig) Endpoint {
	return Endpoint{
		URL:                   strings.TrimSpace(cfg.GetUrl()),
		InternalServiceSecret: strings.TrimSpace(cfg.GetInternalServiceSecret()),
	}
}

// Hosted reports whether a control plane is configured.
func (e Endpoint) Hosted() bool { return e.URL != "" }

func (c *client) CloneRepoOntoDaemon(ctx context.Context, jwt string, in CloneRepoRequest) (CloneRepoResult, error) {
	req := &gitcredentialv1.CloneRepoRequest{
		DaemonId:  in.DaemonID,
		GitRepo:   in.CloneURL,
		GitBranch: in.Branch,
		Path:      in.Path,
		RequestId: in.RequestID,
	}
	ctx = withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.gitCredentials.CloneRepo(ctx, req)
	if err != nil {
		return CloneRepoResult{}, err
	}
	return CloneRepoResult{
		ClonedPath: resp.GetClonedPath(),
		Queued:     resp.GetQueued(),
		DaemonName: resp.GetDaemonName(),
	}, nil
}

func (c *client) ListMyOrganizations(ctx context.Context, jwt string) ([]MyOrganization, error) {
	req := &organizationv1.ListMyOrganizationsRequest{}
	ctx = withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.organizations.ListMyOrganizations(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]MyOrganization, 0, len(resp.GetOrganizations()))
	for _, o := range resp.GetOrganizations() {
		out = append(out, MyOrganization{ID: o.GetId(), Role: o.GetRole()})
	}
	return out, nil
}

func (c *client) PreviewAccountDeletionWallet(ctx context.Context, jwt string) (*AccountDeletionWalletQuote, error) {
	req := &userv1.PreviewAccountDeletionRequest{}
	ctx = withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.users.PreviewAccountDeletion(ctx, req)
	if err != nil {
		return nil, err
	}
	w := resp.GetWallet()
	if w == nil {
		return nil, nil
	}
	return &AccountDeletionWalletQuote{
		RefundCents:         w.GetRefundCents(),
		Destinations:        refundDestinations(w.GetDestinations()),
		UnrefundableCents:   w.GetUnrefundableCents(),
		ForfeitedPromoCents: w.GetForfeitedPromoCents(),
	}, nil
}

func (c *client) DeleteCurrentUserAccount(ctx context.Context, jwt string) (*AccountDeletionResult, error) {
	req := &userv1.DeleteCurrentUserAccountRequest{}
	ctx = withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.users.DeleteCurrentUserAccount(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &AccountDeletionResult{RefundPending: resp.GetRefundPending()}
	for _, b := range resp.GetBlockers() {
		out.Blockers = append(out.Blockers, AccountDeletionBlocker{
			Reason: b.GetReason(),
			Detail: b.GetDetail(),
		})
	}
	if w := resp.GetWallet(); w != nil {
		out.RefundedCents = w.GetRefundedCents()
		out.RefundDestinations = refundDestinations(w.GetDestinations())
		out.RefundOwedCents = w.GetRefundOwedCents()
		out.ForfeitedPromoCents = w.GetForfeitedPromoCents()
	}
	return out, nil
}

func refundDestinations(in []*userv1.WalletRefundDestination) []RefundDestination {
	out := make([]RefundDestination, 0, len(in))
	for _, d := range in {
		out = append(out, RefundDestination{
			CardBrand:   d.GetCardBrand(),
			CardLast4:   d.GetCardLast4(),
			AmountCents: d.GetAmountCents(),
		})
	}
	return out
}

func (c *client) MintLLMKey(ctx context.Context, jwt, deviceName string) (LLMKey, error) {
	req := &accesstokenv1.CreateMyTokenRequest{
		Name:   deviceName,
		Scopes: []string{"llm:invoke"},
		Rotate: true,
	}
	ctx = withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.accessTokens.CreateMyToken(ctx, req)
	if err != nil {
		return LLMKey{}, err
	}
	return LLMKey{
		Plaintext: strings.TrimSpace(resp.GetSecret()),
		Rotated:   resp.GetRotated(),
	}, nil
}

func (c *client) MintDaemonResumeToken(ctx context.Context, jwt, daemonID, name string) (DaemonResumeToken, error) {
	req := &accesstokenv1.CreateMyTokenRequest{
		Name:     name,
		Scopes:   []string{daemonResumeScope},
		Resource: &accesstokenv1.ResourceBinding{Kind: daemonResourceKind, Id: daemonID},
		Rotate:   true,
	}
	ctx = withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.accessTokens.CreateMyToken(ctx, req)
	if err != nil {
		return DaemonResumeToken{}, err
	}
	return DaemonResumeToken{Plaintext: strings.TrimSpace(resp.GetSecret())}, nil
}

func (c *client) RevokeDaemonResumeTokens(ctx context.Context, jwt, daemonID string) error {
	scope := daemonResumeScope
	listReq := &accesstokenv1.ListMyTokensRequest{Scope: &scope}
	listed, err := c.accessTokens.ListMyTokens(withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt)), listReq)
	if err != nil {
		return err
	}
	var firstErr error
	for _, tok := range listed.GetTokens() {
		if tok.GetResource().GetKind() != daemonResourceKind || tok.GetResource().GetId() != daemonID {
			continue
		}
		if tok.GetRevokedAt() != nil {
			continue
		}
		revokeReq := &accesstokenv1.RevokeMyTokenRequest{Id: tok.GetId()}
		if _, err := c.accessTokens.RevokeMyToken(withAuthorization(ctx, "Bearer "+strings.TrimSpace(jwt)), revokeReq); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// withAuthorization returns ctx carrying a fresh client CallInfo whose request
// header sends authHeader as Authorization (nothing when it is blank). Each
// call gets its own context: a CallInfo describes one call.
func withAuthorization(ctx context.Context, authHeader string) context.Context {
	ctx, info := connect.NewClientContext(ctx)
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		info.RequestHeader().Set("Authorization", trimmed)
	}
	return ctx
}
