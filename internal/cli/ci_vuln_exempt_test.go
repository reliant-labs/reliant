package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// The exemption mechanism exists so an unfixable, unreachable advisory
// cannot force the choice between a permanently red gate and no gate. The
// risk it introduces is obvious — a place to make findings disappear — so
// these tests pin the LIMITS much harder than the happy path: the scope of
// a suppression, and every way an entry stops being honored.

func mustParseFindings(t *testing.T, stream string) []vulnFinding {
	t.Helper()
	got, err := parseGovulncheckJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseGovulncheckJSON: %v", err)
	}
	return got
}

// A trace whose top frame names a function is a CALL; one without is mere
// presence in the build graph. govulncheck emits both for the same
// advisory, and only the first should be able to fail the gate — treating
// presence as a failure would make the gate unpassable for any module that
// is merely compiled in.
func TestParseGovulncheckJSON_OnlyCalledFindings(t *testing.T) {
	t.Parallel()

	const stream = `
{"osv":{"id":"GO-2026-4887","summary":"AuthZ plugin bypass"}}
{"osv":{"id":"GO-2025-0001","summary":"present but never called"}}
{"finding":{"osv":"GO-2026-4887","trace":[{"module":"github.com/docker/docker","version":"v28.3.3+incompatible"}]}}
{"finding":{"osv":"GO-2026-4887","trace":[{"module":"github.com/docker/docker","version":"v28.3.3+incompatible","package":"github.com/docker/docker/api/types/versions","function":"init"}]}}
{"finding":{"osv":"GO-2025-0001","trace":[{"module":"example.com/quiet","version":"v1.0.0","package":"example.com/quiet"}]}}
`

	got := mustParseFindings(t, stream)
	if len(got) != 1 {
		t.Fatalf("want exactly the 1 CALLED advisory, got %d: %+v", len(got), got)
	}
	if got[0].ID != "GO-2026-4887" {
		t.Errorf("wrong advisory kept: got %q, want GO-2026-4887", got[0].ID)
	}
	if got[0].Summary != "AuthZ plugin bypass" {
		t.Errorf("summary not joined from the osv record: got %q", got[0].Summary)
	}
	if got[0].Module != "github.com/docker/docker" {
		t.Errorf("module not carried from the trace: got %q", got[0].Module)
	}
}

// The whole safety argument for the mechanism: an exemption suppresses the
// advisory it names and NOTHING else. If accepting one docker CVE also
// accepted the next one, this would be a bypass wearing an allowlist's
// clothes.
func TestEvaluateVulnFindings_ExemptionIsScopedToItsID(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	findings := []vulnFinding{
		{ID: "GO-2026-4887", Module: "github.com/docker/docker"},
		{ID: "GO-2026-9999", Module: "github.com/docker/docker"}, // same module, not accepted
	}
	res := evaluateVulnFindings(findings, []config.CIVulnExemption{
		{ID: "GO-2026-4887", Reason: "init-only, unreachable", Expires: "2026-12-31"},
	}, now)

	if len(res.Accepted) != 1 || res.Accepted[0].ID != "GO-2026-4887" {
		t.Errorf("the named advisory should be accepted; got %+v", res.Accepted)
	}
	if len(res.Blocking) != 1 || res.Blocking[0].ID != "GO-2026-9999" {
		t.Fatalf("a NEW advisory in an already-exempted module must still block the gate; got %+v", res.Blocking)
	}
}

