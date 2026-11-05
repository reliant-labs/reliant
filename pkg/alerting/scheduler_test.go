// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type fakeRules struct {
	mu    sync.Mutex
	rules map[string][]Rule
}

func (f *fakeRules) set(owner string, r ...Rule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rules == nil {
		f.rules = map[string][]Rule{}
	}
	f.rules[owner] = r
}

func (f *fakeRules) Owners(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for t, r := range f.rules {
		if len(r) > 0 {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeRules) Rules(_ context.Context, owner string) ([]Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Rule(nil), f.rules[owner]...), nil
}

func ruleFor(owner, name string) Rule {
	r := evalRule()
	r.Owner, r.Name, r.Interval = owner, name, time.Millisecond
	r.Identity = map[string]string{"reliant_org_id": owner}
	return r
}

func newSched(t *testing.T, h *harness, rules *fakeRules, shards int, lk Locker) *Scheduler {
	t.Helper()
	s, err := NewScheduler(SchedulerDeps{Rules: rules, Store: h.store, Eval: h.ev, Shards: shards, Locker: lk, Tick: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTickEvaluatesEachOwnerWithItsOwnKey(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Millisecond})
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	h.q.rows["org-b"] = []Row{{"s": "b"}}
	rules := &fakeRules{}
	rules.set("org-a", ruleFor("org-a", "r"))
	rules.set("org-b", ruleFor("org-b", "r"))
	s := newSched(t, h, rules, 1, nil)

	cache := map[string]*ownerState{}
	s.tick(context.Background(), 0, cache)
	time.Sleep(5 * time.Millisecond)
	s.tick(context.Background(), 0, cache)

	orgs := map[string]string{}
	for _, a := range h.n.all() {
		orgs[a.Labels["reliant_org_id"]] = a.Labels["s"]
	}
	if orgs["org-a"] != "a" || orgs["org-b"] != "b" || len(orgs) != 2 {
		t.Fatalf("each owner's alerts must carry only its own rows and identity: %v", orgs)
	}
	for _, c := range h.q.calls {
		if c[:5] != "org-a" && c[:5] != "org-b" {
			t.Fatalf("query without an owner key: %q", c)
		}
	}
}

func TestRuleSourceCannotSmuggleAnotherOwnersRule(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Millisecond})
	h.q.rows["org-b"] = []Row{{"s": "b"}}
	rules := &fakeRules{}
	rules.set("org-a", ruleFor("org-b", "stolen"))
	s := newSched(t, h, rules, 1, nil)
	cache := map[string]*ownerState{}
	for i := 0; i < 2; i++ {
		s.tick(context.Background(), 0, cache)
		time.Sleep(5 * time.Millisecond)
	}
	if len(h.q.calls) != 0 {
		t.Fatalf("a rule listed under the wrong owner was evaluated: %v", h.q.calls)
	}
}

func TestRemovedRuleIsRetiredAndResolved(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Millisecond})
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	rules := &fakeRules{}
	rules.set("org-a", ruleFor("org-a", "r"))
	s := newSched(t, h, rules, 1, nil)
	cache := map[string]*ownerState{}
	s.tick(context.Background(), 0, cache)
	time.Sleep(5 * time.Millisecond)
	s.tick(context.Background(), 0, cache)

	rules.set("org-a") // the rule is no longer declared
	s.tick(context.Background(), 0, cache)

	all := h.n.all()
	if last := all[len(all)-1]; !last.EndsAt.Equal(h.now) {
		t.Fatalf("removal must resolve, got an alert ending %v", last.EndsAt)
	}
	left, _ := h.store.LoadOwner(context.Background(), "org-a")
	if len(left) != 1 || left[0].State != StateResolved || !left[0].ResolveSent {
		t.Fatalf("left = %+v", left)
	}
	s.tick(context.Background(), 0, cache)
	left, _ = h.store.LoadOwner(context.Background(), "org-a")
	if len(left) != 0 {
		t.Fatalf("a delivered removal must be cleaned up: %+v", left)
	}
}

func TestStateSurvivesAHandover(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Millisecond})
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	rules := &fakeRules{}
	r := ruleFor("org-a", "r")
	r.For = time.Hour
	rules.set("org-a", r)
	s := newSched(t, h, rules, 1, nil)
	owner1 := map[string]*ownerState{}
	s.tick(context.Background(), 0, owner1)
	time.Sleep(5 * time.Millisecond)
	s.tick(context.Background(), 0, owner1)
	owner2 := map[string]*ownerState{} // the lease moved: empty caches
	s.tick(context.Background(), 0, owner2)
	time.Sleep(5 * time.Millisecond)
	s.tick(context.Background(), 0, owner2)

	stored, _ := h.store.LoadOwner(context.Background(), "org-a")
	if len(stored) != 1 || stored[0].State != StatePending {
		t.Fatalf("stored = %+v", stored)
	}
	if got := len(allEvents(t, h.store)); got != 1 {
		t.Fatalf("the new owner must continue the pending run, not restart it: %d pending events", got)
	}
}

