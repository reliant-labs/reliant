// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package controlplane

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/v1/controlplanev1connect"
)

const defaultBaseURL = "http://localhost:8090"

type Client interface {
	IssueMyReliantAPIKey(ctx context.Context, jwt string) (string, error)
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

func (c *connectClient) billingClient() controlplanev1connect.BillingServiceClient {
	return controlplanev1connect.NewBillingServiceClient(c.httpClient, c.baseURL)
}

func (c *connectClient) IssueMyReliantAPIKey(ctx context.Context, jwt string) (string, error) {
	req := connect.NewRequest(&controlplanev1.IssueMyReliantAPIKeyRequest{})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	resp, err := c.billingClient().IssueMyReliantAPIKey(ctx, req)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Msg.GetPlaintextKey()), nil
}

func attachAuthorization[T any](req *connect.Request[T], authHeader string) {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		req.Header().Set("Authorization", trimmed)
	}
}
