// Copyright (c) 2025 Reliant Labs
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

const testStepKeyPlain = "rlsk_0123456789abcdef_plaintext"

func stepKeyServer(t *testing.T, handle func(w http.ResponseWriter, path string, body map[string]any)) (Service, *string) {
	t.Helper()
	var bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		handle(w, r.URL.Path, body)
	}))
	t.Cleanup(srv.Close)
	return New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")}), &bearer
}

// The client speaks LLMStepKeyInternalService's contract: its paths, the
// internal-service bearer, proto3 JSON camelCase names, int64 as a string.
func TestMintStepKey_SpeaksTheContract(t *testing.T) {
	t.Parallel()
	var path string
	var got map[string]any
	c, bearer := stepKeyServer(t, func(w http.ResponseWriter, p string, body map[string]any) {
		path, got = p, body
		_, _ = w.Write([]byte(`{"keyId":"sk_1","key":"` + testStepKeyPlain + `","baseUrl":"https://llm.example/",
			"models":["claude-sonnet-4-6"],"maxBudgetUsd":2,"expiresAt":"2026-11-22T07:00:00Z","billingOrgId":"org_1"}`))
	})
	key, err := c.MintStepKey(context.Background(), StepKeyRequest{
		RequestID: "runstep-abc", UserID: "user_ext", OrgID: "org_1", RunID: "wf", StepID: "loop/bench",
		ChatID: "chat", ProjectID: "proj", TriggerID: "trig", WorkflowName: "bench",
		CLI: "claude-code", Models: []string{"claude-sonnet-4-6"}, MaxBudgetUSD: 2, TTL: 7 * time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, "/controlplane.v1.LLMStepKeyInternalService/MintStepKey", path)
	assert.Equal(t, "Bearer signed-s", *bearer)
	assert.Equal(t, map[string]any{
		"requestId": "runstep-abc", "userId": "user_ext", "orgId": "org_1", "runId": "wf", "stepId": "loop/bench",
		"chatId": "chat", "projectId": "proj", "triggerId": "trig", "workflowName": "bench", "cli": "claude-code",
		"models": []any{"claude-sonnet-4-6"}, "maxBudgetUsd": float64(2), "ttlSeconds": "420",
	}, got)
	assert.Equal(t, StepKey{
		KeyID: "sk_1", Key: StepKeySecret(testStepKeyPlain), BaseURL: "https://llm.example",
		Models: []string{"claude-sonnet-4-6"}, MaxBudgetUSD: 2,
		ExpiresAt: time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC), BillingOrgID: "org_1",
	}, key)
	assert.Equal(t, testStepKeyPlain, key.Key.Reveal())
}

func TestRevokeStepKey_SpeaksTheContract(t *testing.T) {
	t.Parallel()
	var path string
	var got map[string]any
	c, _ := stepKeyServer(t, func(w http.ResponseWriter, p string, body map[string]any) {
		path, got = p, body
		_, _ = w.Write([]byte(`{"revoked":true,"keyId":"sk_1","spendUsd":0.42,"spendKnown":true}`))
	})
	rev, err := c.RevokeStepKey(context.Background(), "runstep-abc")
	require.NoError(t, err)
	assert.Equal(t, "/controlplane.v1.LLMStepKeyInternalService/RevokeStepKey", path)
	assert.Equal(t, map[string]any{"requestId": "runstep-abc"}, got)
	assert.Equal(t, StepKeyRevocation{Revoked: true, KeyID: "sk_1", SpendUSD: 0.42, SpendKnown: true}, rev)

	// Already revoked (or unknown): revoked=false is an answer, not an
	// error, and still carries the key's recorded spend.
	c, _ = stepKeyServer(t, func(w http.ResponseWriter, _ string, _ map[string]any) {
		_, _ = w.Write([]byte(`{"keyId":"sk_1","spendUsd":0.42,"spendKnown":true}`))
	})
	rev, err = c.RevokeStepKey(context.Background(), "runstep-abc")
	require.NoError(t, err)
	assert.Equal(t, StepKeyRevocation{KeyID: "sk_1", SpendUSD: 0.42, SpendKnown: true}, rev)
}

