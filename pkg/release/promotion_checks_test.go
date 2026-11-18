package release

import (
	"reflect"
	"testing"
)

func TestJudgeRequiredGates(t *testing.T) {
	passed := func(n string) Gate { return Gate{Name: n, Status: GateStatusPassed} }
	for name, tc := range map[string]struct {
		gates       []Gate
		missing     []string
		failed      []string
		wantReason  string
		wantOK      bool
		wantSummary string
	}{
		"all passed":  {gates: []Gate{passed("e2e"), passed("scan"), passed("extra")}, wantOK: true, wantSummary: "every required gate passed"},
		"one missing": {gates: []Gate{passed("e2e")}, missing: []string{"scan"}, wantReason: RefusalRequiredGateMissing, wantSummary: "missing: scan"},
		"one failed": {
			gates:  []Gate{passed("e2e"), {Name: "scan", Status: GateStatusFailed}},
			failed: []string{"scan=failed"}, wantReason: RefusalRequiredGateFailed, wantSummary: "not passed: scan=failed",
		},
		// A skipped check did not pass.
		"skipped is not passed": {
			gates:  []Gate{passed("e2e"), {Name: "scan", Status: GateStatusSkipped}},
			failed: []string{"scan=skipped"}, wantReason: RefusalRequiredGateFailed, wantSummary: "not passed: scan=skipped",
		},
		// A passing re-run beside a failing one is still a failure on the
		// record the promotion would freeze.
		"pass beside a fail": {
			gates:  []Gate{passed("e2e"), {Name: "e2e", Status: GateStatusFailed}, passed("scan")},
			failed: []string{"e2e=failed"}, wantReason: RefusalRequiredGateFailed, wantSummary: "not passed: e2e=failed",
		},
		"failed outranks missing": {
			gates:  []Gate{{Name: "e2e", Status: GateStatusError}},
			failed: []string{"e2e=error"}, missing: []string{"scan"}, wantReason: RefusalRequiredGateFailed,
			wantSummary: "not passed: e2e=error; missing: scan",
		},
	} {
		v := JudgeRequiredGates([]string{"e2e", "scan"}, tc.gates)
		if v.OK() != tc.wantOK || !reflect.DeepEqual(v.Missing, tc.missing) || !reflect.DeepEqual(v.Failed, tc.failed) {
			t.Errorf("%s: got %+v", name, v)
		}
		if !tc.wantOK && v.Reason() != tc.wantReason {
			t.Errorf("%s: reason %q, want %q", name, v.Reason(), tc.wantReason)
		}
		if v.Summary() != tc.wantSummary {
			t.Errorf("%s: summary %q, want %q", name, v.Summary(), tc.wantSummary)
		}
	}
}

func TestParseApprovalDecision(t *testing.T) {
	for body, want := range map[string]string{
		`{"decision":"approve"}`:                          ApprovalApprove,
		`{"decision":" DENY ","reason":"change freeze"}`:  ApprovalDeny,
		`{"decision":"pending","url":"https://a.test/1"}`: ApprovalPending,
	} {
		d, err := ParseApprovalDecision([]byte(body))
		if err != nil || d.Decision != want {
			t.Errorf("%s: got %+v, %v; want %s", body, d, err, want)
		}
	}
	// Anything an enforcing caller cannot read as one of the three is an
	// error, which it reads as deny: an unreadable answer must not approve.
	for _, body := range []string{``, `approve`, `{}`, `{"decision":"yes"}`, `{"decision":true}`} {
		if d, err := ParseApprovalDecision([]byte(body)); err == nil {
			t.Errorf("%q parsed as %+v", body, d)
		}
	}
}

func TestValidateApprovalHookURL(t *testing.T) {
	for _, ok := range []string{"https://approvals.acme.test/forge", "http://localhost:8080/hook", "http://127.0.0.1/hook"} {
		if err := ValidateApprovalHookURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://approvals.acme.test", "ftp://a.test", "/relative", "https://u:p@a.test", "https://"} {
		if err := ValidateApprovalHookURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
