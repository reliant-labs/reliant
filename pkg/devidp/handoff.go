package devidp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
)

// Sign-in handoff: one client's signed-in session gives a NEW, independent
// session to another client — a desktop or CLI app (RFC 8252: the system
// browser, a loopback or private-use-scheme redirect) or a sibling host
// (console.acme.com beside app.acme.com) — without a parent-Domain cookie and
// without sharing a refresh token.
//
//	POST /auth/handoff/start      {code_challenge, code_challenge_method: "S256", redirect_uri, state}
//	                              → {authorize_url: "<app>/auth/handoff?req=<id>"}      (5 minutes)
//	GET  /auth/handoff/authorize?req=<id>      the consent page reads who and where; signed out → 401
//	POST /auth/handoff/authorize  {req}        the explicit consent; mints a one-time code
//	                              → redirect: <redirect_uri>?code=<code>&state=<state>  (60 seconds)
//	POST /auth/handoff/redeem     {code, code_verifier, redirect_uri}
//	                              → https redirect: a new session, cookie on the redeeming host
//	                              → loopback / private-use redirect: {session: "fs_…", expires_at}
//
// ── How the new session gets tokens of its own ─────────────────────────
//
// A sign-in through this package finalizes an auth request with an issuer
// session (Zitadel v2) and keeps that session, sealed, with the server-side
// session it created (Tokens.IssuerSession → session.Tokens). At consent the
// primary's issuer session finalizes a SECOND auth request for the broker's
// client (Client.ReissueTokens): a fresh token set with its own refresh token.
// That set is sealed under a key derived from the one-time code, so the row
// holding it is useless without the code, and redeem turns it into a session.
// A primary session that predates this (no issuer session kept) is told to
// sign in again (CodeReauthRequired), never failed silently.
//
// ── Security properties ────────────────────────────────────────────────
//
//   - The redirect is matched EXACTLY against an allowlist (the only
//     relaxation is RFC 8252 §7.3's any port on a loopback IP). An unlisted
//     redirect is refused at start and nothing about it is echoed back.
//   - Only S256 PKCE. The verifier never reaches this server until redeem, and
//     its hash is compared in constant time.
//   - The code is 32 random bytes, lives 60 seconds and is TAKEN (deleted) by
//     the first redeem attempt, whether or not that attempt succeeds. It is
//     bound to the request's challenge, redirect, the broker's client, the
//     subject and the approving session.
//   - Consent is an explicit POST (a GET never mints a code), behind the same
//     CrossOriginProtection as every sign-in route, carrying the primary's
//     cookie. A revoked primary can neither consent nor have its pending code
//     redeemed: redeem checks the approving session is still live.
//   - The new session is stamped session.ChannelHandoff, and its ProvedAt is
//     the primary's — a click proves no credential, so an action that wants a
//     recent proof still asks for one on the new client.
//   - Start and redeem are rate limited per client address.

// The routes. The scaffolded broker mounts each with
// mux.Handle(<constant>, http.HandlerFunc(routes.Handle<Name>)).
const (
	HandoffStartPath     = "POST /auth/handoff/start"
	HandoffAuthorizePath = "GET /auth/handoff/authorize"
	HandoffConsentPath   = "POST /auth/handoff/authorize"
	HandoffRedeemPath    = "POST /auth/handoff/redeem"
	// HandoffPagePath is the app's own consent page (a frontend route), the
	// default authorize_url: it reads HandoffAuthorizePath and posts
	// HandoffConsentPath.
	HandoffPagePath = "/auth/handoff"
)

// Lifetimes.
const (
	// HandoffRequestTTL is how long a started handoff waits for consent.
	HandoffRequestTTL = 5 * time.Minute
	// HandoffCodeTTL is how long a one-time code may be redeemed.
	HandoffCodeTTL = 60 * time.Second
)

