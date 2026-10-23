package deploytarget

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

const (
	oldBundle = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newBundle = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// appliedCP reports the platform's applied revision per ListConvergences poll
// and serves a workload status that is bad until the new revision applies.
type appliedCP struct {
	fakeCP
	mu2 sync.Mutex
	// revisions[n] is the applied revision on the nth ListConvergences poll
	// (the last repeats).
	revisions []string
	nConv     int
	// failedReason, when set, makes every poll's newest row a FAILED apply of
	// promo-1 carrying this message (revision stays the old one).
	failedReason string
	// statusAfterApply is the GetStatus body once the new bundle is applied.
	statusAfterApply string
	// staleStatus is the previous revision's workload errors.
	staleStatus string
}

func (f *appliedCP) Call(ctx context.Context, proc string, req, out any) error {
	if strings.HasSuffix(proc, "/ListConvergences") {
		f.mu2.Lock()
		i := f.nConv
		if i >= len(f.revisions) {
			i = len(f.revisions) - 1
		}
		f.nConv++
		rev := f.revisions[i]
		f.mu2.Unlock()
		reply := `{"convergences":[]}`
		if f.failedReason != "" {
			reply = fmt.Sprintf(`{"convergences":[{"promotionId":"promo-1","revision":"%s","state":"failed","reason":"BuildFailed","message":%q}]}`, rev, f.failedReason)
		} else if rev != "" {
			reply = fmt.Sprintf(`{"convergences":[{"revision":"%s","state":"converged"}]}`, rev)
		}
		return jsonInto(reply, out)
	}
	if strings.HasSuffix(proc, "/GetStatus") {
		f.mu2.Lock()
		applied := f.nConv > 0 && f.revisions[min(f.nConv-1, len(f.revisions)-1)] == newBundle
		f.mu2.Unlock()
		body := f.staleStatus
		if applied {
			body = f.statusAfterApply
		}
		return jsonInto(body, out)
	}
	return f.fakeCP.Call(ctx, proc, req, out)
}

func jsonInto(reply string, out any) error {
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(reply), out)
}

const imageRejectedStatus = `{"environmentVerdict":"DEPLOY_VERDICT_DIVERGED","deployments":[
 {"deployment":{"id":"dep-api","name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_DEGRADED","lastError":"ImageRejected: double-qualified ref"}},"verdict":"DEPLOY_VERDICT_DIVERGED"},
 {"deployment":{"id":"dep-orders","name":"orders","observed":{"state":"DEPLOY_OBSERVED_STATE_READY"}},"verdict":"DEPLOY_VERDICT_CONVERGED"}]}`

func deployApplied(t *testing.T, cp *appliedCP, timeout time.Duration) (string, error) {
	t.Helper()
	cp.fakeCP.status = readyStatus(digestA)
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		Rollout:      cluster.RolloutPolicy{Mode: cluster.RolloutWait, Timeout: timeout},
		RecordBundle: func(context.Context, string) (string, error) { return newBundle, nil }}
	err := p.Deploy(context.Background(), hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}))
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), err
}

func TestHostedWaitJudgesWorkloadsOnlyAfterThePromotedBundleIsApplied(t *testing.T) {
	cp := &appliedCP{
		revisions:        []string{oldBundle, oldBundle, oldBundle, newBundle},
		staleStatus:      imageRejectedStatus,
		statusAfterApply: readyStatus(digestA)(0),
	}
	out, err := deployApplied(t, cp, 5*time.Second)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if strings.Contains(out, "ImageRejected") {
		t.Errorf("the previous revision's errors leaked into the output:\n%s", out)
	}
	if !strings.Contains(out, "waiting for the platform to apply") {
		t.Errorf("no apply-wait line:\n%s", out)
	}
}

func TestHostedWaitTimeoutNamesTheMissingApplyNotTheOldRevisionsErrors(t *testing.T) {
	cp := &appliedCP{revisions: []string{oldBundle}, staleStatus: imageRejectedStatus}
	_, err := deployApplied(t, cp, 150*time.Millisecond)
	if err == nil {
		t.Fatal("deploy succeeded though the platform never applied the bundle")
	}
	if strings.Contains(err.Error(), "ImageRejected") || !strings.Contains(err.Error(), "has not applied this release yet") {
		t.Errorf("err = %v, want the missing apply named, not the old revision's errors", err)
	}
}

func TestHostedWaitReportsARealErrorOnThePromotedRevision(t *testing.T) {
	cp := &appliedCP{
		revisions:        []string{oldBundle, newBundle},
		staleStatus:      imageRejectedStatus,
		statusAfterApply: strings.ReplaceAll(imageRejectedStatus, "double-qualified ref", "CrashLoopBackOff"),
	}
	_, err := deployApplied(t, cp, 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Fatalf("err = %v, want the promoted revision's error reported", err)
	}
}

// A failed Flux apply leaves the convergence row's revision at the OLD one, so
// it must not read as "not applied yet": the reconciler's reason is the answer.
func TestHostedWaitReportsAFailedApplyNotJustNotAppliedYet(t *testing.T) {
	cp := &appliedCP{revisions: []string{oldBundle}, staleStatus: imageRejectedStatus,
		failedReason: "kustomize build failed: accumulating resources: no such file"}
	out, err := deployApplied(t, cp, 150*time.Millisecond)
	if err == nil {
		t.Fatal("deploy succeeded though the apply failed")
	}
	for _, want := range []string{"BuildFailed", "kustomize build failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want the reconciler's %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "has not applied this release yet") || strings.Contains(err.Error(), "ImageRejected") {
		t.Errorf("a failed apply was reported as not-yet-applied / old workload errors: %v", err)
	}
	if !strings.Contains(out, "apply of") || !strings.Contains(out, "failed") {
		t.Errorf("failure not surfaced while waiting:\n%s", out)
	}
}

// A failed row for a DIFFERENT promotion is not this deploy's failure.
func TestHostedWaitIgnoresAFailedRowForAnotherPromotion(t *testing.T) {
	st, err := pollBundleApplied(context.Background(), failedRowCP{}, "env-1", "promo-2", newBundle)
	if err != nil || st.failure != "" {
		t.Fatalf("state = %+v err = %v, want no failure for another promotion's row", st, err)
	}
}

type failedRowCP struct{}

func (failedRowCP) Call(_ context.Context, _ string, _, out any) error {
	return json.Unmarshal([]byte(`{"convergences":[{"promotionId":"promo-1","revision":"`+oldBundle+`","state":"failed","reason":"x"}]}`), out)
}
