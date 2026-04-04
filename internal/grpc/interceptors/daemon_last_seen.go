package interceptors

import (
	"context"
	"strconv"
	"time"
)

type ctxKey int

const daemonLastSeenKey ctxKey = iota

// WithDaemonLastSeen stashes the raw x-daemon-last-seen header value in context.
func WithDaemonLastSeen(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, daemonLastSeenKey, raw)
}

// DaemonLastSeenFresh returns true if the context carries an x-daemon-last-seen
// timestamp that is within maxAge of now. This lets downstream code skip the
// IsDaemonOnline DB query when the frontend recently confirmed the daemon is alive.
func DaemonLastSeenFresh(ctx context.Context, maxAge time.Duration) bool {
	raw, _ := ctx.Value(daemonLastSeenKey).(string)
	if raw == "" {
		return false
	}
	unix, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(unix, 0)) < maxAge
}
