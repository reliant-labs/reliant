package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func policyWith(required *release.ShapeRequiredGates, hook *release.ShapeApprovalHook) *release.ShapeDeliveryPolicy {
	p := release.DefaultSelfHostedDeliveryPolicy()
	p.RequiredGates, p.ApprovalHook = required, hook
	return &p
}

func answering(d release.ApprovalDecision, err error) approvalAsker {
	return func(context.Context, release.ShapeApprovalHook, release.ApprovalRequest, string) (release.ApprovalDecision, error) {
		return d, err
	}
}

func refusalReason(t *testing.T, err error) string {
	t.Helper()
	var refused *promoteRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want a promote refusal, got %v", err)
	}
	return refused.Reason
}

var (
	requireE2E = &release.ShapeRequiredGates{Mode: release.DeliveryEnforce, Names: []string{"e2e"}}
	urlHook    = &release.ShapeApprovalHook{Mode: release.DeliveryEnforce, URL: "https://approvals.test/hook", TimeoutSeconds: 5}
)

func TestPromotionPolicy_RequiredGates(t *testing.T) {
	ctx := context.Background()
	in := promotionCheckInput{Env: "prod", Version: "v1", Ask: true}

	// Missing: refused, exit 4 (nothing wrong with the release yet).
	_, err := judgePromotionPolicy(ctx, io.Discard, policyWith(requireE2E, nil), in, nil)
	if got := refusalReason(t, err); got != release.RefusalRequiredGateMissing || exitCodeForRefusal(got) != exitRefused {
		t.Errorf("missing gate: reason %q exit %d", got, exitCodeForRefusal(got))
	}

	// Failed: refused, exit 1.
	in.Gates = []release.Gate{{Name: "e2e", Status: release.GateStatusFailed}}
	_, err = judgePromotionPolicy(ctx, io.Discard, policyWith(requireE2E, nil), in, nil)
	if got := refusalReason(t, err); got != release.RefusalRequiredGateFailed || exitCodeForRefusal(got) != exitWrong {
		t.Errorf("failed gate: reason %q exit %d", got, exitCodeForRefusal(got))
	}

	// Passed: admitted, and the verdict is recorded on the promotion.
	in.Gates = []release.Gate{{Name: "e2e", Status: release.GateStatusPassed}}
	gates, err := judgePromotionPolicy(ctx, io.Discard, policyWith(requireE2E, nil), in, nil)
	if err != nil || len(gates) != 1 || gates[0].Name != release.RequiredGatesGateName || gates[0].Status != release.GateStatusPassed {
		t.Fatalf("passed gate: %+v, %v", gates, err)
	}

	// Audit admits a failure and records it.
	audit := &release.ShapeRequiredGates{Mode: release.DeliveryAudit, Names: []string{"e2e", "scan"}}
	gates, err = judgePromotionPolicy(ctx, io.Discard, policyWith(audit, nil), in, nil)
	if err != nil || len(gates) != 1 || gates[0].Status != release.GateStatusFailed || !strings.HasPrefix(gates[0].Summary, "AUDIT") {
		t.Fatalf("audit: %+v, %v", gates, err)
	}

	// A caller cannot forge the check's own verdict.
	in.Gates = []release.Gate{{Name: release.RequiredGatesGateName, Status: release.GateStatusPassed}}
	if _, err = judgePromotionPolicy(ctx, io.Discard, policyWith(requireE2E, nil), in, nil); err == nil || !strings.Contains(err.Error(), "records its own verdict") {
		t.Errorf("reserved gate name: %v", err)
	}

	// No policy, or one that declares no checks: nothing judged, nothing recorded.
	for _, pol := range []*release.ShapeDeliveryPolicy{nil, policyWith(nil, nil), policyWith(&release.ShapeRequiredGates{Mode: release.DeliveryEnforce}, nil)} {
		if gates, err := judgePromotionPolicy(ctx, io.Discard, pol, promotionCheckInput{Env: "prod"}, nil); err != nil || gates != nil {
			t.Errorf("undeclared checks: %+v, %v", gates, err)
		}
	}
}

