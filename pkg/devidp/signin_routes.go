package devidp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// SignInRoutes serves the sign-in methods a LOGIN_POLICY document turns on —
// passkeys, magic links, social sign-in, invitations, address verification
// and sign-up per mode — for an app's native login broker.
//
// The app's own internal/app/login_broker.go mounts each handler on its own
// route (the route constants below), so an app can drop, wrap or replace any
// one of them. The broker keeps what is the app's: the session cookie
// (SignedIn), reading it back (SessionToken) and the password checklist
// (PasswordPolicy, RejectPassword).
//
// ── THE RULE EVERY HANDLER HERE FOLLOWS ───────────────────────────────
//
// The broker's credential can do almost anything to any account: set a
// password, mark an address verified, mint codes, register a passkey. So
// every privileged call below follows a PROOF that succeeded for the same
// user IN THE SAME REQUEST — an invitation code, an email-verification code,
// an emailed one-time code, a passkey assertion, a provider intent, or a
// session token the issuer validated. The user id a privileged call acts on
// comes from that proof, never from a request body.
//
// And no route issues a session for an unverified address while the policy
// requires verified email: CompleteSignIn reads the session back and refuses.
type SignInRoutes struct {
	cfg    SignInRoutesConfig
	origin string // scheme://host[:port] of AppBase: the only origin a passkey may come from
	rpID   string
	seal   *sealer
	// decoyKey is the current decoy secret (see decoySecret).
	decoyKey func() []byte
	guard    *http.CrossOriginProtection

	magicByAddress  *limiter
	magicByIP       *limiter
	magicByCode     *limiter
	passkeyByIP     *limiter
	verifyResendBy  *limiter
	verifyResendIP  *limiter
	codeByUser      *limiter
	codeByIP        *limiter
	registerByIP    *limiter
	socialStartByIP *limiter

	// proofs runs address proofs of one account one at a time (proveAddress).
	proofs keyedMutex
}

// The routes. The scaffolded broker mounts each with
// mux.Handle(<constant>, http.HandlerFunc(routes.Handle<Name>)); the
// scaffolded frontend is written against these exact paths.
const (
	PasskeyBeginPath          = "POST /auth/passkey/begin"
	PasskeyFinishPath         = "POST /auth/passkey/finish"
	PasskeyRegisterBeginPath  = "POST /auth/passkey/register/begin"
	PasskeyRegisterFinishPath = "POST /auth/passkey/register/finish"
	PasskeysPath              = "GET /auth/passkeys"
	MagicRequestPath          = "POST /auth/magic/request"
	MagicConsumePath          = "POST /auth/magic/consume"
	SocialStartPath           = "POST /auth/social/start"
	SocialCallbackPath        = "POST /auth/social/callback"
	InviteAcceptPath          = "POST /auth/invite/accept"
	VerifyPath                = "POST /auth/verify"
	VerifyResendPath          = "POST /auth/verify/resend"
)

// Error codes in a refusal's `code` field, which the sign-in screen branches on.
const (
	CodeEmailUnverified    = "email_unverified"
	CodePasskeyFailed      = "passkey_failed"
	CodeNotSignedIn        = "not_signed_in"
	CodeInvalidCode        = "invalid_code"
	CodeMagicOtherBrowser  = "magic_other_browser"
	CodeSocialFailed       = "social_failed"
	CodeSocialLinkRequired = "social_link_required"
	CodeSocialNoAccount    = "social_no_account"
	CodeSignupNotAllowed   = "signup_not_allowed"
	CodePasswordRequired   = "password_required"
	CodeMethodNotOffered   = "method_not_offered"
	CodeUnknownProvider    = "unknown_provider"
	CodeRateLimited        = "rate_limited"
	CodeAccountExists      = "account_exists"
	CodeSocialTaken        = "social_taken"
	CodeCrossSite          = "cross_site"
	CodePasswordTooLong    = "password_too_long"
)

// The sentences the browser is shown. None says whether an address has an
// account.
const (
	msgMagicSent     = "If that address can sign in, a sign-in link is on its way."
	msgVerifySent    = "Check your email: we sent a link to confirm the address."
	msgVerifyResent  = "If that address is waiting to be confirmed, a new link is on its way."
	msgUnverified    = "Confirm your email address first: use the link we sent, or ask for a new one."
	msgPasskeyFailed = "That passkey was not accepted."
	msgInvalidCode   = "That code or link is not valid. Codes are single-use and expire; ask for a new one."
	msgOtherBrowser  = "This link only works in the browser where you asked for it. Type the code from the email there."
	msgSocialFailed  = "That sign-in did not complete. Please try again."
	msgSocialLink    = "Sign in another way, then connect %s from your account."
	msgSocialNoAcct  = "No account for this address — ask an owner for an invitation."
	msgSignupDomain  = "Accounts with that email address cannot be created here."
	msgTooMany       = "Too many attempts. Please wait a while before trying again."
	msgUnavailable   = "Sign-in is unavailable right now. Please try again."
	msgNotSignedIn   = "Sign in first."
	msgMethod        = "That sign-in method is not offered here."
	msgAccountExists = "That address already has an account. Sign in instead."
	msgSocialTaken   = "That %s account is already connected to another user. Use another one, or another way to sign in."
)

// SignInRoutesConfig is what the app's broker supplies.
type SignInRoutesConfig struct {
	Client *Client
	// Flow is the OAuth client the session is finalized into. Its
	// RequireVerifiedEmail is set from Policy.
	Flow   FlowConfig
	Policy LoginPolicy
	// AppBase is the app's public origin plus base path ("https://app.example.com"),
	// from idp_login_uri. Every mailed link and the passkey origin derive from
	// it — never from a request header the caller controls.
	AppBase string
	// ProjectID is the identity project (jwt_audience), which a signup_role
	// is granted on. "" grants nothing.
	ProjectID string
	// CookiePrefix names the binding cookies (<prefix>_pk, _ml, …).
	CookiePrefix string
	SecureCookie bool
	Logger       *slog.Logger

	// TrustedProxyHops is how many reverse proxies in front of the server
	// append to X-Forwarded-For; rate limits key on the client address they
	// saw (see ClientIP). 0 uses the connection's peer.
	TrustedProxyHops int
	// ResponseFloor is the least time HandleMagicRequest and
	// HandlePasskeyBegin take, so their timing does not say whether an
	// address has an account. 0 is DefaultResponseFloor; negative disables
	// it (tests).
	ResponseFloor time.Duration

	// SignedIn stores tokens as the app's session and returns the identity
	// body the browser renders. Required.
	SignedIn func(http.ResponseWriter, Tokens) any
	// SessionToken returns the request's session access token, "" when none.
	// Required.
	SessionToken func(*http.Request) string
	// PasswordPolicy is the checklist a new password is held to before the
	// issuer is asked (nil: none). RejectPassword answers a rejected one.
	PasswordPolicy func(context.Context) (PasswordPolicy, error)
	RejectPassword func(ctx context.Context, w http.ResponseWriter, password string, err *PasswordPolicyError)
}

