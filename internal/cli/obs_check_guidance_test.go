package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// A low-traffic release is INCONCLUSIVE, exit 2 — deliberately not exit 8
// (obs_check.go): 8 means "--wait ran out, check again later", and a closed
// after window with too few requests is final for that promotion. Guidance
// that mapped "not enough data yet" to 8 had agents waiting forever on a quiet
// static site's deploy (forge bug report 07d1d05f). The command's help and the
// observability/query skill must both say where low traffic lands, by the
// reason_code a gate branches on.
func TestObsCheckGuidanceSaysLowTrafficIsExit2(t *testing.T) {
	help := newObsCheckCmd().Long
	exit2 := sectionBetween(help, "\n  2  ", "\n  8  ")
	exit8 := sectionBetween(help, "\n  8  ", "\n\n")
	if !strings.Contains(exit2, releasehealth.ReasonLowTraffic) || !strings.Contains(exit2, "final") {
		t.Errorf("`obs check --help` exit 2 must say low traffic (%s) lands there and is final:\n%s", releasehealth.ReasonLowTraffic, exit2)
	}
	if !strings.Contains(exit8, "--wait") || !strings.Contains(exit8, "Low traffic is never 8") {
		t.Errorf("`obs check --help` exit 8 must be the --wait timeout and say low traffic is never 8:\n%s", exit8)
	}

	skills := filepath.Join("..", "templates", "project", "skills", "forge", "observability")
	query, err := os.ReadFile(filepath.Join(skills, "query", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(query), "Low traffic is exit 2, not 8") {
		t.Error("observability/query skill (the exit-code table) does not say low traffic is exit 2, not 8")
	}
	health, err := os.ReadFile(filepath.Join(skills, "release-health", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Low traffic: exit 2",
		"`" + releasehealth.ReasonLowTraffic + "`",
		// The sentence the document's reason carries for it, so an agent
		// reading the reason finds this section.
		"no service had at least N requests after the promotion",
	} {
		if !strings.Contains(string(health), want) {
			t.Errorf("observability/release-health skill does not say %q", want)
		}
	}
	quiet := releasehealth.Report{AfterComplete: true, Services: []releasehealth.ServiceHealth{{
		Service: "web",
		Before:  releasehealth.WindowStats{Requests: 500},
		After:   releasehealth.WindowStats{Requests: 3},
	}}}
	if code := releasehealth.InconclusiveCode(quiet); code != releasehealth.ReasonLowTraffic {
		t.Fatalf("fixture: a quiet after window is %q, want %q", code, releasehealth.ReasonLowTraffic)
	}
	if why := releasehealth.InconclusiveWhy(quiet); !strings.HasPrefix(why, "no service had at least ") {
		t.Errorf("the low-traffic reason changed (%q): update the skill's quote of it", why)
	}
}

// sectionBetween is s from the first start to the next end after it.
func sectionBetween(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}
