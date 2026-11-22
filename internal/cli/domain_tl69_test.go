package cli

import (
	"strings"
	"testing"
)

// --require-verified turns a domain's state into an exit a script can branch
// on; plain verify keeps exiting 0, as it always has.
func TestDomainStateExit(t *testing.T) {
	for state, want := range map[string]int{
		"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE":        exitOK,
		"DEPLOY_CUSTOM_DOMAIN_STATE_ISSUING":     exitOK,
		"DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS": exitTimedOut,
		"DEPLOY_CUSTOM_DOMAIN_STATE_VERIFYING":   exitTimedOut,
		"DEPLOY_CUSTOM_DOMAIN_STATE_FAILED":      exitWrong,
		"DEPLOY_CUSTOM_DOMAIN_STATE_CONFLICT":    exitWrong,
	} {
		err := domainStateExit(domainWire{Hostname: "shop.example.com", State: state})
		if got := exitCodeForError(err); (err == nil && want != exitOK) || (err != nil && got != want) {
			t.Errorf("%s: exit %d (%v), want %d", state, got, err, want)
		}
	}
}

// A binding in the env this ran with is named, not shown as its id.
func TestBindingSummaryNamesTheKnownEnv(t *testing.T) {
	b := &domainWireBinding{Target: "api", EnvironmentID: "4731c46b"}
	if got := bindingSummary(b, map[string]string{"4731c46b": "prod"}); got != "api in env prod" {
		t.Errorf("summary = %q", got)
	}
	if got := bindingSummary(b); !strings.Contains(got, "4731c46b") {
		t.Errorf("an unknown env keeps its id: %q", got)
	}
}