// A control plane without the service, one with no gateway for it, or an
// adapter that cannot sign: unsupported. Any other refusal carries its reason.
func TestStepKey_Errors(t *testing.T) {
	t.Parallel()
	req := StepKeyRequest{RequestID: "r", UserID: "u", CLI: "claude-code", MaxBudgetUSD: 1, TTL: time.Minute}

	for name, respond := range map[string]func(w http.ResponseWriter){
		"404": func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) },
		"unimplemented": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"code":"unimplemented"}`))
		},
		"not configured": func(w http.ResponseWriter) {
			w.Header().Set("X-Forge-Error-Reason", ReasonStepKeysNotConfigured)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"unavailable","message":"no gateway"}`))
		},
	} {
		c, _ := stepKeyServer(t, func(w http.ResponseWriter, _ string, _ map[string]any) { respond(w) })
		_, err := c.MintStepKey(context.Background(), req)
		assert.ErrorIs(t, err, ErrStepKeysUnsupported, name)
		_, err = c.RevokeStepKey(context.Background(), "r")
		assert.ErrorIs(t, err, ErrStepKeysUnsupported, name)
	}

	unsigned := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: "http://127.0.0.1:1"}})
	_, err := unsigned.MintStepKey(context.Background(), req)
	assert.ErrorIs(t, err, ErrStepKeysUnsupported, "no signer")

	for _, reason := range []string{ReasonStepKeyNoBillingOrg, ReasonStepKeyCreditExhausted, ReasonStepKeyNoModels} {
		c, _ := stepKeyServer(t, func(w http.ResponseWriter, _ string, _ map[string]any) {
			w.Header().Set("X-Forge-Error-Reason", reason)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"refused: ` + reason + `"}`))
		})
		_, err := c.MintStepKey(context.Background(), req)
		var refused *StepKeyError
		require.True(t, errors.As(err, &refused), "%s: %v", reason, err)
		assert.Equal(t, StepKeyError{Code: "failed_precondition", Reason: reason, Message: "refused: " + reason}, *refused)
	}

	// An answer without a usable key is an error, never an empty key.
	c, _ := stepKeyServer(t, func(w http.ResponseWriter, _ string, _ map[string]any) {
		_, _ = w.Write([]byte(`{"keyId":"sk_1","baseUrl":"https://llm.example"}`))
	})
	_, err = c.MintStepKey(context.Background(), req)
	assert.Error(t, err)

	// A request missing what the contract requires never leaves the process.
	called := false
	c, _ = stepKeyServer(t, func(http.ResponseWriter, string, map[string]any) { called = true })
	_, err = c.MintStepKey(context.Background(), StepKeyRequest{RequestID: "r", UserID: "u"})
	assert.Error(t, err)
	_, err = c.RevokeStepKey(context.Background(), " ")
	assert.Error(t, err)
	assert.False(t, called)
}

// The plaintext never prints: not with any verb, not in slog, not as part of
// the StepKey.
func TestStepKeySecretNeverPrints(t *testing.T) {
	t.Parallel()
	key := StepKey{KeyID: "sk_1", Key: StepKeySecret(testStepKeyPlain), BaseURL: "https://llm.example", BillingOrgID: "org_1"}
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	logger.Info("minted", "key", key, "secret", key.Key)
	logger.Info("minted", slog.Any("key", &key))
	for _, s := range []string{
		fmt.Sprintf("%v", key), fmt.Sprintf("%+v", key), fmt.Sprintf("%#v", key), fmt.Sprintf("%s", key.Key),
		fmt.Sprintf("%q", key.Key), fmt.Sprintf("%v", &key), fmt.Sprint(key.Key), logged.String(),
	} {
		assert.NotContains(t, s, testStepKeyPlain)
	}
	assert.Contains(t, logged.String(), "sk_1", "the id is logged")
}
