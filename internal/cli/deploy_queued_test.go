package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// A QUEUED hosted deploy: the control plane ACCEPTED and RECORDED it and is
// holding it on a person (billing). These run the real release path through
// the real hostedStore against the fake control plane, so the holds forge acts
// on are the ones the server's Promote response carried.
//
// The contract under test is what an agent relies on: exit 7 (and nothing else
// exits 7), one block naming what it waits on, why, and the action URL, the
// same facts under --json, and --wait blocking through the queue instead.

const billingURL = "https://app.reliant.test/forge/env/prod?forgeProject=acme"

func billingHold() deploytarget.HostedHold {
	return deploytarget.HostedHold{
		Kind:      "DEPLOY_HOLD_KIND_BILLING",
		Reason:    "this runs compute (1 workload, 1 database) and the organization has no active compute plan",
		Fix:       "Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can). The deploy starts automatically once the plan is active; nothing needs to be re-run.",
		ActionURL: billingURL,
	}
}

func queuedFixture(t *testing.T) *hostedStore {
	t.Helper()
	fake, store := hostedPromoteFixture(t, "v1")
	fake.queueOn = []deploytarget.HostedHold{billingHold()}
	return store
}

// THE OWNER'S CASE. A deploy the control plane queues on billing exits 7 with
// the block — what, why, where — and does NOT sit out a fifteen-minute wait on
// a promotion nothing is moving.
func TestDeployRelease_QueuedOnBillingExitsSevenWithTheBlock(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	var publish capturedClientDeploy
	publish.install(t)

	store := queuedFixture(t)
	_, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()})
	if got := exitCodeForError(err); got != exitQueued {
		t.Fatalf("exit code = %d, want %d (queued) — err: %v", got, exitQueued, err)
	}
	msg := err.Error()
	for _, want := range []string{
		"QUEUED, not failed", "release v2", "waits on billing",
		"why   this runs compute (1 workload, 1 database)",
		"do    Subscribe to a Reliant Compute plan",
		"open  " + billingURL,
		"nothing to re-run", "forge env status prod --wait",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the queued block must say %q, got:\n%s", want, msg)
		}
	}
	if len(wait.calls) != 0 {
		t.Errorf("a queued deploy ran the health gate %d time(s); nothing moves until a person acts", len(wait.calls))
	}
	// The promotion LANDED — queued is accepted, not refused.
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v2" {
		t.Fatalf("a queued deploy must still record its promotion: prod is on %s", cur.Release)
	}
	// And the bundle was still PUBLISHED, with nothing to wait on: it is what
	// the platform applies the moment the hold clears.
	if len(publish.calls) != 1 {
		t.Fatalf("the hosted publish ran %d time(s), want 1", len(publish.calls))
	}
	if mode := publish.calls[0].rollout.Mode; mode != cluster.RolloutSkip {
		t.Errorf("the queued deploy's publish waited (rollout mode %q); there is nothing to wait for", mode)
	}
}

