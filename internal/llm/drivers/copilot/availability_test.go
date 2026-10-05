// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
)

func TestParseEnabledModels(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"gpt-5-mini","policy":{"state":"enabled"}},
		{"id":"claude-sonnet-5","policy":{"state":"enabled"}},
		{"id":"claude-opus-4.8","policy":{"state":"disabled"}},
		{"id":"gpt-4o","policy":null},
		{"id":"gpt-4o-mini"}
	]}`)

	got, err := parseEnabledModels(body)
	if err != nil {
		t.Fatalf("parseEnabledModels: %v", err)
	}

	want := map[string]bool{
		"gpt-5-mini":      true,
		"claude-sonnet-5": true,
		"claude-opus-4.8": false, // policy disabled -> not available
		"gpt-4o":          true,  // null policy -> unrestricted
		"gpt-4o-mini":     true,  // absent policy -> unrestricted
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %v", len(got), len(want), got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("model %s: got enabled=%v, want %v", id, got[id], w)
		}
	}
}

func TestIsModelEnabledUnknownFailsOpen(t *testing.T) {
	// A model not present in the account catalog is treated as enabled so a
	// stale/renamed catalog never hides a Reliant-mapped model.
	enabled := map[string]bool{"gpt-5-mini": true}
	if _, known := enabled["some-unmapped-model"]; known {
		t.Fatal("precondition failed")
	}
}

// testdata/copilot_models.json is GET /models recorded 2026-10-04 with
// credentials stripped.
func TestParseModels_RecordedAccountPolicyAndLimits(t *testing.T) {
	body, err := os.ReadFile("testdata/copilot_models.json")
	if err != nil {
		t.Fatal(err)
	}
	enabled, limits, _, err := parseModels(body)
	if err != nil {
		t.Fatal(err)
	}
	if enabled["claude-opus-5.5"] {
		t.Error("claude-opus-5.5 is policy=disabled on the recorded account")
	}
	if !enabled["grok-4.7"] || !enabled["kimi-k3"] {
		t.Error("grok-4.7 and kimi-k3 are enabled on the recorded account")
	}
	if limits["kimi-k3"] != 1048576 || limits["grok-4.5"] != 500000 {
		t.Errorf("limits = kimi-k3:%d grok-4.5:%d", limits["kimi-k3"], limits["grok-4.5"])
	}
}

// A failed /models fetch fails OPEN and is cached briefly, so an outage costs one
// timed-out request per window instead of one per LLM call.
func TestAccountModels_FailureIsCachedAndReturned(t *testing.T) {
	token := "gho_test_failure_cache"
	key := tokenKey(token)
	availabilityMu.Lock()
	availabilityCache[key] = availabilityEntry{err: errors.New("boom"), fetchedAt: time.Now()}
	availabilityMu.Unlock()
	defer EvictAvailabilityCache(token)

	if _, err := accountModels(context.Background(), token); err == nil {
		t.Fatal("expected the cached failure to be returned without a refetch")
	}
}

func TestReportAvailability_DisabledCarriesSettingsHint(t *testing.T) {
	token := "gho_test_report"
	key := tokenKey(token)
	availabilityMu.Lock()
	availabilityCache[key] = availabilityEntry{
		enabled:   map[string]bool{"claude-opus-5": false, "gpt-5-mini": true},
		limits:    map[string]int{"gpt-5-mini": 400000},
		fetchedAt: time.Now(),
	}
	availabilityMu.Unlock()
	defer EvictAvailabilityCache(token)

	client := &CopilotClient{options: llm.DriverOptions{ApiKey: token}}
	report, err := client.ReportAvailability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	off := report.For("claude-opus-5")
	if !off.Disabled || !strings.Contains(off.Reason, "enable it in GitHub Copilot settings") {
		t.Errorf("disabled model report = %+v", off)
	}
	if got := report.For("gpt-5-mini"); got.Disabled || got.ContextWindow != 400000 {
		t.Errorf("enabled model report = %+v", got)
	}
	if report.For("not-in-catalog").Disabled {
		t.Error("a model absent from /models must stay servable")
	}
}
