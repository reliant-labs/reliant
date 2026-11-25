package terminal

import (
	"crypto/tls"
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
// authentication that accepts exactly the session id "fs_good", under either
// cookie name.
func cookieTerminalServer(t *testing.T) *httptest.Server {
	t.Helper()
	router := &terminalCreateRouter{createResponse: []byte(`{"success":true,"session_id":"daemon-session-1","pid":42}`)}
	srv := httptest.NewServer(TerminalWSHandler(router, refusingValidator{}, WithSessionCookie(func(r *http.Request) (string, error) {
		for _, name := range []string{auth.SecureSessionCookieName, auth.SessionCookieName} {
			if c, err := r.Cookie(name); err == nil && c.Value == "fs_good" {
				return terminalTestUserID, nil
			}
		}
		return "", errors.New("no session")
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

// readFirstJSON reads the connection's first message, closing the connection
// if none arrives within 5s so a silent server fails the test rather than
// hanging it.
func readFirstJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	got := make(chan error, 1)
	var first map[string]any
	go func() { got <- conn.ReadJSON(&first) }()
	select {
	case err := <-got:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		_ = conn.Close()
		t.Fatal("no message from the terminal within 5s")
	}
	return first
}

// A connection with no token parameter authenticates with the sign-in
// session cookie when the upgrade comes from the API's own origin — a
// non-browser (the desktop app's main process sends no Origin), the browser's
// own same-origin verdict, or an Origin naming this request's whole origin.
func TestTerminalWSAcceptsTheSessionCookieFromItsOwnOrigin(t *testing.T) {
	srv := cookieTerminalServer(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	cookie := auth.SecureSessionCookieName + "=fs_good"
	for name, h := range map[string]http.Header{
		"no Origin (the desktop main process)":               {"Cookie": {cookie}},
		"Sec-Fetch-Site same-origin (behind /_api/@reliant)": {"Cookie": {cookie}, "Origin": {"https://app.example.com"}, "Sec-Fetch-Site": {"same-origin"}},
		"Origin naming this http origin (local development)": {"Cookie": {auth.SessionCookieName + "=fs_good"}, "Origin": {"http://" + host}},
		"https Origin behind a TLS-terminating ingress":      {"Cookie": {cookie}, "Origin": {"https://" + host}, "X-Forwarded-Proto": {"https"}},
	} {
		t.Run(name, func(t *testing.T) {
			conn, status := dialWithHeaders(t, srv, h)
			require.Equal(t, http.StatusSwitchingProtocols, status)
			first := readFirstJSON(t, conn)
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
	host := strings.TrimPrefix(srv.URL, "http://")
	hostname, _, _ := strings.Cut(host, ":")
	cookie := auth.SecureSessionCookieName + "=fs_good"
	for name, tc := range map[string]struct {
		h    http.Header
		want int
	}{
		"cross-site page":        {http.Header{"Cookie": {cookie}, "Origin": {"https://evil.example"}, "Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		"same-site sibling host": {http.Header{"Cookie": {cookie}, "Origin": {"https://evil.reliantlabs.io"}, "Sec-Fetch-Site": {"same-site"}}, http.StatusForbidden},
		"Origin null":            {http.Header{"Cookie": {cookie}, "Origin": {"null"}}, http.StatusForbidden},
		// An insecure page on this host opening the secure socket.
		"http Origin on the https API":          {http.Header{"Cookie": {cookie}, "Origin": {"http://" + host}, "X-Forwarded-Proto": {"https"}}, http.StatusForbidden},
		"the secure cookie from an http Origin": {http.Header{"Cookie": {cookie}, "Origin": {"http://" + host}}, http.StatusForbidden},
		// The browser's own verdict is final, even beside an Origin naming this host.
		"Sec-Fetch-Site cross-site":  {http.Header{"Cookie": {cookie}, "Origin": {"https://" + host}, "X-Forwarded-Proto": {"https"}, "Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		"mismatched port":            {http.Header{"Cookie": {cookie}, "Origin": {"https://" + hostname + ":1"}, "X-Forwarded-Proto": {"https"}}, http.StatusForbidden},
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

// sameOriginUpgrade compares the whole origin: scheme, host and port (the
// scheme's default port made explicit), against the scheme the request
// arrived on, and refuses when that scheme cannot be established.
func TestSameOriginUpgradeComparesTheWholeOrigin(t *testing.T) {
	for name, tc := range map[string]struct {
		host string
		h    http.Header
		tls  bool
		want bool
	}{
		"no Origin":                         {"api.example.com", http.Header{}, false, true},
		"same-origin verdict":               {"upstream:8080", http.Header{"Origin": {"https://app.example.com"}, "Sec-Fetch-Site": {"same-origin"}}, false, true},
		"same-site verdict":                 {"api.example.com", http.Header{"Origin": {"https://api.example.com"}, "Sec-Fetch-Site": {"same-site"}}, true, false},
		"cross-site verdict":                {"api.example.com", http.Header{"Origin": {"https://api.example.com"}, "Sec-Fetch-Site": {"cross-site"}}, true, false},
		"https origin over TLS":             {"api.example.com", http.Header{"Origin": {"https://api.example.com"}}, true, true},
		"default port made explicit":        {"API.example.com:443", http.Header{"Origin": {"https://api.example.com"}}, true, true},
		"https origin via forwarded proto":  {"api.example.com", http.Header{"Origin": {"https://api.example.com"}, "X-Forwarded-Proto": {"https, http"}}, false, true},
		"http origin over TLS":              {"api.example.com", http.Header{"Origin": {"http://api.example.com"}}, true, false},
		"http origin via forwarded https":   {"api.example.com", http.Header{"Origin": {"http://api.example.com"}, "X-Forwarded-Proto": {"https"}}, false, false},
		"http origin on :443":               {"api.example.com:443", http.Header{"Origin": {"http://api.example.com:443"}}, true, false},
		"mismatched port":                   {"api.example.com:8443", http.Header{"Origin": {"https://api.example.com"}}, true, false},
		"unknown forwarded proto":           {"api.example.com", http.Header{"Origin": {"https://api.example.com"}, "X-Forwarded-Proto": {"wss"}}, false, false},
		"non-http origin scheme":            {"api.example.com", http.Header{"Origin": {"chrome-extension://api.example.com"}}, false, false},
		"http dev origin":                   {"localhost:5173", http.Header{"Origin": {"http://localhost:5173"}}, false, true},
		"secure cookie from an http origin": {"localhost:5173", http.Header{"Origin": {"http://localhost:5173"}, "Cookie": {auth.SecureSessionCookieName + "=s"}}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Host = tc.host
			r.Header = tc.h
			if !tc.tls {
				r.TLS = nil
			} else if r.TLS == nil {
				r.TLS = &tls.ConnectionState{}
			}
			require.Equal(t, tc.want, sameOriginUpgrade(r))
		})
	}
}