// Error codes the handoff routes answer with, beside CodeNotSignedIn,
// CodeRateLimited, CodeCrossSite and CodeReauthRequired (account.go), which
// a handoff answers when the signed-in session cannot hand off (it predates
// handoffs, or the issuer no longer honours its sign-in): sign in again and
// approve once more.
const (
	// CodeHandoffInvalid: the request, code, redirect or verifier was not
	// accepted. One code for every cause a client cannot fix by retrying.
	CodeHandoffInvalid = "handoff_invalid"
)

// Redirect kinds, as the authorize answer and redeem name them.
const (
	// HandoffWeb is an https redirect: a sibling host, which redeems into a
	// cookie on its own origin.
	HandoffWeb = "web"
	// HandoffNative is a loopback or private-use-scheme redirect: an app on
	// the device, which redeems into a bearer session credential.
	HandoffNative = "native"
)

// HandoffConfig configures [NewHandoffRoutes].
type HandoffConfig struct {
	// Client and Flow are the broker's: the new token set is minted for
	// Flow's client.
	Client *Client
	Flow   FlowConfig
	// Sessions is the broker's session manager, and Store the handoff store
	// beside it (usually Sessions.Store() asserted to session.HandoffStore),
	// shared by every replica.
	Sessions *session.Manager
	Store    session.HandoffStore
	// AppBase is the app's public origin (and base path): where the consent
	// page and the sign-in page are.
	AppBase string
	// ConsentPage is the URL authorize_url points at; empty is AppBase +
	// HandoffPagePath. The page must be served on the origin that holds the
	// primary's session cookie.
	ConsentPage string
	// Redirects is the allowlist (ValidateHandoffRedirect states the rules).
	// Required; any invalid entry fails NewHandoffRoutes.
	Redirects []string
	// PrimarySession returns the session id of the signed-in request (the
	// session cookie's value), "" when there is none.
	PrimarySession func(r *http.Request) string
	// SignedIn sets the new session's cookie on a web (https) redeem, as
	// the broker sets its own after a sign-in.
	SignedIn func(w http.ResponseWriter, r *http.Request, issued session.Issued) error
	// TrustedProxyHops is as for ClientIP.
	TrustedProxyHops int
	Logger           *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// HandoffRoutes serves the sign-in handoff (see the file header).
type HandoffRoutes struct {
	cfg       HandoffConfig
	redirects []handoffRedirect
	consent   string

	startByIP  *limiter
	redeemByIP *limiter

	sweepMu   sync.Mutex
	lastSweep time.Time
}

// NewHandoffRoutes validates cfg and returns the routes.
func NewHandoffRoutes(cfg HandoffConfig) (*HandoffRoutes, error) {
	if cfg.Client == nil || cfg.Sessions == nil || cfg.Store == nil || cfg.PrimarySession == nil || cfg.SignedIn == nil {
		return nil, errors.New("handoff routes need a client, sessions, a handoff store, PrimarySession and SignedIn")
	}
	if strings.TrimSpace(cfg.Flow.ClientID) == "" {
		return nil, errors.New("handoff routes need the broker's client id")
	}
	u, err := url.Parse(strings.TrimRight(cfg.AppBase, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("handoff routes need the app's public origin (idp_login_uri), got %q", cfg.AppBase)
	}
	cfg.AppBase = strings.TrimRight(cfg.AppBase, "/")
	if len(cfg.Redirects) == 0 {
		return nil, errors.New("handoff routes need at least one allowed redirect")
	}
	h := &HandoffRoutes{cfg: cfg}
	for _, raw := range cfg.Redirects {
		r, err := parseHandoffRedirect(raw)
		if err != nil {
			return nil, err
		}
		h.redirects = append(h.redirects, r)
	}
	h.consent = cfg.ConsentPage
	if h.consent == "" {
		h.consent = cfg.AppBase + HandoffPagePath
	}
	if h.cfg.Logger == nil {
		h.cfg.Logger = slog.Default()
	}
	if h.cfg.Now == nil {
		h.cfg.Now = time.Now
	}
	h.startByIP = newLimiter(30, 10*time.Minute)
	h.redeemByIP = newLimiter(30, 10*time.Minute)
	h.startByIP.now, h.redeemByIP.now = h.cfg.Now, h.cfg.Now
	return h, nil
}

// SplitHandoffRedirects splits a comma- or whitespace-separated allowlist
// (the handoff_redirects config field) into its entries.
func SplitHandoffRedirects(list string) []string {
	return strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
}

// ValidateHandoffRedirect reports whether uri may be on a handoff allowlist:
//
//   - https://<host>[:port][/path] — a sibling host, matched exactly;
//   - http://127.0.0.1[:port][/path] or http://[::1][:port][/path] — a loopback
//     redirect (RFC 8252 §7.3), matched exactly except for the port, which a
//     native app picks at run time ("localhost" is refused: it can resolve
//     off the device, §8.3);
//   - <reverse.domain.scheme>:/path — a private-use scheme (RFC 8252 §7.1),
//     which must contain a dot, matched exactly.
//
// Refused everywhere: wildcards, fragments, queries, user info, and any other
// http or scheme.
func ValidateHandoffRedirect(uri string) error {
	_, err := parseHandoffRedirect(uri)
	return err
}

type handoffRedirect struct {
	raw  string
	kind string
	// loopback only: the URI's scheme, IP and path; the port is free.
	host, path string
}

func parseHandoffRedirect(raw string) (handoffRedirect, error) {
	bad := func(why string) (handoffRedirect, error) {
		return handoffRedirect{}, fmt.Errorf("handoff redirect %q: %s", raw, why)
	}
	if raw == "" || len(raw) > 2048 {
		return bad("must be 1 to 2048 characters")
	}
	for _, c := range raw {
		if c <= ' ' || c == 0x7f {
			return bad("contains whitespace or a control character")
		}
	}
	if strings.ContainsAny(raw, "*#") {
		return bad("wildcards and fragments are not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("is not a URI")
	}
	if u.User != nil {
		return bad("user info is not allowed")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return bad("a query is not allowed (the code and state are appended)")
	}
	if !strings.HasPrefix(raw, u.Scheme+":") {
		return bad("the scheme must be lower case")
	}
	switch u.Scheme {
	case "https":
		if u.Hostname() == "" || u.Opaque != "" {
			return bad("an https redirect needs a host")
		}
		if p := u.Port(); p != "" && !allDigits(p) {
			return bad("the port is not a number")
		}
		return handoffRedirect{raw: raw, kind: HandoffWeb}, nil
	case "http":
		host := u.Hostname()
		if host != "127.0.0.1" && host != "::1" {
			return bad("plain http is allowed only for a loopback IP (127.0.0.1 or [::1])")
		}
		if p := u.Port(); p != "" && !allDigits(p) {
			return bad("the port is not a number")
		}
		return handoffRedirect{raw: raw, kind: HandoffNative, host: host, path: u.EscapedPath()}, nil
	default:
		if !strings.Contains(u.Scheme, ".") {
			return bad("a private-use scheme must be a reverse domain name (com.example.app)")
		}
		if u.Host != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
			return bad("a private-use redirect has the form <scheme>:/path")
		}
		return handoffRedirect{raw: raw, kind: HandoffNative}, nil
	}
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// match returns the allowlisted entry uri matches, and its kind.
func (h *HandoffRoutes) match(uri string) (string, bool) {
	got, err := parseHandoffRedirect(uri)
	if err != nil {
		return "", false
	}
	for _, want := range h.redirects {
		if want.kind != got.kind {
			continue
		}
		if want.host != "" {
			if got.host == want.host && got.path == want.path {
				return got.kind, true
			}
			continue
		}
		if want.raw == got.raw {
			return got.kind, true
		}
	}
	return "", false
}

// ── The one-time values and their sealed records ─────────────────────────

const (
	handoffRequestPrefix = "fhr_"
	handoffCodePrefix    = "fhc_"
	handoffSecretBytes   = 32
)

func newHandoffSecret(prefix string) (string, error) {
	raw := make([]byte, handoffSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate handoff secret: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

type handoffKeys struct{ lookup, seal []byte }

// deriveHandoffKeys derives the row key and the sealing key from a one-time
// value, as session ids are (HKDF-SHA256 over 256 bits of entropy). ok is
// false for a value of the wrong shape, which is refused before any store
// read.
func deriveHandoffKeys(secret, prefix string) (handoffKeys, bool) {
	if !strings.HasPrefix(secret, prefix) || len(secret) != len(prefix)+base64.RawURLEncoding.EncodedLen(handoffSecretBytes) {
		return handoffKeys{}, false
	}
	if raw, err := base64.RawURLEncoding.DecodeString(secret[len(prefix):]); err != nil || len(raw) != handoffSecretBytes {
		return handoffKeys{}, false
	}
	lookup, err := hkdf.Key(sha256.New, []byte(secret), nil, "forge/pkg/devidp handoff "+prefix+" lookup v1", 32)
	if err != nil {
		return handoffKeys{}, false
	}
	seal, err := hkdf.Key(sha256.New, []byte(secret), nil, "forge/pkg/devidp handoff "+prefix+" seal v1", 32)
	if err != nil {
		return handoffKeys{}, false
	}
	return handoffKeys{lookup: lookup, seal: seal}, true
}

func (k handoffKeys) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.seal)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealRecord encrypts v under k, with the row key as additional data so a
// sealed blob moved onto another row does not open there.
func (k handoffKeys) sealRecord(v any) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	gcm, err := k.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, k.lookup), nil
}

func (k handoffKeys) openRecord(blob []byte, v any) error {
	gcm, err := k.aead()
	if err != nil {
		return err
	}
	if len(blob) < gcm.NonceSize() {
		return errors.New("handoff record is malformed")
	}
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], k.lookup)
	if err != nil {
		return errors.New("handoff record cannot be opened")
	}
	return json.Unmarshal(plain, v)
}

