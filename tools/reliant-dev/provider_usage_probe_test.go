// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactJSONKeepsShapeAndDropsIdentity(t *testing.T) {
	in := `{
	  "account": {"uuid": "6f0d3c2a-7b1e-4a5c-9e8d-3f2b1a0c4d5e", "email": "real.person@corp.com", "full_name": "Real Person", "has_claude_max": true},
	  "organization": {"uuid": "11111111-2222-4333-8444-555555555555", "name": "Real Person's Org", "organization_type": "claude_max", "rate_limit_tier": "default_claude_max_20x"},
	  "five_hour": {"utilization": 42.5, "resets_at": "2026-09-02T22:00:00+00:00"},
	  "limits": [{"kind": "weekly_scoped", "scope": {"model": {"display_name": "Fable"}}, "percent": 99}],
	  "again": "real.person@corp.com", "nothing": null
	}`
	out, err := redactJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, leaked := range []string{"real.person", "Real Person", "6f0d3c2a", "11111111-2222", "corp.com"} {
		if strings.Contains(s, leaked) {
			t.Errorf("redacted output still contains %q:\n%s", leaked, s)
		}
	}
	for _, kept := range []string{`"organization_type": "claude_max"`, `"rate_limit_tier": "default_claude_max_20x"`, `"utilization": 42.5`, `"resets_at": "2026-09-02T22:00:00+00:00"`, `"kind": "weekly_scoped"`, `"has_claude_max": true`, `"percent": 99`, `"nothing": null`} {
		if !strings.Contains(s, kept) {
			t.Errorf("redaction dropped %q:\n%s", kept, s)
		}
	}
	// The same value maps to the same placeholder.
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["again"] != m["account"].(map[string]any)["email"] {
		t.Errorf("repeated value must reuse its placeholder:\n%s", s)
	}
	if _, err := redactJSON([]byte("<html>")); err == nil {
		t.Error("non-JSON must be an error, never echoed")
	}
}

func TestProviderUsageProbeNeedsOwnCredential(t *testing.T) {
	t.Setenv(probeClaudeTokenEnv, "")
	t.Setenv(probeCodexTokenEnv, "")
	var out, errOut bytes.Buffer
	for _, p := range []string{"claude", "codex"} {
		if err := runProviderUsageProbe(context.Background(), &out, &errOut, p, ""); err == nil || !strings.Contains(err.Error(), "export") {
			t.Errorf("%s: err = %v, want an instruction to export a credential", p, err)
		}
	}
	if err := runProviderUsageProbe(context.Background(), &out, &errOut, "gemini", ""); err == nil {
		t.Error("unknown provider must fail")
	}
	if out.Len() != 0 {
		t.Errorf("nothing may be printed without a credential: %q", out.String())
	}
}
