package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

type fakeManagedUsageControlPlaneClient struct {
	reserveCalls  int
	finalizeCalls int
	releaseCalls  int
	managedKey    string
	reservationID string
	reservation   controlplane.ManagedReliantReservationRequest
	finalize      controlplane.ManagedReliantFinalizeRequest
	ctxUserID     string
	err           error
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

func (f *fakeManagedUsageControlPlaneClient) CheckManagedReliantAffordability(context.Context, string, controlplane.ManagedReliantAffordabilityRequest) (*controlplanev1.CheckManagedReliantAffordabilityResponse, error) {
	panic("unexpected call")
}

func (f *fakeManagedUsageControlPlaneClient) ReserveManagedReliantUsage(ctx context.Context, managedKey string, request controlplane.ManagedReliantReservationRequest) (*controlplanev1.ReserveManagedReliantUsageResponse, error) {
	f.reserveCalls++
	f.managedKey = managedKey
	f.reservationID = request.ReservationID
	f.reservation = request
	if userID, ok := ctx.Value(auth.UserIDContextKey).(string); ok {
		f.ctxUserID = userID
	}
	if f.err != nil {
		return nil, f.err
	}
	return &controlplanev1.ReserveManagedReliantUsageResponse{ReservedSpendUsdNanos: 250_000_000, WalletBalanceAfterReservationUsdNanos: 750_000_000}, nil
}

func (f *fakeManagedUsageControlPlaneClient) FinalizeManagedReliantUsage(ctx context.Context, managedKey string, request controlplane.ManagedReliantFinalizeRequest) (*controlplanev1.FinalizeManagedReliantUsageResponse, error) {
	f.finalizeCalls++
	f.managedKey = managedKey
	f.reservationID = request.ReservationID
	f.finalize = request
	if userID, ok := ctx.Value(auth.UserIDContextKey).(string); ok {
		f.ctxUserID = userID
	}
	if f.err != nil {
		return nil, f.err
	}
	return &controlplanev1.FinalizeManagedReliantUsageResponse{TotalSpendUsd: request.SpendUSD}, nil
}

func (f *fakeManagedUsageControlPlaneClient) ReleaseManagedReliantUsageReservation(ctx context.Context, managedKey, reservationID string) (*controlplanev1.ReleaseManagedReliantUsageReservationResponse, error) {
	f.releaseCalls++
	f.managedKey = managedKey
	f.reservationID = reservationID
	if userID, ok := ctx.Value(auth.UserIDContextKey).(string); ok {
		f.ctxUserID = userID
	}
	if f.err != nil {
		return nil, f.err
	}
	return &controlplanev1.ReleaseManagedReliantUsageReservationResponse{}, nil
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

func TestReserveManagedReliantUsage_SendsManagedReliantEstimate(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	requireNoError(t, repo.SetProviderAPIKey(ctx, "user-1", "reliant", "rlnt_managed_key"))

	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	driver := fakeManagedUsageDriver{
		name:  "reliant",
		model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8, DefaultMaxTokens: 4096},
	}
	history := []message.Message{{TokenCount: 1000, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	prompts := []string{"You are helpful."}

	reservation, err := activity.reserveManagedReliantUsage(ctx, &db.Chat{ID: "chat-1", UserID: "user-1"}, driver, history, prompts, "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("reserveManagedReliantUsage: %v", err)
	}
	if reservation == nil {
		t.Fatal("expected reservation")
	}
	if cp.reserveCalls != 1 {
		t.Fatalf("reserve calls = %d, want 1", cp.reserveCalls)
	}
	if cp.managedKey != "rlnt_managed_key" {
		t.Fatalf("managed key = %q, want rlnt_managed_key", cp.managedKey)
	}
	if cp.ctxUserID != "user-1" {
		t.Fatalf("ctx user ID = %q, want user-1", cp.ctxUserID)
	}
	if cp.reservation.ReservationID == "" {
		t.Fatal("expected reservation id")
	}
	if cp.reservation.EstimatedInputTokens <= 1000 {
		t.Fatalf("estimated input tokens = %d, want > 1000", cp.reservation.EstimatedInputTokens)
	}
	if cp.reservation.EstimatedOutputTokens != 4096 {
		t.Fatalf("estimated output tokens = %d, want 4096", cp.reservation.EstimatedOutputTokens)
	}
	if cp.reservation.EstimatedSpendUSD <= 0 {
		t.Fatalf("estimated spend usd = %v, want > 0", cp.reservation.EstimatedSpendUSD)
	}
	if cp.reservation.CanonicalModelID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("canonical model id = %q, want anthropic/claude-sonnet-4-5", cp.reservation.CanonicalModelID)
	}
}

func TestReserveManagedReliantUsage_PropagatesErrors(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	requireNoError(t, repo.SetProviderAPIKey(ctx, "user-1", "reliant", "rlnt_managed_key"))
	cp := &fakeManagedUsageControlPlaneClient{err: errors.New("boom")}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	driver := fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8, DefaultMaxTokens: 4096}}
	_, err := activity.reserveManagedReliantUsage(ctx, &db.Chat{ID: "chat-1", UserID: "user-1"}, driver, []message.Message{{TokenCount: 1000}}, []string{"You are helpful."}, "anthropic/claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompleteManagedReliantReservation_FinalizesManagedSpend(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	requireNoError(t, repo.SetProviderAPIKey(ctx, "user-1", "reliant", "rlnt_managed_key"))

	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	driver := fakeManagedUsageDriver{
		name:  "reliant",
		model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8},
	}
	usage := llm.TokenUsage{InputTokens: 1000, OutputTokens: 2000, CachedInputTokens: 150}
	reservation := &ManagedReliantReservation{ManagedKey: "rlnt_managed_key", ReservationID: "res-123", ModelID: "claude-sonnet-4-5", CanonicalModelID: "anthropic/claude-sonnet-4-5"}

	activity.completeManagedReliantReservation(ctx, &db.Chat{ID: "chat-1", UserID: "user-1"}, driver, usage, nil, reservation)

	if cp.finalizeCalls != 1 {
		t.Fatalf("finalize calls = %d, want 1", cp.finalizeCalls)
	}
	if cp.releaseCalls != 0 {
		t.Fatalf("release calls = %d, want 0", cp.releaseCalls)
	}
	wantSpend := estimateManagedReliantSpendUSD(driver.model, usage)
	if cp.finalize.SpendUSD != wantSpend {
		t.Fatalf("spend usd = %v, want %v", cp.finalize.SpendUSD, wantSpend)
	}
	if cp.finalize.ObservedCostUSD == nil || *cp.finalize.ObservedCostUSD != wantSpend {
		t.Fatalf("observed cost = %v, want %v", cp.finalize.ObservedCostUSD, wantSpend)
	}
	if cp.finalize.InputTokens != 1000 || cp.finalize.OutputTokens != 2000 || cp.finalize.CachedInputTokens != 150 {
		t.Fatalf("unexpected token usage payload: %+v", cp.finalize)
	}
	if cp.finalize.CanonicalModelID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("canonical model id = %q, want anthropic/claude-sonnet-4-5", cp.finalize.CanonicalModelID)
	}
}

