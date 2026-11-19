package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// The claim block tells the agent what the person gets once they claim:
// where it is managed, what the free plan covers, and how to redeploy once
// this machine's claimable credential is revoked. The --json instructions an
// agent relays say the same.
func TestClaimBlockSaysWhatTheClaimerGets(t *testing.T) {
	prev := claimableNow
	now := time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC)
	claimableNow = func() time.Time { return now }
	defer func() { claimableNow = prev }()

	var b strings.Builder
	printClaimBlock(&b, "prod", &deployClaimJSON{
		State: "live", Published: true, URL: "https://amber-quokka.reliantpreview.dev", ClaimURL: "https://app.reliant.dev/claim#rcl_9f3KQe",
		ExpiresAt: now.Add(72 * time.Hour), PurgeAfter: now.Add(240 * time.Hour),
		ConsoleURL: "https://forge.reliantlabs.io/forge/p/shop?env=prod",
	})
	text := b.String()
	for _, want := range []string{
		"ONCE CLAIMED it is theirs, in their own Reliant account (free to sign up).",
		"Its releases, logs and dashboards: https://forge.reliantlabs.io/forge/p/shop?env=prod (signed in)",
		"The free plan includes one static site; a backend or database needs billing on their account.",
		"revokes this machine's claimable credential",
		Name() + " login, then " + Name() + " env deploy prod",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("claim block lacks %q:\n%s", want, text)
		}
	}
	// The ownership lines follow the claim link and the agent's instructions.
	if strings.Index(text, "ONCE CLAIMED") < strings.Index(text, "FOR AN AGENT") {
		t.Errorf("the after-claim lines come before the instructions:\n%s", text)
	}

	// No console URL known: the rest still prints, with no link.
	b.Reset()
	printClaimBlock(&b, "prod", &deployClaimJSON{State: "live", Published: true, ClaimURL: "https://app.reliant.dev/claim#x", ExpiresAt: now.Add(time.Hour), PurgeAfter: now.Add(2 * time.Hour)})
	if strings.Contains(b.String(), "dashboards:") || !strings.Contains(b.String(), "ONCE CLAIMED") {
		t.Errorf("without a console URL:\n%s", b.String())
	}

	doc := claimDocument("prod", credentials.Credential{Claimable: &credentials.Claimable{
		URL:      "https://amber-quokka.reliantpreview.dev",
		ClaimURL: "https://app.reliant.dev/claim#x", ExpiresAt: now.Add(72 * time.Hour), PurgeAfter: now.Add(240 * time.Hour),
	}}, now, false)
	for _, want := range []string{"Once claimed it is theirs", "free plan includes one static site", Name() + " login, then " + Name() + " env deploy prod"} {
		if !strings.Contains(doc.AgentInstructions, want) {
			t.Errorf("agent_instructions lacks %q:\n%s", want, doc.AgentInstructions)
		}
	}

	// A deploy that can no longer be claimed promises nothing about claiming.
	for _, state := range []string{"claimed", "expired"} {
		b.Reset()
		printClaimBlock(&b, "prod", &deployClaimJSON{State: state, Published: true, ClaimURL: "https://app.reliant.dev/claim#x", ExpiresAt: now.Add(time.Hour), PurgeAfter: now.Add(2 * time.Hour)})
		if strings.Contains(b.String(), "ONCE CLAIMED") {
			t.Errorf("a %s deploy's block says what claiming gives:\n%s", state, b.String())
		}
	}
	expired := claimDocument("prod", credentials.Credential{Claimable: &credentials.Claimable{
		URL:      "https://amber-quokka.reliantpreview.dev",
		ClaimURL: "https://app.reliant.dev/claim#x", ExpiresAt: now.Add(-48 * time.Hour), PurgeAfter: now.Add(-time.Hour),
	}}, now, false)
	if expired.State != "expired" || strings.Contains(expired.AgentInstructions, "Once claimed") {
		t.Errorf("an expired deploy's instructions (state %s) promise a claim:\n%s", expired.State, expired.AgentInstructions)
	}
}
