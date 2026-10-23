// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
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
	d SchedulerDeps
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
	return &Scheduler{d: d}, nil
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

	kept, dropped := s.d.Eval.Select(rules)
	if dropped > 0 {
		log.WarnContext(ctx, "alerting: owner is over its rule cap; excess rules are not evaluated", "dropped", dropped)
	}
	live := make(map[RuleKey]bool, len(kept))
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
		after, err := s.d.Eval.EvaluateRule(ctx, r, st.instances[r.RuleKey])
		due[r.RuleKey] = time.Now().Add(interval)
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

func setInstances(m map[RuleKey][]Instance, k RuleKey, v []Instance) {
	if len(v) == 0 {
		delete(m, k)
		return
	}
	m[k] = v
}

// jitter is a stable offset in [0, interval) for a rule.
func jitter(k RuleKey, interval time.Duration) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k.String()))
	return time.Duration(h.Sum64() % uint64(interval))
}
