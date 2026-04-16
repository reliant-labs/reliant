package controlplane

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/gen/controlplane/v1/controlplanev1connect"
)

const defaultBaseURL = "http://localhost:8090"

type Client interface {
	GetCurrentUserReliantState(ctx context.Context, authHeader string) (*controlplanev1.GetCurrentUserReliantStateResponse, error)
	RepairCurrentUserReliantAccess(ctx context.Context, authHeader string) (*controlplanev1.RepairCurrentUserReliantAccessResponse, error)
	RotateCurrentUserReliantAccess(ctx context.Context, authHeader, gracePeriod string) (*controlplanev1.RotateCurrentUserReliantAccessResponse, error)
	RecordManagedReliantUsage(ctx context.Context, managedKey string, spendUSD float64, model string) (*controlplanev1.RecordManagedReliantUsageResponse, error)
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

func (c *connectClient) billingClient() controlplanev1connect.BillingServiceClient {
	return controlplanev1connect.NewBillingServiceClient(c.httpClient, c.baseURL)
}

func (c *connectClient) GetCurrentUserReliantState(ctx context.Context, authHeader string) (*controlplanev1.GetCurrentUserReliantStateResponse, error) {
	req := connect.NewRequest(&controlplanev1.GetCurrentUserReliantStateRequest{})
	attachAuthorization(req, authHeader)
	resp, err := c.billingClient().GetCurrentUserReliantState(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectClient) RepairCurrentUserReliantAccess(ctx context.Context, authHeader string) (*controlplanev1.RepairCurrentUserReliantAccessResponse, error) {
	req := connect.NewRequest(&controlplanev1.RepairCurrentUserReliantAccessRequest{})
	attachAuthorization(req, authHeader)
	resp, err := c.billingClient().RepairCurrentUserReliantAccess(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectClient) RotateCurrentUserReliantAccess(ctx context.Context, authHeader, gracePeriod string) (*controlplanev1.RotateCurrentUserReliantAccessResponse, error) {
	msg := &controlplanev1.RotateCurrentUserReliantAccessRequest{}
	if trimmed := strings.TrimSpace(gracePeriod); trimmed != "" {
		msg.GracePeriod = &trimmed
	}
	req := connect.NewRequest(msg)
	attachAuthorization(req, authHeader)
	resp, err := c.billingClient().RotateCurrentUserReliantAccess(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectClient) RecordManagedReliantUsage(ctx context.Context, managedKey string, spendUSD float64, model string) (*controlplanev1.RecordManagedReliantUsageResponse, error) {
	req := connect.NewRequest(&controlplanev1.RecordManagedReliantUsageRequest{
		ManagedKey: strings.TrimSpace(managedKey),
		SpendUsd:   spendUSD,
		Model:      strings.TrimSpace(model),
	})
	resp, err := c.billingClient().RecordManagedReliantUsage(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func attachAuthorization[T any](req *connect.Request[T], authHeader string) {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		req.Header().Set("Authorization", trimmed)
	}
}