func TestPromotionPolicy_ApprovalHook(t *testing.T) {
	ctx := context.Background()
	in := promotionCheckInput{Env: "prod", Version: "v1", Ask: true}
	for name, tc := range map[string]struct {
		ask        approvalAsker
		wantReason string
		wantStatus release.GateStatus
	}{
		"approve":   {answering(release.ApprovalDecision{Decision: release.ApprovalApprove, URL: "https://a.test/1"}, nil), "", release.GateStatusPassed},
		"deny":      {answering(release.ApprovalDecision{Decision: release.ApprovalDeny, Reason: "change freeze"}, nil), release.RefusalApprovalDenied, release.GateStatusFailed},
		"pending":   {answering(release.ApprovalDecision{Decision: release.ApprovalPending}, nil), release.RefusalApprovalPending, release.GateStatusError},
		"timed out": {answering(release.ApprovalDecision{}, errors.New("the hook did not answer within 5s")), release.RefusalApprovalDenied, release.GateStatusFailed},
	} {
		gates, err := judgePromotionPolicy(ctx, io.Discard, policyWith(nil, urlHook), in, tc.ask)
		if tc.wantReason == "" {
			if err != nil || len(gates) != 1 || gates[0].Status != tc.wantStatus || gates[0].URL != "https://a.test/1" {
				t.Errorf("%s: %+v, %v", name, gates, err)
			}
			continue
		}
		if got := refusalReason(t, err); got != tc.wantReason || exitCodeForRefusal(got) != exitRefused {
			t.Errorf("%s: reason %q exit %d", name, got, exitCodeForRefusal(got))
		}
		// The same answer under audit admits and records it.
		audit := *urlHook
		audit.Mode = release.DeliveryAudit
		gates, err = judgePromotionPolicy(ctx, io.Discard, policyWith(nil, &audit), in, tc.ask)
		if err != nil || len(gates) != 1 || gates[0].Status != tc.wantStatus {
			t.Errorf("%s under audit: %+v, %v", name, gates, err)
		}
	}

	// A run that writes nothing does not ask: asking can open a request.
	asked := false
	spy := func(context.Context, release.ShapeApprovalHook, release.ApprovalRequest, string) (release.ApprovalDecision, error) {
		asked = true
		return release.ApprovalDecision{Decision: release.ApprovalDeny}, nil
	}
	if _, err := judgePromotionPolicy(ctx, io.Discard, policyWith(nil, urlHook), promotionCheckInput{Env: "prod", Version: "v1"}, spy); err != nil || asked {
		t.Errorf("plan-only asked the hook (asked=%v, err=%v)", asked, err)
	}

	// The hook is told the gates, including the required-gates verdict.
	var seen release.ApprovalRequest
	capture := func(_ context.Context, _ release.ShapeApprovalHook, req release.ApprovalRequest, _ string) (release.ApprovalDecision, error) {
		seen = req
		return release.ApprovalDecision{Decision: release.ApprovalApprove}, nil
	}
	in.Gates = []release.Gate{{Name: "e2e", Status: release.GateStatusPassed}}
	if _, err := judgePromotionPolicy(ctx, io.Discard, policyWith(requireE2E, urlHook), in, capture); err != nil {
		t.Fatal(err)
	}
	if seen.Schema != release.ApprovalRequestSchema || seen.Env != "prod" || seen.Release != "v1" || len(seen.Gates) != 2 {
		t.Errorf("the hook was asked %+v", seen)
	}
}

func TestAskApprovalHook_URL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req release.ApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Release {
		case "slow":
			<-r.Context().Done()
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{"decision":"deny","reason":"freeze until Monday"}`))
		}
	}))
	defer srv.Close()
	hook := release.ShapeApprovalHook{Mode: release.DeliveryEnforce, URL: srv.URL, TimeoutSeconds: 1}
	ctx := context.Background()

	d, err := askApprovalHook(ctx, hook, release.ApprovalRequest{Release: "v1"}, "")
	if err != nil || d.Decision != release.ApprovalDeny || d.Reason != "freeze until Monday" {
		t.Errorf("answer: %+v, %v", d, err)
	}
	if _, err := askApprovalHook(ctx, hook, release.ApprovalRequest{Release: "broken"}, ""); err == nil {
		t.Error("an HTTP 500 must be an error (enforce reads it as deny)")
	}
	if _, err := askApprovalHook(ctx, hook, release.ApprovalRequest{Release: "slow"}, ""); err == nil || !strings.Contains(err.Error(), "within 1s") {
		t.Errorf("a hook that never answers must time out: %v", err)
	}
}

func TestAskApprovalHook_Argv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "approve.sh")
	body := `#!/bin/sh
req=$(cat)
case "$req" in
  *'"release":"v-deny"'*) echo "not during the freeze" >&2; exit 1 ;;
  *'"release":"v-pending"'*) echo '{"decision":"pending","url":"https://a.test/2"}' ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("host resource missing: the argv hook under test is a POSIX shell script, which Windows cannot exec")
	}
	hook := release.ShapeApprovalHook{Mode: release.DeliveryEnforce, Argv: []string{"./approve.sh"}, TimeoutSeconds: 5}
	ctx := context.Background()
	for version, want := range map[string]string{"v1": release.ApprovalApprove, "v-deny": release.ApprovalDeny, "v-pending": release.ApprovalPending} {
		d, err := askApprovalHook(ctx, hook, release.ApprovalRequest{Release: version}, dir)
		if err != nil || d.Decision != want {
			t.Errorf("%s: %+v, %v; want %s", version, d, err, want)
		}
		if version == "v-deny" && d.Reason != "not during the freeze" {
			t.Errorf("deny reason %q", d.Reason)
		}
	}
}
