// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// SchedulerDeps are the scheduler's collaborators.
type SchedulerDeps struct {
	Rules RuleSource
	Store StateStore
	Eval  *Evaluator
	// Locker shards evaluation across replicas. Nil runs every shard in this
	// process, which is right for a single replica.
	Locker Locker
	// Shards is how many lease-owned partitions owners hash into. Default 16.
	Shards int
	// LeasePrefix names the leases: "<prefix>/shard/<n>". Default "alerting".
	LeasePrefix string
	// Tick is how often a shard looks for due rules. Default 10s.
	Tick time.Duration
	// Concurrency bounds owners evaluated at once within a shard. Default 4.
	Concurrency int
	Logger      *slog.Logger
	// Meter records the alerting.rules.* instruments. otel's global meter
	// provider when nil.
	Meter metric.Meter
	// Notifier receives the AlertRulesOverCap alert for an env that is over its
	// rule cap. Without one that alert is only logged and counted, so every
	// real deployment sets it (the Evaluator's own Notifier is the one to pass).
	Notifier Notifier
}

// Scheduler evaluates every owner's rules on their intervals.
//
// Owners hash into Shards partitions and each partition is a lease, so exactly
// one replica evaluates an owner at a time and a replica that dies hands its
// partitions over within the lease's retry interval. Every replica contends for
// every shard, each starting after a random delay so partitions spread across
// replicas rather than the fastest one taking all.
//
// Within a shard each rule is due on its own interval, first offset by a jitter
// derived from its key so a restart does not fire every rule in the same second.
type Scheduler struct {
	d       SchedulerDeps
	dropped metric.Int64Gauge
}

// NewScheduler validates deps.
func NewScheduler(d SchedulerDeps) (*Scheduler, error) {
	switch {
	case d.Rules == nil:
		return nil, errors.New("alerting: SchedulerDeps.Rules is required")
	case d.Store == nil:
		return nil, errors.New("alerting: SchedulerDeps.Store is required")
	case d.Eval == nil:
		return nil, errors.New("alerting: SchedulerDeps.Eval is required")
	}
	if d.Shards <= 0 {
		d.Shards = 16
	}
	if d.LeasePrefix == "" {
		d.LeasePrefix = "alerting"
	}
	if d.Tick <= 0 {
		d.Tick = 10 * time.Second
	}
	if d.Concurrency <= 0 {
		d.Concurrency = 4
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Meter == nil {
		d.Meter = otel.Meter(meterName)
	}
	dropped, err := d.Meter.Int64Gauge("alerting.rules.dropped",
		metric.WithDescription("Rules an env declares beyond Limits.MaxRulesPerEnv: declared, enabled and NOT evaluated. Non-zero means alerts that can never fire."),
		metric.WithUnit("{rule}"))
	if err != nil {
		return nil, fmt.Errorf("alerting: creating the dropped-rules gauge: %w", err)
	}
	return &Scheduler{d: d, dropped: dropped}, nil
}

// ShardOf is the partition an owner belongs to.
func (s *Scheduler) ShardOf(owner string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(owner))
	return int(h.Sum32() % uint32(s.d.Shards))
}

// Run blocks until ctx is done.
func (s *Scheduler) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := 0; i < s.d.Shards; i++ {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			stagger := time.Duration(rand.Int64N(int64(s.d.Tick)))
			select {
			case <-ctx.Done():
				return
			case <-time.After(stagger):
			}
			if s.d.Locker == nil {
				_ = s.runShard(ctx, shard)
				return
			}
			name := fmt.Sprintf("%s/shard/%d", s.d.LeasePrefix, shard)
			if err := s.d.Locker.Run(ctx, name, func(ctx context.Context) error { return s.runShard(ctx, shard) }); err != nil {
				s.d.Logger.ErrorContext(ctx, "alerting shard stopped", "shard", shard, "error", err)
			}
		}(i)
	}
	wg.Wait()
	return nil
}

// runShard is restartable: all of its state is rebuilt from the store each time
// the lease is won, so a handover loses nothing but the in-memory due times.
func (s *Scheduler) runShard(ctx context.Context, shard int) error {
	cache := map[string]*ownerState{}
	t := time.NewTicker(s.d.Tick)
	defer t.Stop()
	for {
		s.tick(ctx, shard, cache)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, shard int, cache map[string]*ownerState) {
	owners, err := s.owners(ctx, shard)
	if err != nil {
		if ctx.Err() == nil {
			s.d.Logger.WarnContext(ctx, "alerting: listing owners failed", "shard", shard, "error", err)
		}
		return
	}
	sem := make(chan struct{}, s.d.Concurrency)
	var wg sync.WaitGroup
	for _, owner := range owners {
		st := cache[owner]
		if st == nil {
			st = &ownerState{}
			cache[owner] = st
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(owner string, st *ownerState) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					s.d.Logger.ErrorContext(ctx, "alerting: owner evaluation panicked", "owner", owner, "panic", r)
				}
			}()
			s.tickOwner(ctx, owner, st)
		}(owner, st)
	}
	wg.Wait()
}