// handoffRequest is a started handoff, waiting for consent.
type handoffRequest struct {
	Challenge   string `json:"c"`
	RedirectURI string `json:"r"`
	State       string `json:"s"`
	ClientID    string `json:"a"`
}

// handoffCode is a consented handoff: the new session's tokens and what the
// code is bound to.
type handoffCode struct {
	Challenge   string `json:"c"`
	RedirectURI string `json:"r"`
	ClientID    string `json:"a"`
	Subject     string `json:"u"`
	// Primary is the approving session's handle (never its id).
	Primary  string `json:"p"`
	ProvedAt int64  `json:"pa"`

	Access  string `json:"at"`
	Refresh string `json:"rt,omitempty"`
	ID      string `json:"it,omitempty"`
	Expiry  int64  `json:"e,omitempty"`
	SID     string `json:"si,omitempty"`
	STok    string `json:"st,omitempty"`
}

// ── POST /auth/handoff/start ─────────────────────────────────────────────

type handoffStartRequest struct {
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	RedirectURI         string `json:"redirect_uri"`
	State               string `json:"state"`
}

// HandleStart begins a handoff. No cookie is needed: it only records the
// client's challenge and redirect, and answers where to send the browser.
func (h *HandoffRoutes) HandleStart(w http.ResponseWriter, r *http.Request) {
	if !h.startByIP.reserve(ClientKey(r, h.cfg.TrustedProxyHops)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, "Too many attempts. Try again later.")
		return
	}
	var req handoffStartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "malformed request")
		return
	}
	if req.CodeChallengeMethod != "S256" {
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "code_challenge_method must be S256")
		return
	}
	if !validChallenge(req.CodeChallenge) {
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "code_challenge must be the base64url SHA-256 of the verifier")
		return
	}
	if len(req.State) == 0 || len(req.State) > 512 || !printableASCII(req.State) {
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "state is required (1 to 512 printable characters)")
		return
	}
	if _, ok := h.match(req.RedirectURI); !ok {
		// Never echoed: the client knows what it sent.
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "redirect_uri is not allowed")
		return
	}
	id, err := newHandoffSecret(handoffRequestPrefix)
	if err != nil {
		h.unavailable(w, r, err)
		return
	}
	k, _ := deriveHandoffKeys(id, handoffRequestPrefix)
	sealed, err := k.sealRecord(handoffRequest{Challenge: req.CodeChallenge, RedirectURI: req.RedirectURI,
		State: req.State, ClientID: h.cfg.Flow.ClientID})
	if err != nil {
		h.unavailable(w, r, err)
		return
	}
	now := h.cfg.Now()
	if err := h.cfg.Store.PutHandoff(r.Context(), session.Handoff{Key: k.lookup, Sealed: sealed, ExpiresAt: now.Add(HandoffRequestTTL)}); err != nil {
		h.unavailable(w, r, err)
		return
	}
	h.sweep(r.Context(), now)
	writeJSON(w, http.StatusOK, map[string]any{
		"authorize_url": h.consent + "?req=" + url.QueryEscape(id),
		"expires_in":    int(HandoffRequestTTL.Seconds()),
	})
}