// The --json document carries the same facts, structured, under the same exit
// code — what a pipeline (or an agent) reads instead of scraping text.
func TestDeployRelease_QueuedJSONCarriesTheHoldAndExitSeven(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	store := queuedFixture(t)

	out, err := runHostedPromote(t, store, "v2", promoteOptions{JSON: true, Follow: waitByDefault()})
	if got := exitCodeForError(err); got != exitQueued {
		t.Fatalf("exit code = %d, want %d (%v)", got, exitQueued, err)
	}
	var doc struct {
		OK       bool `json:"ok"`
		ExitCode int  `json:"exit_code"`
		Applied  bool `json:"applied"`
		Queued   *struct {
			Env       string `json:"env"`
			Release   string `json:"release"`
			WaitingOn string `json:"waiting_on"`
			Holds     []struct {
				Kind      string `json:"kind"`
				Reason    string `json:"reason"`
				ActionURL string `json:"action_url"`
			} `json:"holds"`
		} `json:"queued"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("a queued deploy must emit ONE JSON document: %v\n%s", jerr, out)
	}
	if doc.OK || doc.ExitCode != exitQueued || !doc.Applied {
		t.Errorf("ok/exit_code/applied = %v/%d/%v, want false/7/true — recorded, and queued", doc.OK, doc.ExitCode, doc.Applied)
	}
	if doc.Queued == nil || len(doc.Queued.Holds) != 1 {
		t.Fatalf("`queued` must name the hold:\n%s", out)
	}
	h := doc.Queued.Holds[0]
	if doc.Queued.WaitingOn != "billing" || h.Kind != "billing" || h.ActionURL != billingURL || h.Reason == "" {
		t.Errorf("queued = %+v, want billing with its reason and action_url", doc.Queued)
	}
}

// --no-wait still reports the queue: the control plane said so on the write,
// so knowing costs nothing, and a pipeline that skipped the health gate is
// exactly the one that would otherwise never learn a person has to act.
func TestDeployRelease_QueuedUnderNoWaitStillExitsSeven(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	store := queuedFixture(t)
	_, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: &promoteFollowOptions{NoWait: true}})
	if got := exitCodeForError(err); got != exitQueued {
		t.Fatalf("--no-wait on a queued deploy: exit code = %d, want %d (%v)", got, exitQueued, err)
	}
}

// --wait blocks THROUGH the queue: the deploy goes to the rollout wait, told
// not to stop at the hold, and the wait's outcome is the deploy's.
func TestDeployRelease_QueuedWithWaitWaitsThroughTheHold(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	store := queuedFixture(t)

	out, err := runHostedPromote(t, store, "v2", promoteOptions{
		Follow: &promoteFollowOptions{Wait: true, Timeout: time.Hour},
	})
	if err != nil {
		t.Fatalf("--wait on a queued deploy whose wait then succeeded: %v", err)
	}
	if len(wait.calls) != 1 {
		t.Fatalf("--wait ran the rollout wait %d time(s), want 1", len(wait.calls))
	}
	if got := wait.calls[0]; got.StopOnHold || got.Timeout != time.Hour {
		t.Errorf("wait options = StopOnHold %v, Timeout %s; want false, 1h", got.StopOnHold, got.Timeout)
	}
	if !strings.Contains(out, "waiting until it is live (up to 1h0m0s)") || !strings.Contains(out, billingURL) {
		t.Errorf("--wait must still show what it waits on and where to act, got:\n%s", out)
	}
}

// A deploy the control plane did NOT queue is untouched: it waits as always,
// and the wait is told to stop (exit 7) should it find a hold after all.
func TestDeployRelease_NotQueuedWaitsAsBefore(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	_, store := hostedPromoteFixture(t, "v1")
	if _, err := runHostedPromote(t, store, "v2", promoteOptions{Follow: waitByDefault()}); err != nil {
		t.Fatal(err)
	}
	if len(wait.calls) != 1 || !wait.calls[0].StopOnHold {
		t.Fatalf("wait calls = %+v, want one, with StopOnHold (no --wait)", wait.calls)
	}
}

func TestValidatePromoteFollow_WaitAndNoWaitConflict(t *testing.T) {
	if err := validatePromoteFollow(promoteFollowOptions{Wait: true, NoWait: true}); err == nil {
		t.Fatal("--wait with --no-wait must be refused before anything is written")
	}
}

// ─── The rollout wait ────────────────────────────────────────────────────────

// heldRollout answers GetRollout with a HELD phase carrying the billing hold,
// then (optionally) moves on — the hold clearing when someone pays.
type heldRollout struct {
	*fakeRolloutService
}

func newHeldRollout(phases ...string) *heldRollout {
	return &heldRollout{fakeRolloutService: newFakeRollout(phases...)}
}

func (h *heldRollout) Call(ctx context.Context, procedure string, req, out any) error {
	if err := h.fakeRolloutService.Call(ctx, procedure, req, out); err != nil {
		return err
	}
	resp, ok := out.(*struct {
		Rollout wireRollout `json:"rollout"`
	})
	if ok && resp.Rollout.Phase == wireRolloutPhaseHeld {
		resp.Rollout.Workloads = nil // nothing applied: the release is queued
		resp.Rollout.Holds = []deploytarget.HostedHold{billingHold()}
	}
	return nil
}

func heldWaitOpts(fake *heldRollout) envWaitOptions {
	return envWaitOptions{
		Target: func(context.Context, string) (waitTarget, error) {
			return waitTarget{Client: fake, EnvironmentID: "env-prod-uuid", Endpoint: "https://cp.example"}, nil
		},
		Interval: time.Millisecond,
		Timeout:  40 * time.Millisecond,
	}
}

func TestEnvWait_HeldIsExitSeven(t *testing.T) {
	cases := []struct {
		name   string
		phases []string
		opts   func(*envWaitOptions)
		want   int
		says   string
	}{
		{"a deploy without --wait stops at the hold", []string{wireRolloutPhaseHeld},
			func(o *envWaitOptions) { o.StopOnHold = true }, exitQueued, "QUEUED, not failed"},
		{"a single read of a held rollout", []string{wireRolloutPhaseHeld},
			func(o *envWaitOptions) { o.Once = true; o.Timeout = 0 }, exitQueued, "QUEUED, not failed"},
		{"--wait still held at the deadline", []string{wireRolloutPhaseHeld},
			func(*envWaitOptions) {}, exitQueued, "still QUEUED after"},
		// The person acts, the hold clears, the release rolls out.
		{"--wait through the hold to live", []string{wireRolloutPhaseHeld, wireRolloutPhaseHeld,
			wireRolloutPhasePending, wireRolloutPhaseProgressing, wireRolloutPhaseSucceeded},
			func(o *envWaitOptions) { o.Timeout = 5 * time.Second }, exitOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newHeldRollout(tc.phases...)
			opts := heldWaitOpts(fake)
			tc.opts(&opts)
			var err error
			captureStdout(t, func() { err = runEnvWait(context.Background(), "prod", opts) })
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("exit code = %d, want %d (%v)", got, tc.want, err)
			}
			if tc.says != "" && (!strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), billingURL)) {
				t.Errorf("message must say %q and carry the action URL, got:\n%v", tc.says, err)
			}
		})
	}
}

// The wait's --json document says it is queued, with the hold, under exit 7.
func TestEnvWait_HeldJSON(t *testing.T) {
	fake := newHeldRollout(wireRolloutPhaseHeld)
	opts := heldWaitOpts(fake)
	opts.JSON, opts.StopOnHold = true, true
	var err error
	out := captureStdout(t, func() { err = runEnvWait(context.Background(), "prod", opts) })
	if got := exitCodeForError(err); got != exitQueued {
		t.Fatalf("exit code = %d, want 7", got)
	}
	var doc struct {
		Phase    string `json:"phase"`
		ExitCode int    `json:"exit_code"`
		Queued   *struct {
			WaitingOn string `json:"waiting_on"`
			Holds     []struct {
				ActionURL string `json:"action_url"`
			} `json:"holds"`
		} `json:"queued"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, out)
	}
	if doc.Phase != "held" || doc.ExitCode != exitQueued || doc.Queued == nil ||
		doc.Queued.WaitingOn != "billing" || doc.Queued.Holds[0].ActionURL != billingURL {
		t.Fatalf("document = %s, want phase held, exit 7, queued on billing with the URL", out)
	}
}