// owners is the shard's owners: those with rules and those with leftover state
// (their rules were all removed, so their instances need retiring).
func (s *Scheduler) owners(ctx context.Context, shard int) ([]string, error) {
	withRules, err := s.d.Rules.Owners(ctx)
	if err != nil {
		return nil, err
	}
	withState, err := s.d.Store.Owners(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range append(withRules, withState...) {
		if !seen[t] && s.ShardOf(t) == shard {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Scheduler) tickOwner(ctx context.Context, owner string, st *ownerState) {
	st.mu.Lock()
	defer st.mu.Unlock()
	due := st.due
	log := s.d.Logger.With("owner", owner)

	rules, err := s.d.Rules.Rules(ctx, owner)
	if err != nil {
		log.WarnContext(ctx, "alerting: listing rules failed", "error", err)
		return
	}
	if st.due == nil {
		st.due = map[RuleKey]time.Time{}
		due = st.due
	}
	if !st.loaded {
		all, err := s.d.Store.LoadOwner(ctx, owner)
		if err != nil {
			log.WarnContext(ctx, "alerting: loading state failed", "error", err)
			return
		}
		st.instances = map[RuleKey][]Instance{}
		for _, in := range all {
			st.instances[in.RuleKey] = append(st.instances[in.RuleKey], in)
		}
		st.loaded = true
	}

	kept, over := s.d.Eval.Select(rules)
	live := make(map[RuleKey]bool, len(kept))
	for _, o := range over {
		// A rule that is not evaluated has an UNKNOWN condition, not a clear
		// one: it stays "live" so the retire pass below never resolves what it
		// had firing. Alertmanager lets the stale alert lapse on its own endsAt.
		for _, r := range o.Dropped {
			live[r.RuleKey] = true
		}
	}
	s.reportOverflow(ctx, log, owner, st, over)
	now := time.Now()
	for _, r := range kept {
		live[r.RuleKey] = true
		if r.Owner != owner {
			log.ErrorContext(ctx, "alerting: RuleSource returned a rule for another owner; skipped", "rule", r.String())
			continue
		}
		if t, ok := due[r.RuleKey]; ok && now.Before(t) {
			continue
		}
		interval := s.d.Eval.EffectiveInterval(r)
		if _, scheduled := due[r.RuleKey]; !scheduled {
			// first sight: spread rules over their own interval.
			due[r.RuleKey] = now.Add(jitter(r.RuleKey, interval))
			if due[r.RuleKey].After(now) {
				continue
			}
		}
		slot := due[r.RuleKey]
		after, err := s.d.Eval.EvaluateRule(ctx, r, st.instances[r.RuleKey])
		due[r.RuleKey] = nextDue(slot, interval, time.Now())
		var storeErr *StoreError
		switch {
		case errors.As(err, &storeErr):
			// state write failed: our copy may be ahead of the store.
			st.loaded = false
			log.WarnContext(ctx, "alerting: evaluation failed", "rule", r.String(), "error", err)
			return
		case err != nil:
			log.WarnContext(ctx, "alerting: evaluation failed; state kept", "rule", r.String(), "error", err)
		}
		setInstances(st.instances, r.RuleKey, after)
	}

	for key, prev := range st.instances {
		if live[key] {
			continue
		}
		after, err := s.d.Eval.Retire(ctx, key, prev, s.d.Eval.limits.MinInterval)
		if err != nil {
			st.loaded = false
			log.WarnContext(ctx, "alerting: retiring a removed rule failed", "rule", key.String(), "error", err)
			return
		}
		delete(due, key)
		setInstances(st.instances, key, after)
	}
}

// OverCapAlertName is the alert raised, once per env, while that env declares
// more rules than Limits.MaxRulesPerEnv: rules past the cap are not evaluated,
// so this is the only thing that says so.
const OverCapAlertName = "AlertRulesOverCap"

const meterName = "github.com/reliant-labs/forge/pkg/alerting"

// overCapLogEvery re-logs an unchanged overflow; a changed one logs at once.
const overCapLogEvery = 10 * time.Minute

// reportOverflow makes every dropped rule loud: a WARN naming the rule ids, the
// alerting.rules.dropped gauge per env (zeroed when the env is back under its
// cap), and an AlertRulesOverCap alert routed with the env's own identity.
func (s *Scheduler) reportOverflow(ctx context.Context, log *slog.Logger, owner string, st *ownerState, over []Overflow) {
	now := time.Now()
	if st.overCap == nil {
		st.overCap = map[string]*overCapState{}
	}
	current := map[string]bool{}
	for _, o := range over {
		current[o.Env] = true
		names := make([]string, len(o.Dropped))
		for i, r := range o.Dropped {
			names[i] = r.Name
		}
		sort.Strings(names)
		sig := strings.Join(names, ",")
		s.dropped.Record(ctx, int64(len(o.Dropped)), metric.WithAttributes(attribute.String("owner", owner), attribute.String("env", o.Env)))

		oc := st.overCap[o.Env]
		if oc == nil {
			oc = &overCapState{}
			st.overCap[o.Env] = oc
		}
		if oc.sig != sig || now.Sub(oc.loggedAt) >= overCapLogEvery {
			oc.sig, oc.loggedAt = sig, now
			log.ErrorContext(ctx, "alerting: env declares more rules than its cap; the excess rules are NOT evaluated and cannot fire",
				"env", o.Env, "configured", o.Configured, "cap", o.Cap, "dropped", len(o.Dropped), "dropped_rules", names)
		}
		// Re-posted at the evaluation floor and lapsing 3 floors after the last
		// post, like any firing alert: it resolves itself once the env is back
		// under its cap, with no state to clean up.
		if s.d.Notifier != nil && now.Sub(oc.postedAt) >= s.d.Eval.limits.MinInterval {
			if err := s.d.Notifier.Notify(ctx, []Alert{overCapAlert(owner, o, names, now, s.d.Eval.limits.MinInterval)}); err != nil {
				log.WarnContext(ctx, "alerting: posting AlertRulesOverCap failed; retrying next tick", "env", o.Env, "error", err)
			} else {
				oc.postedAt = now
			}
		}
	}
	for env := range st.overCap {
		if !current[env] {
			s.dropped.Record(ctx, 0, metric.WithAttributes(attribute.String("owner", owner), attribute.String("env", env)))
			delete(st.overCap, env)
		}
	}
}

// overCapState is what one over-cap env remembers between ticks.
type overCapState struct {
	sig      string
	loggedAt time.Time
	postedAt time.Time
}

func overCapAlert(owner string, o Overflow, names []string, now time.Time, floor time.Duration) Alert {
	labels := map[string]string{}
	for k, v := range o.Dropped[0].Identity {
		labels[k] = v
	}
	labels["alertname"] = OverCapAlertName
	labels["severity"] = "critical"
	shown := names
	if len(shown) > 20 {
		shown = append(append([]string(nil), shown[:20]...), fmt.Sprintf("… and %d more", len(names)-20))
	}
	return Alert{
		Labels: labels,
		Annotations: map[string]string{
			"summary": fmt.Sprintf("This env declares %d alert rules but at most %d are evaluated: %d are NOT evaluated and can never fire (%s). Remove rules or raise the per-env rule cap.",
				o.Configured, o.Cap, len(o.Dropped), strings.Join(shown, ", ")),
		},
		StartsAt: now,
		EndsAt:   now.Add(3 * floor),
	}
}

func setInstances(m map[RuleKey][]Instance, k RuleKey, v []Instance) {
	if len(v) == 0 {
		delete(m, k)
		return
	}
	m[k] = v
}

// nextDue is when a rule whose slot was `slot` runs next: one interval after the
// SLOT, not after the evaluation ended. Re-arming from the end of an evaluation
// adds the tick's lateness and the pass's own duration to every period, so a 1m
// rule ran about every 78s (a 10s tick, and an owner's rules evaluated one after
// another). A slot missed because the pass ran long is skipped, not replayed, so
// a slow pass never turns into a burst of catch-up queries.
func nextDue(slot time.Time, interval time.Duration, now time.Time) time.Time {
	next := slot.Add(interval)
	if next.After(now) {
		return next
	}
	missed := now.Sub(slot) / interval
	return slot.Add((missed + 1) * interval)
}

// jitter is a stable offset in [0, interval) for a rule.
func jitter(k RuleKey, interval time.Duration) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k.String()))
	return time.Duration(h.Sum64() % uint64(interval))
}
