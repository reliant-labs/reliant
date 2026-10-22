package controlplane

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	accesstokenv1 "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1"
	accesstokenv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1/controlplanev1connect"
	daemonv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	gitcredentialv1 "github.com/reliant-labs/reliant/gen/controlplane/services/git_credential/v1"
	gitcredentialv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/git_credential/v1/controlplanev1connect"
	userv1 "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1"
	userv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1/controlplanev1connect"
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
}

// New returns the adapter for the control plane at deps.BaseURL.
//
// forge:constructor
func New(deps Deps) Service {
	baseURL := strings.TrimRight(strings.TrimSpace(deps.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// Two clients by default, because the two kinds of call are traced at
	// different layers: the plain POST (run_resume.go) by the transport, the
	// Connect rpcs by the otelconnect interceptor. An otelhttp transport under
	// the Connect clients would nest a duplicate span under every rpc.
	httpClient, rpcClient := deps.HTTPClient, deps.HTTPClient
	var rpcOpts []connect.ClientOption
	if httpClient == nil {
		httpClient = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second})
		rpcClient = httpClient
		if stack, err := observe.NewClientStack(observe.ClientStackDeps{Timeout: 30 * time.Second}); err == nil {
			rpcClient, rpcOpts = stack.HTTPClient, stack.ClientOptions
		}
	}
	return &client{
		httpClient:     httpClient,
		baseURL:        baseURL,
		sign:           deps.Sign,
		accessTokens:   accesstokenv1connect.NewAccessTokenServiceClient(rpcClient, baseURL, rpcOpts...),
		users:          userv1connect.NewUserServiceClient(rpcClient, baseURL, rpcOpts...),
		gitCredentials: gitcredentialv1connect.NewGitCredentialServiceClient(rpcClient, baseURL, rpcOpts...),
		daemons:        daemonv1connect.NewDaemonServiceClient(rpcClient, baseURL, rpcOpts...),
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

// Hosted reports whether a control plane is configured.
func (e Endpoint) Hosted() bool { return e.URL != "" }

func (c *client) CloneRepoOntoDaemon(ctx context.Context, jwt string, in CloneRepoRequest) (CloneRepoResult, error) {
	req := connect.NewRequest(&gitcredentialv1.CloneRepoRequest{
		DaemonId:  in.DaemonID,
		GitRepo:   in.CloneURL,
		GitBranch: in.Branch,
		Path:      in.Path,
		RequestId: in.RequestID,
	})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.gitCredentials.CloneRepo(ctx, req)
	if err != nil {
		return CloneRepoResult{}, err
	}
	return CloneRepoResult{
		ClonedPath: resp.Msg.GetClonedPath(),
		Queued:     resp.Msg.GetQueued(),
		DaemonName: resp.Msg.GetDaemonName(),
	}, nil
}

func (c *client) PreviewAccountDeletionWallet(ctx context.Context, jwt string) (*AccountDeletionWalletQuote, error) {
	req := connect.NewRequest(&userv1.PreviewAccountDeletionRequest{})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.users.PreviewAccountDeletion(ctx, req)
	if err != nil {
		return nil, err
	}
	w := resp.Msg.GetWallet()
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
	req := connect.NewRequest(&userv1.DeleteCurrentUserAccountRequest{})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.users.DeleteCurrentUserAccount(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &AccountDeletionResult{RefundPending: resp.Msg.GetRefundPending()}
	for _, b := range resp.Msg.GetBlockers() {
		out.Blockers = append(out.Blockers, AccountDeletionBlocker{
			Reason: b.GetReason(),
			Detail: b.GetDetail(),
		})
	}
	if w := resp.Msg.GetWallet(); w != nil {
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
	req := connect.NewRequest(&accesstokenv1.CreateMyTokenRequest{
		Name:   deviceName,
		Scopes: []string{"llm:invoke"},
		Rotate: true,
	})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.accessTokens.CreateMyToken(ctx, req)
	if err != nil {
		return LLMKey{}, err
	}
	return LLMKey{
		Plaintext: strings.TrimSpace(resp.Msg.GetSecret()),
		Rotated:   resp.Msg.GetRotated(),
	}, nil
}

func (c *client) MintDaemonResumeToken(ctx context.Context, jwt, daemonID, name string) (DaemonResumeToken, error) {
	req := connect.NewRequest(&accesstokenv1.CreateMyTokenRequest{
		Name:     name,
		Scopes:   []string{daemonResumeScope},
		Resource: &accesstokenv1.ResourceBinding{Kind: daemonResourceKind, Id: daemonID},
		Rotate:   true,
	})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.accessTokens.CreateMyToken(ctx, req)
	if err != nil {
		return DaemonResumeToken{}, err
	}
	return DaemonResumeToken{Plaintext: strings.TrimSpace(resp.Msg.GetSecret())}, nil
}

func (c *client) RevokeDaemonResumeTokens(ctx context.Context, jwt, daemonID string) error {
	scope := daemonResumeScope
	listReq := connect.NewRequest(&accesstokenv1.ListMyTokensRequest{Scope: &scope})
	attachAuthorization(listReq, "Bearer "+strings.TrimSpace(jwt))
	listed, err := c.accessTokens.ListMyTokens(ctx, listReq)
	if err != nil {
		return err
	}
	var firstErr error
	for _, tok := range listed.Msg.GetTokens() {
		if tok.GetResource().GetKind() != daemonResourceKind || tok.GetResource().GetId() != daemonID {
			continue
		}
		if tok.GetRevokedAt() != nil {
			continue
		}
		revokeReq := connect.NewRequest(&accesstokenv1.RevokeMyTokenRequest{Id: tok.GetId()})
		attachAuthorization(revokeReq, "Bearer "+strings.TrimSpace(jwt))
		if _, err := c.accessTokens.RevokeMyToken(ctx, revokeReq); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func attachAuthorization[T any](req *connect.Request[T], authHeader string) {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		req.Header().Set("Authorization", trimmed)
	}
}
