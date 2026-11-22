package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// LLMStepKeyInternalService (control-plane), withheld from the exported
// contract like LLMPriceInternalService: Connect unary over plain HTTP+JSON,
// signed with this process's internal-service identity. A step key is a
// short-lived `rlsk_` bearer for the LLM gateway, capped to one workflow run
// step's models, budget and lifetime and billed to the run's org.
const (
	mintStepKeyPath   = "/controlplane.v1.LLMStepKeyInternalService/MintStepKey"
	revokeStepKeyPath = "/controlplane.v1.LLMStepKeyInternalService/RevokeStepKey"
)

// Reasons control-plane refuses a step key for (x-forge-error-reason).
const (
	// ReasonStepKeyNoBillingOrg: the run's owner may not bill the run's org.
	ReasonStepKeyNoBillingOrg = "no_billing_org"
	// ReasonStepKeyCreditExhausted: the org's Reliant credits are spent.
	ReasonStepKeyCreditExhausted = "reliant_credit_exhausted"
	// ReasonStepKeyNoModels: none of the requested models is one the gateway
	// serves for the CLI's protocol.
	ReasonStepKeyNoModels = "llm_step_key_no_models"
	// ReasonStepKeysNotConfigured: the deployment has no gateway to issue
	// step keys for. Answered as ErrStepKeysUnsupported.
	ReasonStepKeysNotConfigured = "llm_step_keys_not_configured"
)

// ErrStepKeysUnsupported is a control plane without LLMStepKeyInternalService
// (or one that answers that step keys are not configured), or an adapter with
// no internal-service identity to call it with.
var ErrStepKeysUnsupported = errors.New("controlplane: LLM gateway step keys are not available")

// StepKeyError is a refusal control-plane returned: Code is the Connect code,
// Reason its x-forge-error-reason (ReasonStepKey*), Message its client-safe
// message.
type StepKeyError struct {
	Code, Reason, Message string
}

func (e *StepKeyError) Error() string {
	if e.Message == "" {
		return "control plane refused the step key (" + e.Code + ")"
	}
	return e.Message
}

// StepKeyRequest asks for one run step's key. RequestID is the idempotency
// key (the step's transport id): a mint repeated with it rotates the key, and
// the revoke names it.
type StepKeyRequest struct {
	RequestID string
	// UserID is the run owner's external id; OrgID the run's org ("" is the
	// owner's current billing org).
	UserID, OrgID string
	// Attribution, recorded on the key and on every call it makes.
	RunID, StepID, ChatID, ProjectID, TriggerID, WorkflowName string
	// CLI names the consumer ("claude-code"), which decides the protocol the
	// key may call.
	CLI string
	// Models the key may call; empty is every model of the CLI's protocol.
	Models []string
	// MaxBudgetUSD caps what it may spend; TTL how long it lives.
	MaxBudgetUSD float64
	TTL          time.Duration
}

// StepKeySecret is a step key's plaintext. It prints as "[redacted]" in every
// format and in slog; Reveal is the one way to read it.
type StepKeySecret string

// Reveal is the plaintext, for the one place that hands it to the machine.
func (s StepKeySecret) Reveal() string { return string(s) }

// String never prints the key.
func (s StepKeySecret) String() string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// GoString is String.
func (s StepKeySecret) GoString() string { return s.String() }

// Format makes every verb print String.
func (s StepKeySecret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// LogValue is String.
func (s StepKeySecret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// StepKey is a minted step key.
type StepKey struct {
	KeyID string
	// Key is the plaintext, returned once by control-plane and never stored.
	Key StepKeySecret
	// BaseURL is the gateway root (no /v1) the CLI calls.
	BaseURL      string
	Models       []string
	MaxBudgetUSD float64
	ExpiresAt    time.Time
	BillingOrgID string
}

// LogValue is the key's attributes without its plaintext.
func (k StepKey) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("key_id", k.KeyID),
		slog.String("billing_org_id", k.BillingOrgID),
		slog.Any("models", k.Models),
		slog.Float64("max_budget_usd", k.MaxBudgetUSD),
		slog.Time("expires_at", k.ExpiresAt),
	)
}

// StepKeyRevocation is what revoking a step key found.
type StepKeyRevocation struct {
	// Revoked is false when there was no live key for the request id.
	Revoked bool
	KeyID   string
	// SpendUSD is the spend control-plane metered for the key as of the
	// revoke. SpendKnown false: a call was still pending settlement or the
	// read failed, so SpendUSD is not final (it may be low, or zero).
	SpendUSD   float64
	SpendKnown bool
}