// NewSignInRoutes validates the configuration. It refuses a policy-bearing
// broker with no AppBase: every mailed link and the passkey origin need it.
func NewSignInRoutes(cfg SignInRoutesConfig) (*SignInRoutes, error) {
	if cfg.Client == nil || cfg.SignedIn == nil || cfg.SessionToken == nil {
		return nil, fmt.Errorf("sign-in routes need a client, SignedIn and SessionToken")
	}
	u, err := url.Parse(strings.TrimRight(cfg.AppBase, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("sign-in routes need the app's public origin (idp_login_uri), got %q", cfg.AppBase)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.CookiePrefix == "" {
		cfg.CookiePrefix = "app"
	}
	if cfg.ResponseFloor == 0 {
		cfg.ResponseFloor = DefaultResponseFloor
	}
	cfg.AppBase = strings.TrimRight(cfg.AppBase, "/")
	cfg.Flow.RequireVerifiedEmail = cfg.Policy.RequireVerifiedEmail
	rpID := strings.ToLower(strings.TrimSpace(cfg.Policy.PasskeyRPID))
	if rpID == "" {
		rpID = u.Hostname()
	}
	host := strings.ToLower(u.Hostname())
	if host != rpID && !strings.HasSuffix(host, "."+rpID) {
		return nil, fmt.Errorf("passkey_rp_id %q is not %q or a parent domain of it", rpID, host)
	}
	fallback := make([]byte, 32)
	if _, err := rand.Read(fallback); err != nil {
		return nil, err
	}
	client := cfg.Client
	r := &SignInRoutes{
		cfg:      cfg,
		origin:   u.Scheme + "://" + u.Host,
		rpID:     rpID,
		seal:     &sealer{key: func() []byte { return []byte(client.bearer()) }, now: time.Now},
		decoyKey: func() []byte { return decoySecret(client.bearer(), fallback) },
		guard:    NewCrossOriginProtection(cfg.AppBase),

		// Per address and per user: small, because they guard one inbox or
		// one code. Per client IP: room for an office or a carrier NAT
		// behind one address, and in front of every per-user budget (see
		// limiter) so no one client can churn the per-user tables.
		magicByAddress:  newLimiter(3, 15*time.Minute),
		magicByIP:       newLimiter(20, time.Hour),
		magicByCode:     newLimiter(5, 15*time.Minute),
		passkeyByIP:     newLimiter(60, 15*time.Minute),
		verifyResendBy:  newLimiter(3, time.Hour),
		verifyResendIP:  newLimiter(20, time.Hour),
		codeByUser:      newLimiter(5, time.Hour),
		codeByIP:        newLimiter(30, time.Hour),
		registerByIP:    newLimiter(20, time.Hour),
		socialStartByIP: newLimiter(60, 15*time.Minute),
	}
	return r, nil
}

// RPID is the passkey relying-party id in use.
func (r *SignInRoutes) RPID() string { return r.rpID }

// Options is the v2 part of GET /auth/options.
type Options struct {
	SelfSignup bool            `json:"selfSignup"`
	Signup     string          `json:"signup"`
	Methods    []string        `json:"methods"`
	Social     []OptionsSocial `json:"social"`
	Mail       bool            `json:"mail"`
}

// OptionsSocial is one social button, without its issuer id.
type OptionsSocial struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

// OptionsFor is what GET /auth/options answers for a policy. selfSignup is
// the legacy switch (development or auth_self_signup), used only when the
// policy is the legacy one.
func OptionsFor(p LoginPolicy, legacySelfSignup bool) Options {
	if !p.Present {
		signup := SignupInvite
		if legacySelfSignup {
			signup = SignupOpen
		}
		return Options{SelfSignup: legacySelfSignup, Signup: signup, Methods: []string{MethodPassword}, Social: []OptionsSocial{}}
	}
	o := Options{SelfSignup: p.RegisterServed(), Signup: p.Signup, Methods: append([]string{}, p.Methods...),
		Social: []OptionsSocial{}, Mail: p.MailDelivers()}
	for _, s := range p.Social {
		label := s.Label
		if label == "" {
			label = s.Type
		}
		o.Social = append(o.Social, OptionsSocial{Type: s.Type, Label: label})
	}
	return o
}

// ── Binding cookies ────────────────────────────────────────────────────

const (
	cookiePasskey  = "_pk"
	cookieRegister = "_pkr"
	cookieMagic    = "_ml"
	cookieSocial   = "_sx"
	cookieEnroll   = "_enroll"
	cookieLink     = "_sxl"
	cookiePassword = "_pwp"
)

type cookieSpec struct {
	suffix   string
	ttl      time.Duration
	sameSite http.SameSite
}

var (
	passkeyCookie  = cookieSpec{cookiePasskey, 5 * time.Minute, http.SameSiteStrictMode}
	registerCookie = cookieSpec{cookieRegister, 5 * time.Minute, http.SameSiteStrictMode}
	magicCookie    = cookieSpec{cookieMagic, 10 * time.Minute, http.SameSiteLaxMode}
	socialCookie   = cookieSpec{cookieSocial, 10 * time.Minute, http.SameSiteLaxMode}
	enrollCookie   = cookieSpec{cookieEnroll, 10 * time.Minute, http.SameSiteStrictMode}
	// linkCookie holds a social account waiting for its new account's
	// address to be proven: the verification link is opened from a mail
	// client, so Lax, and it lives as long as a person takes to read mail.
	linkCookie = cookieSpec{cookieLink, time.Hour, http.SameSiteLaxMode}
	// passwordCookie holds the password a sign-up chose, waiting, like
	// linkCookie, for the address's code to be proven in this browser.
	passwordCookie = cookieSpec{cookiePassword, time.Hour, http.SameSiteLaxMode}
)

// setBinding writes a sealed binding cookie. Path=/ because the dev server
// proxies the API under /_api: a path-scoped cookie would never come back.
// cookieName is a binding cookie's name. With Secure cookies it carries the
// __Host- prefix, which the browser accepts only from a secure origin, with
// Path=/ and no Domain: a sibling subdomain cannot set or overwrite it.
func (r *SignInRoutes) cookieName(c cookieSpec) string {
	if r.cfg.SecureCookie {
		return "__Host-" + r.cfg.CookiePrefix + c.suffix
	}
	return r.cfg.CookiePrefix + c.suffix
}

// ip is the key per-client rate limits count under (ClientKey).
func (r *SignInRoutes) ip(req *http.Request) string { return ClientKey(req, r.cfg.TrustedProxyHops) }

// admit refuses a cross-site request before anything else runs. The
// scaffolded broker also mounts every route behind the same guard (GuardMux);
// checking here keeps a route mounted without it safe.
func (r *SignInRoutes) admit(w http.ResponseWriter, req *http.Request) bool {
	if err := r.guard.Check(req); err != nil {
		writeErr(w, http.StatusForbidden, CodeCrossSite, "That request came from another site.")
		return false
	}
	return true
}

func (r *SignInRoutes) setBinding(w http.ResponseWriter, c cookieSpec, payload any) error {
	v, err := r.seal.seal(c.suffix, payload, c.ttl)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: r.cookieName(c), Value: v, Path: "/", MaxAge: int(c.ttl.Seconds()),
		HttpOnly: true, Secure: r.cfg.SecureCookie, SameSite: c.sameSite,
	})
	return nil
}

func (r *SignInRoutes) readBinding(req *http.Request, c cookieSpec, out any) bool {
	ck, err := req.Cookie(r.cookieName(c))
	if err != nil {
		return false
	}
	return r.seal.open(c.suffix, ck.Value, out) == nil
}

func (r *SignInRoutes) clearBinding(w http.ResponseWriter, c cookieSpec) {
	http.SetCookie(w, &http.Cookie{
		Name: r.cookieName(c), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.cfg.SecureCookie, SameSite: c.sameSite,
	})
}

// sessionBinding is a challenge session held across two requests, or a decoy.
type sessionBinding struct {
	Decoy bool   `json:"d,omitempty"`
	ID    string `json:"i,omitempty"`
	Token string `json:"t,omitempty"`
}

// magicBinding is a pending magic link: a one-time-code session ("s"), an
// email-verification code for an unverified account ("v"), or nothing ("n",
// the address cannot sign in — indistinguishable from outside).
//
// Attempts is the key its consume attempts are charged to, set for EVERY
// kind (see magicAttemptKey): a decoy that answered 401 forever, while a real
// link answered 429 after five wrong codes, would say which addresses have
// an account.
type magicBinding struct {
	Kind     string `json:"k"`
	ID       string `json:"i,omitempty"`
	Token    string `json:"t,omitempty"`
	UserID   string `json:"u,omitempty"`
	Attempts string `json:"a,omitempty"`
}

type enrollBinding struct {
	UserID string `json:"u"`
	CodeID string `json:"c"`
	Code   string `json:"s"`
}

type registerBinding struct {
	UserID    string `json:"u"`
	PasskeyID string `json:"p"`
}

// socialBinding is a social sign-in in flight. InviteUserID and InviteCode
// are set when it accepts an invitation: the code is checked at the callback,
// in the same request that links the provider account, and never before, so
// an abandoned provider round trip spends nothing.
type socialBinding struct {
	IntentID     string `json:"i"`
	ReturnTo     string `json:"r"`
	InviteUserID string `json:"u,omitempty"`
	InviteCode   string `json:"c,omitempty"`
}

// pendingLinkBinding is a provider account that asserted an UNVERIFIED
// address for a new account. It is linked only when the browser that holds
// it proves that address's code (HandleVerify); linking at creation would let
// whoever's provider reports a victim's address keep a way into the account
// once the victim confirms it.
type pendingLinkBinding struct {
	UserID string  `json:"u"`
	Link   IDPLink `json:"l"`
}

// pendingPasswordBinding is the password a sign-up chose. The account is
// created WITHOUT it, and it is set only when the browser that holds it
// proves the address's code (HandleVerify): a password set at creation would
// let whoever registered a victim's address keep a way into the account once
// the victim confirms it. UserID is "" when the address already has a
// verified account: nothing is pending, but the cookie is set alike.
type pendingPasswordBinding struct {
	UserID string `json:"u"`
	// Password is sealed as bytes (base64 in the envelope): its sealed size
	// is then a function of its length alone, whatever characters it holds.
	Password []byte `json:"p"`
}

// placeholderUserID stands in for an account id when a sign-up's binding is
// trial-sealed before the address is looked up. Longer than any issuer id
// (Zitadel's are 18 digits), so a binding that seals with it seals with the
// real one.
const placeholderUserID = "000000000000000000000000000000"