// An expiry that does not bite is decoration. Past its date the entry must
// stop being honored, so the finding blocks again and someone has to look
// at it.
func TestEvaluateVulnFindings_ExpiredExemptionStopsAccepting(t *testing.T) {
	t.Parallel()

	findings := []vulnFinding{{ID: "GO-2026-4887"}}
	ex := []config.CIVulnExemption{
		{ID: "GO-2026-4887", Reason: "init-only", Expires: "2026-01-31"},
	}

	live := evaluateVulnFindings(findings, ex, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	if len(live.Accepted) != 1 {
		t.Fatalf("before the expiry date the exemption must hold; got %+v", live)
	}

	lapsed := evaluateVulnFindings(findings, ex, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if len(lapsed.Blocking) != 1 {
		t.Errorf("an expired exemption must let the finding block again; got %+v", lapsed.Blocking)
	}
	if len(lapsed.Malformed) == 0 {
		t.Error("an expired exemption must be reported, not silently dropped — otherwise the gate " +
			"goes red with no hint that the cause is a lapsed acceptance")
	}
}

// Reason and expiry are the only things that keep the list reviewable, so
// an entry missing either is not honored. Fail-closed: the finding blocks.
func TestEvaluateVulnFindings_IncompleteEntriesAreNotHonored(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	findings := []vulnFinding{{ID: "GO-2026-4887"}}

	for name, ex := range map[string]config.CIVulnExemption{
		"no reason":       {ID: "GO-2026-4887", Expires: "2026-12-31"},
		"no expiry":       {ID: "GO-2026-4887", Reason: "unreachable"},
		"bad expiry":      {ID: "GO-2026-4887", Reason: "unreachable", Expires: "31/12/2026"},
		"blank id":        {Reason: "unreachable", Expires: "2026-12-31"},
		"whitespace only": {ID: "  ", Reason: "unreachable", Expires: "2026-12-31"},
	} {
		t.Run(name, func(t *testing.T) {
			res := evaluateVulnFindings(findings, []config.CIVulnExemption{ex}, now)
			if len(res.Accepted) != 0 {
				t.Errorf("an entry with %s must not suppress anything; got accepted=%+v", name, res.Accepted)
			}
			if len(res.Blocking) != 1 {
				t.Errorf("the finding must still block when the exemption is not honored; got %+v", res.Blocking)
			}
			if len(res.Malformed) == 0 {
				t.Errorf("an entry with %s must be reported so the author learns it did nothing", name)
			}
		})
	}
}

// An exemption matching nothing has done its job and should go. Left in
// place it silently pre-authorizes a future finding that nobody reviewed —
// the advisory could be re-reported against a reachable path tomorrow.
func TestEvaluateVulnFindings_StaleExemptionIsReported(t *testing.T) {
	t.Parallel()

	res := evaluateVulnFindings(nil, []config.CIVulnExemption{
		{ID: "GO-2026-4887", Reason: "fixed upstream since", Expires: "2026-12-31"},
	}, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	if len(res.StaleExemptions) != 1 || res.StaleExemptions[0] != "GO-2026-4887" {
		t.Errorf("an exemption that matched no finding must be reported as stale; got %+v", res.StaleExemptions)
	}
	if len(res.Blocking) != 0 {
		t.Errorf("a stale exemption is not itself a failure; got blocking=%+v", res.Blocking)
	}
}

// With no exemptions configured at all, the gate must behave exactly as it
// did before this mechanism existed.
func TestEvaluateVulnFindings_NoExemptionsBlocksEverything(t *testing.T) {
	t.Parallel()

	res := evaluateVulnFindings([]vulnFinding{{ID: "GO-2026-4887"}, {ID: "GO-2026-4883"}},
		nil, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	if len(res.Blocking) != 2 {
		t.Errorf("every called finding must block when nothing is exempted; got %+v", res.Blocking)
	}
	if len(res.Accepted) != 0 {
		t.Errorf("nothing can be accepted without an exemption; got %+v", res.Accepted)
	}
}

// The accepted risks and the reason they were accepted must be visible in
// the CI log. An acceptance nobody can see is one nobody re-reviews.
func TestVulnGateResult_ReportNamesAcceptedAndStale(t *testing.T) {
	t.Parallel()

	res := vulnGateResult{
		Accepted:        []vulnFinding{{ID: "GO-2026-4887", Summary: "AuthZ plugin bypass", Module: "github.com/docker/docker", Version: "v28.3.3"}},
		StaleExemptions: []string{"GO-2026-1111"},
	}
	var sb strings.Builder
	res.report(&sb)
	out := sb.String()

	for _, want := range []string{"GO-2026-4887", "AuthZ plugin bypass", "github.com/docker/docker", "GO-2026-1111"} {
		if !strings.Contains(out, want) {
			t.Errorf("report must surface %q so the accepted risk is visible in CI; got:\n%s", want, out)
		}
	}
}
