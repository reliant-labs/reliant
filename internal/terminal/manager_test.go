package terminal

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// addIdleSession registers a session with no PTY or process. Enough to
// exercise session bookkeeping without spending a PTY from the machine-wide
// pool this cap exists to protect.
func addIdleSession(m *Manager, id string, lastActive time.Time) *Session {
	session := &Session{ID: id, LastActive: lastActive, done: make(chan struct{})}
	m.sessions[id] = session
	return session
}

func isClosed(session *Session) bool {
	select {
	case <-session.done:
		return true
	default:
		return false
	}
}

func TestMakeRoomForSession_UnderCapClosesNothing(t *testing.T) {
	m := NewManager()
	m.maxSessions = 3
	base := time.Now()
	first := addIdleSession(m, "first", base)
	second := addIdleSession(m, "second", base.Add(time.Second))

	m.makeRoomForSession()

	require.Len(t, m.sessions, 2)
	require.False(t, isClosed(first))
	require.False(t, isClosed(second))
}

func TestMakeRoomForSession_AtCapClosesLeastRecentlyActive(t *testing.T) {
	m := NewManager()
	m.maxSessions = 3
	base := time.Now()
	// Inserted out of activity order, so the choice cannot fall out of map
	// or insertion order.
	recent := addIdleSession(m, "recent", base.Add(2*time.Minute))
	orphan := addIdleSession(m, "orphan", base)
	middle := addIdleSession(m, "middle", base.Add(time.Minute))

	m.makeRoomForSession()

	require.Len(t, m.sessions, 2, "one slot must be free for the session about to be created")
	require.NotContains(t, m.sessions, "orphan")
	require.True(t, isClosed(orphan), "the evicted session must be cleaned up, not just forgotten")
	require.False(t, isClosed(middle))
	require.False(t, isClosed(recent))
}

// A leak that has already overshot the cap (sessions added before the cap
// existed, or a lowered cap) is brought back under it in one pass.
func TestMakeRoomForSession_OverCapClosesDownToCap(t *testing.T) {
	m := NewManager()
	m.maxSessions = 2
	base := time.Now()
	sessions := make([]*Session, 5)
	for i := range sessions {
		sessions[i] = addIdleSession(m, fmt.Sprintf("s%d", i), base.Add(time.Duration(i)*time.Second))
	}

	m.makeRoomForSession()

	require.Len(t, m.sessions, 1)
	require.Contains(t, m.sessions, "s4", "the most recently active session survives")
	for _, session := range sessions[:4] {
		require.True(t, isClosed(session), "session %s should have been closed", session.ID)
	}
}

func TestNewManager_CapsSessions(t *testing.T) {
	require.Equal(t, defaultMaxSessions, NewManager().maxSessions)
	require.Positive(t, defaultMaxSessions)
}