// ── Passkey sign-in ────────────────────────────────────────────────────

type passkeyBeginRequest struct {
	Email string `json:"email"`
}

// HandlePasskeyBegin requests a passkey challenge for an address. An unknown
// address (or one the issuer refuses a challenge for) gets a DECOY that is
// byte-for-byte the shape the issuer emits, in a binding cookie of the same
// length, after the same minimum time — so neither the answer, the cookie nor
// the timing says whether the account exists. With no address, the
// enrolment cookie of an invite or verification names the user.
func (r *SignInRoutes) HandlePasskeyBegin(w http.ResponseWriter, req *http.Request) {
	var body passkeyBeginRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	// Deferred: net/http sends nothing until the handler returns, so the
	// whole answer waits for the floor.
	defer holdUntil(ctx, time.Now(), r.cfg.ResponseFloor)
	if !r.passkeyByIP.reserve(r.ip(req)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	email := normalizeEmail(body.Email)
	userID := ""
	if email == "" {
		var e enrollBinding
		if !r.readBinding(req, enrollCookie, &e) {
			writeErr(w, http.StatusBadRequest, "", "Enter your email address.")
			return
		}
		userID = e.UserID
	} else if u, err := r.cfg.Client.FindUserByEmail(ctx, email); err == nil {
		userID = u.ID
	} else if !errors.Is(err, ErrUserNotFound) {
		r.cfg.Logger.ErrorContext(ctx, "passkey sign-in: user lookup failed", "error", err)
	}
	if userID != "" {
		cs, err := r.cfg.Client.CreateChallengeSession(ctx, "", userID, Challenge{WebAuthnDomain: r.rpID})
		if err == nil {
			if err := r.setBinding(w, passkeyCookie, sessionBinding{ID: cs.ID, Token: cs.Token}); err != nil {
				r.unavailable(ctx, w, "passkey sign-in: seal", err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"options": normalizeJSON(cs.WebAuthnOptions)})
			return
		}
		r.cfg.Logger.InfoContext(ctx, "passkey sign-in: no challenge for the account; answering a decoy", "user_id", userID, "error", err)
	}
	if err := r.setBinding(w, passkeyCookie, sessionBinding{Decoy: true}); err != nil {
		r.unavailable(ctx, w, "passkey sign-in: seal", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": r.decoyOptions(email)})
}

// decoyOptions is a request-options document exactly as Zitadel v4.19.5
// emits one (go-webauthn v0.10.2 CredentialAssertion: a 32-byte challenge,
// timeout 300000 ms for a user-verification-required login, rpId,
// allowCredentials of {type, id} with no transports, userVerification), with
// a fresh challenge and credential ids that are STABLE for the address — their
// number (one, sometimes two) and their lengths (16, 20, 32 or 64 bytes, the
// common authenticators' id sizes) derived from a keyed hash — so asking twice
// does not tell a decoy from a real account.
func (r *SignInRoutes) decoyOptions(email string) json.RawMessage {
	seed := r.decoyBytes(email, 0, 32)
	count := 1
	if seed[0]%8 == 0 {
		count = 2
	}
	creds := make([]any, 0, count)
	for i := 0; i < count; i++ {
		n := []int{16, 20, 32, 64}[seed[1+i]%4]
		creds = append(creds, map[string]any{"type": "public-key", "id": base64.RawURLEncoding.EncodeToString(r.decoyBytes(email, i+1, n))})
	}
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	b, _ := json.Marshal(map[string]any{"publicKey": map[string]any{
		"challenge":        base64.RawURLEncoding.EncodeToString(challenge),
		"timeout":          300000,
		"rpId":             r.rpID,
		"allowCredentials": creds,
		"userVerification": "required",
	}})
	return normalizeJSON(b)
}

const decoyInfo = "forge devidp decoy v1"

// decoySecret keys the decoys. It is derived from the broker's token, which
// every replica shares (as the binding-cookie key is, under its own domain
// separation): a per-process random secret would give an unknown address
// different credential ids on every replica and after every restart, while a
// real account's stay the same — the enumeration oracle again, read across
// requests. With no token there is no shared material (and no cookie can be
// sealed, so no sign-in completes); the per-process fallback is used then.
func decoySecret(token string, fallback []byte) []byte {
	if token == "" {
		return fallback
	}
	k := sha256.Sum256(append([]byte(decoyInfo+"\x00"), token...))
	return k[:]
}

// decoyBytes is n bytes keyed by the address and an index.
func (r *SignInRoutes) decoyBytes(email string, index, n int) []byte {
	key := r.decoyKey()
	var out []byte
	for block := 0; len(out) < n; block++ {
		m := hmac.New(sha256.New, key)
		fmt.Fprintf(m, "%d\x00%d\x00%s", index, block, email)
		out = m.Sum(out)
	}
	return out[:n]
}

// normalizeJSON re-encodes a JSON document with one encoder, so a decoy and
// the issuer's real options cannot differ in key order, spacing or number
// format.
func normalizeJSON(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

type credentialRequest struct {
	Credential json.RawMessage `json:"credential"`
	Name       string          `json:"name,omitempty"`
}

// HandlePasskeyFinish checks a passkey assertion and signs in. The origin and
// ceremony type are checked HERE, before the issuer, because the issuer
// accepts whatever origin the client reports.
//
// A decoy and a real binding are answered identically on every failure:
// the binding is cleared first, whatever follows, and the assertion is
// checked before the binding is looked at, so neither the status, the body
// nor a Set-Cookie header says whether the address got a real challenge.
func (r *SignInRoutes) HandlePasskeyFinish(w http.ResponseWriter, req *http.Request) {
	var body credentialRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	var b sessionBinding
	bound := r.readBinding(req, passkeyCookie, &b)
	r.clearBinding(w, passkeyCookie)
	if err := CheckClientData(body.Credential, r.origin, WebAuthnGet); err != nil {
		r.cfg.Logger.WarnContext(ctx, "passkey sign-in refused before the issuer", "error", err)
		writeErr(w, http.StatusUnauthorized, CodePasskeyFailed, msgPasskeyFailed)
		return
	}
	if !bound || b.Decoy {
		writeErr(w, http.StatusUnauthorized, CodePasskeyFailed, msgPasskeyFailed)
		return
	}
	session, err := r.cfg.Client.SetSession(ctx, ChallengeSession{ID: b.ID, Token: b.Token}, Answer{WebAuthnAssertion: body.Credential})
	if err != nil {
		r.cfg.Logger.WarnContext(ctx, "passkey sign-in rejected", "error", err)
		writeErr(w, http.StatusUnauthorized, CodePasskeyFailed, msgPasskeyFailed)
		return
	}
	r.complete(w, req, session, func(tokens Tokens) {
		r.clearBinding(w, enrollCookie)
		writeJSON(w, http.StatusOK, r.cfg.SignedIn(w, tokens))
	})
}

// complete finalizes a session, answering the gate's refusals itself.
func (r *SignInRoutes) complete(w http.ResponseWriter, req *http.Request, s Session, ok func(Tokens)) {
	ctx := req.Context()
	tokens, err := r.cfg.Client.CompleteSignIn(ctx, r.cfg.Flow, s)
	switch {
	case err == nil:
		ok(tokens)
	case errors.Is(err, ErrEmailUnverified):
		writeErr(w, http.StatusForbidden, CodeEmailUnverified, msgUnverified)
	default:
		r.unavailable(ctx, w, "sign-in could not be completed", err)
	}
}

// ── Passkey registration ───────────────────────────────────────────────

// enrollingUser is the user a passkey registration is for: the signed-in
// user (from the session token, validated by the issuer), or the user an
// enrolment cookie names (set only after an invitation or verification code
// was proven). Never a request body.
//
// The enrolment cookie wins: it was set by a proof in this browser moments
// ago, for a person who is setting up. If the browser is ALSO signed in as
// someone else, the request is refused rather than guessed at — registering a
// passkey on the wrong one of two accounts is not recoverable by the person.
func (r *SignInRoutes) enrollingUser(req *http.Request) (userID string, code *PasskeyRegistrationCode, ok bool) {
	var e enrollBinding
	enrolling := r.readBinding(req, enrollCookie, &e) && e.UserID != ""
	subject := ""
	if tok := r.cfg.SessionToken(req); tok != "" {
		if info, err := r.cfg.Client.UserInfo(req.Context(), tok); err == nil {
			subject = info.Subject
		}
	}
	switch {
	case enrolling && subject != "" && subject != e.UserID:
		return "", nil, false
	case enrolling:
		return e.UserID, &PasskeyRegistrationCode{ID: e.CodeID, Code: e.Code}, true
	case subject != "":
		return subject, nil, true
	}
	return "", nil, false
}

// HandlePasskeyRegisterBegin starts registering a passkey for the signed-in
// (or enrolling) user.
func (r *SignInRoutes) HandlePasskeyRegisterBegin(w http.ResponseWriter, req *http.Request) {
	if !r.admit(w, req) {
		return
	}
	ctx := req.Context()
	userID, code, ok := r.enrollingUser(req)
	if !ok {
		writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		return
	}
	reg, err := r.cfg.Client.RegisterPasskey(ctx, userID, r.rpID, code)
	if err != nil {
		r.unavailable(ctx, w, "passkey registration could not start", err)
		return
	}
	if err := r.setBinding(w, registerCookie, registerBinding{UserID: userID, PasskeyID: reg.PasskeyID}); err != nil {
		r.unavailable(ctx, w, "passkey registration: seal", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": reg.Options})
}

// HandlePasskeyRegisterFinish verifies the browser's attestation.
func (r *SignInRoutes) HandlePasskeyRegisterFinish(w http.ResponseWriter, req *http.Request) {
	var body credentialRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	var b registerBinding
	userID, _, ok := r.enrollingUser(req)
	if !ok || !r.readBinding(req, registerCookie, &b) || b.UserID != userID {
		writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		return
	}
	if err := CheckClientData(body.Credential, r.origin, WebAuthnCreate); err != nil {
		r.cfg.Logger.WarnContext(ctx, "passkey registration refused before the issuer", "error", err)
		writeErr(w, http.StatusBadRequest, CodePasskeyFailed, "That passkey could not be registered.")
		return
	}
	r.clearBinding(w, registerCookie)
	if err := r.cfg.Client.VerifyPasskeyRegistration(ctx, userID, b.PasskeyID, body.Credential, body.Name); err != nil {
		r.cfg.Logger.WarnContext(ctx, "passkey registration rejected", "user_id", userID, "error", err)
		writeErr(w, http.StatusBadRequest, CodePasskeyFailed, "That passkey could not be registered.")
		return
	}
	r.cfg.Logger.InfoContext(ctx, "passkey registered", "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]any{"registered": true})
}

// HandlePasskeys lists the signed-in user's passkeys.
func (r *SignInRoutes) HandlePasskeys(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tok := r.cfg.SessionToken(req)
	info, err := r.cfg.Client.UserInfo(ctx, tok)
	if tok == "" || err != nil {
		writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		return
	}
	keys, err := r.cfg.Client.ListPasskeys(ctx, info.Subject)
	if err != nil {
		r.unavailable(ctx, w, "list passkeys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": keys})
}

// ── Magic link ─────────────────────────────────────────────────────────

type emailRequest struct {
	Email string `json:"email"`
}

func (r *SignInRoutes) magicTemplate() string { return r.cfg.AppBase + "/auth/magic?code={{.Code}}" }

func (r *SignInRoutes) verifyTemplate() string {
	return r.cfg.AppBase + "/auth/verify?userID={{.UserID}}&code={{.Code}}"
}

// HandleMagicRequest mails a sign-in link and binds it to THIS browser.
//
// Always 202 with one body, one binding cookie, and one minimum response
// time: known, unknown, unverified, rate-limited and refused addresses are
// indistinguishable. The cookie is the same length for every kind of binding
// because sealing pads it (seal.go); the floor covers the extra issuer calls a
// real account costs (DefaultResponseFloor). A verified account gets the issuer's one-time-code
// session; an unverified one (or a new one, where sign-up allows the address)
// gets an email-verification code instead, because the issuer refuses the
// one-time-code method for an unverified address. Both links have one shape.
func (r *SignInRoutes) HandleMagicRequest(w http.ResponseWriter, req *http.Request) {
	var body emailRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	email := normalizeEmail(body.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeErr(w, http.StatusBadRequest, "", "Enter your email address.")
		return
	}
	defer holdUntil(ctx, time.Now(), r.cfg.ResponseFloor)
	binding := magicBinding{Kind: "n"}
	if !r.magicByIP.reserve(r.ip(req)) || !r.magicByAddress.reserve(email) {
		r.cfg.Logger.WarnContext(ctx, "magic link refused: rate limited")
	} else {
		binding = r.startMagic(ctx, email)
	}
	binding.Attempts = r.magicAttemptKey(email)
	if err := r.setBinding(w, magicCookie, binding); err != nil {
		r.cfg.Logger.ErrorContext(ctx, "magic link: seal", "error", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": msgMagicSent})
}

// magicAttemptKey is the consume-attempt budget of a magic link for an
// address: an HMAC of the address under the decoy secret, so it is the same
// for every outcome (verified, unverified, unknown, rate-limited) and every
// link requested for that address, and it names no account. Charging by
// address rather than by link also keeps a fresh link from buying fresh
// guesses at an unverified account's verification code.
func (r *SignInRoutes) magicAttemptKey(email string) string {
	m := hmac.New(sha256.New, r.decoyKey())
	m.Write([]byte("magic-link attempts\x00" + email))
	return "m:" + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (r *SignInRoutes) startMagic(ctx context.Context, email string) magicBinding {
	c := r.cfg.Client
	user, err := c.FindUserByEmail(ctx, email)
	switch {
	case err == nil && user.EmailVerified:
		return r.magicForVerified(ctx, user.ID)
	case err == nil:
		if err := c.ResendEmailCode(ctx, user.ID, r.magicTemplate()); err != nil {
			r.cfg.Logger.ErrorContext(ctx, "magic link: could not mail a verification code", "user_id", user.ID, "error", err)
			return magicBinding{Kind: "n"}
		}
		return magicBinding{Kind: "v", UserID: user.ID}
	case errors.Is(err, ErrUserNotFound) && r.cfg.Policy.SignupAllows(email):
		id, err := c.CreateUser(WithAuthMethod(ctx, observe.AuthMethodMagicLink), NewUser{Email: email, VerifyURLTemplate: r.magicTemplate(), SelfSignup: true})
		if err != nil {
			r.cfg.Logger.ErrorContext(ctx, "magic link: could not create the account", "error", err)
			return magicBinding{Kind: "n"}
		}
		r.cfg.Logger.InfoContext(ctx, "account created (unverified) for a magic link", "user_id", id)
		return magicBinding{Kind: "v", UserID: id}
	case errors.Is(err, ErrUserNotFound):
		r.cfg.Logger.InfoContext(ctx, "magic link requested for an address with no single account")
	default:
		r.cfg.Logger.ErrorContext(ctx, "magic link: user lookup failed", "error", err)
	}
	return magicBinding{Kind: "n"}
}

func (r *SignInRoutes) magicForVerified(ctx context.Context, userID string) magicBinding {
	c := r.cfg.Client
	if err := c.EnsureOTPEmail(ctx, userID); err != nil {
		r.cfg.Logger.ErrorContext(ctx, "magic link: could not enable the one-time-code method", "user_id", userID, "error", err)
		return magicBinding{Kind: "n"}
	}
	cs, err := c.CreateChallengeSession(ctx, "", userID, Challenge{OTPEmailURLTemplate: r.magicTemplate()})
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "magic link: the issuer refused the code", "user_id", userID, "error", err)
		return magicBinding{Kind: "n"}
	}
	r.cfg.Logger.InfoContext(ctx, "magic link requested", "user_id", userID)
	return magicBinding{Kind: "s", ID: cs.ID, Token: cs.Token}
}

type codeRequest struct {
	Code string `json:"code"`
}

// HandleMagicConsume redeems the code from a magic link (or typed from the
// email) in the browser that asked for it. Without that browser's binding
// cookie it refuses BEFORE touching the issuer, so a link scanner or a
// forwarded link burns nothing.
func (r *SignInRoutes) HandleMagicConsume(w http.ResponseWriter, req *http.Request) {
	var body codeRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	var b magicBinding
	if !r.readBinding(req, magicCookie, &b) {
		writeErr(w, http.StatusBadRequest, CodeMagicOtherBrowser, msgOtherBrowser)
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, CodeInvalidCode, msgInvalidCode)
		return
	}
	// Every binding, a decoy included, is charged to its address's budget
	// BEFORE its kind is looked at, so exhausting it answers 429 alike.
	key := b.Attempts
	if key == "" {
		// A link requested before Attempts existed; its life is minutes.
		key = b.ID + b.UserID
		if b.Kind == "n" {
			writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
			return
		}
	}
	if !r.codeByIP.reserve(r.ip(req)) || !r.magicByCode.reserve(key) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	c := r.cfg.Client
	var session Session
	var err error
	switch b.Kind {
	case "s":
		session, err = c.SetSession(ctx, ChallengeSession{ID: b.ID, Token: b.Token}, Answer{OTPEmailCode: code})
	case "v":
		session, err = r.sessionFromVerification(ctx, b.UserID, code)
	default:
		err = ErrInvalidCode
	}
	if errors.Is(err, ErrInvalidCode) {
		writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
		return
	}
	if err != nil {
		r.unavailable(ctx, w, "magic link could not be redeemed", err)
		return
	}
	r.magicByCode.forget(key)
	r.clearBinding(w, magicCookie)
	r.complete(w, req, session, func(tokens Tokens) { writeJSON(w, http.StatusOK, r.cfg.SignedIn(w, tokens)) })
}

// sessionFromVerification turns a proven email-verification code into a
// session. PROOF FIRST: VerifyEmail must succeed for this user in this
// request. Then any credential the account held from before the proof is
// revoked (proveAddress): nothing proves who set it. Only then does the broker
// mint a one-time code with its own credential and redeem it at once — the
// address is now verified, so the issuer allows the method, and the person
// already proved the inbox.
//
// The signup_role follows the address's first proof, as in HandleVerify:
// it is read from the account (MetaSelfSignup), not carried in the browser
// binding, so a replacement link (a second request before the first was
// used) grants it too.
func (r *SignInRoutes) sessionFromVerification(ctx context.Context, userID, code string) (Session, error) {
	c := r.cfg.Client
	before, err := c.FindUserByID(ctx, userID)
	if err != nil {
		return Session{}, err
	}
	if _, err := r.proveAddress(ctx, before, func() error { return c.VerifyEmail(ctx, userID, code) }); err != nil {
		return Session{}, err
	}
	r.grantSignupRoleIfSelfSignup(ctx, userID, before.Email)
	if err := c.EnsureOTPEmail(ctx, userID); err != nil {
		return Session{}, err
	}
	cs, err := c.CreateChallengeSession(ctx, "", userID, Challenge{OTPEmailReturnCode: true})
	if err != nil {
		return Session{}, err
	}
	return c.SetSession(ctx, cs, Answer{OTPEmailCode: cs.OTPEmailCode})
}

func (r *SignInRoutes) grantSignupRole(ctx context.Context, userID string) bool {
	role := r.cfg.Policy.SignupRole
	if role == "" || r.cfg.ProjectID == "" {
		return false
	}
	if err := r.cfg.Client.GrantProjectRole(ctx, userID, r.cfg.ProjectID, role); err != nil {
		r.cfg.Logger.ErrorContext(ctx, "could not grant the sign-up role", "user_id", userID, "role", role, "error", err)
		return false
	}
	r.cfg.Logger.InfoContext(ctx, "sign-up role granted", "user_id", userID, "role", role)
	return true
}

// ── Social ─────────────────────────────────────────────────────────────

type socialStartRequest struct {
	Provider string `json:"provider"`
	ReturnTo string `json:"returnTo,omitempty"`
}

// HandleSocialStart begins a social sign-in. The intent id goes into a
// binding cookie; the callback requires it, which defeats login CSRF.
func (r *SignInRoutes) HandleSocialStart(w http.ResponseWriter, req *http.Request) {
	var body socialStartRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	social, ok := r.cfg.Policy.SocialByType(strings.TrimSpace(body.Provider))
	if !ok {
		writeErr(w, http.StatusNotFound, CodeUnknownProvider, msgMethod)
		return
	}
	if !r.socialStartByIP.reserve(r.ip(req)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	authURL, intentID, err := r.cfg.Client.StartIDPIntent(ctx, social.IdPID,
		r.cfg.AppBase+"/auth/social/callback", r.cfg.AppBase+"/auth/sign-in?error=social")
	if err != nil {
		r.unavailable(ctx, w, "social sign-in could not start", err)
		return
	}
	if err := r.setBinding(w, socialCookie, socialBinding{IntentID: intentID, ReturnTo: SafeReturnTo(body.ReturnTo)}); err != nil {
		r.unavailable(ctx, w, "social sign-in: seal", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": authURL})
}

// SafeReturnTo keeps a same-origin path and turns anything else into "/".
//
// It refuses what a browser would read as another site: an absolute or
// scheme-relative URL, a backslash (browsers treat `/\host` as `//host`), any
// control character (tabs and newlines are stripped by the URL parser, so
// `/<tab>/host` is `//host`), and a path that RESOLVES to one starting with
// `//` (`/.//host`, `/a/..//host`, `/%2e//host`).
func SafeReturnTo(p string) string {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\") {
		return "/"
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	// Conservative on the path: browsers decode %2e as a dot in a dot
	// segment and collapse segments Go's parser would keep, so any empty
	// segment ("//") or dot segment is refused rather than resolved.
	path := strings.NewReplacer("%2e", ".", "%2E", ".").Replace(u.EscapedPath())
	if strings.Contains(path, "//") {
		return "/"
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return "/"
		}
	}
	return p
}

type socialCallbackRequest struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// HandleSocialCallback completes a social sign-in. The frontend's
// /auth/social/callback page posts the id and token the issuer appended.
//
// A provider identity already linked signs in. Otherwise:
//   - an account with the provider's address exists: link ONLY when the
//     provider asserts the address verified AND the account's is verified —
//     an unverified auto-link is an account takeover;
//   - no account: create one where sign-up allows the address (domain mode
//     needs a provider-verified one), else refuse.
func (r *SignInRoutes) HandleSocialCallback(w http.ResponseWriter, req *http.Request) {
	var body socialCallbackRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	var b socialBinding
	if !r.readBinding(req, socialCookie, &b) || b.IntentID == "" || b.IntentID != strings.TrimSpace(body.ID) {
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	r.clearBinding(w, socialCookie)
	c := r.cfg.Client
	res, err := c.RetrieveIDPIntent(ctx, b.IntentID, body.Token)
	if err != nil {
		r.cfg.Logger.WarnContext(ctx, "social sign-in: intent not retrieved", "error", err)
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	social, ok := r.cfg.Policy.SocialByIdPID(res.Link.IdPID)
	if !ok {
		r.cfg.Logger.WarnContext(ctx, "social sign-in: intent for an undeclared provider", "idp_id", res.Link.IdPID)
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	intent := &IDPIntent{ID: b.IntentID, Token: strings.TrimSpace(body.Token)}
	// The sign-in and any account it creates are recorded as social, through
	// this provider (auth_events.go).
	ctx = withSocialMethod(ctx, social.Type)
	req = req.WithContext(ctx)
	signIn := func(userID string) {
		s, err := c.CreateSession(ctx, Credentials{UserID: userID, IDPIntent: intent})
		if err != nil {
			r.cfg.Logger.WarnContext(ctx, "social sign-in: session refused", "user_id", userID, "error", err)
			writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
			return
		}
		r.complete(w, req, s, func(tokens Tokens) {
			writeJSON(w, http.StatusOK, map[string]any{"identity": r.cfg.SignedIn(w, tokens), "returnTo": b.ReturnTo})
		})
	}
	if b.InviteUserID != "" {
		r.acceptInviteBySocial(w, req, b, res, social, signIn)
		return
	}
	if res.LinkedUserID != "" {
		signIn(res.LinkedUserID)
		return
	}
	email := normalizeEmail(res.Email)
	var existing User
	err = ErrUserNotFound
	if email != "" {
		existing, err = c.FindUserByEmail(ctx, email)
	}
	switch {
	case err == nil:
		if !res.EmailVerified || !existing.EmailVerified {
			r.cfg.Logger.InfoContext(ctx, "social sign-in: not linking an unverified address", "user_id", existing.ID,
				"provider_verified", res.EmailVerified, "account_verified", existing.EmailVerified)
			writeErr(w, http.StatusConflict, CodeSocialLinkRequired, fmt.Sprintf(msgSocialLink, social.Label))
			return
		}
		if err := c.AddIDPLink(ctx, existing.ID, res.Link); err != nil {
			r.unavailable(ctx, w, "social sign-in: link", err)
			return
		}
		r.cfg.Logger.InfoContext(ctx, "social account linked", "user_id", existing.ID, "provider", social.Type)
		signIn(existing.ID)
	case !errors.Is(err, ErrUserNotFound):
		r.unavailable(ctx, w, "social sign-in: user lookup", err)
	case !r.cfg.Policy.SelfSignup() || email == "":
		writeErr(w, http.StatusForbidden, CodeSocialNoAccount, msgSocialNoAcct)
	case !r.cfg.Policy.SignupAllows(email) || (r.cfg.Policy.Signup == SignupDomain && !res.EmailVerified):
		writeErr(w, http.StatusForbidden, CodeSignupNotAllowed, msgSignupDomain)
	default:
		// The provider account is linked at creation only when the provider
		// asserted the address. Otherwise the account is created bare and
		// the link waits, sealed in THIS browser, for the address's code.
		nu := NewUser{Email: email, GivenName: res.GivenName, FamilyName: res.FamilyName,
			EmailVerified: res.EmailVerified, VerifyURLTemplate: r.verifyTemplate(), SelfSignup: true}
		if res.EmailVerified {
			nu.IDPLinks = []IDPLink{res.Link}
		}
		id, err := c.CreateUser(ctx, nu)
		if err != nil {
			r.unavailable(ctx, w, "social sign-in: create account", err)
			return
		}
		r.cfg.Logger.InfoContext(ctx, "account created from a social sign-in", "user_id", id, "provider", social.Type, "verified", res.EmailVerified)
		if !res.EmailVerified {
			if err := r.setBinding(w, linkCookie, pendingLinkBinding{UserID: id, Link: res.Link}); err != nil {
				r.cfg.Logger.ErrorContext(ctx, "social sign-in: the pending link could not be sealed", "user_id", id, "error", err)
			}
			writeJSON(w, http.StatusAccepted, map[string]string{"verify": "sent", "message": msgVerifySent})
			return
		}
		r.grantSignupRoleIfSelfSignup(ctx, id, email)
		signIn(id)
	}
}

// acceptInviteBySocial finishes a social invitation acceptance. PROOF FIRST:
// the invitation code sealed at the start is checked HERE, in the request
// that links the provider account, for the user the invitation names. A
// provider account already linked to someone else is refused before the
// invitation is spent.
func (r *SignInRoutes) acceptInviteBySocial(w http.ResponseWriter, req *http.Request, b socialBinding, res IDPResult,
	social LoginPolicySocial, signIn func(string)) {
	ctx := req.Context()
	c := r.cfg.Client
	userID := b.InviteUserID
	if res.LinkedUserID != "" && res.LinkedUserID != userID {
		r.cfg.Logger.WarnContext(ctx, "invitation: the provider account belongs to another user", "user_id", userID, "provider", social.Type)
		writeErr(w, http.StatusConflict, CodeSocialTaken, fmt.Sprintf(msgSocialTaken, social.Label))
		return
	}
	if !r.codeByIP.reserve(r.ip(req)) || !r.codeByUser.reserve(userID) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	before, err := c.FindUserByID(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
		return
	}
	if err != nil {
		r.codeByUser.refund(userID)
		r.unavailable(ctx, w, "invitation: read account", err)
		return
	}
	if _, err := r.proveAddress(ctx, before, func() error { return c.VerifyInviteCode(ctx, userID, b.InviteCode) }); err != nil {
		if errors.Is(err, ErrInvalidCode) {
			r.cfg.Logger.WarnContext(ctx, "invitation rejected", "user_id", userID, "error", err)
			writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
			return
		}
		if !errors.Is(err, errRevokeFailed) {
			r.codeByUser.refund(userID)
		}
		r.unavailable(ctx, w, "invitation could not be checked", err)
		return
	}
	r.codeByUser.forget(userID)
	if res.LinkedUserID == "" {
		if err := c.AddIDPLink(ctx, userID, res.Link); err != nil {
			r.unavailable(ctx, w, "invitation: link the provider account", err)
			return
		}
	}
	r.cfg.Logger.InfoContext(ctx, "invitation accepted", "user_id", userID, "method", InviteMethodSocial, "provider", social.Type)
	reportInviteAccepted(ctx, userID)
	signIn(userID)
}

// ── Invitations ────────────────────────────────────────────────────────

type inviteRequest struct {
	UserID   string `json:"userID"`
	Code     string `json:"code"`
	OrgID    string `json:"orgID,omitempty"`
	Method   string `json:"method"`
	Password string `json:"password,omitempty"`
	// Provider is the social button's type, for method "social".
	Provider string `json:"provider,omitempty"`
}

// HandleInviteAccept redeems an invitation and sets the invitee up with the
// method they chose. NOTHING after the invitation check runs unless it
// succeeded for this user in this request.
func (r *SignInRoutes) HandleInviteAccept(w http.ResponseWriter, req *http.Request) {
	var body inviteRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	userID, code := strings.TrimSpace(body.UserID), strings.TrimSpace(body.Code)
	if userID == "" || code == "" {
		writeErr(w, http.StatusBadRequest, CodeInvalidCode, msgInvalidCode)
		return
	}
	if body.Method == InviteMethodSocial {
		r.startInviteSocial(w, req, userID, code, body.Provider)
		return
	}
	if !r.cfg.Policy.Offers(body.Method) {
		writeErr(w, http.StatusBadRequest, CodeMethodNotOffered, msgMethod)
		return
	}
	if body.Method == MethodPassword {
		if body.Password == "" {
			writeErr(w, http.StatusBadRequest, CodePasswordRequired, "Choose a password.")
			return
		}
		// Checked BEFORE the single-use invitation is spent, so a weak
		// password does not strand the invitee.
		if r.rejectWeak(ctx, w, body.Password) {
			return
		}
	}
	if !r.codeByIP.reserve(r.ip(req)) || !r.codeByUser.reserve(userID) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	c := r.cfg.Client
	before, err := c.FindUserByID(ctx, userID)
	if err != nil {
		// No such account answers like a wrong code.
		if errors.Is(err, ErrUserNotFound) {
			writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
			return
		}
		r.codeByUser.refund(userID)
		r.unavailable(ctx, w, "invitation: read account", err)
		return
	}
	// Someone may have registered this address before the invitation: what
	// they set must not survive the invitee's proof (proveAddress).
	if _, err := r.proveAddress(ctx, before, func() error { return c.VerifyInviteCode(ctx, userID, code) }); err != nil {
		if errors.Is(err, ErrInvalidCode) {
			r.cfg.Logger.WarnContext(ctx, "invitation rejected", "user_id", userID, "error", err)
			writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
			return
		}
		if !errors.Is(err, errRevokeFailed) {
			r.codeByUser.refund(userID)
		}
		r.unavailable(ctx, w, "invitation could not be checked", err)
		return
	}
	r.codeByUser.forget(userID)
	r.cfg.Logger.InfoContext(ctx, "invitation accepted", "user_id", userID, "method", body.Method)
	reportInviteAccepted(ctx, userID)

	switch body.Method {
	case MethodPassword:
		r.setPasswordAndSignIn(w, req, userID, body.Password)
	case MethodPasskey:
		r.enroll(w, req, userID)
	case MethodMagicLink:
		b := r.magicForVerified(ctx, userID)
		if b.Kind != "s" {
			r.unavailable(ctx, w, "invitation: magic link", fmt.Errorf("the issuer refused the one-time code"))
			return
		}
		if err := r.setBinding(w, magicCookie, b); err != nil {
			r.unavailable(ctx, w, "invitation: seal", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"message": "A sign-in link is on its way to your email."})
	}
}

// startInviteSocial begins accepting an invitation by connecting a social
// account — the only way in for an invite-only policy that offers social
// sign-in alone. The invitation is NOT checked here: its code is sealed into
// the social binding cookie and checked at the callback, in the request that
// links the account (acceptInviteBySocial).
func (r *SignInRoutes) startInviteSocial(w http.ResponseWriter, req *http.Request, userID, code, provider string) {
	ctx := req.Context()
	social, ok := r.cfg.Policy.SocialByType(strings.TrimSpace(provider))
	if !ok {
		writeErr(w, http.StatusBadRequest, CodeMethodNotOffered, msgMethod)
		return
	}
	if !r.socialStartByIP.reserve(r.ip(req)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	back := url.Values{"userID": {userID}, "code": {code}, "error": {"social"}}
	authURL, intentID, err := r.cfg.Client.StartIDPIntent(ctx, social.IdPID,
		r.cfg.AppBase+"/auth/social/callback", r.cfg.AppBase+"/auth/invite?"+back.Encode())
	if err != nil {
		r.unavailable(ctx, w, "invitation: social sign-in could not start", err)
		return
	}
	if err := r.setBinding(w, socialCookie, socialBinding{IntentID: intentID, ReturnTo: "/", InviteUserID: userID, InviteCode: code}); err != nil {
		r.unavailable(ctx, w, "invitation: seal", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": authURL})
}

// setPasswordAndSignIn sets an invitee's first password: the broker mints a
// reset code with its own credential and redeems it at once — allowed only
// because the invitation was proven in this request — then signs in.
func (r *SignInRoutes) setPasswordAndSignIn(w http.ResponseWriter, req *http.Request, userID, password string) {
	ctx := req.Context()
	c := r.cfg.Client
	reset, err := c.MintPasswordResetCode(ctx, userID)
	if err == nil {
		err = c.ResetPassword(ctx, PasswordReset{UserID: userID, Code: reset, NewPassword: password})
	}
	var policyErr *PasswordPolicyError
	if errors.As(err, &policyErr) {
		r.rejectPassword(ctx, w, password, policyErr)
		return
	}
	if err != nil {
		r.unavailable(ctx, w, "invitation: set password", err)
		return
	}
	user, err := c.FindUserByID(ctx, userID)
	if err != nil {
		r.unavailable(ctx, w, "invitation: read account", err)
		return
	}
	tokens, err := c.SignIn(ctx, r.cfg.Flow, Credentials{LoginName: user.LoginName, Password: password})
	if errors.Is(err, ErrEmailUnverified) {
		writeErr(w, http.StatusForbidden, CodeEmailUnverified, msgUnverified)
		return
	}
	if err != nil {
		r.unavailable(ctx, w, "invitation: sign in", err)
		return
	}
	writeJSON(w, http.StatusOK, r.cfg.SignedIn(w, tokens))
}

// enroll lets a user who just proved an emailed code register a passkey: a
// registration code is minted and sealed into an enrolment cookie.
func (r *SignInRoutes) enroll(w http.ResponseWriter, req *http.Request, userID string) {
	ctx := req.Context()
	code, err := r.cfg.Client.MintPasskeyRegistrationCode(ctx, userID)
	if err != nil {
		r.unavailable(ctx, w, "passkey enrolment", err)
		return
	}
	if err := r.setBinding(w, enrollCookie, enrollBinding{UserID: userID, CodeID: code.ID, Code: code.Code}); err != nil {
		r.unavailable(ctx, w, "passkey enrolment: seal", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enroll": true})
}

// ── Verification ───────────────────────────────────────────────────────

type verifyRequest struct {
	UserID string `json:"userID"`
	Code   string `json:"code"`
}

// HandleVerify redeems an email-verification code. A self-signed-up account
// gets its signup_role now, never before. An account with no password, where
// passkeys are offered, may enrol one next (enroll: true).
func (r *SignInRoutes) HandleVerify(w http.ResponseWriter, req *http.Request) {
	var body verifyRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	userID, code := strings.TrimSpace(body.UserID), strings.TrimSpace(body.Code)
	if userID == "" || code == "" {
		writeErr(w, http.StatusBadRequest, CodeInvalidCode, msgInvalidCode)
		return
	}
	if !r.codeByIP.reserve(r.ip(req)) || !r.codeByUser.reserve(userID) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	c := r.cfg.Client
	before, err := c.FindUserByID(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
		return
	}
	if err != nil {
		r.codeByUser.refund(userID)
		r.unavailable(ctx, w, "verification: read account", err)
		return
	}
	// Order matters: the proof takes back whatever the account held from
	// before it (nothing proves who set it) with the account locked
	// throughout (proveAddress), THEN this browser's pending social link and
	// password are attached.
	reset, err := r.proveAddress(ctx, before, func() error { return c.VerifyEmail(ctx, userID, code) })
	if err != nil {
		if errors.Is(err, ErrInvalidCode) {
			writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
			return
		}
		if !errors.Is(err, errRevokeFailed) {
			r.codeByUser.refund(userID)
		}
		r.unavailable(ctx, w, "verification could not be completed", err)
		return
	}
	r.codeByUser.forget(userID)
	r.cfg.Logger.InfoContext(ctx, "email address verified", "user_id", userID)
	r.attachPendingLink(w, req, userID)
	pw := r.setPendingPassword(w, req, userID)
	r.grantSignupRoleIfSelfSignup(ctx, userID, before.Email)
	if pw.set {
		reset = false
	}
	if pw.failed {
		// The address is proven, but the password the sign-up chose was not
		// set: say so rather than "you can sign in now" to an account with
		// no credential. The continuation is a reset code minted for THIS
		// browser, which just proved the address, so the person chooses a
		// password again on the reset form; the chosen one is not kept.
		body := map[string]any{"verified": true, "enroll": false, "password": passwordNotSet, "credentialsReset": reset}
		if pw.reset != nil {
			body["reset"] = pw.reset
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	if r.cfg.Policy.Offers(MethodPasskey) {
		if has, err := c.HasPassword(ctx, userID); err == nil && (reset || !has) {
			code, err := c.MintPasskeyRegistrationCode(ctx, userID)
			if err == nil {
				err = r.setBinding(w, enrollCookie, enrollBinding{UserID: userID, CodeID: code.ID, Code: code.Code})
			}
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"verified": true, "enroll": true, "credentialsReset": reset})
				return
			}
			r.cfg.Logger.ErrorContext(ctx, "verification: passkey enrolment unavailable", "user_id", userID, "error", err)
		}
	}
	// credentialsReset: what was set before the proof was not this
	// browser's and is gone; the page offers "Forgot password" to set one.
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "enroll": false, "credentialsReset": reset})
}

// attachPendingLink links the provider account a social sign-up left waiting,
// now that THIS browser proved the address of the account it created. A
// verification opened in any other browser links nothing.
func (r *SignInRoutes) attachPendingLink(w http.ResponseWriter, req *http.Request, userID string) {
	ctx := req.Context()
	var pl pendingLinkBinding
	if !r.readBinding(req, linkCookie, &pl) || pl.UserID != userID {
		return
	}
	r.clearBinding(w, linkCookie)
	if err := r.cfg.Client.AddIDPLink(ctx, userID, pl.Link); err != nil {
		r.cfg.Logger.ErrorContext(ctx, "verification: the pending social link could not be added", "user_id", userID, "error", err)
		return
	}
	r.cfg.Logger.InfoContext(ctx, "social account linked after verification", "user_id", userID)
}

// passwordNotSet is HandleVerify's "password" field when the address was
// proven but the password its sign-up chose could not be set.
const passwordNotSet = "not_set"

// passwordContinuation is a reset code for a proven account whose sign-up
// password could not be set: the browser redeems it at the reset route with
// a password chosen again.
type passwordContinuation struct {
	UserID string `json:"userID"`
	Code   string `json:"code"`
}

// pendingPasswordOutcome is what setPendingPassword did. failed: a pending
// password existed for this user and was not set; reset, when not nil, is the
// way to set one now.
type pendingPasswordOutcome struct {
	set    bool
	failed bool
	reset  *passwordContinuation
}

// setPendingPassword sets the password a sign-up chose, now that THIS
// browser proved the address of the account it registered. PROOF FIRST: the
// broker mints the reset code with its own credential only after VerifyEmail
// succeeded for this user in this request. A verification opened in any
// other browser sets nothing: the account then has no password until its
// owner sets one (a passkey, a magic link, or a reset).
//
// When the issuer refuses the password (a rule rejectWeak could not see) or
// fails, the chosen password is dropped and a FRESH reset code is minted for
// this same proven browser, so the person can choose another at once. If even
// that cannot be minted, failed alone tells them to use "forgot password".
func (r *SignInRoutes) setPendingPassword(w http.ResponseWriter, req *http.Request, userID string) pendingPasswordOutcome {
	ctx := req.Context()
	var pp pendingPasswordBinding
	if !r.readBinding(req, passwordCookie, &pp) || pp.UserID != userID || len(pp.Password) == 0 {
		return pendingPasswordOutcome{}
	}
	r.clearBinding(w, passwordCookie)
	c := r.cfg.Client
	code, err := c.MintPasswordResetCode(ctx, userID)
	if err == nil {
		err = c.ResetPassword(ctx, PasswordReset{UserID: userID, Code: code, NewPassword: string(pp.Password)})
	}
	if err == nil {
		r.cfg.Logger.InfoContext(ctx, "sign-up password set after verification", "user_id", userID)
		return pendingPasswordOutcome{set: true}
	}
	r.cfg.Logger.ErrorContext(ctx, "verification: the sign-up password could not be set", "user_id", userID, "error", err)
	out := pendingPasswordOutcome{failed: true}
	next, err := c.MintPasswordResetCode(ctx, userID)
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "verification: no reset code for choosing the password again", "user_id", userID, "error", err)
		return out
	}
	out.reset = &passwordContinuation{UserID: userID, Code: next}
	return out
}

// HandleVerifyResend mails a fresh verification link. Always 202.
func (r *SignInRoutes) HandleVerifyResend(w http.ResponseWriter, req *http.Request) {
	var body emailRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	// A pending address costs a resend the others do not: hide it.
	defer holdUntil(ctx, time.Now(), r.cfg.ResponseFloor)
	email := normalizeEmail(body.Email)
	if email != "" && r.verifyResendIP.reserve(r.ip(req)) && r.verifyResendBy.reserve(email) {
		if u, err := r.cfg.Client.FindUserByEmail(ctx, email); err == nil && !u.EmailVerified {
			if err := r.cfg.Client.ResendEmailCode(ctx, u.ID, r.verifyTemplate()); err != nil {
				r.cfg.Logger.ErrorContext(ctx, "verification resend failed", "user_id", u.ID, "error", err)
			}
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": msgVerifyResent})
}

// ── Sign-up ────────────────────────────────────────────────────────────

type registerRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// HandleRegister creates an account under an open or domain sign-up. It
// answers 202 with NO session, identically whether or not the address
// already has an account: the verification mail is what proves the address,
// and the session comes after it. A chosen password is set only when the
// browser that registered proves the address (setPendingPassword).
//
// Where no mail can arrive, the policy decides (RegisterServed): sign-up is
// refused unless it is open, with a password, and the policy does not
// require a verified address — then registerWithoutMail signs in at once.
func (r *SignInRoutes) HandleRegister(w http.ResponseWriter, req *http.Request) {
	var body registerRequest
	if !r.admit(w, req) || !decode(w, req, &body) {
		return
	}
	ctx := req.Context()
	email := normalizeEmail(body.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeErr(w, http.StatusBadRequest, "", "Enter your email address.")
		return
	}
	p := r.cfg.Policy
	if !p.RegisterServed() {
		writeErr(w, http.StatusForbidden, CodeSignupNotAllowed, "Accounts cannot be created here.")
		return
	}
	if !p.SignupAllows(email) {
		writeErr(w, http.StatusForbidden, CodeSignupNotAllowed, msgSignupDomain)
		return
	}
	// Without mail, a password is the only credential a new account can
	// have: passkey enrolment and magic links both follow a mailed code.
	passwordless := p.MailDelivers() && (p.Offers(MethodPasskey) || p.Offers(MethodMagicLink))
	switch {
	case body.Password == "" && !passwordless:
		writeErr(w, http.StatusBadRequest, CodePasswordRequired, "Choose a password.")
		return
	case body.Password != "" && !p.Offers(MethodPassword):
		writeErr(w, http.StatusBadRequest, CodeMethodNotOffered, msgMethod)
		return
	}
	if len(body.Password) > maxPasswordBytes {
		writeErr(w, http.StatusBadRequest, CodePasswordTooLong, "That password is too long.")
		return
	}
	// The password is judged BEFORE the address is looked up, so a weak
	// password is refused for every address alike.
	if body.Password != "" && r.rejectWeak(ctx, w, body.Password) {
		return
	}
	// And it must fit the sealed binding it will wait in — checked with a
	// stand-in id, before the lookup, so "too long" is one answer for every
	// address rather than depending on which account it has.
	if body.Password != "" {
		_, err := r.seal.seal(passwordCookie.suffix, pendingPasswordBinding{UserID: placeholderUserID, Password: []byte(body.Password)}, passwordCookie.ttl)
		switch {
		case errors.Is(err, errPayloadTooLarge):
			writeErr(w, http.StatusBadRequest, CodePasswordTooLong, "That password is too long.")
			return
		case err != nil:
			r.unavailable(ctx, w, "sign-up: the password cannot be sealed", err)
			return
		}
	}
	defer holdUntil(ctx, time.Now(), r.cfg.ResponseFloor)
	if !r.registerByIP.reserve(r.ip(req)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	if !p.MailDelivers() {
		r.registerWithoutMail(w, req, email, body)
		return
	}
	// The account is created WITHOUT the password: it waits, sealed in THIS
	// browser, for the address's code (setPendingPassword). Set at creation,
	// whoever registered a victim's address would keep a way in once the
	// victim confirmed it.
	c := r.cfg.Client
	pending := pendingPasswordBinding{Password: []byte(body.Password)}
	user, err := c.FindUserByEmail(ctx, email)
	switch {
	case err == nil:
		// NEVER touch an existing account here. This registrant's password
		// waits, sealed in their own browser, for their proof; whatever the
		// account held from before is revoked then (proveAddress).
		if !user.EmailVerified {
			pending.UserID = user.ID
			if err := c.ResendEmailCode(ctx, user.ID, r.verifyTemplate()); err != nil {
				r.cfg.Logger.ErrorContext(ctx, "sign-up: resend for an existing account failed", "user_id", user.ID, "error", err)
			}
		}
		r.cfg.Logger.InfoContext(ctx, "sign-up for an address that already has an account", "user_id", user.ID)
	case errors.Is(err, ErrUserNotFound):
		id, err := c.CreateUser(WithAuthMethod(ctx, observe.AuthMethodPassword), NewUser{Email: email, GivenName: body.GivenName,
			FamilyName: body.FamilyName, VerifyURLTemplate: r.verifyTemplate(), SelfSignup: true})
		if err != nil {
			r.unavailable(ctx, w, "sign-up: create account", err)
			return
		}
		pending.UserID = id
		r.cfg.Logger.InfoContext(ctx, "account created (unverified)", "user_id", id)
	default:
		r.unavailable(ctx, w, "sign-up: user lookup", err)
		return
	}
	if body.Password != "" {
		// Never "check your email" without the password the person chose:
		// they would confirm the address and have no way in.
		if err := r.setBinding(w, passwordCookie, pending); err != nil {
			r.unavailable(ctx, w, "sign-up: the password could not be sealed", err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"verify": "sent", "message": msgVerifySent})
}

// registerWithoutMail is the sign-up of a policy whose mail reaches nobody
// and which therefore does not require a verified address (mail "none"):
// open sign-up, with a password. No mail means no "check your email" answer
// can be uniform, so this is the legacy broker's sign-up: the account is
// created and signed in at once, and an address that already has an account
// is told to sign in. The address stays unverified, as the policy accepts.
func (r *SignInRoutes) registerWithoutMail(w http.ResponseWriter, req *http.Request, email string, body registerRequest) {
	ctx := req.Context()
	c := r.cfg.Client
	if _, err := c.FindUserByEmail(ctx, email); err == nil {
		writeErr(w, http.StatusConflict, CodeAccountExists, msgAccountExists)
		return
	} else if !errors.Is(err, ErrUserNotFound) {
		r.unavailable(ctx, w, "sign-up: user lookup", err)
		return
	}
	id, err := c.CreateUser(ctx, NewUser{Email: email, Password: body.Password, GivenName: body.GivenName,
		FamilyName: body.FamilyName})
	var policyErr *PasswordPolicyError
	if errors.As(err, &policyErr) {
		r.rejectPassword(ctx, w, body.Password, policyErr)
		return
	}
	if err != nil {
		r.unavailable(ctx, w, "sign-up: create account", err)
		return
	}
	r.cfg.Logger.InfoContext(ctx, "account created (no mail delivery; signed in unverified)", "user_id", id)
	reportSignUp(ctx, id, observe.AuthMethodPassword, false)
	r.grantSignupRole(ctx, id)
	tokens, err := c.SignIn(ctx, r.cfg.Flow, Credentials{LoginName: email, Password: body.Password})
	if err != nil {
		r.unavailable(ctx, w, "sign-up: sign in", err)
		return
	}
	writeJSON(w, http.StatusOK, r.cfg.SignedIn(w, tokens))
}

// rejectWeak answers (and reports true) when password breaks the known
// policy. No policy known: false, and the issuer judges it.
func (r *SignInRoutes) rejectWeak(ctx context.Context, w http.ResponseWriter, password string) bool {
	if r.cfg.PasswordPolicy == nil {
		return false
	}
	policy, err := r.cfg.PasswordPolicy(ctx)
	if err != nil {
		return false
	}
	unmet := policy.Unmet(password)
	if len(unmet) == 0 {
		return false
	}
	r.rejectPassword(ctx, w, password, &PasswordPolicyError{Rule: unmet[0], Reason: policy.Requirement(unmet[0])})
	return true
}

func (r *SignInRoutes) rejectPassword(ctx context.Context, w http.ResponseWriter, password string, err *PasswordPolicyError) {
	if r.cfg.RejectPassword != nil {
		r.cfg.RejectPassword(ctx, w, password, err)
		return
	}
	writeErr(w, http.StatusBadRequest, "password_policy", "That password was not accepted: "+err.Reason+".")
}

// ── Helpers ────────────────────────────────────────────────────────────

func (r *SignInRoutes) unavailable(ctx context.Context, w http.ResponseWriter, what string, err error) {
	r.cfg.Logger.ErrorContext(ctx, what, "error", err)
	writeErr(w, http.StatusBadGateway, "", msgUnavailable)
}

func decode(w http.ResponseWriter, req *http.Request, out any) bool {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type"))), "application/json") {
		// Only the scaffolded client's JSON: a form or text/plain body is
		// what a cross-site page can send without a preflight.
		writeErr(w, http.StatusUnsupportedMediaType, "", "malformed request")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 64<<10)).Decode(out); err != nil {
		writeErr(w, http.StatusBadRequest, "", "malformed request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	body := map[string]string{"error": message}
	if code != "" {
		body["code"] = code
	}
	writeJSON(w, status, body)
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
