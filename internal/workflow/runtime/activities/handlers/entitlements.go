// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// entitlementTimeout bounds asking whether the run's owner is entitled to the
// gated features a declaration reaches. The gate's membership check is a
// control-plane round trip on a hosted deployment, and a slow one must cost
// the turn its gated tools, not the turn.
const entitlementTimeout = 5 * time.Second

// How long an answer is kept (entitlementCache): an answer, yes or no, for a
// minute, so a run does not ask on every turn; an error for 5s, so an outage
// does not stall every turn on the gate's timeout, while recovery is quick.
// A revoked entitlement therefore holds for at most a minute; the feature's
// own service re-checks the gate on every call.
const (
	entitlementAnswerTTL = 60 * time.Second
	entitlementErrorTTL  = 5 * time.Second
	// entitlementCacheSweep is the size past which an insert first drops
	// every expired entry.
	entitlementCacheSweep = 4096
)

// entitlementCache keeps the gate's answers per (user, org, feature). A nil
// cache keeps nothing.
type entitlementCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[entitlementKey]entitlementEntry
}

type entitlementKey struct {
	user, org string
	feature   featuregate.Feature
}

type entitlementEntry struct {
	allowed bool
	expires time.Time
}

func newEntitlementCache(now func() time.Time) *entitlementCache {
	if now == nil {
		now = time.Now
	}
	return &entitlementCache{now: now, entries: map[entitlementKey]entitlementEntry{}}
}

func (c *entitlementCache) get(k entitlementKey) (allowed, ok bool) {
	if c == nil {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || !c.now().Before(e.expires) {
		return false, false
	}
	return e.allowed, true
}

func (c *entitlementCache) put(k entitlementKey, allowed bool, ttl time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= entitlementCacheSweep {
		for key, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, key)
			}
		}
	}
	c.entries[k] = entitlementEntry{allowed: allowed, expires: now.Add(ttl)}
}

// entitledFeatures asks the gate which of the features behind the gated tools
// a declaration reaches the run's owner is entitled to in the run's org. The
// answer is the capability resolver's EntitledFeatures input: a gated tool
// whose feature is not in it is excluded as if it did not exist.
//
// It fails closed, as usableIntegrations does: no gate, a chat with no owner
// or no org, a refusal, a timeout or an error leaves the feature out of the
// answer, and the resolver excludes every gated tool not in it. An answer is
// kept in cache (nil keeps none) for entitlementAnswerTTL, an error, as "no",
// for entitlementErrorTTL. A declaration that reaches no gated tool never
// asks.
func entitledFeatures(ctx context.Context, gate featuregate.Gate, cache *entitlementCache, chat *db.Chat, access tools.ToolAccess) map[string]bool {
	reached := tools.ReachedGatedFeatures(access)
	if len(reached) == 0 || gate == nil || chat == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, entitlementTimeout)
	defer cancel()
	orgID := chatOrgID(chat)
	var entitled map[string]bool
	for _, f := range reached {
		key := entitlementKey{user: chat.UserID, org: orgID, feature: f}
		allowed, cached := cache.get(key)
		if !cached {
			var err error
			allowed, err = gate.Allowed(ctx, f, chat.UserID, orgID)
			if err != nil {
				slog.WarnContext(ctx, "[CallLLM] Could not establish whether the run's owner is entitled to a feature; withholding its tools",
					"chatID", chat.ID, "feature", string(f), "error", err)
				cache.put(key, false, entitlementErrorTTL)
				continue
			}
			cache.put(key, allowed, entitlementAnswerTTL)
		}
		if !allowed {
			continue
		}
		if entitled == nil {
			entitled = map[string]bool{}
		}
		entitled[string(f)] = true
	}
	return entitled
}
