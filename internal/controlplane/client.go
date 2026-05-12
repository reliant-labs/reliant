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
	CheckManagedReliantAffordability(ctx context.Context, managedKey string, request ManagedReliantAffordabilityRequest) (*controlplanev1.CheckManagedReliantAffordabilityResponse, error)
	ReserveManagedReliantUsage(ctx context.Context, managedKey string, request ManagedReliantReservationRequest) (*controlplanev1.ReserveManagedReliantUsageResponse, error)
	FinalizeManagedReliantUsage(ctx context.Context, managedKey string, request ManagedReliantFinalizeRequest) (*controlplanev1.FinalizeManagedReliantUsageResponse, error)
	ReleaseManagedReliantUsageReservation(ctx context.Context, managedKey, reservationID string) (*controlplanev1.ReleaseManagedReliantUsageReservationResponse, error)
}

type ManagedReliantAffordabilityRequest struct {
	EstimatedSpendUSD     float64
	Model                 string
	CanonicalModelID      string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
}

type ManagedReliantReservationRequest struct {
	ReservationID         string
	EstimatedSpendUSD     float64
	Model                 string
	CanonicalModelID      string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
}

type ManagedReliantFinalizeRequest struct {
	ReservationID     string
	SpendUSD          float64
	Model             string
	CanonicalModelID  string
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	ObservedCostUSD   *float64
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

func (c *connectClient) CheckManagedReliantAffordability(ctx context.Context, managedKey string, request ManagedReliantAffordabilityRequest) (*controlplanev1.CheckManagedReliantAffordabilityResponse, error) {
	msg := &controlplanev1.CheckManagedReliantAffordabilityRequest{
		ManagedKey:            strings.TrimSpace(managedKey),
		EstimatedSpendUsd:     request.EstimatedSpendUSD,
		Model:                 strings.TrimSpace(request.Model),
		CanonicalModelId:      strings.TrimSpace(request.CanonicalModelID),
		EstimatedInputTokens:  request.EstimatedInputTokens,
		EstimatedOutputTokens: request.EstimatedOutputTokens,
	}
	req := connect.NewRequest(msg)
	attachAuthorization(req, "Bearer "+strings.TrimSpace(managedKey))
	resp, err := c.billingClient().CheckManagedReliantAffordability(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectClient) ReserveManagedReliantUsage(ctx context.Context, managedKey string, request ManagedReliantReservationRequest) (*controlplanev1.ReserveManagedReliantUsageResponse, error) {
	msg := &controlplanev1.ReserveManagedReliantUsageRequest{
		ManagedKey:            strings.TrimSpace(managedKey),
		ReservationId:         strings.TrimSpace(request.ReservationID),
		EstimatedSpendUsd:     request.EstimatedSpendUSD,
		Model:                 strings.TrimSpace(request.Model),
		CanonicalModelId:      strings.TrimSpace(request.CanonicalModelID),
		EstimatedInputTokens:  request.EstimatedInputTokens,
		EstimatedOutputTokens: request.EstimatedOutputTokens,
	}
	req := connect.NewRequest(msg)
	attachAuthorization(req, "Bearer "+strings.TrimSpace(managedKey))
	resp, err := c.billingClient().ReserveManagedReliantUsage(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectClient) FinalizeManagedReliantUsage(ctx context.Context, managedKey string, request ManagedReliantFinalizeRequest) (*controlplanev1.FinalizeManagedReliantUsageResponse, error) {
	msg := &controlplanev1.FinalizeManagedReliantUsageRequest{
		ManagedKey:        strings.TrimSpace(managedKey),
		ReservationId:     strings.TrimSpace(request.ReservationID),
		SpendUsd:          request.SpendUSD,
		Model:             strings.TrimSpace(request.Model),
		CanonicalModelId:  strings.TrimSpace(request.CanonicalModelID),
		InputTokens:       request.InputTokens,
		OutputTokens:      request.OutputTokens,
		CachedInputTokens: request.CachedInputTokens,
	}
	if request.ObservedCostUSD != nil {
		msg.ObservedCostUsd = request.ObservedCostUSD
	}
	req := connect.NewRequest(msg)
	attachAuthorization(req, "Bearer "+strings.TrimSpace(managedKey))
	resp, err := c.billingClient().FinalizeManagedReliantUsage(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectClient) ReleaseManagedReliantUsageReservation(ctx context.Context, managedKey, reservationID string) (*controlplanev1.ReleaseManagedReliantUsageReservationResponse, error) {
	req := connect.NewRequest(&controlplanev1.ReleaseManagedReliantUsageReservationRequest{ManagedKey: strings.TrimSpace(managedKey), ReservationId: strings.TrimSpace(reservationID)})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(managedKey))
	resp, err := c.billingClient().ReleaseManagedReliantUsageReservation(ctx, req)
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
