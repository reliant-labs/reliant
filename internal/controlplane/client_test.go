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