// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeQuerier struct {
	mu    sync.Mutex
	rows  map[string][]Row
	err   error
	calls []string
	opts  []QueryOptions
}

func (f *fakeQuerier) Query(_ context.Context, owner, q string, o QueryOptions) ([]Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, owner+"|"+q)
	f.opts = append(f.opts, o)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows[owner], nil
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent [][]Alert
	err  error
}

func (f *fakeNotifier) Notify(_ context.Context, a []Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, a)
	return nil
}

func (f *fakeNotifier) all() []Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Alert
	for _, s := range f.sent {
		out = append(out, s...)
	}
	return out
}

type harness struct {
	ev    *Evaluator
	q     *fakeQuerier
	n     *fakeNotifier
	store *MemoryStore
	now   time.Time
}

func newHarness(t *testing.T, lim Limits) *harness {
	t.Helper()
	h := &harness{q: &fakeQuerier{rows: map[string][]Row{}}, n: &fakeNotifier{}, store: NewMemoryStore(), now: t0}
	ev, err := NewEvaluator(EvaluatorDeps{Querier: h.q, Notifier: h.n, Store: h.store, Limits: lim, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	h.ev = ev
	return h
}

func evalRule() Rule {
	return Rule{
		RuleKey: RuleKey{"org-a", "prod", "slow"}, Expr: "SELECT service, p95 AS value FROM x", For: 0, Interval: time.Minute,
		Identity: map[string]string{"reliant_org_id": "org-a", "reliant_env_id": "prod"},
	}
}

func TestEvaluateFiringThenResolving(t *testing.T) {
	h := newHarness(t, Limits{})
	r := evalRule()
	h.q.rows["org-a"] = []Row{{"service": "api", "value": 900.0}}

	state, err := h.ev.EvaluateRule(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := h.n.all()
	if len(sent) != 1 || sent[0].Labels["reliant_org_id"] != "org-a" || sent[0].Labels["alertname"] != "slow" {
		t.Fatalf("sent = %+v", sent)
	}
	if !sent[0].EndsAt.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("EndsAt = %v", sent[0].EndsAt)
	}
	stored, _ := h.store.LoadOwner(context.Background(), "org-a")
	if len(stored) != 1 || stored[0].State != StateFiring {
		t.Fatalf("stored = %+v", stored)
	}

	h.q.rows["org-a"] = nil
	h.now = t0.Add(time.Minute)
	state, _ = h.ev.EvaluateRule(context.Background(), r, state) // held
	h.now = t0.Add(2 * time.Minute)
	state, _ = h.ev.EvaluateRule(context.Background(), r, state) // resolved
	if got := h.n.all(); len(got) != 3 || !got[2].EndsAt.Equal(h.now) {
		t.Fatalf("want fire, re-send while held, resolve; got %+v", got)
	}
	if len(state) != 1 || state[0].State != StateResolved || !state[0].ResolveSent {
		t.Fatalf("state = %+v", state)
	}

	h.now = t0.Add(3 * time.Minute)
	state, _ = h.ev.EvaluateRule(context.Background(), r, state)
	if len(state) != 0 || len(h.n.all()) != 3 {
		t.Fatalf("a delivered resolution must send nothing further: state=%+v sent=%d", state, len(h.n.all()))
	}
	got := map[Transition]int{}
	for _, e := range allEvents(t, h.store) {
		got[e.Transition]++
	}
	if got[TransitionFiring] != 1 || got[TransitionResolved] != 1 {
		t.Fatalf("history = %v", got)
	}
}

func TestQueryErrorKeepsStateAndKeepsRefreshing(t *testing.T) {
	h := newHarness(t, Limits{})
	r := evalRule()
	h.q.rows["org-a"] = []Row{{"service": "api", "value": 1.0}}
	state, _ := h.ev.EvaluateRule(context.Background(), r, nil)

	h.q.err = errors.New("clickhouse down")
	h.now = t0.Add(time.Minute)
	after, err := h.ev.EvaluateRule(context.Background(), r, state)
	if err == nil {
		t.Fatal("expected the error")
	}
	if len(after) != 1 || after[0].State != StateFiring || !after[0].ClearedAt.IsZero() {
		t.Fatalf("an error must not count as a miss: %+v", after)
	}
	if got := h.n.all(); len(got) != 2 || !got[1].EndsAt.After(h.now) {
		t.Fatalf("a firing alert must keep being refreshed through an outage: %+v", got)
	}
}

func TestRefusedExprNeverReachesTheQuerier(t *testing.T) {
	h := newHarness(t, Limits{})
	r := evalRule()
	r.Expr = "SELECT 1; DROP TABLE t"
	if _, err := h.ev.EvaluateRule(context.Background(), r, nil); !errors.Is(err, ErrBadExpr) {
		t.Fatalf("err = %v", err)
	}
	if len(h.q.calls) != 0 {
		t.Fatalf("querier called: %v", h.q.calls)
	}
}

func TestInstanceCapIsAnErrorNotATruncation(t *testing.T) {
	h := newHarness(t, Limits{MaxInstancesPerRule: 2})
	r := evalRule()
	h.q.rows["org-a"] = []Row{{"s": "a"}, {"s": "b"}, {"s": "c"}}
	if _, err := h.ev.EvaluateRule(context.Background(), r, nil); !errors.Is(err, ErrBadResult) {
		t.Fatalf("err = %v", err)
	}
	if len(h.n.all()) != 0 {
		t.Fatal("an over-cap result must notify nothing")
	}
	if h.q.opts[0].MaxResultRows != 3 {
		t.Fatalf("MaxResultRows = %d, want cap+1 so overflow is detectable", h.q.opts[0].MaxResultRows)
	}
}

func TestQueryOptionsCarryLimits(t *testing.T) {
	h := newHarness(t, Limits{MaxExecutionTime: 7 * time.Second})
	h.q.rows["org-a"] = nil
	_, _ = h.ev.EvaluateRule(context.Background(), evalRule(), nil)
	if h.q.opts[0].MaxExecutionTime != 7*time.Second {
		t.Fatalf("opts = %+v", h.q.opts[0])
	}
}

func TestIntervalFloor(t *testing.T) {
	h := newHarness(t, Limits{MinInterval: time.Minute})
	r := evalRule()
	r.Interval = time.Second
	if got := h.ev.EffectiveInterval(r); got != time.Minute {
		t.Fatalf("interval = %v", got)
	}
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	_, _ = h.ev.EvaluateRule(context.Background(), r, nil)
	if !h.n.all()[0].EndsAt.Equal(t0.Add(3 * time.Minute)) {
		t.Fatal("EndsAt must use the floored interval")
	}
}

func capRule(env, name, severity string) Rule {
	r := evalRule()
	r.Env, r.Name = env, name
	r.Labels = map[string]string{"severity": severity}
	r.Identity = map[string]string{"reliant_org_id": "org-a", "reliant_env_id": env}
	return r
}

func TestRuleCapIsPerEnvNotPerOwner(t *testing.T) {
	h := newHarness(t, Limits{MaxRulesPerEnv: 3})
	var rules []Rule
	for _, env := range []string{"e1", "e2", "e3"} {
		for _, n := range []string{"a", "b", "c"} {
			rules = append(rules, capRule(env, n, "warning"))
		}
	}
	kept, over := h.ev.Select(rules)
	if len(kept) != 9 || len(over) != 0 {
		t.Fatalf("9 rules over 3 envs, each exactly at the cap, must all run: kept=%d over=%v", len(kept), over)
	}
}

func TestRuleCapDropsOnlyTheOverflowingEnvAndNamesWhatItDropped(t *testing.T) {
	h := newHarness(t, Limits{MaxRulesPerEnv: 2})
	rules := []Rule{
		capRule("big", "z-info", "info"),
		capRule("big", "a-warn", "warning"),
		capRule("big", "m-crit", "critical"),
		capRule("big", "b-warn", "warning"),
		capRule("small", "only", "warning"),
	}
	kept, over := h.ev.Select(rules)
	if len(over) != 1 || over[0].Env != "big" || over[0].Configured != 4 || over[0].Cap != 2 {
		t.Fatalf("over = %+v", over)
	}
	var dropped []string
	for _, r := range over[0].Dropped {
		dropped = append(dropped, r.Name)
	}
	// critical first, then warnings by name: the rules that fall are b-warn and the info one.
	if len(dropped) != 2 || dropped[0] != "b-warn" || dropped[1] != "z-info" {
		t.Fatalf("dropped = %v, want [b-warn z-info]", dropped)
	}
	var got []string
	for _, r := range kept {
		got = append(got, r.Env+"/"+r.Name)
	}
	want := []string{"big/a-warn", "big/m-crit", "small/only"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("kept = %v, want %v", got, want)
	}
}

func TestRuleCapDefaultIsNotTheOldPerOwnerHundred(t *testing.T) {
	if DefaultLimits().MaxRulesPerEnv != DefaultMaxRulesPerEnv || DefaultMaxRulesPerEnv <= 100 {
		t.Fatalf("default = %d", DefaultLimits().MaxRulesPerEnv)
	}
}

func TestNotifierFailureRetriesResolution(t *testing.T) {
	h := newHarness(t, Limits{})
	r := evalRule()
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	state, _ := h.ev.EvaluateRule(context.Background(), r, nil)
	h.q.rows["org-a"] = nil
	h.now = t0.Add(time.Minute)
	state, _ = h.ev.EvaluateRule(context.Background(), r, state)
	h.n.err = errors.New("alertmanager down")
	h.now = t0.Add(2 * time.Minute)
	state, _ = h.ev.EvaluateRule(context.Background(), r, state)
	if len(state) != 1 || state[0].State != StateResolved || state[0].ResolveSent {
		t.Fatalf("an undelivered resolution must stay pending delivery: %+v", state)
	}
	h.n.err = nil
	h.now = t0.Add(3 * time.Minute)
	state, _ = h.ev.EvaluateRule(context.Background(), r, state)
	if len(state) != 1 || !state[0].ResolveSent {
		t.Fatalf("resolution not retried: %+v", state)
	}
}

func TestRetireSendsResolutionForRemovedRule(t *testing.T) {
	h := newHarness(t, Limits{})
	r := evalRule()
	h.q.rows["org-a"] = []Row{{"s": "a"}}
	state, _ := h.ev.EvaluateRule(context.Background(), r, nil)
	h.now = t0.Add(time.Minute)
	state, err := h.ev.Retire(context.Background(), r.RuleKey, state, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.n.all(); len(got) != 2 || !got[1].EndsAt.Equal(h.now) {
		t.Fatalf("sent = %+v", got)
	}
	if len(state) != 1 || !state[0].ResolveSent {
		t.Fatalf("state = %+v", state)
	}
}

func TestNewEvaluatorRequiresDeps(t *testing.T) {
	if _, err := NewEvaluator(EvaluatorDeps{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAlertmanagerNotifier(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]map[string]any
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var v []map[string]any
		_ = json.Unmarshal(b, &v)
		mu.Lock()
		bodies = append(bodies, v)
		mu.Unlock()
		if r.URL.Path != "/api/v2/alerts" || r.Method != http.MethodPost {
			w.WriteHeader(404)
		}
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", 500) }))
	defer bad.Close()

	a := Alert{Labels: map[string]string{"alertname": "x"}, StartsAt: t0, EndsAt: t0.Add(time.Minute)}

	n := NewAlertmanagerNotifier(nil, bad.URL, good.URL+"/")
	if err := n.Notify(context.Background(), []Alert{a}); err != nil {
		t.Fatalf("one healthy replica is enough: %v", err)
	}
	if len(bodies) != 1 || bodies[0][0]["endsAt"] == nil || bodies[0][0]["labels"].(map[string]any)["alertname"] != "x" {
		t.Fatalf("bodies = %v", bodies)
	}
	if err := NewAlertmanagerNotifier(nil, bad.URL).Notify(context.Background(), []Alert{a}); err == nil {
		t.Fatal("all replicas failing must be an error")
	}
	if err := NewAlertmanagerNotifier(nil).Notify(context.Background(), []Alert{a}); err == nil {
		t.Fatal("no URL must be an error")
	}
	if err := n.Notify(context.Background(), nil); err != nil {
		t.Fatal("empty batch is a no-op")
	}
}
