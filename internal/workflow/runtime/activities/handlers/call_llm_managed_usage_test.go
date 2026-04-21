package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

type fakeManagedUsageControlPlaneClient struct {
	calls      int
	managedKey string
	usage      controlplane.ManagedReliantUsage
	err        error
}

func (f *fakeManagedUsageControlPlaneClient) GetCurrentUserReliantState(context.Context, string) (*controlplanev1.GetCurrentUserReliantStateResponse, error) {
	panic("unexpected call")
}

func (f *fakeManagedUsageControlPlaneClient) RepairCurrentUserReliantAccess(context.Context, string) (*controlplanev1.RepairCurrentUserReliantAccessResponse, error) {
	panic("unexpected call")
}

func (f *fakeManagedUsageControlPlaneClient) RotateCurrentUserReliantAccess(context.Context, string, string) (*controlplanev1.RotateCurrentUserReliantAccessResponse, error) {
	panic("unexpected call")
}

func (f *fakeManagedUsageControlPlaneClient) RecordManagedReliantUsage(_ context.Context, managedKey string, usage controlplane.ManagedReliantUsage) (*controlplanev1.RecordManagedReliantUsageResponse, error) {
	f.calls++
	f.managedKey = managedKey
	f.usage = usage
	if f.err != nil {
		return nil, f.err
	}
	return &controlplanev1.RecordManagedReliantUsageResponse{TotalSpendUsd: usage.LegacySpendUSD}, nil
}

type fakeManagedUsageDriver struct {
	name  string
	model models.Model
}

func (f fakeManagedUsageDriver) Name() string { return f.name }
func (f fakeManagedUsageDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	panic("unexpected call")
}
func (f fakeManagedUsageDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	panic("unexpected call")
}
func (f fakeManagedUsageDriver) Model() models.Model               { return f.model }
func (f fakeManagedUsageDriver) ValidateKey(context.Context) error { return nil }

func TestEstimateManagedReliantSpendUSD_UsesUsageCostWhenAvailable(t *testing.T) {
	model := models.Model{CostPer1MIn: 1.5, CostPer1MOut: 7.5}
	usage := llm.TokenUsage{TokenCount: 1000, InputTokens: 500, OutputTokens: 500, Cost: 0.42}
	if got := estimateManagedReliantSpendUSD(model, usage); got != 0.42 {
		t.Fatalf("estimateManagedReliantSpendUSD = %v, want 0.42", got)
	}
}

func TestEstimateManagedReliantSpendUSD_ComputesFromTokens(t *testing.T) {
	model := models.Model{CostPer1MIn: 2, CostPer1MOut: 8}
	usage := llm.TokenUsage{InputTokens: 2000, OutputTokens: 3000}
	want := (float64(2000)*2 + float64(3000)*8) / 1_000_000
	if got := estimateManagedReliantSpendUSD(model, usage); got != want {
		t.Fatalf("estimateManagedReliantSpendUSD = %v, want %v", got, want)
	}
}

func TestEstimateManagedReliantSpendUSD_FallsBackToTokenCount(t *testing.T) {
	model := models.Model{CostPer1MIn: 4}
	usage := llm.TokenUsage{TokenCount: 2500}
	want := float64(2500) * 4 / 1_000_000
	if got := estimateManagedReliantSpendUSD(model, usage); got != want {
		t.Fatalf("estimateManagedReliantSpendUSD = %v, want %v", got, want)
	}
}

func TestRecordManagedReliantUsage_SendsManagedReliantSpend(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	requireNoError(t, repo.SetProviderAPIKey(ctx, "user-1", "reliant", "rlnt_managed_key"))

	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	driver := fakeManagedUsageDriver{
		name:  "reliant",
		model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8},
	}
	usage := llm.TokenUsage{InputTokens: 1000, OutputTokens: 2000, CachedInputTokens: 150}

	activity.recordManagedReliantUsage(ctx, &db.Chat{ID: "chat-1", UserID: "user-1"}, driver, usage, nil)

	if cp.calls != 1 {
		t.Fatalf("record calls = %d, want 1", cp.calls)
	}
	if cp.managedKey != "rlnt_managed_key" {
		t.Fatalf("managed key = %q, want rlnt_managed_key", cp.managedKey)
	}
	wantSpend := estimateManagedReliantSpendUSD(driver.model, usage)
	if cp.usage.LegacySpendUSD != wantSpend {
		t.Fatalf("spend usd = %v, want %v", cp.usage.LegacySpendUSD, wantSpend)
	}
	if cp.usage.LegacyModel != "claude-sonnet-4-5" {
		t.Fatalf("legacy model = %q, want claude-sonnet-4-5", cp.usage.LegacyModel)
	}
	if cp.usage.CanonicalModelID != "claude-sonnet-4-5" {
		t.Fatalf("canonical model = %q, want claude-sonnet-4-5", cp.usage.CanonicalModelID)
	}
	if cp.usage.InputTokens != 1000 || cp.usage.OutputTokens != 2000 || cp.usage.CachedInputTokens != 150 {
		t.Fatalf("unexpected token usage payload: %+v", cp.usage)
	}
	if cp.usage.ObservedCostUSD == nil || *cp.usage.ObservedCostUSD != wantSpend {
		t.Fatalf("observed cost = %v, want %v", cp.usage.ObservedCostUSD, wantSpend)
	}
}

func TestRecordManagedReliantUsage_SkipsNonManagedOrFailedCalls(t *testing.T) {
	t.Run("skips non reliant driver", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()
		requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "rlnt_managed_key"))
		cp := &fakeManagedUsageControlPlaneClient{}
		activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
		activity.recordManagedReliantUsage(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "openai"}, llm.TokenUsage{Cost: 1}, nil)
		if cp.calls != 0 {
			t.Fatalf("record calls = %d, want 0", cp.calls)
		}
	})

	t.Run("skips non managed key", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()
		requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "sk-test"))
		cp := &fakeManagedUsageControlPlaneClient{}
		activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
		activity.recordManagedReliantUsage(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant"}, llm.TokenUsage{Cost: 1}, nil)
		if cp.calls != 0 {
			t.Fatalf("record calls = %d, want 0", cp.calls)
		}
	})

	t.Run("skips errored stream", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()
		requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "rlnt_managed_key"))
		cp := &fakeManagedUsageControlPlaneClient{}
		activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
		activity.recordManagedReliantUsage(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant"}, llm.TokenUsage{Cost: 1}, errors.New("boom"))
		if cp.calls != 0 {
			t.Fatalf("record calls = %d, want 0", cp.calls)
		}
	})
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}