// Copyright (c) 2025 Reliant Labs
package main

import (
	"regexp"
	"testing"
)

func TestProbeMatrixBuilder(t *testing.T) {
	ms := []probeModel{
		{ID: "b", Drivers: []string{"openrouter", "anthropic"}, Levels: []string{"low", "high"}},
		{ID: "a", Drivers: []string{"codex"}, Levels: nil, TempOmit: true},
	}
	providers := map[string]bool{"anthropic": true, "codex": true}
	cells := buildProbeMatrix(ms, providers, nil)

	// a@codex: basic + tool (temp omitted, no levels). b@anthropic: default+2 levels, 2 temps, tool.
	counts := map[string]int{}
	for _, c := range cells {
		counts[c.Kind]++
		if c.Provider == "openrouter" {
			t.Fatalf("uncredentialed provider leaked into matrix: %+v", c)
		}
	}
	if counts[cellBasic] != 1+3 || counts[cellTemp] != 2 || counts[cellTool] != 2 {
		t.Fatalf("unexpected counts %v", counts)
	}
	if cells[0].Model != "a" {
		t.Fatalf("matrix not sorted by model: %+v", cells[0])
	}

	filtered := buildProbeMatrix(ms, providers, regexp.MustCompile(`^b@`))
	for _, c := range filtered {
		if c.Model != "b" {
			t.Fatalf("filter leaked %+v", c)
		}
	}
	if len(filtered) != 3+2+1 {
		t.Fatalf("filtered len %d", len(filtered))
	}
}

func TestProbeTagCells(t *testing.T) {
	cells := buildTagCells(probeCoreTags, nil)
	if len(cells) != len(probeCoreTags) {
		t.Fatalf("got %d", len(cells))
	}
	if got := buildTagCells(probeCoreTags, regexp.MustCompile(`tag:fast`)); len(got) != 1 {
		t.Fatalf("filter: %d", len(got))
	}
}

func TestClassifyProbeFailure(t *testing.T) {
	cases := []struct{ err, provider, want string }{
		{"401 Unauthorized: invalid x-api-key", "anthropic", classCredential},
		{"failed to resolve model: model not found: foo", "openrouter", classCatalogMapping},
		{"openrouter 404: No endpoints found for model", "openrouter", classCatalogMapping},
		{"429 rate limit exceeded", "openrouter", classProviderSide},
		{"context deadline exceeded", "codex", classProviderSide},
		{"400 invalid_request_error: temperature is not supported", "anthropic", classDriverBug},
		{"400 thinking.type: adaptive requires effort", "anthropic", classDriverBug},
		{"failed to get LLM driver: no driver registered for xai", "xai", classExpectedUnsupported},
		{"POST openrouter: openai/gpt-6-terra is not a valid model ID 400", "openrouter", classCatalogMapping},
		{"", "x", classDriverBug},
		{"something weird", "x", classUnclassified},
	}
	for _, c := range cases {
		if got := classifyProbeFailure(c.err, c.provider); got != c.want {
			t.Errorf("classify(%q,%q)=%s want %s", c.err, c.provider, got, c.want)
		}
	}
}

func TestProbeAssertionsAndTally(t *testing.T) {
	if !assertPong("pong.") || assertPong("nope") {
		t.Fatal("assertPong")
	}
	if !assertSecretWord("It is Pineapple") || assertSecretWord("") {
		t.Fatal("assertSecretWord")
	}
	tl := tallyOutcomes([]probeOutcome{{Status: "pass"}, {Status: "fail", Class: classDriverBug}, {Status: "fail", Class: classDriverBug}})
	if tl.Total != 3 || tl.Passed != 1 || tl.ByClass[classDriverBug] != 2 {
		t.Fatalf("%+v", tl)
	}
}

func TestReasoningMatrixLowHighDefault(t *testing.T) {
	ms := []probeModel{
		{ID: "think", Drivers: []string{"anthropic", "openrouter"}, Levels: []string{"low", "medium", "high"}},
		{ID: "one", Drivers: []string{"anthropic"}, Levels: []string{"high"}},
		{ID: "plain", Drivers: []string{"anthropic"}},
	}
	cells := buildReasoningMatrix(ms, map[string]bool{"anthropic": true}, nil)
	got := map[string][]string{}
	for _, c := range cells {
		if c.Kind != cellReasoning {
			t.Fatalf("kind %s", c.Kind)
		}
		got[c.Model] = append(got[c.Model], c.Level)
	}
	if want := []string{"", "low", "high"}; len(got["think"]) != 3 || got["think"][1] != want[1] || got["think"][2] != want[2] {
		t.Fatalf("think levels %v", got["think"])
	}
	if len(got["one"]) != 2 {
		t.Fatalf("single-level model should get default+that level, got %v", got["one"])
	}
	if _, ok := got["plain"]; ok {
		t.Fatal("non-reasoning model must be skipped")
	}
}

func TestAssertReasoningAnswer(t *testing.T) {
	for _, ok := range []string{"104104", "104,104", " 104 104\n", "The answer is 104104."} {
		if !assertReasoningAnswer(ok) {
			t.Errorf("%q should pass", ok)
		}
	}
	for _, bad := range []string{"", "104105", "1041"} {
		if assertReasoningAnswer(bad) {
			t.Errorf("%q should fail", bad)
		}
	}
}

// A cell pinned to one provider but carried by another driver is a failure,
// not a pass. Before this check the probe recorded the registry's pick, so a
// stale copilot allowlist that re-routed nine "copilot" models to other
// providers produced an all-green copilot table.
func TestServedElsewhere(t *testing.T) {
	cases := []struct {
		provider, servedBy string
		want               bool
	}{
		{"copilot", "copilot", false},
		{"copilot", "anthropic", true},
		{"copilot", "antigravity", true},
		{"anthropic", "claude-code", false}, // Claude OAuth runs on the anthropic driver id
		{"codex", "claude-code", true},
		{"copilot", "", false}, // nothing recorded: nothing to judge
		{"", "copilot", false},
	}
	for _, tc := range cases {
		if got := servedElsewhere(tc.provider, tc.servedBy); got != tc.want {
			t.Errorf("servedElsewhere(%q, %q) = %v, want %v", tc.provider, tc.servedBy, got, tc.want)
		}
	}
}