// validChallenge reports whether c is a base64url SHA-256 (43 characters).
func validChallenge(c string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && len(raw) == sha256.Size && len(c) == 43
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// sweep deletes expired handoff rows, at most once a minute, best effort:
// every read refuses an expired row anyway.
func (h *HandoffRoutes) sweep(ctx context.Context, now time.Time) {
	h.sweepMu.Lock()
	due := now.Sub(h.lastSweep) >= time.Minute
	if due {
		h.lastSweep = now
	}
	h.sweepMu.Unlock()
	if !due {
		return
	}
	if _, err := h.cfg.Store.DeleteExpiredHandoffs(ctx, now); err != nil {
		h.cfg.Logger.DebugContext(ctx, "handoff: sweep failed", "error", err)
	}
}

// ── GET /auth/handoff/authorize ──────────────────────────────────────────

// HandleAuthorize tells the consent page who is signed in and where the
// handoff would go. It never mints anything. Signed out, it answers 401 with
// a sign_in URL that comes back to the page.
func (h *HandoffRoutes) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	reqID := r.URL.Query().Get("req")
	primary, info, ok := h.primary(w, r, reqID)
	if !ok {
		return
	}
	req, _, ok := h.loadRequest(w, r, reqID, false)
	if !ok {
		return
	}
	kind, _ := h.match(req.RedirectURI)
	account := map[string]string{"subject": info.Subject}
	if token, err := h.cfg.Sessions.AccessToken(r.Context(), primary); err == nil {
		if ui, err := h.cfg.Client.UserInfo(r.Context(), token); err == nil && ui.Subject == info.Subject {
			account["email"], account["name"] = ui.Email, ui.Name
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account":     account,
		"redirect":    req.RedirectURI,
		"destination": destinationOf(req.RedirectURI),
		"kind":        kind,
	})
}