// MintStepKey is Service.MintStepKey.
func (c *client) MintStepKey(ctx context.Context, r StepKeyRequest) (StepKey, error) {
	if strings.TrimSpace(r.RequestID) == "" || strings.TrimSpace(r.UserID) == "" {
		return StepKey{}, errors.New("controlplane: a step key needs a request id and the run's owner")
	}
	if r.MaxBudgetUSD <= 0 || r.TTL <= 0 {
		return StepKey{}, errors.New("controlplane: a step key needs a positive budget and lifetime")
	}
	in := map[string]any{
		"requestId":    r.RequestID,
		"userId":       r.UserID,
		"orgId":        r.OrgID,
		"runId":        r.RunID,
		"stepId":       r.StepID,
		"chatId":       r.ChatID,
		"projectId":    r.ProjectID,
		"triggerId":    r.TriggerID,
		"workflowName": r.WorkflowName,
		"cli":          r.CLI,
		"models":       nonNilStrings(r.Models),
		"maxBudgetUsd": r.MaxBudgetUSD,
		// int64 travels as a JSON string (proto3 JSON).
		"ttlSeconds": strconv.FormatInt(int64((r.TTL+time.Second-1)/time.Second), 10),
	}
	var out struct {
		KeyID        string     `json:"keyId"`
		Key          string     `json:"key"`
		BaseURL      string     `json:"baseUrl"`
		Models       []string   `json:"models"`
		MaxBudgetUSD float64    `json:"maxBudgetUsd"`
		ExpiresAt    *time.Time `json:"expiresAt"`
		BillingOrgID string     `json:"billingOrgId"`
	}
	if err := c.stepKeyCall(ctx, mintStepKeyPath, in, &out); err != nil {
		return StepKey{}, err
	}
	if out.Key == "" || out.BaseURL == "" || out.KeyID == "" {
		// A key the machine cannot use; revoke what may exist.
		return StepKey{}, fmt.Errorf("controlplane: step key answer is missing its key, id or gateway url")
	}
	key := StepKey{
		KeyID: out.KeyID, Key: StepKeySecret(out.Key), BaseURL: strings.TrimRight(out.BaseURL, "/"),
		Models: out.Models, MaxBudgetUSD: out.MaxBudgetUSD, BillingOrgID: out.BillingOrgID,
	}
	if out.ExpiresAt != nil {
		key.ExpiresAt = out.ExpiresAt.UTC()
	}
	return key, nil
}

// RevokeStepKey is Service.RevokeStepKey.
func (c *client) RevokeStepKey(ctx context.Context, requestID string) (StepKeyRevocation, error) {
	if strings.TrimSpace(requestID) == "" {
		return StepKeyRevocation{}, errors.New("controlplane: revoking a step key needs its request id")
	}
	var out struct {
		Revoked    bool    `json:"revoked"`
		KeyID      string  `json:"keyId"`
		SpendUSD   float64 `json:"spendUsd"`
		SpendKnown bool    `json:"spendKnown"`
	}
	if err := c.stepKeyCall(ctx, revokeStepKeyPath, map[string]any{"requestId": requestID}, &out); err != nil {
		return StepKeyRevocation{}, err
	}
	return StepKeyRevocation{Revoked: out.Revoked, KeyID: out.KeyID, SpendUSD: out.SpendUSD, SpendKnown: out.SpendKnown}, nil
}

// stepKeyCall is projectOrgMoveCall with step-key errors: unsupported (no
// service, no signer, or not configured) as ErrStepKeysUnsupported, any other
// refusal as a *StepKeyError.
func (c *client) stepKeyCall(ctx context.Context, path string, in, out any) error {
	err := c.projectOrgMoveCall(ctx, path, in, out)
	if errors.Is(err, ErrProjectOrgMoveUnsupported) {
		return ErrStepKeysUnsupported
	}
	var refused *ProjectOrgMoveError
	if errors.As(err, &refused) {
		if refused.Reason == ReasonStepKeysNotConfigured {
			return ErrStepKeysUnsupported
		}
		return &StepKeyError{Code: refused.Code, Reason: refused.Reason, Message: refused.Message}
	}
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