// ─── env status ──────────────────────────────────────────────────────────────

// `forge env status` on a hosted env whose bound release is queued: exit 7 with
// the block — not exit 1 "drifted", which would blame a release nothing is
// wrong with for not running yet.
func TestEnvStatus_QueuedHostedEnvIsSevenNotDrift(t *testing.T) {
	binding := release.Promotion{ID: "promo-9", Env: "prod", Release: "v2",
		Resolved: map[string]string{"ghcr.io/acme/api": sha("2")}}
	opts := envStatusOptions{
		Timeout: time.Second,
		HostedRollout: func(context.Context, string, string) (wireRollout, error) {
			return wireRollout{
				Promotion: wirePromotion{ID: "promo-9", ReleaseVersion: "v2"},
				Phase:     wireRolloutPhaseHeld,
				Holds:     []deploytarget.HostedHold{billingHold()},
			}, nil
		},
	}
	results, queued := verifyHosted(context.Background(), "prod", binding, opts)
	if queued == nil {
		t.Fatal("a HELD rollout must be reported as queued")
	}
	if got := exitCodeForError(queued); got != exitQueued {
		t.Fatalf("exit code = %d, want 7", got)
	}
	if !strings.Contains(queued.Error(), billingURL) || !strings.Contains(queued.Error(), "release v2") {
		t.Errorf("status must say what it waits on and where to act:\n%s", queued.Error())
	}
	_ = results
}

// ─── The capacity pre-flight ─────────────────────────────────────────────────

// A pre-flight the control plane answers "would be queued" is NOT a refusal:
// the build, the record and the promote still run, which is what lets the
// deploy go live with no re-run once billing is set up.
func TestCapacityVerdict_QueuedIsNotARefusal(t *testing.T) {
	v := deploytarget.CapacityVerdict{
		Allowed: false, Code: "NO_COMPUTE_PLAN",
		Reason: "this runs compute (1 workload) and the organization has no active compute plan",
		Holds:  []deploytarget.HostedHold{billingHold()},
	}
	if !v.Queued() {
		t.Fatal("allowed=false WITH holds is a queue")
	}
	if s := v.Summary(); !strings.HasPrefix(s, "QUEUED on billing") {
		t.Errorf("summary = %q, want it to say QUEUED", s)
	}
	refused := v
	refused.Holds = nil
	if refused.Queued() {
		t.Fatal("allowed=false with NO holds (an older control plane, or a plan too small) is still a refusal")
	}
}