// destinationOf is what the consent page names as the destination: the
// origin of an https redirect, the loopback origin, or the private-use scheme.
func destinationOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	if u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + ":"
}

// primary is the request's signed-in session, or false with a 401 written.
func (h *HandoffRoutes) primary(w http.ResponseWriter, r *http.Request, reqID string) (string, session.Info, bool) {
	id := h.cfg.PrimarySession(r)
	if id != "" {
		info, err := h.cfg.Sessions.Info(r.Context(), id)
		if err == nil {
			return id, info, true
		}
		if !errors.Is(err, session.ErrNoSession) {
			h.unavailable(w, r, err)
			return "", session.Info{}, false
		}
	}
	back := HandoffPagePath
	if reqID != "" {
		back += "?req=" + url.QueryEscape(reqID)
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"code":    CodeNotSignedIn,
		"error":   "Sign in to continue.",
		"sign_in": h.cfg.AppBase + "/auth/sign-in?returnTo=" + url.QueryEscape(back),
	})
	return "", session.Info{}, false
}

// loadRequest reads (take: removes) the started handoff reqID names, or
// writes the refusal.
func (h *HandoffRoutes) loadRequest(w http.ResponseWriter, r *http.Request, reqID string, take bool) (handoffRequest, session.Handoff, bool) {
	expired := func() (handoffRequest, session.Handoff, bool) {
		writeErr(w, http.StatusNotFound, CodeHandoffInvalid, "This sign-in request has expired or was already used. Start again from the app.")
		return handoffRequest{}, session.Handoff{}, false
	}
	k, ok := deriveHandoffKeys(reqID, handoffRequestPrefix)
	if !ok {
		return expired()
	}
	var (
		row session.Handoff
		err error
	)
	if take {
		row, err = h.cfg.Store.TakeHandoff(r.Context(), k.lookup)
	} else {
		row, err = h.cfg.Store.GetHandoff(r.Context(), k.lookup)
	}
	if errors.Is(err, session.ErrNoHandoff) {
		return expired()
	}
	if err != nil {
		h.unavailable(w, r, err)
		return handoffRequest{}, session.Handoff{}, false
	}
	var req handoffRequest
	if !h.cfg.Now().Before(row.ExpiresAt) || k.openRecord(row.Sealed, &req) != nil || req.ClientID != h.cfg.Flow.ClientID {
		return expired()
	}
	return req, row, true
}

