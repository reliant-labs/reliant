package cli

// A hosted deploy that is STILL APPLYING: published, accepted, and not yet
// reported applied when the wait's apply budget ran out.
//
// That is not a failure. The release is recorded, the bundle is published, and
// the control plane's reconciler is applying it. Nothing reported an error.
// Rendering it as "TIMED OUT … the deploy FAILED", which forge did until
// v1.0.4, sent operators to re-run or roll back deploys that went live a
// minute later. It also filed a FAILED apply gate on the promotion, so the
// next plan read the live release as NOT APPLIED.
//
// So it is its own outcome: one block saying what is known and the command
// that keeps watching, and exit 8 (exitTimedOut), the code every wait uses
// for "still in progress when the budget ran out". A pipeline that needs the
// release live branches on 8 and runs `.applying.watch`:
// `forge env status <env> --wait --promotion <id>`, pinned to this deploy.
//
// ONLY WHEN NOTHING IS LEFT FOR THIS MACHINE TO DO. In a mixed env the hosted
// stage can come before a local apply or a bucket/Firebase frontend (the
// default order, or a declared dependency), and a stage that stopped on the
// pending apply never ran those. The platform cannot run them, so watching
// can never finish the deploy: that is deployStagesNotRunError, a failure
// naming the stages that did not run, never "still applying".

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// applyingOf extracts a still-applying outcome from an error chain.
func applyingOf(err error) *deploytarget.ApplyPendingError {
	var pending *deploytarget.ApplyPendingError
	if errors.As(err, &pending) {
		return pending
	}
	return nil
}

// deployApplyingError carries a still-applying follow-through out of the
// deploy, with exit 8. The chain is kept whole, so errors.As still finds the
// provider's *deploytarget.ApplyPendingError.
type deployApplyingError struct {
	err     error
	pending *deploytarget.ApplyPendingError
}

func (e *deployApplyingError) ExitCode() int { return exitTimedOut }
func (e *deployApplyingError) Unwrap() error { return e.err }

func (e *deployApplyingError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "deploy to %s is STILL APPLYING, not failed: %s", e.pending.Env, e.pending.Error())
	b.WriteString("\n  (exit 8: still in progress when the wait ended, not a failure)")
	return b.String()
}

// asApplying wraps err as a deployApplyingError when it is a still-applying
// outcome, and returns it unchanged otherwise.
func asApplying(err error) error {
	if p := applyingOf(err); p != nil {
		var already *deployApplyingError
		if errors.As(err, &already) {
			return err
		}
		return &deployApplyingError{err: err, pending: p}
	}
	return err
}

// deployApplyingJSON is the `applying` object of the deploy document.
type deployApplyingJSON struct {
	Env string `json:"env"`
	// PromotionID is the promotion this deploy recorded. Watch is pinned to
	// it, so a pipeline following .applying.watch waits on THIS release even
	// after a newer deploy moves the env.
	PromotionID    string `json:"promotion_id,omitempty"`
	Bundle         string `json:"bundle"`
	CurrentApplied string `json:"current_applied,omitempty"`
	WaitedSeconds  int    `json:"waited_seconds"`
	Watch          string `json:"watch"`
}

// applyingDocument renders a pending apply. recorded is the promotion this
// command recorded; it fills in a provider error that carried none, and never
// overrides one that did.
func applyingDocument(p *deploytarget.ApplyPendingError, recorded string) *deployApplyingJSON {
	pinned := *p
	if pinned.PromotionID == "" {
		pinned.PromotionID = recorded
	}
	return &deployApplyingJSON{
		Env: pinned.Env, PromotionID: pinned.PromotionID, Bundle: pinned.Bundle, CurrentApplied: pinned.Current,
		WaitedSeconds: int(pinned.Waited.Seconds()),
		Watch:         pinned.WatchCommand(),
	}
}

// deployStagesNotRunError is a deploy whose hosted stage was still applying
// when its wait ended, with stages after it that never ran. It deliberately
// does not unwrap to the pending apply: it is an incomplete deploy (exit 1),
// not a still-applying one.
type deployStagesNotRunError struct {
	env    string
	cause  error
	notRun []string
}

func (e *deployStagesNotRunError) Error() string {
	watch := "forge env status " + e.env + " --wait"
	if p := applyingOf(e.cause); p != nil {
		watch = p.WatchCommand()
	}
	return fmt.Sprintf("INCOMPLETE: %v; the stages after it did not run, and the platform cannot run them: %s. "+
		"Once it is applied (%s), deploy again to ship them",
		e.cause, strings.Join(e.notRun, ", "), watch)
}

// stageStopError is the error a deploy returns when a stage fails with err and
// the stages in later never run. A still-applying hosted stage with work left
// in a later stage becomes a deployStagesNotRunError; every other error is
// returned as it is.
func stageStopError(env string, err error, later []deployStage, work map[deployStage][]string) error {
	if applyingOf(err) == nil {
		return err
	}
	var notRun []string
	for _, stage := range later {
		notRun = append(notRun, work[stage]...)
	}
	if len(notRun) == 0 {
		return err
	}
	return &deployStagesNotRunError{env: env, cause: err, notRun: notRun}
}
