package terminal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
)

// refusingValidator fails every token: a connection it lets through came in
// by the session cookie.
type refusingValidator struct{}

func (refusingValidator) ValidateToken(string) (*auth.JWTClaims, error) {
	return nil, errors.New("no token accepted here")
}

// cookieTerminalServer is the terminal websocket with session-cookie
// authentication that accepts exactly the session id "fs_good".
func cookieTerminalServer(t *testing.T) *httptest.Server {
	t.Helper()
	router := &terminalCreateRouter{createResponse: []byte(`{"success":true,"session_id":"daemon-session-1","pid":42}`)}
	srv := httptest.NewServer(TerminalWSHandler(router, refusingValidator{}, WithSessionCookie(func(r *http.Request) (string, error) {
		c, err := r.Cookie(auth.SecureSessionCookieName)
		if err != nil || c.Value != "fs_good" {
			return "", errors.New("no session")
		}
		return terminalTestUserID, nil
	})))
	t.Cleanup(srv.Close)
	return srv
}

func dialWithHeaders(t *testing.T, srv *httptest.Server, h http.Header) (*websocket.Conn, int) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?workingDir=/tmp", h)
	if err != nil {
		require.NotNil(t, resp, "the handshake failed without an HTTP answer: %v", err)
		return nil, resp.StatusCode
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, http.StatusSwitchingProtocols
}

// A connection with no token parameter authenticates with the sign-in
// session cookie when the upgrade comes from the API's own origin — a
// non-browser (the desktop app's main process sends no Origin), the browser's
// own same-origin verdict, or an Origin naming this host.
func TestTerminalWSAcceptsTheSessionCookieFromItsOwnOrigin(t *testing.T) {
	srv := cookieTerminalServer(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	cookie := auth.SecureSessionCookieName + "=fs_good"
	for name, h := range map[string]http.Header{
		"no Origin (the desktop main process)":               {"Cookie": {cookie}},
		"Sec-Fetch-Site same-origin (behind /_api/@reliant)": {"Cookie": {cookie}, "Origin": {"https://app.example.com"}, "Sec-Fetch-Site": {"same-origin"}},
		"Origin naming this host":                            {"Cookie": {cookie}, "Origin": {"http://" + host}},
	} {
		t.Run(name, func(t *testing.T) {
			conn, status := dialWithHeaders(t, srv, h)
			require.Equal(t, http.StatusSwitchingProtocols, status)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var first map[string]any
			require.NoError(t, conn.ReadJSON(&first))
			require.Equal(t, "init", first["type"], "the session cookie opened the terminal")
		})
	}
}

// Cross-site WebSocket hijacking: a page on another site (or a same-site
// sibling host) that opens the socket gets the victim's cookie attached by
// the browser. It is refused before anything reaches the daemon, as are a
// wrong session and no credential at all.
func TestTerminalWSRefusesACookieUpgradeFromAnotherOrigin(t *testing.T) {
	srv := cookieTerminalServer(t)
	cookie := auth.SecureSessionCookieName + "=fs_good"
	for name, tc := range map[string]struct {
		h    http.Header
		want int
	}{
		"cross-site page":            {http.Header{"Cookie": {cookie}, "Origin": {"https://evil.example"}, "Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		"same-site sibling host":     {http.Header{"Cookie": {cookie}, "Origin": {"https://evil.reliantlabs.io"}, "Sec-Fetch-Site": {"same-site"}}, http.StatusForbidden},
		"Origin null":                {http.Header{"Cookie": {cookie}, "Origin": {"null"}}, http.StatusForbidden},
		"a session that is not":      {http.Header{"Cookie": {auth.SecureSessionCookieName + "=fs_bad"}}, http.StatusUnauthorized},
		"no token and no cookie":     {http.Header{}, http.StatusUnauthorized},
		"a token the server refuses": {nil, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.h == nil {
				_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?token=x", http.Header{"Cookie": {cookie}})
				require.Error(t, err)
				require.Equal(t, tc.want, resp.StatusCode, "a token parameter is judged as a token, never by the cookie beside it")
				return
			}
			_, status := dialWithHeaders(t, srv, tc.h)
			require.Equal(t, tc.want, status)
		})
	}
}