func TestShardingPartitionsOwners(t *testing.T) {
	h := newHarness(t, Limits{})
	s := newSched(t, h, &fakeRules{}, 8, nil)
	count := map[int]int{}
	for i := 0; i < 400; i++ {
		count[s.ShardOf("org-"+string(rune('a'+i%26))+string(rune('a'+i/26)))]++
	}
	if len(count) < 6 {
		t.Fatalf("owners hash into only %d of 8 shards", len(count))
	}
	a, b := "org-a", "org-"+"a"
	if s.ShardOf(a) != s.ShardOf(b) {
		t.Fatal("ShardOf is not stable")
	}
}

type recordingLocker struct {
	mu    sync.Mutex
	names []string
}

func (l *recordingLocker) Run(ctx context.Context, name string, fn func(context.Context) error) error {
	l.mu.Lock()
	l.names = append(l.names, name)
	l.mu.Unlock()
	return fn(ctx)
}

func TestRunTakesOneLeasePerShardAndStops(t *testing.T) {
	h := newHarness(t, Limits{})
	lk := &recordingLocker{}
	s, _ := NewScheduler(SchedulerDeps{Rules: &fakeRules{}, Store: h.store, Eval: h.ev, Shards: 4, Locker: lk, Tick: 5 * time.Millisecond, LeasePrefix: "x"})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	sort.Strings(lk.names)
	want := []string{"x/shard/0", "x/shard/1", "x/shard/2", "x/shard/3"}
	if len(lk.names) != 4 {
		t.Fatalf("leases = %v, want %v", lk.names, want)
	}
	for i := range want {
		if lk.names[i] != want[i] {
			t.Fatalf("leases = %v, want %v", lk.names, want)
		}
	}
}

func TestJitterIsStableAndWithinTheInterval(t *testing.T) {
	k := RuleKey{"t", "e", "r"}
	k2 := RuleKey{k.Owner, k.Env, k.Name}
	if jitter(k, time.Minute) != jitter(k2, time.Minute) {
		t.Fatal("jitter must be stable across restarts")
	}
	seen := map[time.Duration]bool{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		j := jitter(RuleKey{"t", "e", n}, time.Minute)
		if j < 0 || j >= time.Minute {
			t.Fatalf("jitter %v out of range", j)
		}
		seen[j] = true
	}
	if len(seen) < 4 {
		t.Fatalf("jitter does not spread rules: %v", seen)
	}
}

func TestNewSchedulerRequiresDeps(t *testing.T) {
	if _, err := NewScheduler(SchedulerDeps{}); err == nil {
		t.Fatal("expected an error")
	}
}

