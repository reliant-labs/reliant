package cli

import (
	"fmt"
	"strings"
	"testing"
)

// A control plane that judges site audits (control-plane CP6) answers with
// seo_regressed / a11y_regressed findings judged by the env's policy, which
// warns on both: the check renders them and still passes.
const seoRegressedFinding = `{"code":"RELEASE_FINDING_CODE_SEO_REGRESSED","service":"web","value":21,"threshold":10,"policy":"warn","message":"web: SEO score dropped since v1: site 92 -> 71 (-21) (limit 10 points)"}`
const a11yRegressedFinding = `{"code":"RELEASE_FINDING_CODE_A11Y_REGRESSED","service":"web","value":1,"threshold":1,"policy":"warn","message":"web: 1 new accessibility critical violation since v1: a11y_violation button-name on /pricing"}`

func TestObsCheckRendersSiteAuditFindings(t *testing.T) {
	reply := healthReply("RELEASE_VERDICT_PASS", true, seoRegressedFinding+","+a11yRegressedFinding, "")
	run := func(args ...string) (int, string) {
		caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
		out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, append([]string{"check", "prod"}, args...)...)
		for _, c := range caller.calls {
			if c.procedure == procHealth && strings.Contains(c.body, "regressed") {
				t.Errorf("--fail-on reached the control plane, which may predate the codes:\n%s", c.body)
			}
		}
		return exitCodeForError(err), out
	}

	// A warn never changes the exit status (design D2).
	code, out := run("--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0: a site-audit warning must not fail the check", code)
	}
	doc := decode(t, out)
	fs := doc["findings"].([]any)
	if len(fs) != 2 || fs[0].(map[string]any)["code"] != "seo_regressed" || fs[1].(map[string]any)["code"] != "a11y_regressed" {
		t.Errorf("findings = %v", fs)
	}
	obsGolden(t, "check.site_audit.json", out)

	code, text := run()
	if code != 0 {
		t.Fatalf("text: exit = %d", code)
	}
	for _, want := range []string{"warn seo_regressed     web: SEO score dropped since v1", "warn a11y_regressed    web: 1 new accessibility"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	// --fail-on opts in, on the CLI's side of the wire.
	for _, failOn := range []string{"seo_regressed", "a11y_regressed", "seo_regressed,a11y_regressed"} {
		code, out := run("--fail-on", failOn, "--json")
		if code != 1 {
			t.Errorf("--fail-on %s: exit = %d, want 1", failOn, code)
		}
		if doc := decode(t, out); doc["verdict"] != "FAIL" {
			t.Errorf("--fail-on %s: verdict = %v", failOn, doc["verdict"])
		}
	}
}

// An audit that has not finished is named in not_evaluated and changes
// nothing else: the verdict stays the traffic's.
func TestObsCheckPendingSiteAuditIsNotEvaluated(t *testing.T) {
	reply := healthReply("RELEASE_VERDICT_PASS", true, "", "")
	reply = strings.Replace(reply, `"thresholds":[`, `"notEvaluated":["site_audit"],"thresholds":[`, 1)
	caller := &seqCaller{replies: map[string][]string{procHealth: {reply}}}
	out, _, err := newObsCheckHarness().run(t, cloudFacts(), caller, nil, "check", "prod", "--json")
	if code := exitCodeForError(err); code != 0 {
		t.Fatalf("exit = %d (%v)", code, err)
	}
	doc := decode(t, out)
	if doc["verdict"] != "PASS" || !strings.Contains(fmt.Sprint(doc["not_evaluated"]), "site_audit") {
		t.Errorf("verdict %v, not_evaluated %v", doc["verdict"], doc["not_evaluated"])
	}
}
