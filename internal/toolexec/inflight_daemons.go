package toolexec

import (
	"sync"
	"time"
)

// inflightTTL bounds how long a request→daemon association is kept when the
// request never completes cleanly (an abandoned call is exactly the one a
// follow-up cancel still needs, so this is generous).
const inflightTTL = 2 * time.Hour

type inflightKey struct{ userID, id string }

type inflightEntry struct {
	daemonID string
	at       time.Time
}

// inflightDaemons maps the ids of a dispatched tool request (transport request
// id and tool call id) to the daemon it was sent to, so a later cancel,
// background or kill for it targets that daemon instead of re-resolving the
// user's default.
type inflightDaemons struct {
	mu sync.Mutex
	m  map[inflightKey]inflightEntry
}

// record remembers the daemon for each non-empty id and returns a func that
// forgets them again (called when the request completed normally).
func (f *inflightDaemons) record(userID, daemonID string, ids ...string) (forget func()) {
	now := time.Now()
	f.mu.Lock()
	if f.m == nil {
		f.m = make(map[inflightKey]inflightEntry)
	}
	if len(f.m) > 1024 {
		for k, e := range f.m {
			if now.Sub(e.at) > inflightTTL {
				delete(f.m, k)
			}
		}
	}
	var keys []inflightKey
	for _, id := range ids {
		if id == "" {
			continue
		}
		k := inflightKey{userID, id}
		f.m[k] = inflightEntry{daemonID: daemonID, at: now}
		keys = append(keys, k)
	}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		for _, k := range keys {
			delete(f.m, k)
		}
		f.mu.Unlock()
	}
}

func (f *inflightDaemons) lookup(userID, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.m[inflightKey{userID, id}]
	if !ok || time.Since(e.at) > inflightTTL {
		return "", false
	}
	return e.daemonID, true
}
