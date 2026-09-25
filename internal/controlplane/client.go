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

type Client interface {
	// MintLLMKey mints the caller's LLM gateway key for deviceName, atomically
	// revoking that device's previous key.
	MintLLMKey(ctx context.Context, jwt, deviceName string) (LLMKey, error)

	// DeleteCurrentUserAccount asks the control plane to tombstone the
	// caller's platform account (billing identity, daemons, PII), forwarding
	// the caller's own JWT.
	//
	// A non-empty blocker slice means the control plane REFUSED and destroyed
	// nothing — the caller must surface the blockers and stop. An error means
	// the call itself failed.
	DeleteCurrentUserAccount(ctx context.Context, jwt string) ([]AccountDeletionBlocker, error)
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

func (c *connectClient) DeleteCurrentUserAccount(ctx context.Context, jwt string) ([]AccountDeletionBlocker, error) {
	req := connect.NewRequest(&userv1.DeleteCurrentUserAccountRequest{})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.userClient().DeleteCurrentUserAccount(ctx, req)
	if err != nil {
		return nil, err
	}
	var blockers []AccountDeletionBlocker
	for _, b := range resp.Msg.GetBlockers() {
		blockers = append(blockers, AccountDeletionBlocker{
			Reason: b.GetReason(),
			Detail: b.GetDetail(),
		})
	}
	return blockers, nil
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

func attachAuthorization[T any](req *connect.Request[T], authHeader string) {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		req.Header().Set("Authorization", trimmed)
	}
}
