// Copyright (c) 2025 Reliant Labs
package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// protoFields is the protojson names of message's fields in a vendored proto.
func protoFields(t *testing.T, src, message string) []string {
	t.Helper()
	body := regexp.MustCompile(`(?s)message ` + message + ` \{(.*?)\n\}`).FindStringSubmatch(src)
	require.NotNil(t, body, "the vendored contract declares %s", message)
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\s+(?:repeated )?[\w.]+ (\w+) = \d+;`).FindAllStringSubmatch(body[1], -1) {
		parts := strings.Split(m[1], "_")
		for i := 1; i < len(parts); i++ {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
		out = append(out, strings.Join(parts, ""))
	}
	sort.Strings(out)
	return out
}

// The client speaks the contract control-plane serves: the path of
// LLMPriceInternalService.GetModelListPrices, the internal-service bearer,
// the request's protojson names, and int64 rates as proto3 JSON strings.
func TestGetModelListPrices_SpeaksTheVendoredContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/llm_price_internal.proto")
	require.NoError(t, err)
	src := string(raw)
	require.Contains(t, src, "package controlplane.v1;")
	require.Contains(t, src, "rpc GetModelListPrices(GetModelListPricesRequest) returns (GetModelListPricesResponse)")
	assert.Equal(t, []string{"models"}, protoFields(t, src, "GetModelListPricesRequest"))
	assert.Equal(t, []string{"model", "provider"}, protoFields(t, src, "LLMModelRef"))
	assert.Equal(t, []string{"cacheReadUsdNanosPerMillionTokens", "cacheWriteUsdNanosPerMillionTokens", "catalogKey",
		"inputUsdNanosPerMillionTokens", "model", "outputUsdNanosPerMillionTokens", "priced", "provider"}, protoFields(t, src, "LLMModelListPrice"))
	assert.Equal(t, []string{"catalogFetchedAt", "prices", "source"}, protoFields(t, src, "GetModelListPricesResponse"))

	var path, bearer string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, bearer = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"prices":[
			{"provider":"anthropic","model":"claude-sonnet-4-5","priced":true,"catalogKey":"claude-sonnet-4-5",
			 "inputUsdNanosPerMillionTokens":"3000000000","outputUsdNanosPerMillionTokens":"15000000000",
			 "cacheReadUsdNanosPerMillionTokens":"300000000","cacheWriteUsdNanosPerMillionTokens":"3750000000"},
			{"provider":"acme","model":"mystery-1"}],
			"source":"litellm_model_catalog","catalogFetchedAt":"2026-11-14T06:00:00Z"}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})

	got, asOf, err := c.GetModelListPrices(context.Background(), []ModelRef{{"anthropic", "claude-sonnet-4-5"}, {"acme", "mystery-1"}})
	require.NoError(t, err)
	assert.Equal(t, "/controlplane.v1.LLMPriceInternalService/GetModelListPrices", path)
	assert.Equal(t, "Bearer signed-s", bearer)
	assert.Equal(t, map[string]any{"models": []any{
		map[string]any{"provider": "anthropic", "model": "claude-sonnet-4-5"},
		map[string]any{"provider": "acme", "model": "mystery-1"},
	}}, body)
	assert.Equal(t, time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC), asOf)
	assert.Equal(t, []ModelListPrice{
		{Ref: ModelRef{"anthropic", "claude-sonnet-4-5"}, Priced: true, CatalogKey: "claude-sonnet-4-5",
			InputNanosPerMTok: 3e9, OutputNanosPerMTok: 15e9, CacheReadNanosPerMTok: 3e8, CacheWriteNanosPerMTok: 375e7},
		{Ref: ModelRef{"acme", "mystery-1"}},
	}, got)
}

// An answer out of request order, or of the wrong length, is no answer; a
// control plane without the service, or no identity to sign with, is
// ErrListPricesUnsupported.
func TestGetModelListPrices_Refusals(t *testing.T) {
	answer := `{"prices":[{"provider":"acme","model":"other"}]}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})
	refs := []ModelRef{{"acme", "mystery-1"}}

	_, _, err := c.GetModelListPrices(context.Background(), refs)
	require.Error(t, err, "an answer about another model")

	answer = `{"prices":[]}`
	_, _, err = c.GetModelListPrices(context.Background(), refs)
	require.Error(t, err, "a short answer")

	status, answer = http.StatusNotFound, ""
	_, _, err = c.GetModelListPrices(context.Background(), refs)
	require.ErrorIs(t, err, ErrListPricesUnsupported)

	unsigned := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}})
	_, _, err = unsigned.GetModelListPrices(context.Background(), refs)
	require.ErrorIs(t, err, ErrListPricesUnsupported)
}
