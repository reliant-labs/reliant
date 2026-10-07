// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: thin HTTP client type for control-plane account endpoints; consumers declare the narrow interface they need
package controlplane

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	accesstokenv1 "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1"
	accesstokenv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1/controlplanev1connect"
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

type Client interface {
	// MintLLMKey mints the caller's LLM gateway key for deviceName, atomically
	// revoking that device's previous key.
	MintLLMKey(ctx context.Context, jwt, deviceName string) (LLMKey, error)

	// MintDaemonResumeToken mints the caller's delegated automation credential
	// for ONE daemon: a daemon-bound `daemon:resume` token that can resolve
	// and wake that daemon and nothing else. Rotate replaces the previous
	// token for the same name, so re-minting never accumulates live secrets.
	MintDaemonResumeToken(ctx context.Context, jwt, daemonID, name string) (DaemonResumeToken, error)

	// RevokeDaemonResumeTokens revokes every live `daemon:resume` token the
	// caller holds that is bound to daemonID.
	RevokeDaemonResumeTokens(ctx context.Context, jwt, daemonID string) error

	// PreviewAccountDeletionWallet asks the control plane what deleting the
	// caller's account would refund and forfeit. Changes nothing.
	PreviewAccountDeletionWallet(ctx context.Context, jwt string) (*AccountDeletionWalletQuote, error)

	// DeleteCurrentUserAccount asks the control plane to settle the caller's
	// wallet (refund paid credit, forfeit promotional credit) and tombstone
	// their platform account (billing identity, daemons, PII), forwarding the
	// caller's own JWT.
	//
	// Non-empty Blockers means the control plane REFUSED and destroyed
	// nothing — the caller must surface the blockers and stop. An error means
	// the call itself failed; a connect.CodeUnavailable error means a refund's
	// outcome could not be confirmed and nothing else was deleted, so the
	// caller may retry.
	DeleteCurrentUserAccount(ctx context.Context, jwt string) (*AccountDeletionResult, error)

	// CloneRepoOntoDaemon asks the control plane to clone a repo onto one of
	// the caller's daemons, using the git credential IT holds — reliant has
	// no access to the user's GitHub token, which is why this is a call and
	// not something reliant does itself.
	//
	// It returns once the clone is QUEUED, not once it has run: the control
	// plane enqueues durably so a daemon that is asleep or booting is still
	// a valid target. Queued=true means the checkout does not exist yet.
	CloneRepoOntoDaemon(ctx context.Context, jwt string, req CloneRepoRequest) (CloneRepoResult, error)
}

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

type connectClient struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(baseURL string) Client {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		trimmed = getBaseURL()
	}
	return &connectClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(trimmed, "/"),
	}
}

func getBaseURL() string {
	for _, key := range []string{"RELIANT_CONTROL_PLANE_URL", "CONTROL_PLANE_API_URL", "CONTROL_PLANE_BASE_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultBaseURL
}

// BaseURLFromEnv returns the configured control-plane origin, or "" when
// this deployment has none configured. Unlike getBaseURL/NewClient there is
// no localhost fallback — callers that need to distinguish "no control
// plane at all" from "control plane at the default address" (e.g. deciding
// whether to wire an optional daemon-registry client) use this instead.
func BaseURLFromEnv() string {
	for _, key := range []string{"RELIANT_CONTROL_PLANE_URL", "CONTROL_PLANE_API_URL", "CONTROL_PLANE_BASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func (c *connectClient) accessTokenClient() accesstokenv1connect.AccessTokenServiceClient {
	return accesstokenv1connect.NewAccessTokenServiceClient(c.httpClient, c.baseURL)
}

func (c *connectClient) userClient() userv1connect.UserServiceClient {
	return userv1connect.NewUserServiceClient(c.httpClient, c.baseURL)
}

func (c *connectClient) gitCredentialClient() gitcredentialv1connect.GitCredentialServiceClient {
	return gitcredentialv1connect.NewGitCredentialServiceClient(c.httpClient, c.baseURL)
}

func (c *connectClient) CloneRepoOntoDaemon(ctx context.Context, jwt string, in CloneRepoRequest) (CloneRepoResult, error) {
	req := connect.NewRequest(&gitcredentialv1.CloneRepoRequest{
		DaemonId:  in.DaemonID,
		GitRepo:   in.CloneURL,
		GitBranch: in.Branch,
		Path:      in.Path,
		RequestId: in.RequestID,
	})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.gitCredentialClient().CloneRepo(ctx, req)
	if err != nil {
		return CloneRepoResult{}, err
	}
	return CloneRepoResult{
		ClonedPath: resp.Msg.GetClonedPath(),
		Queued:     resp.Msg.GetQueued(),
		DaemonName: resp.Msg.GetDaemonName(),
	}, nil
}

func (c *connectClient) PreviewAccountDeletionWallet(ctx context.Context, jwt string) (*AccountDeletionWalletQuote, error) {
	req := connect.NewRequest(&userv1.PreviewAccountDeletionRequest{})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.userClient().PreviewAccountDeletion(ctx, req)
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

func (c *connectClient) DeleteCurrentUserAccount(ctx context.Context, jwt string) (*AccountDeletionResult, error) {
	req := connect.NewRequest(&userv1.DeleteCurrentUserAccountRequest{})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.userClient().DeleteCurrentUserAccount(ctx, req)
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

func (c *connectClient) MintLLMKey(ctx context.Context, jwt, deviceName string) (LLMKey, error) {
	req := connect.NewRequest(&accesstokenv1.CreateMyTokenRequest{
		Name:   deviceName,
		Scopes: []string{"llm:invoke"},
		Rotate: true,
	})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.accessTokenClient().CreateMyToken(ctx, req)
	if err != nil {
		return LLMKey{}, err
	}
	return LLMKey{
		Plaintext: strings.TrimSpace(resp.Msg.GetSecret()),
		Rotated:   resp.Msg.GetRotated(),
	}, nil
}

func (c *connectClient) MintDaemonResumeToken(ctx context.Context, jwt, daemonID, name string) (DaemonResumeToken, error) {
	req := connect.NewRequest(&accesstokenv1.CreateMyTokenRequest{
		Name:     name,
		Scopes:   []string{daemonResumeScope},
		Resource: &accesstokenv1.ResourceBinding{Kind: daemonResourceKind, Id: daemonID},
		Rotate:   true,
	})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.accessTokenClient().CreateMyToken(ctx, req)
	if err != nil {
		return DaemonResumeToken{}, err
	}
	return DaemonResumeToken{Plaintext: strings.TrimSpace(resp.Msg.GetSecret())}, nil
}

func (c *connectClient) RevokeDaemonResumeTokens(ctx context.Context, jwt, daemonID string) error {
	scope := daemonResumeScope
	listReq := connect.NewRequest(&accesstokenv1.ListMyTokensRequest{Scope: &scope})
	attachAuthorization(listReq, "Bearer "+strings.TrimSpace(jwt))
	listed, err := c.accessTokenClient().ListMyTokens(ctx, listReq)
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
		if _, err := c.accessTokenClient().RevokeMyToken(ctx, revokeReq); err != nil && firstErr == nil {
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
