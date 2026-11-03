package oauth2

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// LoopbackLogin runs the whole authorization-code + PKCE flow for a native
// app, as RFC 8252 describes. It binds a loopback listener on an ephemeral
// port, sends the user's browser to the authorization endpoint with that
// listener as the redirect, receives the code, and redeems it at the token
// endpoint with the verifier.
//
// This is the ONE implementation of a CLI's browser login: `forge login` and
// `reliant auth login` both run it, so the two cannot drift on the security
// properties that matter (state, S256, exact redirect).
//
// WHY PORT 0. The OS picks a free port, so two logins on one machine (two
// developers, or two agents) never collide, which a fixed port cannot promise.
// RFC 8252 §7.3 requires the authorization server to accept any port on a
// loopback redirect.
type LoopbackLogin struct {
	// AuthorizeEndpoint and TokenEndpoint are the server's two URLs.
	AuthorizeEndpoint string
	TokenEndpoint     string
	// ClientID is the public client (no secret: a CLI cannot keep one).
	ClientID string
	// Scopes requested. The server may grant fewer, and Token.Scope reports
	// what it granted.
	Scopes []string
	// Extra carries additional authorization parameters (e.g. a device
	// label). Reserved names are refused, per AuthRequest.
	Extra url.Values
	// OpenURL launches the browser. When it fails the flow continues: the URL
	// has already been written to Out, so a headless user can open it by hand.
	OpenURL func(string) error
	// Out receives the "open this URL" notice. May be nil.
	Out io.Writer
	// Timeout bounds the wait for the browser to come back. Defaults to 5m.
	Timeout time.Duration
	// HTTPClient performs the token exchange. Nil uses a bounded default.
	HTTPClient HTTPDoer
	// SuccessHTML is the page shown in the browser after the callback. It is
	// shown whether or not the exchange then succeeds, because the exchange
	// happens after the page is served. A default is used when empty.
	SuccessHTML string
}

// callbackPath is the loopback listener's one route.
const callbackPath = "/callback"

// Run performs the flow and returns the token.
func (l LoopbackLogin) Run(ctx context.Context) (*Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("oauth2: start callback listener: %w", err)
	}
	// Normally already closed by srv.Shutdown below; this covers the paths
	// that return before the server starts.
	defer func() { _ = listener.Close() }()

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", listener.Addr().(*net.TCPAddr).Port, callbackPath)
	verifier, err := NewVerifier()
	if err != nil {
		return nil, err
	}
	state, err := NewState()
	if err != nil {
		return nil, err
	}
	authorizeURL, err := AuthRequest{
		Endpoint:    l.AuthorizeEndpoint,
		ClientID:    l.ClientID,
		RedirectURI: redirectURI,
		Scopes:      l.Scopes,
		State:       state,
		Challenge:   verifier.Challenge(),
		Extra:       l.Extra,
	}.URL()
	if err != nil {
		return nil, err
	}

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	deliver := func(r result) {
		select {
		case results <- r:
		default: // a second callback after the first is ignored
		}
	}
	successHTML := l.SuccessHTML
	if successHTML == "" {
		successHTML = defaultSuccessHTML
	}

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// State FIRST: until it matches, nothing in this request came from
		// the flow this process started. Any local process can hit the
		// listener.
		if err := CompareState(state, q.Get("state")); err != nil {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			deliver(result{err: fmt.Errorf("login callback rejected: %w", err)})
			return
		}
		if code := q.Get("error"); code != "" {
			msg := q.Get("error_description")
			writeCallbackPage(w, http.StatusBadRequest, "Login failed", msg)
			deliver(result{err: &Error{Code: code, Description: msg}})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code in callback", http.StatusBadRequest)
			deliver(result{err: errors.New("oauth2: login callback carried no code")})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, successHTML)
		deliver(result{code: code})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if l.Out != nil {
		fmt.Fprintf(l.Out, "Opening your browser to log in.\nIf it does not open, visit:\n    %s\n\n", authorizeURL)
	}
	if l.OpenURL != nil {
		_ = l.OpenURL(authorizeURL)
	}

	timeout := l.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var code string
	select {
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	case <-timer.C:
		return nil, fmt.Errorf("oauth2: timed out after %s waiting for the browser to return", timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return (&Exchanger{Client: l.HTTPClient}).Exchange(ctx, TokenRequest{
		Endpoint:    l.TokenEndpoint,
		ClientID:    l.ClientID,
		RedirectURI: redirectURI,
		Code:        code,
		Verifier:    verifier,
	})
}

func writeCallbackPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title></head>`+
		`<body style="font-family:system-ui;text-align:center;padding-top:4rem"><h1>%s</h1><p>%s</p></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

const defaultSuccessHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Logged in</title></head>
<body style="font-family:system-ui;text-align:center;padding-top:4rem">
<h1>Logged in</h1>
<p>You can close this tab and return to your terminal.</p>
</body></html>`
