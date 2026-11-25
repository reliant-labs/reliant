package cigate

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A check run picks the tier from the GitHub event that started it, in
// either spelling: GitHub Actions' event_name or a Reliant trigger's
// event.action.
func TestTierForEvent(t *testing.T) {
	for event, want := range map[string]string{
		"pull_request":                 TierPR,
		"pull_request.synchronize":     TierPR,
		"pull_request.opened":          TierPR,
		"pull_request_target":          TierPR,
		"merge_group":                  TierQueue,
		"merge_group.checks_requested": TierQueue,
		"push":                         TierMain,
		"push.received":                TierMain,
		"schedule":                     TierFull,
		"workflow_dispatch":            TierFull,
		" PUSH ":                       TierMain,
	} {
		got, err := TierForEvent(event)
		if err != nil || got.Name != want {
			t.Errorf("TierForEvent(%q) = %q, %v; want %q", event, got.Name, err, want)
		}
	}
	_, err := TierForEvent("issue_comment")
	if err == nil || !strings.Contains(err.Error(), "pull_request, pull_request_target, merge_group, push, schedule, workflow_dispatch") {
		t.Errorf("an event with no tier must be refused naming the known ones, got %v", err)
	}
}

func TestParseTier(t *testing.T) {
	for in, want := range map[string]string{"pr": TierPR, "queue": TierQueue, "merge": TierQueue, "MAIN": TierMain, "full": TierFull} {
		got, err := ParseTier(in)
		if err != nil || got.Name != want {
			t.Errorf("ParseTier(%q) = %q, %v; want %q", in, got.Name, err, want)
		}
	}
	if _, err := ParseTier("nightly"); err == nil || !strings.Contains(err.Error(), "pr, queue, main, full") {
		t.Errorf("an unknown tier must name the valid ones, got %v", err)
	}
}

// What each tier runs, per lane: the contract a check workflow relies on.
func TestTierLanes(t *testing.T) {
	for _, c := range []struct {
		tier      string
		lanes     []Lane
		fullSuite bool
	}{
		{TierPR, []Lane{LaneChecks, LaneTest, LaneIntegration}, false},
		{TierQueue, []Lane{LaneChecks, LaneTest, LaneIntegration}, false},
		{TierMain, []Lane{LaneChecks, LaneTest}, true},
		{TierFull, []Lane{LaneChecks, LaneTest, LaneIntegration}, true},
	} {
		tier, err := ParseTier(c.tier)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(tier.Lanes, c.lanes) || tier.FullSuite != c.fullSuite {
			t.Errorf("%s: lanes %v full %v; want %v full %v", c.tier, tier.Lanes, tier.FullSuite, c.lanes, c.fullSuite)
		}
		// A lane of the project's own runs whenever it is asked for.
		if !tier.Runs("docker") {
			t.Errorf("%s must run a lane an extra check declares", c.tier)
		}
	}
}

// The tiers mirror the scaffolded ci.yml: its integration job runs on pull
// requests only, which is why the main (push) tier defers that lane. If the
// workflow's condition changes, the tier table must change with it.
func TestTiersMirrorScaffoldedWorkflow(t *testing.T) {
	b, err := os.ReadFile("../templates/ci/github/ci.yml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	i := strings.Index(wf, "\n  integration-test:")
	if i < 0 {
		t.Fatal("ci.yml.tmpl has no integration-test job")
	}
	job := wf[i:]
	if j := strings.Index(job, "steps:"); j > 0 {
		job = job[:j]
	}
	prOnly := strings.Contains(job, "github.event_name == 'pull_request'")
	main, _ := ParseTier(TierMain)
	if prOnly == main.Runs(LaneIntegration) {
		t.Errorf("ci.yml's integration job is pull-request-only=%v, but the main tier runs integration=%v", prOnly, main.Runs(LaneIntegration))
	}
	for _, trigger := range []string{"push:", "pull_request:"} {
		if !strings.Contains(wf, "\n  "+trigger) {
			t.Errorf("ci.yml.tmpl no longer triggers on %s; revisit the tier table", trigger)
		}
	}
}

func TestTierDefer(t *testing.T) {
	checks := []Check{
		{Name: "go-build", Lane: LaneChecks},
		{Name: UnitTests, Lane: LaneTest},
		{Name: "integration-tests", Lane: LaneIntegration},
		{Name: "image", Lane: "docker"},
	}
	main, _ := ParseTier(TierMain)
	got := main.Defer(checks)
	for _, c := range got {
		deferred := c.Name == "integration-tests"
		if (c.Deferred != "") != deferred {
			t.Errorf("%s deferred=%q under main", c.Name, c.Deferred)
		}
	}
	if want := "the main tier does not run the integration lane; pr, queue, full do"; got[2].Deferred != want {
		t.Errorf("note %q, want %q", got[2].Deferred, want)
	}
	if checks[2].Deferred != "" {
		t.Error("Defer must not modify its input")
	}
	pr, _ := ParseTier(TierPR)
	for _, c := range pr.Defer(checks) {
		if c.Deferred != "" {
			t.Errorf("pr defers %s", c.Name)
		}
	}
}

// A deferred row runs nothing, is listed, and does not fail the gate.
func TestRun_DeferredRowRunsNothingAndPasses(t *testing.T) {
	fx := &fakeExec{}
	rep := Run(context.Background(), "all",
		[]Check{check("build", "build"), {Name: "integration-tests", Lane: LaneIntegration, Argv: []string{"itest"}, Deferred: "later"}},
		Options{Out: io.Discard, Exec: fx.run})
	if got := fx.ranSorted(); got != "build" {
		t.Errorf("ran %q; a deferred check must not run", got)
	}
	if st := statuses(rep)["integration-tests"]; st != Deferred {
		t.Errorf("status %s, want DEFERRED", st)
	}
	if rep.Failed() {
		t.Error("a deferred row must not fail the gate")
	}
	var b strings.Builder
	rep.RenderText(&b)
	if !strings.Contains(b.String(), "DEFERRED") || !strings.Contains(b.String(), "1 deferred") {
		t.Errorf("the table must list the deferred row:\n%s", b.String())
	}
}

// stepClock is a fake clock that moves only when the test advances it, so a
// measured duration is exactly what the test scripted.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Queued is the wait for a heavy slot, measured apart from the run: the slot
// is granted after 3s of the fake clock, and the command runs for 1s of it.
func TestRun_RecordsQueuedTime(t *testing.T) {
	clock := &stepClock{now: time.Unix(0, 0)}
	slots := &fakeSlots{sem: make(chan struct{}, 1)}
	slot := func(ctx context.Context) (HeldSlot, error) {
		clock.advance(3 * time.Second)
		return slots.take(ctx)
	}
	run := func(_ context.Context, _ string, _ []string, _ []string, _ io.Writer) error {
		clock.advance(time.Second)
		return nil
	}
	c := check("build", "build")
	c.Heavy = true
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: run, Slot: slot, Clock: clock.Now})
	if got := rep.Results[0]; got.Queued != 3*time.Second || got.Duration != time.Second {
		t.Errorf("queued %v, ran %v; want 3s queued and 1s run", got.Queued, got.Duration)
	}
}
