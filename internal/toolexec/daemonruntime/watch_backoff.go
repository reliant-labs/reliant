package daemonruntime

import "time"

// watchFailureBackoffMax caps how far a failing watcher stretches its polls.
// A project path the server still asks this daemon to watch can be gone for
// good — a deleted worktree, a removed /tmp checkout — and the server only
// stops asking when it is told. Polled at the healthy cadence, each such path
// cost a full snapshot attempt and a WARN line every two seconds, forever: 13
// deleted worktrees produced ~60 identical lines a minute on one machine. At
// the cap a dead path costs one cheap failed poll every five minutes, and a
// path that comes back is noticed within that.
const watchFailureBackoffMax = 5 * time.Minute

// watchBackoff paces one watcher's polls: the healthy interval while polls
// succeed, doubling up to watchFailureBackoffMax while they fail. It also says
// when a poll changed the watcher's state, so the caller logs a failure streak
// once when it starts and once when it ends rather than on every poll.
// Not safe for concurrent use; each watcher goroutine owns its own.
type watchBackoff struct {
	interval time.Duration // healthy poll interval
	max      time.Duration // failing poll interval cap
	delay    time.Duration // current failing delay; 0 while healthy
}

func newWatchBackoff(interval time.Duration) *watchBackoff {
	return &watchBackoff{interval: interval, max: max(interval, watchFailureBackoffMax)}
}

// next records whether the latest poll failed and returns how long to wait
// before the next one, and whether this poll started or ended a failure streak.
func (b *watchBackoff) next(failed bool) (wait time.Duration, changed bool) {
	if !failed {
		changed = b.delay != 0
		b.delay = 0
		return b.interval, changed
	}
	changed = b.delay == 0
	if changed {
		b.delay = b.interval
	}
	b.delay = min(b.delay*2, b.max)
	return b.delay, changed
}
