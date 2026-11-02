// Copyright (c) 2025 Reliant Labs
package queueddelivery

import (
	"sync"
	"time"
)

// Redelivery bounds. A delivered run that finds its machine and reads the
// message clears the queue for good; one that is queued again soon after was
// delivered to a machine the run itself could not reach — the deliverer's
// liveness read and the run's disagreed. Delivering it again at once only
// repeats the failure (on 2026-10-26 one chat did, every few seconds, for
// three days), so each redelivery of the same chat waits twice as long as the
// last, from redeliveryBase up to redeliveryMax. The chat stays queued all the
// while: the user's own send is never held, and once the machine really is
// reachable the next allowed delivery reaches it.
const (
	redeliveryBase = time.Minute
	redeliveryMax  = 30 * time.Minute
	// redeliveryForget is how long after its hold ended a chat's record is
	// kept. A chat queued again after that is a new outage, delivered at once.
	redeliveryForget = time.Hour
)

// redeliveryBackoff remembers, per chat, when this replica last delivered its
// queued message and how many times in a row. Per replica and in memory: a
// restart forgets it, and with several replicas each holds its own, which
// bounds a loop at a few deliveries per hold rather than one — still the
// difference between a handful of runs an hour and thousands.
type redeliveryBackoff struct {
	mu    sync.Mutex
	chats map[string]redelivery
}

type redelivery struct {
	at    time.Time
	count int
}

func newRedeliveryBackoff() *redeliveryBackoff {
	return &redeliveryBackoff{chats: map[string]redelivery{}}
}

// hold reports whether a delivery of chatID now must wait, and for how long.
func (b *redeliveryBackoff) hold(chatID string, now time.Time) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.chats[chatID]
	if !ok {
		return 0, false
	}
	until := rec.at.Add(redeliveryHold(rec.count))
	if now.Before(until) {
		return until.Sub(now), true
	}
	if now.Sub(until) > redeliveryForget {
		delete(b.chats, chatID)
	}
	return 0, false
}

// delivered records a delivery of chatID at now.
func (b *redeliveryBackoff) delivered(chatID string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.chats[chatID]
	b.chats[chatID] = redelivery{at: now, count: rec.count + 1}
	// Drop what has been forgotten, so the map holds only chats in a hold.
	for id, r := range b.chats {
		if now.Sub(r.at.Add(redeliveryHold(r.count))) > redeliveryForget {
			delete(b.chats, id)
		}
	}
}

// redeliveryHold is how long after the count-th delivery the next one waits.
func redeliveryHold(count int) time.Duration {
	hold := redeliveryBase
	for i := 1; i < count && hold < redeliveryMax; i++ {
		hold *= 2
	}
	return min(hold, redeliveryMax)
}