func TestCompleteManagedReliantReservation_ReleasesOnFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "rlnt_managed_key"))
	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	reservation := &ManagedReliantReservation{ManagedKey: "rlnt_managed_key", ReservationID: "res-err", ModelID: "claude-sonnet-4-5", CanonicalModelID: "anthropic/claude-sonnet-4-5"}
	activity.completeManagedReliantReservation(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5")}}, llm.TokenUsage{Cost: 1}, errors.New("boom"), reservation)
	if cp.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", cp.releaseCalls)
	}
	if cp.finalizeCalls != 0 {
		t.Fatalf("finalize calls = %d, want 0", cp.finalizeCalls)
	}
}

func TestCompleteManagedReliantReservation_ReleasesZeroSpend(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "rlnt_managed_key"))
	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	reservation := &ManagedReliantReservation{ManagedKey: "rlnt_managed_key", ReservationID: "res-zero", ModelID: "claude-sonnet-4-5", CanonicalModelID: "anthropic/claude-sonnet-4-5"}
	activity.completeManagedReliantReservation(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5")}}, llm.TokenUsage{}, nil, reservation)
	if cp.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", cp.releaseCalls)
	}
}

func TestReserveManagedReliantUsage_SkipsNonManagedInputs(t *testing.T) {
	t.Run("non reliant driver", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()
		requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "rlnt_managed_key"))
		cp := &fakeManagedUsageControlPlaneClient{}
		activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
		reservation, err := activity.reserveManagedReliantUsage(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "openai"}, nil, nil, "")
		if err != nil || reservation != nil {
			t.Fatalf("expected nil reservation, got %#v err=%v", reservation, err)
		}
		if cp.reserveCalls != 0 {
			t.Fatalf("reserve calls = %d, want 0", cp.reserveCalls)
		}
	})

	t.Run("non managed key", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()
		requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "sk-test"))
		cp := &fakeManagedUsageControlPlaneClient{}
		activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
		reservation, err := activity.reserveManagedReliantUsage(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5")}}, nil, nil, "anthropic/claude-sonnet-4-5")
		if err != nil || reservation != nil {
			t.Fatalf("expected nil reservation, got %#v err=%v", reservation, err)
		}
		if cp.reserveCalls != 0 {
			t.Fatalf("reserve calls = %d, want 0", cp.reserveCalls)
		}
	})
}

