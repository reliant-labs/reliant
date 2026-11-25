package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

func pendingApply() *deploytarget.ApplyPendingError {
	return &deploytarget.ApplyPendingError{
		Env:         "prod",
		PromotionID: "promo-1",
		Bundle:      "sha256:badfa995129f7cd83b1db0a44b80733f5bd29a130705cafb6e9962f8eda40721",
		Current:     "sha256:4750cbaa564172e82a0b51ef319a08ddd6a48ad09eab602d1e0ce3f55c95e3f6",
		Waited:      10 * time.Minute,
	}
}

// A publish the platform had not reported applied is exit 8 (still in
// progress), not exit 1, and the wrap keeps the provider's error findable.
func TestDeployApplying_IsExitEightAndKeepsTheChain(t *testing.T) {
	inner := fmt.Errorf("deploy hosted: %w", pendingApply())
	err := asApplying(inner)
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Fatalf("exit code = %d, want %d (still in progress)", got, exitTimedOut)
	}
	if applyingOf(err) == nil {
		t.Fatal("the wrapped error lost the *ApplyPendingError")
	}
	if again := asApplying(err); again != err {
		t.Error("asApplying wrapped an already-wrapped error a second time")
	}
	if msg := err.Error(); strings.Contains(msg, "TIMED OUT") || !strings.Contains(msg, "STILL APPLYING, not failed") {
		t.Errorf("message = %q, want still applying and no TIMED OUT", msg)
	}
	other := errors.New("boom")
	if asApplying(other) != other {
		t.Error("asApplying changed an unrelated error")
	}
}

// The deploy's headline says the release is published and still applying,
// names the command that keeps watching, and says neither FAILED nor re-plan.
func TestRenderPromotePlan_StillApplyingIsNotAFailure(t *testing.T) {
	err := asApplying(pendingApply())
	plan := promotePlan{Env: "prod", Applied: true, FollowError: err.Error(),
		Target:   promotePlanTarget{Release: "v2"},
		Shipped:  []string{"[hosted] published to the control plane: api, web"},
		Applying: applyingDocument(pendingApply(), "promo-1")}
	plan.followed = true
	var out bytes.Buffer
	renderPromotePlanText(&out, plan)
	got := out.String()
	for _, want := range []string{"PUBLISHED, STILL APPLYING", "Nothing has failed", "forge env status prod --wait --promotion promo-1",
		"shipped   [hosted] published", "sha256:badfa995129f", "sha256:4750cbaa5641"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan does not say %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"FAILED", "TIMED OUT", "PARTLY", "--plan-only"} {
		if strings.Contains(got, bad) {
			t.Errorf("a still-applying deploy rendered %q:\n%s", bad, got)
		}
	}
}

// A still-applying publish is recorded as shipped, never as "failed part-way".
func TestShipLog_StillApplyingIsShipped(t *testing.T) {
	var log shipLog
	_ = log.run([]string{"[hosted] published"}, func() error { return fmt.Errorf("x: %w", pendingApply()) })
	if len(log.Shipped()) != 1 || len(log.Partial()) != 0 {
		t.Fatalf("shipped %v partial %v, want the publish shipped", log.Shipped(), log.Partial())
	}
}

// A hosted stage still applying with a later machine-owned stage left (a
// local apply, a bucket/Firebase frontend) is an INCOMPLETE deploy: exit 1,
// naming what did not run, never "still applying" (review of #1499).
func TestStageStopError_PendingHostedWithLaterStagesIsIncomplete(t *testing.T) {
	pending := fmt.Errorf("deploy hosted: %w", pendingApply())
	work := map[deployStage][]string{
		stageLocal:     {"[k8s-cluster] cluster=prod ns=app"},
		stageFrontends: {"frontend web"},
	}
	err := stageStopError("prod", pending, []deployStage{stageFrontends}, work)
	if applyingOf(err) != nil || applyingOf(asApplying(err)) != nil {
		t.Fatalf("an incomplete deploy was classified as still applying: %v", err)
	}
	if got := exitCodeForError(asApplying(err)); got != exitWrong {
		t.Errorf("exit code = %d, want %d", got, exitWrong)
	}
	if msg := err.Error(); !strings.Contains(msg, "INCOMPLETE") || !strings.Contains(msg, "frontend web") ||
		!strings.Contains(msg, "deploy again") {
		t.Errorf("message = %q, want the stages that did not run and how to ship them", msg)
	}
	var log shipLog
	_ = log.run([]string{"[hosted] published"}, func() error { return pending })
	if len(log.Shipped()) != 1 {
		t.Errorf("the hosted publish itself still shipped: %v", log.Shipped())
	}

	// Nothing left for this machine: still applying.
	if err := stageStopError("prod", pending, []deployStage{stageFrontends}, map[deployStage][]string{}); applyingOf(err) == nil {
		t.Errorf("a pending hosted stage with nothing after it must stay still-applying: %v", err)
	}
	if err := stageStopError("prod", pending, nil, work); applyingOf(err) == nil {
		t.Errorf("a pending hosted stage that ran last must stay still-applying: %v", err)
	}
	// Any other error is untouched.
	other := errors.New("boom")
	if stageStopError("prod", other, []deployStage{stageFrontends}, work) != other {
		t.Error("a non-pending error was rewritten")
	}
}

// The watch hint stays pinned to the promotion this deploy recorded when the
// env advances: a pipeline following .applying.watch must keep waiting on
// promo-1, never succeed on a newer deploy's promo-2 (review of #1499).
func TestApplyingWatchKeepsTheOriginalPromotionWhenTheEnvAdvances(t *testing.T) {
	pinned := pendingApply() // the provider's error names promo-1
	doc := applyingDocument(pinned, "promo-2")
	if doc.PromotionID != "promo-1" || doc.Watch != "forge env status prod --wait --promotion promo-1" {
		t.Errorf("document = %+v, want the watch pinned to promo-1", doc)
	}
	// A provider error that carried no promotion takes the one this command
	// recorded, never an unpinned watch.
	bare := pendingApply()
	bare.PromotionID = ""
	if doc := applyingDocument(bare, "promo-1"); doc.Watch != "forge env status prod --wait --promotion promo-1" {
		t.Errorf("watch = %q, want it pinned to the recorded promo-1", doc.Watch)
	}
	for name, err := range map[string]error{
		"still applying": asApplying(pinned),
		"incomplete": stageStopError("prod", pinned, []deployStage{stageFrontends},
			map[deployStage][]string{stageFrontends: {"frontend web"}}),
	} {
		if !strings.Contains(err.Error(), "--wait --promotion promo-1") {
			t.Errorf("%s: hint %q is not pinned to promo-1", name, err.Error())
		}
	}
}