// ── POST /auth/handoff/authorize ─────────────────────────────────────────

// HandleConsent is the explicit approval. It takes the started request (once),
// mints the new token set from the primary's issuer session, seals it under a
// fresh one-time code and answers the redirect: a 303 to it for an HTML form
// post (a top-level navigation), else {"redirect": …} for the page's script.
func (h *HandoffRoutes) HandleConsent(w http.ResponseWriter, r *http.Request) {
	form := strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	var reqID string
	if form {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "malformed request")
			return
		}
		reqID = r.PostForm.Get("req")
	} else {
		var body struct {
			Req string `json:"req"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "malformed request")
			return
		}
		reqID = body.Req
	}
	primary, _, ok := h.primary(w, r, reqID)
	if !ok {
		return
	}
	req, row, ok := h.loadRequest(w, r, reqID, true)
	if !ok {
		return
	}
	// putBack returns the request for another try after a failure the
	// person can fix (sign in again) or that was not theirs (the issuer
	// was unreachable).
	putBack := func() {
		if err := h.cfg.Store.PutHandoff(context.WithoutCancel(r.Context()), row); err != nil {
			h.cfg.Logger.DebugContext(r.Context(), "handoff: could not restore the request", "error", err)
		}
	}
	tokens, info, err := h.cfg.Sessions.Open(r.Context(), primary)
	if errors.Is(err, session.ErrNoSession) {
		putBack()
		h.primary(w, r, reqID) // the 401, as if signed out
		return
	}
	if err != nil {
		putBack()
		h.unavailable(w, r, err)
		return
	}
	if tokens.IssuerSessionID == "" || tokens.IssuerSessionToken == "" {
		putBack()
		writeErr(w, http.StatusUnauthorized, CodeReauthRequired, "Sign in again to continue.")
		return
	}
	if h.cfg.Flow.RequireVerifiedEmail {
		user, err := h.cfg.Client.FindUserByID(r.Context(), info.Subject)
		if err != nil {
			putBack()
			h.unavailable(w, r, err)
			return
		}
		if !user.EmailVerified {
			writeErr(w, http.StatusForbidden, CodeEmailUnverified, "Verify your email address to continue.")
			return
		}
	}
	fresh, err := h.cfg.Client.ReissueTokens(r.Context(), h.cfg.Flow, Session{ID: tokens.IssuerSessionID, Token: tokens.IssuerSessionToken})
	if err != nil {
		putBack()
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status >= 400 && apiErr.status < 500 {
			h.cfg.Logger.InfoContext(r.Context(), "handoff: the issuer no longer honours the session's sign-in", "error", err)
			writeErr(w, http.StatusUnauthorized, CodeReauthRequired, "Sign in again to continue.")
			return
		}
		h.unavailable(w, r, err)
		return
	}
	if sub := tokenSubject(fresh); sub == "" || sub != info.Subject {
		// The issuer answered for someone else: refuse, and keep nothing.
		h.cfg.Logger.ErrorContext(r.Context(), "handoff: the reissued tokens name a different subject; refused")
		h.revokeQuietly(r.Context(), fresh.RefreshToken)
		writeErr(w, http.StatusBadGateway, CodeHandoffInvalid, "Sign-in is unavailable right now. Please try again.")
		return
	}
	st := SessionTokens(fresh, h.cfg.Now())
	code, err := newHandoffSecret(handoffCodePrefix)
	if err != nil {
		h.unavailable(w, r, err)
		return
	}
	k, _ := deriveHandoffKeys(code, handoffCodePrefix)
	rec := handoffCode{Challenge: req.Challenge, RedirectURI: req.RedirectURI, ClientID: req.ClientID,
		Subject: info.Subject, Primary: info.Handle, ProvedAt: info.ProvedAt.UnixMilli(),
		Access: st.AccessToken, Refresh: st.RefreshToken, ID: st.IDToken,
		SID: st.IssuerSessionID, STok: st.IssuerSessionToken}
	if !st.Expiry.IsZero() {
		rec.Expiry = st.Expiry.UnixMilli()
	}
	sealed, err := k.sealRecord(rec)
	if err == nil {
		err = h.cfg.Store.PutHandoff(r.Context(), session.Handoff{Key: k.lookup, Sealed: sealed, ExpiresAt: h.cfg.Now().Add(HandoffCodeTTL)})
	}
	if err != nil {
		h.revokeQuietly(r.Context(), fresh.RefreshToken)
		h.unavailable(w, r, err)
		return
	}
	target := req.RedirectURI + "?" + url.Values{"code": {code}, "state": {req.State}}.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if form {
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": target})
}

func (h *HandoffRoutes) revokeQuietly(ctx context.Context, refresh string) {
	if refresh == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := h.cfg.Client.RevokeToken(ctx, h.cfg.Flow, refresh, "refresh_token"); err != nil {
		h.cfg.Logger.DebugContext(ctx, "handoff: could not revoke an unused refresh token", "error", err)
	}
}

// ── POST /auth/handoff/redeem ────────────────────────────────────────────

type handoffRedeemRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

// HandleRedeem exchanges a one-time code and its PKCE verifier for a new
// session. The code is TAKEN before anything is checked, so a wrong verifier
// or redirect burns it: there is exactly one attempt per code.
func (h *HandoffRoutes) HandleRedeem(w http.ResponseWriter, r *http.Request) {
	if !h.redeemByIP.reserve(ClientKey(r, h.cfg.TrustedProxyHops)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, "Too many attempts. Try again later.")
		return
	}
	var req handoffRedeemRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "malformed request")
		return
	}
	refuse := func() {
		writeErr(w, http.StatusBadRequest, CodeHandoffInvalid, "The code is not valid. Start again from the app.")
	}
	k, ok := deriveHandoffKeys(req.Code, handoffCodePrefix)
	if !ok {
		refuse()
		return
	}
	row, err := h.cfg.Store.TakeHandoff(r.Context(), k.lookup)
	if errors.Is(err, session.ErrNoHandoff) {
		refuse()
		return
	}
	if err != nil {
		h.unavailable(w, r, err)
		return
	}
	var rec handoffCode
	if !h.cfg.Now().Before(row.ExpiresAt) || k.openRecord(row.Sealed, &rec) != nil {
		refuse()
		return
	}
	// From here on the code is spent: every refusal also revokes the
	// token set it carried, so nothing it minted outlives it.
	spent := func() {
		h.revokeQuietly(r.Context(), rec.Refresh)
		refuse()
	}
	if req.RedirectURI != rec.RedirectURI || rec.ClientID != h.cfg.Flow.ClientID || !verifierMatches(req.CodeVerifier, rec.Challenge) {
		spent()
		return
	}
	kind, ok := h.match(rec.RedirectURI)
	if !ok {
		// Removed from the allowlist since the code was minted.
		spent()
		return
	}
	if live, err := h.approverLive(r.Context(), rec); err != nil {
		h.revokeQuietly(r.Context(), rec.Refresh)
		h.unavailable(w, r, err)
		return
	} else if !live {
		// The session that approved this was signed out (or revoked) since.
		spent()
		return
	}
	tokens := session.Tokens{AccessToken: rec.Access, RefreshToken: rec.Refresh, IDToken: rec.ID,
		IssuerSessionID: rec.SID, IssuerSessionToken: rec.STok}
	if rec.Expiry != 0 {
		tokens.Expiry = time.UnixMilli(rec.Expiry)
	}
	issued, err := h.cfg.Sessions.Create(r.Context(), tokens, session.Meta{Subject: rec.Subject, UserAgent: r.UserAgent(),
		IssuerSession: rec.SID, ProvedAt: time.UnixMilli(rec.ProvedAt), Channel: session.ChannelHandoff})
	if err != nil {
		h.revokeQuietly(r.Context(), rec.Refresh)
		h.unavailable(w, r, err)
		return
	}
	// The check above and Create are two operations, and the approving
	// session may be revoked between them (a sign-out, RevokeSubject). So
	// the approver is read again now that the new session exists: a
	// revocation that completed before this read is seen here and ends the
	// new session; one that runs after it finds the new session stored (and
	// RevokeSubject ends it with the rest). Either way a revocation that
	// completes before the redeem answers wins. Fails closed.
	if live, err := h.approverLive(r.Context(), rec); err != nil || !live {
		if _, _, rerr := h.cfg.Sessions.Revoke(r.Context(), issued.ID); rerr != nil && !errors.Is(rerr, session.ErrNoSession) {
			h.cfg.Logger.ErrorContext(r.Context(), "handoff: could not end a session whose approver was revoked", "error", rerr)
		}
		if err != nil {
			h.revokeQuietly(r.Context(), rec.Refresh)
			h.unavailable(w, r, err)
			return
		}
		spent()
		return
	}
	h.redeemByIP.refund(ClientKey(r, h.cfg.TrustedProxyHops))
	if kind == HandoffWeb {
		if err := h.cfg.SignedIn(w, r, issued); err != nil {
			h.unavailable(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "kind": kind})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       kind,
		"session":    issued.ID,
		"token_type": "Bearer",
		"expires_at": issued.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// approverLive reports whether the session that approved rec is still live
// and still the subject's. An error is the store's, not a refusal.
func (h *HandoffRoutes) approverLive(ctx context.Context, rec handoffCode) (bool, error) {
	approver, err := h.cfg.Sessions.InfoByHandle(ctx, rec.Primary)
	if errors.Is(err, session.ErrNoSession) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return approver.Subject == rec.Subject, nil
}

// verifierMatches reports whether verifier is an RFC 7636 verifier whose S256
// is challenge, comparing in constant time.
func verifierMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for i := 0; i < len(verifier); i++ {
		c := verifier[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (h *HandoffRoutes) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	h.cfg.Logger.ErrorContext(r.Context(), "handoff: unavailable", "error", err)
	writeErr(w, http.StatusServiceUnavailable, "", "Sign-in is unavailable right now. Please try again.")
}