func TestReserveManagedReliantUsageForChat_UsesOverrideClient(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	requireNoError(t, repo.SetProviderAPIKey(ctx, "user-1", "reliant", "rlnt_managed_key"))
	activity := &CallLLMActivity{repo: repo}
	cp := &fakeManagedUsageControlPlaneClient{}
	driver := fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8, DefaultMaxTokens: 4096}}
	reservation, err := activity.ReserveManagedReliantUsageForChat(ctx, &db.Chat{ID: "chat-1", UserID: "user-1"}, driver, []message.Message{{TokenCount: 1000}}, []string{"You are helpful."}, cp)
	if err != nil || reservation == nil {
		t.Fatalf("reservation=%#v err=%v", reservation, err)
	}
	if cp.reserveCalls != 1 {
		t.Fatalf("reserve calls = %d, want 1", cp.reserveCalls)
	}
	if cp.reservation.CanonicalModelID != "claude-sonnet-4-5" {
		t.Fatalf("canonical model id = %q, want claude-sonnet-4-5", cp.reservation.CanonicalModelID)
	}
}

func TestCompleteManagedReliantReservationForChat_UsesOverrideClient(t *testing.T) {
	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{}
	activity.CompleteManagedReliantReservationForChat(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8}}, llm.TokenUsage{InputTokens: 1000, OutputTokens: 2000}, nil, &ManagedReliantReservation{ManagedKey: "rlnt_managed_key", ReservationID: "res-123", ModelID: "claude-sonnet-4-5", CanonicalModelID: "anthropic/claude-sonnet-4-5"}, cp)
	if cp.finalizeCalls != 1 {
		t.Fatalf("finalize calls = %d, want 1", cp.finalizeCalls)
	}
	if cp.finalize.CanonicalModelID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("canonical model id = %q, want anthropic/claude-sonnet-4-5", cp.finalize.CanonicalModelID)
	}
}

func TestCompleteManagedReliantReservation_FallsBackToLegacyModelIDForCanonicalModel(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	requireNoError(t, repo.SetProviderAPIKey(context.Background(), "user-1", "reliant", "rlnt_managed_key"))
	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	reservation := &ManagedReliantReservation{ManagedKey: "rlnt_managed_key", ReservationID: "res-fallback", ModelID: "claude-sonnet-4-5"}
	activity.completeManagedReliantReservation(context.Background(), &db.Chat{ID: "chat-1", UserID: "user-1"}, fakeManagedUsageDriver{name: "reliant", model: models.Model{ID: models.ModelID("claude-sonnet-4-5"), CostPer1MIn: 2, CostPer1MOut: 8}}, llm.TokenUsage{InputTokens: 1, OutputTokens: 1}, nil, reservation)
	if cp.finalizeCalls != 1 {
		t.Fatalf("finalize calls = %d, want 1", cp.finalizeCalls)
	}
	if cp.finalize.CanonicalModelID != "claude-sonnet-4-5" {
		t.Fatalf("canonical model id = %q, want claude-sonnet-4-5", cp.finalize.CanonicalModelID)
	}
}

func TestManagedReliantCanonicalModelID_StripsDriverSuffix(t *testing.T) {
	got := managedReliantCanonicalModelID("claude-sonnet-4-5@anthropic", "")
	if got != "claude-sonnet-4-5" {
		t.Fatalf("managedReliantCanonicalModelID = %q, want claude-sonnet-4-5", got)
	}
}

func TestManagedReliantCanonicalModelID_UsesRequestedModelFallback(t *testing.T) {
	got := managedReliantCanonicalModelID("", "claude-4.5-sonnet", "claude-sonnet-4-5@anthropic")
	if got != "claude-4.5-sonnet" {
		t.Fatalf("managedReliantCanonicalModelID = %q, want claude-4.5-sonnet", got)
	}
}

func TestManagedReliantCanonicalModelID_StripsProviderPrefix(t *testing.T) {
	got := managedReliantCanonicalModelID("anthropic/claude-sonnet-4-5", "")
	if got != "claude-sonnet-4-5" {
		t.Fatalf("managedReliantCanonicalModelID = %q, want claude-sonnet-4-5", got)
	}
}

func TestReserveManagedReliantUsage_FallsBackToDriverModelWithoutSuffix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	requireNoError(t, repo.SetProviderAPIKey(ctx, "user-1", "reliant", "rlnt_managed_key"))
	cp := &fakeManagedUsageControlPlaneClient{}
	activity := &CallLLMActivity{repo: repo, controlPlaneClient: cp}
	driver := fakeManagedUsageDriver{
		name:  "reliant",
		model: models.Model{ID: models.ModelID("claude-sonnet-4-5@anthropic"), CostPer1MIn: 2, CostPer1MOut: 8, DefaultMaxTokens: 4096},
	}

	reservation, err := activity.reserveManagedReliantUsage(ctx, &db.Chat{ID: "chat-1", UserID: "user-1"}, driver, []message.Message{{TokenCount: 1000}}, []string{"You are helpful."}, "")
	if err != nil {
		t.Fatalf("reserveManagedReliantUsage: %v", err)
	}
	if reservation == nil {
		t.Fatal("expected reservation")
	}
	if cp.reservation.CanonicalModelID != "claude-sonnet-4-5" {
		t.Fatalf("canonical model id = %q, want claude-sonnet-4-5", cp.reservation.CanonicalModelID)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}