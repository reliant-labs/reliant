package oauth2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HandoffLogin runs a desktop or CLI app's side of a forge login broker's
// sign-in HANDOFF (forge/pkg/devidp.HandoffRoutes): the person is already
// signed in to the web app in their browser, and approves giving this app a
// session of its own. It is RFC 8252 with the broker standing in for the
// authorization server:
//
//  1. bind a loopback listener (127.0.0.1, a port the OS picks);
//  2. POST /auth/handoff/start with an S256 challenge, that redirect and a state;
//  3. open the system browser at the authorize_url it answers — the web app's
//     consent page, where the person clicks "Continue";
//  4. receive the one-time code on the listener (state checked first);
//  5. POST /auth/handoff/redeem with the code and the verifier.
//
// The result is an opaque session credential ("fs_…") the app sends as
// `Authorization: Bearer`, which the server refreshes on its side and lists
// and revokes like any session. Keep it in the OS keychain; sign out with
// POST /auth/logout carrying it.
//
// The broker's allowlist (handoff_redirects) must name
// http://127.0.0.1<CallbackPath>; the port is free.
type HandoffLogin struct {
	// Base is the broker's origin: where /auth/handoff/* is served (the
	// app's API URL).
	Base string
	// CallbackPath is the listener's path; "" is "/callback".
	CallbackPath string
	// OpenURL launches the browser. When it fails the flow continues: the URL
	// has already been written to Out.
	OpenURL func(string) error
	// Out receives the "open this URL" notice. May be nil.
	Out io.Writer
	// Timeout bounds the wait for the browser to come back. 0 is 5 minutes.
	Timeout time.Duration
	// HTTPClient performs start and redeem. Nil uses a bounded default.
	HTTPClient HTTPDoer
	// SuccessHTML is the page the browser shows after the callback.
	SuccessHTML string
}

// HandoffSession is the credential a handoff redeemed into.
type HandoffSession struct {
	// Session is the session credential: send it as
	// `Authorization: Bearer <Session>`. A secret; it redacts itself.
	Session   string
	ExpiresAt time.Time
}

// String implements fmt.Stringer and redacts the credential.
func (HandoffSession) String() string { return "oauth2.HandoffSession{" + redacted + "}" }

// GoString implements fmt.GoStringer.
func (s HandoffSession) GoString() string { return s.String() }

// LogValue implements slog.LogValuer.
func (HandoffSession) LogValue() slog.Value { return slog.StringValue(redacted) }

// Run performs the handoff and returns the new session.
func (l HandoffLogin) Run(ctx context.Context) (*HandoffSession, error) {
	base := strings.TrimRight(strings.TrimSpace(l.Base), "/")
	if base == "" {
		return nil, errors.New("oauth2: a handoff needs the broker's base URL")
	}
	path := l.CallbackPath
	if path == "" {
		path = callbackPath
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("oauth2: CallbackPath %q must start with /", path)
	}
	verifier, err := NewVerifier()
	if err != nil {
		return nil, err
	}
	state, err := NewState()
	if err != nil {
		return nil, err
	}
	cb, err := startLoopback(path, state, l.SuccessHTML)
	if err != nil {
		return nil, err
	}
	defer cb.close()

	client := l.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := postHandoff(ctx, client, base+"/auth/handoff/start", map[string]string{
		"code_challenge":        verifier.Challenge().Value,
		"code_challenge_method": string(MethodS256),
		"redirect_uri":          cb.redirectURI,
		"state":                 state,
	}, &started); err != nil {
		return nil, fmt.Errorf("oauth2: start the handoff: %w", err)
	}
	if !strings.HasPrefix(started.AuthorizeURL, "https://") && !strings.HasPrefix(started.AuthorizeURL, "http://") {
		return nil, fmt.Errorf("oauth2: the broker answered no authorize_url")
	}
	if l.Out != nil {
		fmt.Fprintf(l.Out, "Opening your browser to sign in.\nIf it does not open, visit:\n    %s\n\n", started.AuthorizeURL)
	}
	if l.OpenURL != nil {
		_ = l.OpenURL(started.AuthorizeURL)
	}
	code, err := cb.wait(ctx, l.Timeout)
	if err != nil {
		return nil, err
	}
	var redeemed struct {
		Session   string `json:"session"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := postHandoff(ctx, client, base+"/auth/handoff/redeem", map[string]string{
		"code":          code,
		"code_verifier": verifier.Secret(),
		"redirect_uri":  cb.redirectURI,
	}, &redeemed); err != nil {
		return nil, fmt.Errorf("oauth2: redeem the handoff: %w", err)
	}
	if redeemed.Session == "" {
		return nil, errors.New("oauth2: the broker redeemed the handoff but answered no session")
	}
	out := &HandoffSession{Session: redeemed.Session}
	if t, err := time.Parse(time.RFC3339, redeemed.ExpiresAt); err == nil {
		out.ExpiresAt = t
	}
	return out, nil
}

// postHandoff POSTs body as JSON and decodes a 200 answer into out. Any other
// status is an error carrying the broker's own message.
func postHandoff(ctx context.Context, client HTTPDoer, url string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = summarizeBody(data)
		}
		return fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, e.Code, e.Error)
	}
	return json.Unmarshal(data, out)
}
