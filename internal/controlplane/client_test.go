package controlplane

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/gen/controlplane/v1/controlplanev1connect"
)

func TestClient_CheckManagedReliantAffordability_DoesNotAttachAuthorization(t *testing.T) {
	var gotAuth string
	var gotManagedKey string
	var gotEstimatedSpend float64
	var gotModel string
	var gotCanonicalModel string
	var gotEstimatedInputTokens int64
	var gotEstimatedOutputTokens int64

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceCheckManagedReliantAffordabilityProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.CheckManagedReliantAffordabilityRequest]) (*connect.Response[controlplanev1.CheckManagedReliantAffordabilityResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			gotManagedKey = req.Msg.GetManagedKey()
			gotEstimatedSpend = req.Msg.GetEstimatedSpendUsd()
			gotModel = req.Msg.GetModel()
			gotCanonicalModel = req.Msg.GetCanonicalModelId()
			gotEstimatedInputTokens = req.Msg.GetEstimatedInputTokens()
			gotEstimatedOutputTokens = req.Msg.GetEstimatedOutputTokens()
			return connect.NewResponse(&controlplanev1.CheckManagedReliantAffordabilityResponse{WalletBalanceUsdNanos: 500_000_000, RequiredSpendUsdNanos: 250_000_000}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.CheckManagedReliantAffordability(context.Background(), " rlnt_test_key ", ManagedReliantAffordabilityRequest{
		EstimatedSpendUSD:     0.25,
		Model:                 " claude-sonnet-4-5 ",
		CanonicalModelID:      " claude-4.5-sonnet ",
		EstimatedInputTokens:  1200,
		EstimatedOutputTokens: 300,
	})
	if err != nil {
		t.Fatalf("CheckManagedReliantAffordability: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("authorization header = %q, want empty", gotAuth)
	}
	if gotManagedKey != "rlnt_test_key" {
		t.Fatalf("managed key = %q, want rlnt_test_key", gotManagedKey)
	}
	if gotEstimatedSpend != 0.25 {
		t.Fatalf("estimated spend = %v, want 0.25", gotEstimatedSpend)
	}
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want claude-sonnet-4-5", gotModel)
	}
	if gotCanonicalModel != "claude-4.5-sonnet" {
		t.Fatalf("canonical model = %q, want claude-4.5-sonnet", gotCanonicalModel)
	}
	if gotEstimatedInputTokens != 1200 || gotEstimatedOutputTokens != 300 {
		t.Fatalf("unexpected token payload = %d/%d, want 1200/300", gotEstimatedInputTokens, gotEstimatedOutputTokens)
	}
	if resp.GetRequiredSpendUsdNanos() != 250_000_000 {
		t.Fatalf("required spend nanos = %d, want %d", resp.GetRequiredSpendUsdNanos(), int64(250_000_000))
	}
}

func TestClient_ReserveManagedReliantUsage_DoesNotAttachAuthorization(t *testing.T) {
	var gotAuth string
	var gotManagedKey string
	var gotReservationID string
	var gotEstimatedSpend float64
	var gotModel string
	var gotCanonicalModel string
	var gotEstimatedInputTokens int64
	var gotEstimatedOutputTokens int64

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceReserveManagedReliantUsageProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.ReserveManagedReliantUsageRequest]) (*connect.Response[controlplanev1.ReserveManagedReliantUsageResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			gotManagedKey = req.Msg.GetManagedKey()
			gotReservationID = req.Msg.GetReservationId()
			gotEstimatedSpend = req.Msg.GetEstimatedSpendUsd()
			gotModel = req.Msg.GetModel()
			gotCanonicalModel = req.Msg.GetCanonicalModelId()
			gotEstimatedInputTokens = req.Msg.GetEstimatedInputTokens()
			gotEstimatedOutputTokens = req.Msg.GetEstimatedOutputTokens()
			return connect.NewResponse(&controlplanev1.ReserveManagedReliantUsageResponse{ReservedSpendUsdNanos: 250_000_000}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.ReserveManagedReliantUsage(context.Background(), " rlnt_test_key ", ManagedReliantReservationRequest{
		ReservationID:         " res-123 ",
		EstimatedSpendUSD:     0.25,
		Model:                 " claude-sonnet-4-5 ",
		CanonicalModelID:      " claude-4.5-sonnet ",
		EstimatedInputTokens:  1200,
		EstimatedOutputTokens: 300,
	})
	if err != nil {
		t.Fatalf("ReserveManagedReliantUsage: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("authorization header = %q, want empty", gotAuth)
	}
	if gotManagedKey != "rlnt_test_key" {
		t.Fatalf("managed key = %q, want rlnt_test_key", gotManagedKey)
	}
	if gotReservationID != "res-123" {
		t.Fatalf("reservation ID = %q, want res-123", gotReservationID)
	}
	if gotEstimatedSpend != 0.25 {
		t.Fatalf("estimated spend = %v, want 0.25", gotEstimatedSpend)
	}
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want claude-sonnet-4-5", gotModel)
	}
	if gotCanonicalModel != "claude-4.5-sonnet" {
		t.Fatalf("canonical model = %q, want claude-4.5-sonnet", gotCanonicalModel)
	}
	if gotEstimatedInputTokens != 1200 || gotEstimatedOutputTokens != 300 {
		t.Fatalf("unexpected token payload = %d/%d, want 1200/300", gotEstimatedInputTokens, gotEstimatedOutputTokens)
	}
	if resp.GetReservedSpendUsdNanos() != 250_000_000 {
		t.Fatalf("reserved spend nanos = %d, want %d", resp.GetReservedSpendUsdNanos(), int64(250_000_000))
	}
}

func TestClient_FinalizeManagedReliantUsage_DoesNotAttachAuthorization(t *testing.T) {
	var gotAuth string
	var gotManagedKey string
	var gotReservationID string
	var gotSpend float64
	var gotModel string
	var gotCanonicalModel string
	var gotInputTokens int64
	var gotOutputTokens int64
	var gotCachedInputTokens int64
	var gotObservedCostUSD *float64

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceFinalizeManagedReliantUsageProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.FinalizeManagedReliantUsageRequest]) (*connect.Response[controlplanev1.FinalizeManagedReliantUsageResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			gotManagedKey = req.Msg.GetManagedKey()
			gotReservationID = req.Msg.GetReservationId()
			gotSpend = req.Msg.GetSpendUsd()
			gotModel = req.Msg.GetModel()
			gotCanonicalModel = req.Msg.GetCanonicalModelId()
			gotInputTokens = req.Msg.GetInputTokens()
			gotOutputTokens = req.Msg.GetOutputTokens()
			gotCachedInputTokens = req.Msg.GetCachedInputTokens()
			gotObservedCostUSD = req.Msg.ObservedCostUsd
			return connect.NewResponse(&controlplanev1.FinalizeManagedReliantUsageResponse{TotalSpendUsd: req.Msg.GetSpendUsd()}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	observedCostUSD := 1.8
	resp, err := client.FinalizeManagedReliantUsage(context.Background(), " rlnt_test_key ", ManagedReliantFinalizeRequest{
		ReservationID:     " res-123 ",
		SpendUSD:          1.75,
		Model:             " claude-sonnet-4-5 ",
		CanonicalModelID:  " claude-4.5-sonnet ",
		InputTokens:       1200,
		OutputTokens:      300,
		CachedInputTokens: 50,
		ObservedCostUSD:   &observedCostUSD,
	})
	if err != nil {
		t.Fatalf("FinalizeManagedReliantUsage: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("authorization header = %q, want empty", gotAuth)
	}
	if gotManagedKey != "rlnt_test_key" {
		t.Fatalf("managed key = %q, want rlnt_test_key", gotManagedKey)
	}
	if gotReservationID != "res-123" {
		t.Fatalf("reservation ID = %q, want res-123", gotReservationID)
	}
	if gotSpend != 1.75 {
		t.Fatalf("spend = %v, want 1.75", gotSpend)
	}
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want claude-sonnet-4-5", gotModel)
	}
	if gotCanonicalModel != "claude-4.5-sonnet" {
		t.Fatalf("canonical model = %q, want claude-4.5-sonnet", gotCanonicalModel)
	}
	if gotInputTokens != 1200 || gotOutputTokens != 300 || gotCachedInputTokens != 50 {
		t.Fatalf("unexpected token payload = %d/%d/%d, want 1200/300/50", gotInputTokens, gotOutputTokens, gotCachedInputTokens)
	}
	if gotObservedCostUSD == nil || *gotObservedCostUSD != observedCostUSD {
		t.Fatalf("observed cost = %v, want %v", gotObservedCostUSD, observedCostUSD)
	}
	if resp.GetTotalSpendUsd() != 1.75 {
		t.Fatalf("response total = %v, want 1.75", resp.GetTotalSpendUsd())
	}
}

func TestClient_ReleaseManagedReliantUsageReservation_DoesNotAttachAuthorization(t *testing.T) {
	var gotAuth string
	var gotManagedKey string
	var gotReservationID string

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceReleaseManagedReliantUsageReservationProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.ReleaseManagedReliantUsageReservationRequest]) (*connect.Response[controlplanev1.ReleaseManagedReliantUsageReservationResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			gotManagedKey = req.Msg.GetManagedKey()
			gotReservationID = req.Msg.GetReservationId()
			return connect.NewResponse(&controlplanev1.ReleaseManagedReliantUsageReservationResponse{}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.ReleaseManagedReliantUsageReservation(context.Background(), " rlnt_test_key ", " res-123 ")
	if err != nil {
		t.Fatalf("ReleaseManagedReliantUsageReservation: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("authorization header = %q, want empty", gotAuth)
	}
	if gotManagedKey != "rlnt_test_key" {
		t.Fatalf("managed key = %q, want rlnt_test_key", gotManagedKey)
	}
	if gotReservationID != "res-123" {
		t.Fatalf("reservation ID = %q, want res-123", gotReservationID)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
}

func TestClient_RecordManagedReliantUsage_DoesNotAttachAuthorization(t *testing.T) {
	var gotAuth string
	var gotManagedKey string
	var gotSpend float64
	var gotModel string
	var gotCanonicalModel string
	var gotInputTokens int64
	var gotOutputTokens int64
	var gotCachedInputTokens int64
	var gotObservedCostUSD *float64

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceRecordManagedReliantUsageProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.RecordManagedReliantUsageRequest]) (*connect.Response[controlplanev1.RecordManagedReliantUsageResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			gotManagedKey = req.Msg.GetManagedKey()
			gotSpend = req.Msg.GetSpendUsd()
			gotModel = req.Msg.GetModel()
			gotCanonicalModel = req.Msg.GetCanonicalModelId()
			gotInputTokens = req.Msg.GetInputTokens()
			gotOutputTokens = req.Msg.GetOutputTokens()
			gotCachedInputTokens = req.Msg.GetCachedInputTokens()
			gotObservedCostUSD = req.Msg.ObservedCostUsd
			return connect.NewResponse(&controlplanev1.RecordManagedReliantUsageResponse{TotalSpendUsd: req.Msg.GetSpendUsd()}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	observedCostUSD := 1.8
	resp, err := client.RecordManagedReliantUsage(context.Background(), " rlnt_test_key ", ManagedReliantUsage{
		LegacySpendUSD:    1.75,
		LegacyModel:       " claude-sonnet-4-5 ",
		CanonicalModelID:  " claude-4.5-sonnet ",
		InputTokens:       1200,
		OutputTokens:      300,
		CachedInputTokens: 50,
		ObservedCostUSD:   &observedCostUSD,
	})
	if err != nil {
		t.Fatalf("RecordManagedReliantUsage: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("authorization header = %q, want empty", gotAuth)
	}
	if gotManagedKey != "rlnt_test_key" {
		t.Fatalf("managed key = %q, want rlnt_test_key", gotManagedKey)
	}
	if gotSpend != 1.75 {
		t.Fatalf("spend = %v, want 1.75", gotSpend)
	}
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want claude-sonnet-4-5", gotModel)
	}
	if gotCanonicalModel != "claude-4.5-sonnet" {
		t.Fatalf("canonical model = %q, want claude-4.5-sonnet", gotCanonicalModel)
	}
	if gotInputTokens != 1200 || gotOutputTokens != 300 || gotCachedInputTokens != 50 {
		t.Fatalf("unexpected token payload = %d/%d/%d, want 1200/300/50", gotInputTokens, gotOutputTokens, gotCachedInputTokens)
	}
	if gotObservedCostUSD == nil || *gotObservedCostUSD != observedCostUSD {
		t.Fatalf("observed cost = %v, want %v", gotObservedCostUSD, observedCostUSD)
	}
	if resp.GetTotalSpendUsd() != 1.75 {
		t.Fatalf("response total = %v, want 1.75", resp.GetTotalSpendUsd())
	}
}

func TestClient_GetCurrentUserReliantState_AttachesAuthorization(t *testing.T) {
	var gotAuth string

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceGetCurrentUserReliantStateProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.GetCurrentUserReliantStateRequest]) (*connect.Response[controlplanev1.GetCurrentUserReliantStateResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			return connect.NewResponse(&controlplanev1.GetCurrentUserReliantStateResponse{}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	if _, err := client.GetCurrentUserReliantState(context.Background(), "Bearer sync-token"); err != nil {
		t.Fatalf("GetCurrentUserReliantState: %v", err)
	}
	if gotAuth != "Bearer sync-token" {
		t.Fatalf("authorization header = %q, want Bearer sync-token", gotAuth)
	}
}

func TestAttachAuthorization_LeavesHeaderUnsetWhenBlank(t *testing.T) {
	req := connect.NewRequest(&controlplanev1.GetCurrentUserReliantStateRequest{})
	attachAuthorization(req, "  ")
	if got := req.Header().Get("Authorization"); got != "" {
		t.Fatalf("authorization header = %q, want empty", got)
	}
}

func TestGetBaseURL_UsesDefaultWhenEnvEmpty(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "")
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")
	if got := getBaseURL(); got != defaultBaseURL {
		t.Fatalf("base url = %q, want %q", got, defaultBaseURL)
	}
}

func TestClient_CheckManagedReliantAffordability_PropagatesErrors(t *testing.T) {
	wantErr := connect.NewError(connect.CodeResourceExhausted, errors.New("insufficient wallet balance"))
	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceCheckManagedReliantAffordabilityProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.CheckManagedReliantAffordabilityRequest]) (*connect.Response[controlplanev1.CheckManagedReliantAffordabilityResponse], error) {
			return nil, wantErr
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.CheckManagedReliantAffordability(context.Background(), "rlnt_test_key", ManagedReliantAffordabilityRequest{EstimatedSpendUSD: 1.0, Model: "claude-sonnet-4-5"})
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("code = %v, want %v", connect.CodeOf(err), connect.CodeResourceExhausted)
	}
}

func TestClient_RecordManagedReliantUsage_PropagatesErrors(t *testing.T) {
	wantErr := connect.NewError(connect.CodeUnavailable, errors.New("boom"))
	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceRecordManagedReliantUsageProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.RecordManagedReliantUsageRequest]) (*connect.Response[controlplanev1.RecordManagedReliantUsageResponse], error) {
			return nil, wantErr
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.RecordManagedReliantUsage(context.Background(), "rlnt_test_key", ManagedReliantUsage{LegacySpendUSD: 1.0, LegacyModel: "claude-sonnet-4-5"})
	if err == nil {
		t.Fatal("expected error")
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code = %v, want %v", connect.CodeOf(err), connect.CodeUnavailable)
	}
}