func allEvents(t *testing.T, s StateStore) []Event {
	t.Helper()
	var out []Event
	for _, owner := range []string{"org-a", "org-b"} {
		ev, err := s.Events(context.Background(), EventQuery{Owner: owner, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev...)
	}
	return out
}

// The cap must never be silent: a log naming the dropped rules, a gauge, and an
// AlertRulesOverCap alert carrying the env's own identity.
func TestOverCapEnvIsLoud(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Millisecond, MaxRulesPerEnv: 2})
	h.q.rows["org-a"] = nil
	reader := sdkmetric.NewManualReader()
	var logs bytes.Buffer
	rules := &fakeRules{}
	for _, n := range []string{"r1", "r2", "r3", "r4"} {
		rules.set("org-a", append(rules.rules["org-a"], ruleFor("org-a", n))...)
	}
	for i := range rules.rules["org-a"] {
		rules.rules["org-a"][i].Env = "prod"
		rules.rules["org-a"][i].Identity["reliant_env_id"] = "prod"
	}
	s, err := NewScheduler(SchedulerDeps{
		Rules: rules, Store: h.store, Eval: h.ev, Shards: 1, Tick: 10 * time.Millisecond,
		Notifier: h.n, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("t"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]*ownerState{}
	s.tick(context.Background(), 0, cache)

	if out := logs.String(); !strings.Contains(out, "NOT evaluated") || !strings.Contains(out, "r3") || !strings.Contains(out, "r4") || strings.Contains(out, "r1") {
		t.Fatalf("the log must name exactly the dropped rule ids (r3, r4):\n%s", out)
	}
	var over []Alert
	for _, a := range h.n.all() {
		if a.Labels["alertname"] == OverCapAlertName {
			over = append(over, a)
		}
	}
	if len(over) != 1 || over[0].Labels["reliant_env_id"] != "prod" || over[0].Labels["reliant_org_id"] != "org-a" || over[0].Labels["severity"] != "critical" {
		t.Fatalf("AlertRulesOverCap = %+v", over)
	}
	if sum := over[0].Annotations["summary"]; !strings.Contains(sum, "r3") || !strings.Contains(sum, "r4") || !strings.Contains(sum, "4 alert rules") {
		t.Fatalf("summary = %q", sum)
	}
	if got := droppedGauge(t, reader, "prod"); got != 2 {
		t.Fatalf("alerting.rules.dropped{env=prod} = %d, want 2", got)
	}

	// Back under the cap: the gauge goes to zero and the alert stops being re-posted.
	rules.set("org-a", rules.rules["org-a"][:2]...)
	before := len(h.n.all())
	time.Sleep(5 * time.Millisecond)
	s.tick(context.Background(), 0, cache)
	if got := droppedGauge(t, reader, "prod"); got != 0 {
		t.Fatalf("gauge after recovery = %d, want 0", got)
	}
	for _, a := range h.n.all()[before:] {
		if a.Labels["alertname"] == OverCapAlertName {
			t.Fatal("AlertRulesOverCap must stop once the env is back under its cap")
		}
	}
}

// A dropped rule's condition is unknown, not clear: the cap must not resolve
// what it had firing, which would tell the owner an incident ended.
func TestDroppedRuleIsNotRetired(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Millisecond, MaxRulesPerEnv: 1})
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	rules := &fakeRules{}
	keep, drop := ruleFor("org-a", "a-keep"), ruleFor("org-a", "b-drop")
	keep.Env, drop.Env = "prod", "prod"
	rules.set("org-a", drop)
	s := newSched(t, h, rules, 1, nil)
	cache := map[string]*ownerState{}
	for i := 0; i < 2; i++ {
		s.tick(context.Background(), 0, cache)
		time.Sleep(5 * time.Millisecond)
	}
	if len(h.n.all()) == 0 {
		t.Fatal("setup: the rule should be firing")
	}
	n := len(h.n.all())
	rules.set("org-a", keep, drop) // b-drop now falls to the cap
	s.tick(context.Background(), 0, cache)
	for _, a := range h.n.all()[n:] {
		if a.Labels["alertname"] == "b-drop" && !a.EndsAt.After(time.Now()) {
			t.Fatalf("a rule dropped by the cap was reported resolved: %+v", a)
		}
	}
	stored, _ := h.store.LoadOwner(context.Background(), "org-a")
	var firing bool
	for _, in := range stored {
		if in.Name == "b-drop" && in.State == StateFiring {
			firing = true
		}
	}
	if !firing {
		t.Fatalf("the dropped rule's firing instance must be left as it was: %+v", stored)
	}
}

func droppedGauge(t *testing.T, r *sdkmetric.ManualReader, env string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "alerting.rules.dropped" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				if v, _ := dp.Attributes.Value("env"); v.AsString() == env {
					return dp.Value
				}
			}
		}
	}
	t.Fatalf("no alerting.rules.dropped for env %q", env)
	return 0
}

func TestNextDueIsFixedRateAndSkipsMissedSlots(t *testing.T) {
	base := time.Date(2026, 10, 26, 12, 0, 7, 0, time.UTC)
	tests := []struct {
		name     string
		now      time.Duration // after base
		interval time.Duration
		want     time.Duration // after base
	}{
		{"evaluated on time", 0, time.Minute, time.Minute},
		{"tick was late and the query slow: the period does not grow", 13 * time.Second, time.Minute, time.Minute},
		{"finished just before the next slot", 59 * time.Second, time.Minute, time.Minute},
		{"finished exactly on the next slot: that slot is missed", time.Minute, time.Minute, 2 * time.Minute},
		{"one slot overrun", 61 * time.Second, time.Minute, 2 * time.Minute},
		{"a pass that stalled for ten intervals does not replay them", 10*time.Minute + time.Second, time.Minute, 11 * time.Minute},
		{"5m interval keeps its phase", 20 * time.Second, 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextDue(base, tc.interval, base.Add(tc.now)); !got.Equal(base.Add(tc.want)) {
				t.Fatalf("nextDue = +%v, want +%v", got.Sub(base), tc.want)
			}
		})
	}
}

func TestARuleKeepsItsPeriodWhileEvaluationsTakeTime(t *testing.T) {
	// A hundred evaluations, each finishing 1s after its slot, must not drift:
	// the n-th slot is exactly n intervals after the first.
	slot := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	first := slot
	for i := 1; i <= 100; i++ {
		slot = nextDue(slot, time.Minute, slot.Add(time.Second))
		if want := first.Add(time.Duration(i) * time.Minute); !slot.Equal(want) {
			t.Fatalf("slot %d = %v, want %v", i, slot, want)
		}
	}
}
