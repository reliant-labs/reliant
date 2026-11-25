package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/reliant-labs/forge/pkg/session"
	"github.com/stretchr/testify/require"
)

// With server-side sessions, /auth/session reports signed in only for a
// live session: a token in the cookie — forged, unsigned, expired or real —
// is no session, reports signed out, and the cookie is cleared.
func TestSessionEndpointRefusesATokenInTheCookie(t *testing.T) {
	m, err := session.NewManager(session.Config{
		Store: session.NewMemoryStore(),
		Refresher: session.RefresherFunc(func(context.Context, string) (session.Tokens, error) {
			return session.Tokens{}, session.ErrRefreshRejected
		}),
	})
	require.NoError(t, err)
	b := &loginBroker{sessions: m, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "user-1", "email": "p@example.com", "name": "P",
		"exp": time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	unknown, err := session.NewID()
	require.NoError(t, err)

	for name, value := range map[string]string{"an unsigned JWT": forged, "an unknown session id": unknown, "junk": "x"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
			req.AddCookie(&http.Cookie{Name: b.sessionCookie(), Value: value})
			rec := httptest.NewRecorder()
			b.handleSession(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			var got identity
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.False(t, got.Authenticated, "a cookie holding no live session reported signed in")
			require.Contains(t, rec.Header().Get("Set-Cookie"), "Max-Age=0", "the cookie is cleared")
		})
	}
}

// A flood of invented user ids cannot lock password resets out for
// everyone: to admit a key it has not seen, the limiter evicts the oldest
// unspent window, and the oldest spent one only when every window is spent.
func TestResetLimiterSurvivesAFloodOfInventedIDs(t *testing.T) {
	l := newAttemptLimiter(5, time.Hour)
	fixed := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return fixed }
	for i := range attemptLimiterMaxKeys {
		require.True(t, l.reserve(fmt.Sprintf("invented-%d", i)))
	}
	require.True(t, l.reserve("real-user"), "a real user was locked out by invented ids")
	require.LessOrEqual(t, len(l.entries), attemptLimiterMaxKeys)

	// Every retained window spent (five guesses at each invented id) must
	// not refuse an unseen real user either: the oldest spent window makes
	// room, and every newer one keeps its count.
	full := newAttemptLimiter(1, time.Hour)
	clock := fixed
	full.now = func() time.Time { return clock }
	for i := range attemptLimiterMaxKeys {
		clock = fixed.Add(time.Duration(i) * time.Millisecond)
		require.True(t, full.reserve(fmt.Sprintf("spent-%d", i)))
	}
	clock = fixed.Add(time.Minute)
	require.True(t, full.reserve("real-user"), "a full table of spent windows locked an unseen user out")
	require.False(t, full.reserve("real-user"), "the admitted user is charged like any other key")
	require.LessOrEqual(t, len(full.entries), attemptLimiterMaxKeys)
	require.False(t, full.spent("spent-0"), "the oldest spent window is the one dropped")
	for _, k := range []string{"spent-1", fmt.Sprintf("spent-%d", attemptLimiterMaxKeys-1)} {
		require.True(t, full.spent(k), "%s: a newer spent window keeps its count", k)
		require.False(t, full.reserve(k), "%s: a newer spent window is still refused", k)
	}
}

// One client cannot charge reset attempts without bound: after its budget,
// it is refused before any user id is charged.
func TestResetIsAdmittedPerClient(t *testing.T) {
	b := &loginBroker{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		resetByUser: newAttemptLimiter(5, time.Hour),
		resetByIP:   newAttemptLimiter(20, time.Hour),
	}
	for i := range 20 {
		require.True(t, b.resetByIP.reserve("203.0.113.7"), "attempt %d", i) // devidp.ClientKey: the peer address
	}
	body, err := json.Marshal(resetRequest{UserID: "victim", Code: "123456", Password: "Pw-1234567!"})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/auth/reset", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.7:4000"
	rec := httptest.NewRecorder()
	b.handleReset(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.False(t, b.resetByUser.spent("victim"))
	require.Empty(t, b.resetByUser.entries, "a refused client charges no user id")
}

// A reset code is an account-takeover credential: the broker never writes
// one to a log, in development included (its mail goes to mailpit).
func TestForgotNeverLogsAResetCode(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "login_broker.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	// Parsed without comments: the file's guidance may name the minter; its
	// code may not call it.
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "MintPasswordResetCode" {
			t.Errorf("%s: the broker mints a reset code it could log", fset.Position(sel.Pos()))
		}
		return true
	})
}
