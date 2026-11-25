package devidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
)

// AccountRoutes serve a signed-in person's account settings for an app's
// native login broker: who they are, changing their address and password,
// connecting and disconnecting social accounts, removing passkeys, and the
// list of places they are signed in.
//
// ── WHO MAY CALL THEM ─────────────────────────────────────────────────
//
// Only a browser holding a SERVER-SIDE session cookie (forge/pkg/session).
// The subject every route acts on is the session record's own subject, read
// from the store the broker wrote at sign-in — never a request body, and
// never a token's claims. A bearer token, an API key or a cookie that holds
// an access token instead of a session id is refused: anyone can put a
// credential in a header, and these routes change how an account is
// reached. SessionID, which the app supplies, reads the cookie and nothing
// else.
//
// ── RECENT PROOF ──────────────────────────────────────────────────────
//
// Every route that changes how the account is reached (its address, its
// password, its social links and passkeys) or signs other devices out needs
// the session to have PROVED a credential within ReauthWindow: at sign-in,
// or by ReauthPath. A stolen session past that window can read the account
// page but cannot take the account over. A refusal is 401 reauth_required,
// listing the ways to prove one again.
//
// ── THE LAST CREDENTIAL ───────────────────────────────────────────────
//
// Nothing removes the account's last way in: a social link or a passkey is
// removed only while a password, another passkey or another link remains.
type AccountRoutes struct {
	cfg   AccountRoutesConfig
	seal  *sealer
	guard *http.CrossOriginProtection

	reauthBySubject *limiter
	reauthByIP      *limiter
	emailBySubject  *limiter
	linkStartByIP   *limiter
}

// The account routes. The scaffolded broker mounts each with
// mux.Handle(<constant>, http.HandlerFunc(routes.Handle<Name>)).
const (
	AccountPath                     = "GET /auth/account"
	AccountEmailPath                = "POST /auth/account/email"
	AccountEmailConfirmPath         = "POST /auth/account/email/confirm"
	AccountPasswordPath             = "POST /auth/account/password"
	AccountLinkStartPath            = "POST /auth/account/links/start"
	AccountUnlinkPath               = "DELETE /auth/account/links/{provider}"
	AccountPasskeyRemovePath        = "DELETE /auth/account/passkeys/{id}"
	AccountSessionsPath             = "GET /auth/account/sessions"
	AccountSessionRevokePath        = "DELETE /auth/account/sessions/{handle}"
	AccountSessionsRevokeOthersPath = "POST /auth/account/sessions/revoke-others"
	ReauthPath                      = "POST /auth/reauth"
)

// Error codes the account routes answer with, beside the sign-in ones.
const (
	// CodeReauthRequired: the action needs a credential proved within the
	// reauth window. The body's "methods" lists how to prove one: "password"
	// (POST ReauthPath) and "sign_in" (sign out and in again, which any
	// account can do).
	CodeReauthRequired = "reauth_required"
	// CodeWrongPassword: a password presented as proof was not accepted.
	CodeWrongPassword = "wrong_password"
	// CodeLastCredential: removing this would leave the account no way in.
	CodeLastCredential = "last_credential"
	// CodeCurrentSession: the session named is the caller's own; sign out
	// instead.
	CodeCurrentSession = "current_session"
	// CodeAddressInUse: another account has that address.
	CodeAddressInUse = "address_in_use"
	// CodeNotLinked: the account has no such link or passkey.
	CodeNotLinked = "not_linked"
	// CodeMailUnavailable: this deployment cannot deliver the code an
	// address change needs.
	CodeMailUnavailable = "mail_unavailable"
	// CodeSameAddress: the address is the account's own already.
	CodeSameAddress = "same_address"
	// CodeInvalidAddress: the address is not one.
	CodeInvalidAddress = "invalid_address"
	// CodeSessionsNotRevoked: the password changed, but the other sessions
	// could not all be signed out; POST AccountSessionsRevokeOthersPath
	// again finishes it.
	CodeSessionsNotRevoked = "sessions_not_revoked"
)

// MetaEmailChangeFrom holds, while an address change waits for its code, the
// address it replaced (account metadata, cleared at confirmation).
const MetaEmailChangeFrom = "forge.email_change_from"

// UpstreamEventLink is the event of an UpstreamGrant handed when a signed-in
// person connected the provider from their account page.
const UpstreamEventLink = "link"

// DefaultReauthWindow is AccountRoutesConfig.ReauthWindow's default.
const DefaultReauthWindow = 10 * time.Minute

// AccountRoutesConfig is what the app's broker supplies.
type AccountRoutesConfig struct {
	Client *Client
	// Sessions is the app's server-side session store. Required: without
	// it there is no record to read a subject or a proof from.
	Sessions *session.Manager
	// SessionID returns the server-side session id the request's session
	// COOKIE holds, "" when it holds none (no cookie, or one holding an
	// access token). Read the cookie only: a header is not a browser
	// session. Required.
	SessionID func(*http.Request) string
	// Policy is the LOGIN_POLICY in force: which social providers can be
	// connected, and whether passwords are offered. The legacy policy
	// offers passwords and no social providers.
	Policy LoginPolicy
	// AppBase is the app's public origin plus base path, from
	// idp_login_uri: the trusted origin, and the prefix of the mailed
	// confirmation link and the social return URLs.
	AppBase      string
	CookiePrefix string
	SecureCookie bool
	Logger       *slog.Logger
	// TrustedProxyHops: see SignInRoutesConfig.
	TrustedProxyHops int
	// ReauthWindow is how recently a credential must have been proved for
	// the routes that need a recent proof. 0 is DefaultReauthWindow.
	ReauthWindow time.Duration
	// EmailConfirmURL is the link mailed to a new address, with the
	// issuer's {{.Code}} (and {{.UserID}}) placeholders. "" is
	// AppBase + "/auth/account/email/confirm?code={{.Code}}": the app's page
	// there posts the code to AccountEmailConfirmPath, signed in.
	EmailConfirmURL string
	// DevMode is a stack with no mail server: an address change's code is
	// minted, not mailed, and its link is LOGGED (never returned), as the
	// broker's forgot-password does in development.
	DevMode bool
	// PasswordPolicy and RejectPassword: see SignInRoutesConfig.
	PasswordPolicy func(context.Context) (PasswordPolicy, error)
	RejectPassword func(ctx context.Context, w http.ResponseWriter, password string, err *PasswordPolicyError)
	// UpstreamTokens and RequireUpstreamTokens: see SignInRoutesConfig. A
	// connected provider that passes tokens hands them with Event
	// UpstreamEventLink; under RequireUpstreamTokens a link whose tokens
	// were not taken is removed again and refused.
	UpstreamTokens        func(ctx context.Context, g UpstreamGrant) error
	RequireUpstreamTokens bool
	// Now is the clock. nil is time.Now.
	Now func() time.Time
}

// NewAccountRoutes validates the configuration.
func NewAccountRoutes(cfg AccountRoutesConfig) (*AccountRoutes, error) {
	if cfg.Client == nil || cfg.Sessions == nil || cfg.SessionID == nil {
		return nil, fmt.Errorf("account routes need a client, a session store and SessionID")
	}
	if cfg.RequireUpstreamTokens && cfg.UpstreamTokens == nil {
		return nil, fmt.Errorf("RequireUpstreamTokens needs an UpstreamTokens hook")
	}
	u, err := url.Parse(strings.TrimRight(cfg.AppBase, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("account routes need the app's public origin (idp_login_uri), got %q", cfg.AppBase)
	}
	cfg.AppBase = strings.TrimRight(cfg.AppBase, "/")
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.CookiePrefix == "" {
		cfg.CookiePrefix = "app"
	}
	if cfg.ReauthWindow <= 0 {
		cfg.ReauthWindow = DefaultReauthWindow
	}
	if cfg.EmailConfirmURL == "" {
		cfg.EmailConfirmURL = cfg.AppBase + "/auth/account/email/confirm?code={{.Code}}"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// As SignInRoutes: no route here adds a credential to an account whose
	// address is unverified.
	client := *cfg.Client
	client.ProvenCredentialsOnly = true
	cfg.Client = &client
	return &AccountRoutes{
		cfg:   cfg,
		seal:  &sealer{key: func() []byte { return []byte(client.bearer()) }, now: cfg.Now},
		guard: NewCrossOriginProtection(cfg.AppBase),

		reauthBySubject: newLimiter(5, 15*time.Minute),
		reauthByIP:      newLimiter(30, 15*time.Minute),
		emailBySubject:  newLimiter(5, time.Hour),
		linkStartByIP:   newLimiter(30, 15*time.Minute),
	}, nil
}

// accountCookie holds a social link in flight. Lax, like the sign-in's: the
// provider sends the browser back with a top-level navigation.
var accountLinkCookie = cookieSpec{"_acl", 10 * time.Minute, http.SameSiteLaxMode}

// linkBinding is a social link in flight, bound to the session that started
// it: the callback links only for that same session, so neither a request
// field nor another signed-in browser can choose whom the provider account
// is connected to.
type linkBinding struct {
	IntentID string `json:"i"`
	Subject  string `json:"s"`
	Handle   string `json:"h"`
	ReturnTo string `json:"r"`
}

// signedInSession is the caller's session, resolved from the store.
type signedInSession struct {
	id   string
	info session.Info
}

// signedIn resolves the request's session cookie, answering 401 (or 502) and
// false when there is no live server-side session.
func (a *AccountRoutes) signedIn(w http.ResponseWriter, req *http.Request) (signedInSession, bool) {
	ctx := req.Context()
	id := a.cfg.SessionID(req)
	if !session.IsID(id) {
		writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		return signedInSession{}, false
	}
	// AccessToken, not just Info: it ends a session whose refresh the
	// issuer refused, exactly as every RPC would.
	if _, err := a.cfg.Sessions.AccessToken(ctx, id); err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		} else {
			a.unavailable(ctx, w, "account: the session could not be read", err)
		}
		return signedInSession{}, false
	}
	info, err := a.cfg.Sessions.Info(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		} else {
			a.unavailable(ctx, w, "account: the session could not be read", err)
		}
		return signedInSession{}, false
	}
	return signedInSession{id: id, info: info}, true
}

// recent reports whether s proved a credential within the reauth window.
func (a *AccountRoutes) recent(s signedInSession) bool {
	return !s.info.ProvedAt.IsZero() && a.cfg.Now().Sub(s.info.ProvedAt) <= a.cfg.ReauthWindow
}

// requireRecent answers 401 reauth_required, and reports false, unless s
// proved a credential within the reauth window.
func (a *AccountRoutes) requireRecent(w http.ResponseWriter, req *http.Request, s signedInSession) bool {
	if a.recent(s) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"code":    CodeReauthRequired,
		"error":   "Confirm it's you first.",
		"methods": a.reauthMethods(req.Context(), s.info.Subject),
	})
	return false
}

// reauthMethods are the ways s's person can prove a credential again.
func (a *AccountRoutes) reauthMethods(ctx context.Context, subject string) []string {
	methods := []string{}
	if a.passwordsOffered() {
		if has, err := a.cfg.Client.HasPassword(ctx, subject); err == nil && has {
			methods = append(methods, MethodPassword)
		}
	}
	return append(methods, "sign_in")
}

func (a *AccountRoutes) passwordsOffered() bool {
	return !a.cfg.Policy.Present || a.cfg.Policy.Offers(MethodPassword)
}

// admit refuses a cross-site request (see SignInRoutes.admit).
func (a *AccountRoutes) admit(w http.ResponseWriter, req *http.Request) bool {
	if err := a.guard.Check(req); err != nil {
		writeErr(w, http.StatusForbidden, CodeCrossSite, "That request came from another site.")
		return false
	}
	return true
}

func (a *AccountRoutes) ip(req *http.Request) string { return ClientKey(req, a.cfg.TrustedProxyHops) }

func (a *AccountRoutes) unavailable(ctx context.Context, w http.ResponseWriter, what string, err error) {
	a.cfg.Logger.ErrorContext(ctx, what, "error", err)
	writeErr(w, http.StatusBadGateway, "", msgUnavailable)
}

// ── The account page ───────────────────────────────────────────────────

// AccountLink is one connected social account on the account page.
type AccountLink struct {
	// Provider is the policy entry's type ("github"), or the issuer's
	// identity provider id for a link to a provider the policy no longer
	// declares.
	Provider string `json:"provider"`
	Label    string `json:"label,omitempty"`
	UserName string `json:"userName,omitempty"`
}

// AccountSession is one place the person is signed in. Handle names it for
// AccountSessionRevokePath; it is not a credential.
type AccountSession struct {
	Handle     string    `json:"handle"`
	UserAgent  string    `json:"userAgent,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Current    bool      `json:"current"`
}

// AccountSocial is a provider the policy declares, and whether it is
// connected.
type AccountSocial struct {
	Type      string `json:"type"`
	Label     string `json:"label"`
	Connected bool   `json:"connected"`
}

// Account is what AccountPath answers.
type Account struct {
	Subject        string           `json:"subject"`
	Email          string           `json:"email"`
	EmailVerified  bool             `json:"emailVerified"`
	HasPassword    bool             `json:"hasPassword"`
	Links          []AccountLink    `json:"links"`
	Social         []AccountSocial  `json:"social"`
	Passkeys       []Passkey        `json:"passkeys"`
	Sessions       []AccountSession `json:"sessions"`
	ProvedAt       time.Time        `json:"provedAt"`
	ReauthRequired bool             `json:"reauthRequired"`
}

// HandleAccount answers the signed-in person's account page.
func (a *AccountRoutes) HandleAccount(w http.ResponseWriter, req *http.Request) {
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	ctx := req.Context()
	c := a.cfg.Client
	subject := s.info.Subject
	user, err := c.FindUserByID(ctx, subject)
	if err != nil {
		a.unavailable(ctx, w, "account: read the account", err)
		return
	}
	hasPassword, err := c.HasPassword(ctx, subject)
	if err != nil {
		a.unavailable(ctx, w, "account: read the sign-in methods", err)
		return
	}
	links, err := c.ListIDPLinks(ctx, subject)
	if err != nil {
		a.unavailable(ctx, w, "account: read the social links", err)
		return
	}
	passkeys, err := c.ListPasskeys(ctx, subject)
	if err != nil {
		a.unavailable(ctx, w, "account: read the passkeys", err)
		return
	}
	sessions, err := a.sessionsOf(ctx, s)
	if err != nil {
		a.unavailable(ctx, w, "account: list the sessions", err)
		return
	}
	out := Account{
		Subject: subject, Email: user.Email, EmailVerified: user.EmailVerified, HasPassword: hasPassword,
		Links: []AccountLink{}, Social: []AccountSocial{}, Passkeys: passkeys, Sessions: sessions,
		ProvedAt: s.info.ProvedAt, ReauthRequired: !a.recent(s),
	}
	if out.Passkeys == nil {
		out.Passkeys = []Passkey{}
	}
	connected := map[string]bool{}
	for _, l := range links {
		al := AccountLink{Provider: l.IdPID, UserName: l.UserName}
		if social, ok := a.cfg.Policy.SocialByIdPID(l.IdPID); ok {
			al.Provider, al.Label = social.Type, social.Label
			connected[social.Type] = true
		}
		out.Links = append(out.Links, al)
	}
	for _, social := range a.cfg.Policy.Social {
		label := social.Label
		if label == "" {
			label = social.Type
		}
		out.Social = append(out.Social, AccountSocial{Type: social.Type, Label: label, Connected: connected[social.Type]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *AccountRoutes) sessionsOf(ctx context.Context, s signedInSession) ([]AccountSession, error) {
	infos, err := a.cfg.Sessions.List(ctx, s.info.Subject)
	if err != nil {
		return nil, err
	}
	out := make([]AccountSession, 0, len(infos))
	for _, i := range infos {
		out = append(out, AccountSession{Handle: i.Handle, UserAgent: i.UserAgent, CreatedAt: i.CreatedAt,
			LastSeenAt: i.LastSeenAt, ExpiresAt: i.ExpiresAt, Current: i.Handle == s.info.Handle})
	}
	return out, nil
}

// ── Re-authentication ──────────────────────────────────────────────────

type reauthRequest struct {
	Password string `json:"password"`
}

// HandleReauth proves the account's password again and records the proof on
// the session (session.Manager.MarkProved), opening the reauth window.
func (a *AccountRoutes) HandleReauth(w http.ResponseWriter, req *http.Request) {
	var body reauthRequest
	if !a.admit(w, req) || !decode(w, req, &body) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	if !a.passwordsOffered() {
		writeErr(w, http.StatusForbidden, CodeMethodNotOffered, msgMethod)
		return
	}
	if !a.proveSessionPassword(w, req, s, body.Password) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proved": true})
}

// proveSessionPassword checks password against s's account at the issuer
// and, when it is right, records the proof on s. On refusal it has answered.
func (a *AccountRoutes) proveSessionPassword(w http.ResponseWriter, req *http.Request, s signedInSession, password string) bool {
	ctx := req.Context()
	if password == "" {
		writeErr(w, http.StatusBadRequest, CodePasswordRequired, "Enter your password.")
		return false
	}
	if len(password) > maxPasswordBytes {
		writeErr(w, http.StatusUnauthorized, CodeWrongPassword, "That password was not accepted.")
		return false
	}
	if !a.reauthByIP.reserve(a.ip(req)) || !a.reauthBySubject.reserve(s.info.Subject) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return false
	}
	// A session at the issuer checked by user id and password: the issuer
	// verifies the password (and counts a wrong one against its lockout
	// policy); the session itself is only the proof and is deleted.
	proof, err := a.cfg.Client.CreateSession(ctx, Credentials{UserID: s.info.Subject, Password: password})
	if err != nil {
		a.cfg.Logger.WarnContext(ctx, "account: re-authentication refused", "user_id", s.info.Subject, "error", err)
		writeErr(w, http.StatusUnauthorized, CodeWrongPassword, "That password was not accepted.")
		return false
	}
	if err := a.cfg.Client.DeleteSession(ctx, proof); err != nil {
		a.cfg.Logger.DebugContext(ctx, "account: the proof's issuer session was not deleted", "error", err)
	}
	if err := a.cfg.Sessions.MarkProved(ctx, s.id); err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, http.StatusUnauthorized, CodeNotSignedIn, msgNotSignedIn)
		} else {
			a.unavailable(ctx, w, "account: record the proof", err)
		}
		return false
	}
	a.reauthBySubject.forget(s.info.Subject)
	return true
}

// ── Address ────────────────────────────────────────────────────────────

type emailChangeRequest struct {
	Email string `json:"email"`
}

// HandleEmailChange starts changing the account's address: the issuer
// records the new address UNVERIFIED and sends a code to it. Recent proof.
//
// Mind the issuer's semantics (Zitadel v2 SetEmail): the account's address
// IS the new one from this call on, unverified until the code is confirmed,
// so a policy that requires a verified address refuses new sign-ins until
// then. Sessions already open keep working. Confirming the code also renames
// the account's username when it was the old address, so the old address
// stops being a way to name the account.
func (a *AccountRoutes) HandleEmailChange(w http.ResponseWriter, req *http.Request) {
	var body emailChangeRequest
	if !a.admit(w, req) || !decode(w, req, &body) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok || !a.requireRecent(w, req, s) {
		return
	}
	ctx := req.Context()
	c := a.cfg.Client
	email := normalizeEmail(body.Email)
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		writeErr(w, http.StatusBadRequest, CodeInvalidAddress, "That is not an email address.")
		return
	}
	if a.cfg.Policy.Present && !a.cfg.Policy.MailDelivers() && !a.cfg.DevMode {
		writeErr(w, http.StatusConflict, CodeMailUnavailable, "This app cannot send the code an address change needs.")
		return
	}
	if !a.emailBySubject.reserve(s.info.Subject) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	current, err := c.FindUserByID(ctx, s.info.Subject)
	if err != nil {
		a.unavailable(ctx, w, "account: read the account", err)
		return
	}
	if normalizeEmail(current.Email) == email {
		writeErr(w, http.StatusBadRequest, CodeSameAddress, "That is your address already.")
		return
	}
	other, err := c.FindUserByEmail(ctx, email)
	switch {
	case err == nil && other.ID != s.info.Subject, errors.Is(err, errAmbiguousAddress):
		writeErr(w, http.StatusConflict, CodeAddressInUse, "Another account uses that address.")
		return
	case err != nil && !errors.Is(err, ErrUserNotFound):
		a.unavailable(ctx, w, "account: look the address up", err)
		return
	}
	// Under the account's lock, so two changes cannot interleave their
	// marker and their change at the issuer.
	err = a.cfg.Sessions.LockSubject(ctx, s.info.Subject, func(ctx context.Context) error {
		// The address that named the account BEFORE any change still
		// pending, for the confirmation's rename (HandleEmailConfirm).
		// Written once, before the first change, and kept across later
		// changes and failed ones until a confirmation settles it: a
		// second change (A→B, then B→C) must not make B the one renamed
		// from.
		if _, pending, err := c.Metadata(ctx, s.info.Subject, MetaEmailChangeFrom); err != nil {
			return err
		} else if !pending {
			if err := c.SetMetadata(ctx, s.info.Subject, MetaEmailChangeFrom, normalizeEmail(current.Email)); err != nil {
				return err
			}
		}
		if !a.cfg.DevMode {
			return c.ChangeEmail(ctx, s.info.Subject, email, a.cfg.EmailConfirmURL)
		}
		code, err := c.ChangeEmailReturnCode(ctx, s.info.Subject, email)
		if err != nil {
			return err
		}
		// A dev stack has no mail: the link goes to the log, never to the
		// response, so the flow is the one production mail drives.
		link := strings.NewReplacer("{{.Code}}", url.QueryEscape(code), "{{.UserID}}", url.QueryEscape(s.info.Subject)).
			Replace(a.cfg.EmailConfirmURL)
		a.cfg.Logger.InfoContext(ctx, "development: confirm the new address with this link (no mail is sent)",
			"user_id", s.info.Subject, "email", email, "link", link)
		return nil
	})
	if err != nil {
		a.unavailable(ctx, w, "account: change the address", err)
		return
	}
	a.cfg.Logger.InfoContext(ctx, "account: address change started", "user_id", s.info.Subject)
	writeJSON(w, http.StatusAccepted, map[string]any{"sent": true, "message": "Check your email: we sent a code to the new address."})
}

type emailConfirmRequest struct {
	Code string `json:"code"`
}

// HandleEmailConfirm proves the code mailed to the new address, signed in as
// the account it was sent for: the address becomes verified.
func (a *AccountRoutes) HandleEmailConfirm(w http.ResponseWriter, req *http.Request) {
	var body emailConfirmRequest
	if !a.admit(w, req) || !decode(w, req, &body) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	ctx := req.Context()
	c := a.cfg.Client
	code := strings.TrimSpace(body.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, CodeInvalidCode, msgInvalidCode)
		return
	}
	if !a.reauthBySubject.reserve("email:" + s.info.Subject) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	var before User
	invalid := false
	err := a.cfg.Sessions.LockSubject(ctx, s.info.Subject, func(ctx context.Context) error {
		var err error
		if before, err = c.FindUserByID(ctx, s.info.Subject); err != nil {
			return err
		}
		if err := c.VerifyEmail(ctx, s.info.Subject, code); err != nil {
			if errors.Is(err, ErrInvalidCode) {
				invalid = true
				return nil
			}
			return err
		}
		a.settleRename(ctx, s.info.Subject, before)
		return nil
	})
	switch {
	case err != nil:
		a.unavailable(ctx, w, "account: confirm the address", err)
		return
	case invalid:
		writeErr(w, http.StatusUnauthorized, CodeInvalidCode, msgInvalidCode)
		return
	}
	a.reauthBySubject.forget("email:" + s.info.Subject)
	a.cfg.Logger.InfoContext(ctx, "account: address confirmed", "user_id", s.info.Subject)
	writeJSON(w, http.StatusOK, map[string]any{"email": before.Email, "emailVerified": true})
}

// settleRename finishes a confirmed address change (the account's address is
// now u.Email, verified). The broker names the accounts it creates by their
// address, so an account whose username is still the address that named it
// before the change (MetaEmailChangeFrom) is renamed to the new one, and the
// old address stops naming it. A username of its own is kept: the
// intentional no-rename. The marker is cleared only once one of the two is
// settled; a rename that failed keeps it for the next confirmation.
func (a *AccountRoutes) settleRename(ctx context.Context, subject string, u User) {
	c := a.cfg.Client
	old, found, err := c.Metadata(ctx, subject, MetaEmailChangeFrom)
	if err != nil || !found {
		return
	}
	if old != "" && strings.EqualFold(u.Username, old) && !strings.EqualFold(u.Username, u.Email) {
		if err := c.setUsername(ctx, subject, normalizeEmail(u.Email)); err != nil {
			a.cfg.Logger.WarnContext(ctx, "account: the username still names the old address; the next confirmation retries", "user_id", subject, "error", err)
			return
		}
	}
	if err := c.DeleteMetadata(ctx, subject, MetaEmailChangeFrom); err != nil {
		a.cfg.Logger.DebugContext(ctx, "account: the address change's marker was not cleared", "error", err)
	}
}

// ── Password ───────────────────────────────────────────────────────────

type passwordChangeRequest struct {
	// CurrentPassword, when given, is checked as a proof first (it counts
	// as re-authentication); without it the session needs a recent proof.
	CurrentPassword string `json:"currentPassword,omitempty"`
	NewPassword     string `json:"newPassword"`
}

// HandlePasswordChange sets or changes the account's password, held to the
// password policy, and then signs every OTHER session of the account out.
func (a *AccountRoutes) HandlePasswordChange(w http.ResponseWriter, req *http.Request) {
	var body passwordChangeRequest
	if !a.admit(w, req) || !decode(w, req, &body) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	if !a.passwordsOffered() {
		writeErr(w, http.StatusForbidden, CodeMethodNotOffered, msgMethod)
		return
	}
	ctx := req.Context()
	if body.CurrentPassword != "" {
		if !a.proveSessionPassword(w, req, s, body.CurrentPassword) {
			return
		}
	} else if !a.requireRecent(w, req, s) {
		return
	}
	if body.NewPassword == "" {
		writeErr(w, http.StatusBadRequest, CodePasswordRequired, "Choose a new password.")
		return
	}
	if len(body.NewPassword) > maxPasswordBytes {
		writeErr(w, http.StatusBadRequest, CodePasswordTooLong, "That password is too long.")
		return
	}
	if a.rejectWeak(ctx, w, body.NewPassword) {
		return
	}
	err := a.cfg.Client.SetPassword(ctx, s.info.Subject, body.NewPassword)
	var policyErr *PasswordPolicyError
	switch {
	case errors.As(err, &policyErr):
		a.rejectPassword(ctx, w, body.NewPassword, policyErr)
		return
	case errors.Is(err, ErrUnverifiedAccount):
		writeErr(w, http.StatusForbidden, CodeEmailUnverified, msgUnverified)
		return
	case err != nil:
		a.unavailable(ctx, w, "account: set the password", err)
		return
	}
	// Whoever else holds a session of this account — the reason a person
	// changes a password — is signed out. This one stays.
	ended, err := a.cfg.Sessions.RevokeOthers(ctx, s.info.Subject, s.id)
	if err != nil {
		// Never a success: the password changed, but whoever else holds a
		// session may still be signed in. Say so, and how to finish it.
		a.cfg.Logger.ErrorContext(ctx, "account: the password changed, but other sessions could not be signed out", "user_id", s.info.Subject, "ended", ended, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code":                 CodeSessionsNotRevoked,
			"error":                "Your password changed, but other devices could not be signed out. Sign out other devices again.",
			"changed":              true,
			"otherSessionsRevoked": false,
			"otherSessionsEnded":   ended,
			"retry":                strings.TrimPrefix(AccountSessionsRevokeOthersPath, "POST "),
		})
		return
	}
	a.cfg.Logger.InfoContext(ctx, "account: password changed", "user_id", s.info.Subject, "other_sessions_ended", ended)
	writeJSON(w, http.StatusOK, map[string]any{"changed": true, "otherSessionsRevoked": true, "otherSessionsEnded": ended})
}

func (a *AccountRoutes) rejectWeak(ctx context.Context, w http.ResponseWriter, password string) bool {
	if a.cfg.PasswordPolicy == nil {
		return false
	}
	policy, err := a.cfg.PasswordPolicy(ctx)
	if err != nil {
		return false
	}
	unmet := policy.Unmet(password)
	if len(unmet) == 0 {
		return false
	}
	a.rejectPassword(ctx, w, password, &PasswordPolicyError{Rule: unmet[0], Reason: policy.Requirement(unmet[0])})
	return true
}

func (a *AccountRoutes) rejectPassword(ctx context.Context, w http.ResponseWriter, password string, err *PasswordPolicyError) {
	if a.cfg.RejectPassword != nil {
		a.cfg.RejectPassword(ctx, w, password, err)
		return
	}
	writeErr(w, http.StatusBadRequest, "password_policy", "That password was not accepted: "+err.Reason+".")
}

// ── Social links ───────────────────────────────────────────────────────

// HandleLinkStart begins connecting a social account to the signed-in
// account. Recent proof. The provider round trip returns to the same
// /auth/social/callback page a sign-in does; SocialCallback, wrapped around
// the sign-in's callback handler, completes it as a LINK.
func (a *AccountRoutes) HandleLinkStart(w http.ResponseWriter, req *http.Request) {
	var body socialStartRequest
	if !a.admit(w, req) || !decode(w, req, &body) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok || !a.requireRecent(w, req, s) {
		return
	}
	ctx := req.Context()
	social, ok := a.cfg.Policy.SocialByType(strings.TrimSpace(body.Provider))
	if !ok {
		writeErr(w, http.StatusNotFound, CodeUnknownProvider, msgMethod)
		return
	}
	if !a.linkStartByIP.reserve(a.ip(req)) {
		writeErr(w, http.StatusTooManyRequests, CodeRateLimited, msgTooMany)
		return
	}
	authURL, intentID, err := a.cfg.Client.StartIDPIntent(ctx, social.IdPID,
		a.cfg.AppBase+"/auth/social/callback", a.cfg.AppBase+SocialFailurePath)
	if err != nil {
		a.unavailable(ctx, w, "account: the social link could not start", err)
		return
	}
	b := linkBinding{IntentID: intentID, Subject: s.info.Subject, Handle: s.info.Handle, ReturnTo: SafeReturnTo(body.ReturnTo)}
	v, err := a.seal.seal(accountLinkCookie.suffix, b, accountLinkCookie.ttl)
	if err != nil {
		a.unavailable(ctx, w, "account: the social link: seal", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(accountLinkCookie), Value: v, Path: "/",
		MaxAge: int(accountLinkCookie.ttl.Seconds()), HttpOnly: true, Secure: a.cfg.SecureCookie, SameSite: accountLinkCookie.sameSite})
	writeJSON(w, http.StatusOK, map[string]string{"redirect": authURL})
}

func (a *AccountRoutes) cookieName(c cookieSpec) string {
	if a.cfg.SecureCookie {
		return "__Host-" + a.cfg.CookiePrefix + c.suffix
	}
	return a.cfg.CookiePrefix + c.suffix
}

func (a *AccountRoutes) clearLinkBinding(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(accountLinkCookie), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.cfg.SecureCookie, SameSite: accountLinkCookie.sameSite})
}

// SocialCallback wraps the sign-in's social callback handler (the
// SocialCallbackPath route): a callback for an intent HandleLinkStart began
// in this browser completes as a link to the session that began it, and
// every other request goes to next unchanged. A link never signs anyone in,
// never switches account, and never connects a provider account that is
// connected to another user already.
func (a *AccountRoutes) SocialCallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var b linkBinding
		ck, err := req.Cookie(a.cookieName(accountLinkCookie))
		if err != nil || a.seal.open(accountLinkCookie.suffix, ck.Value, &b) != nil || b.IntentID == "" {
			next.ServeHTTP(w, req)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 64<<10))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "", "malformed request")
			return
		}
		var body socialCallbackRequest
		if json.Unmarshal(raw, &body) != nil || strings.TrimSpace(body.ID) != b.IntentID {
			// Not the link's intent: a sign-in begun in this browser.
			req.Body = io.NopCloser(bytes.NewReader(raw))
			next.ServeHTTP(w, req)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		a.completeLink(w, req, b, body)
	})
}

func (a *AccountRoutes) completeLink(w http.ResponseWriter, req *http.Request, b linkBinding, body socialCallbackRequest) {
	if !a.admit(w, req) {
		return
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type"))), "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "", "malformed request")
		return
	}
	a.clearLinkBinding(w)
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	ctx := req.Context()
	if s.info.Subject != b.Subject || s.info.Handle != b.Handle {
		// Signed out and in again (as anyone) since the link began.
		a.cfg.Logger.WarnContext(ctx, "account: a social link returned to another session", "user_id", s.info.Subject)
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	c := a.cfg.Client
	res, err := c.RetrieveIDPIntent(ctx, b.IntentID, body.Token)
	if err != nil {
		a.cfg.Logger.WarnContext(ctx, "account: social link: intent not retrieved", "error", err)
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	social, ok := a.cfg.Policy.SocialByIdPID(res.Link.IdPID)
	if !ok {
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	done := func() {
		writeJSON(w, http.StatusOK, map[string]any{"linked": true, "provider": social.Type, "returnTo": b.ReturnTo,
			"identity": map[string]any{"authenticated": true, "subject": s.info.Subject}})
	}
	switch res.LinkedUserID {
	case s.info.Subject:
		// Connected already: nothing to add.
		if a.handUpstream(ctx, social, res, s.info.Subject) {
			done()
			return
		}
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	case "":
	default:
		a.cfg.Logger.WarnContext(ctx, "account: the provider account belongs to another user", "user_id", s.info.Subject, "provider", social.Type)
		writeErr(w, http.StatusConflict, CodeSocialTaken, fmt.Sprintf(msgSocialTaken, social.Label))
		return
	}
	if err := c.AddIDPLink(ctx, s.info.Subject, res.Link); err != nil {
		if errors.Is(err, ErrUnverifiedAccount) {
			writeErr(w, http.StatusForbidden, CodeEmailUnverified, msgUnverified)
			return
		}
		a.unavailable(ctx, w, "account: social link", err)
		return
	}
	if !a.handUpstream(ctx, social, res, s.info.Subject) {
		if err := c.RemoveIDPLink(ctx, s.info.Subject, res.Link); err != nil {
			a.cfg.Logger.ErrorContext(ctx, "account: a refused link could not be removed", "user_id", s.info.Subject, "error", err)
		}
		writeErr(w, http.StatusUnauthorized, CodeSocialFailed, msgSocialFailed)
		return
	}
	a.cfg.Logger.InfoContext(ctx, "account: social account connected", "user_id", s.info.Subject, "provider", social.Type)
	done()
}

func (a *AccountRoutes) handUpstream(ctx context.Context, social LoginPolicySocial, res IDPResult, subject string) bool {
	return handUpstreamTokens(ctx, a.cfg.Logger, a.cfg.UpstreamTokens, a.cfg.RequireUpstreamTokens, social, res, subject, UpstreamEventLink)
}

// HandleUnlink disconnects a social account (the route's {provider}: the
// policy entry's type, or the identity provider id an AccountLink names).
// Recent proof; refused for the account's last usable credential.
func (a *AccountRoutes) HandleUnlink(w http.ResponseWriter, req *http.Request) {
	if !a.admit(w, req) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok || !a.requireRecent(w, req, s) {
		return
	}
	name := strings.TrimSpace(req.PathValue("provider"))
	idpID := name
	if social, ok := a.cfg.Policy.SocialByType(name); ok {
		idpID = social.IdPID
	}
	a.removeCredential(w, req, s, func(cr accountCredentials) ([]IDPLink, string, bool) {
		var remove []IDPLink
		for _, l := range cr.links {
			if l.IdPID == idpID && idpID != "" {
				remove = append(remove, l)
			}
		}
		return remove, "", len(remove) > 0
	}, "That account is not connected.")
}

// HandlePasskeyRemove removes one of the account's passkeys. Recent proof;
// refused for the account's last usable credential.
func (a *AccountRoutes) HandlePasskeyRemove(w http.ResponseWriter, req *http.Request) {
	if !a.admit(w, req) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok || !a.requireRecent(w, req, s) {
		return
	}
	id := strings.TrimSpace(req.PathValue("id"))
	a.removeCredential(w, req, s, func(cr accountCredentials) ([]IDPLink, string, bool) {
		for _, k := range cr.passkeys {
			if k.ID == id && id != "" {
				return nil, id, true
			}
		}
		return nil, "", false
	}, "No such passkey.")
}

// removeCredential removes what pick chooses from s's account (links and/or
// a passkey), unless that would leave the account no USABLE way in. The
// count, the check and the removal run under the account's lock in the
// session store (session.Manager.LockSubject), so two removals — on this
// replica or another — cannot each count the other's credential as the one
// that remains. A store that cannot lock refuses the removal.
func (a *AccountRoutes) removeCredential(w http.ResponseWriter, req *http.Request, s signedInSession,
	pick func(accountCredentials) (links []IDPLink, passkey string, found bool), notFound string) {
	ctx := req.Context()
	c := a.cfg.Client
	var (
		outcome  int // http status to answer with
		code     string
		message  string
		removing []IDPLink
		passkey  string
	)
	err := a.cfg.Sessions.LockSubject(ctx, s.info.Subject, func(ctx context.Context) error {
		cr, err := a.credentialsOf(ctx, s.info.Subject)
		if err != nil {
			return err
		}
		var found bool
		removing, passkey, found = pick(cr)
		if !found {
			outcome, code, message = http.StatusNotFound, CodeNotLinked, notFound
			return nil
		}
		if cr.usableWithout(a.cfg.Policy, removing, passkey) < 1 {
			outcome, code, message = http.StatusConflict, CodeLastCredential, "This is your only way to sign in. Add another first."
			return nil
		}
		for _, l := range removing {
			if err := c.RemoveIDPLink(ctx, s.info.Subject, l); err != nil {
				return err
			}
		}
		if passkey != "" {
			if err := c.RemovePasskey(ctx, s.info.Subject, passkey); err != nil {
				return err
			}
		}
		outcome = http.StatusOK
		return nil
	})
	switch {
	case err != nil:
		a.unavailable(ctx, w, "account: remove a sign-in method", err)
	case outcome != http.StatusOK:
		writeErr(w, outcome, code, message)
	case passkey != "":
		a.cfg.Logger.InfoContext(ctx, "account: passkey removed", "user_id", s.info.Subject)
		writeJSON(w, http.StatusOK, map[string]any{"removed": true})
	default:
		a.cfg.Logger.InfoContext(ctx, "account: social account disconnected", "user_id", s.info.Subject, "links", len(removing))
		writeJSON(w, http.StatusOK, map[string]any{"unlinked": true})
	}
}

// accountCredentials is what the account holds that could let its person in.
type accountCredentials struct {
	password      bool
	passkeys      []Passkey
	links         []IDPLink
	emailVerified bool
}

func (a *AccountRoutes) credentialsOf(ctx context.Context, subject string) (accountCredentials, error) {
	c := a.cfg.Client
	var cr accountCredentials
	var err error
	if cr.password, err = c.HasPassword(ctx, subject); err != nil {
		return cr, err
	}
	if cr.passkeys, err = c.ListPasskeys(ctx, subject); err != nil {
		return cr, err
	}
	if cr.links, err = c.ListIDPLinks(ctx, subject); err != nil {
		return cr, err
	}
	u, err := c.FindUserByID(ctx, subject)
	if err != nil {
		return cr, err
	}
	cr.emailVerified = u.EmailVerified
	return cr, nil
}

// usableWithout counts the ways in that would remain, after removing the
// links and the passkey named, that THIS broker serves under policy p: a
// password only where passwords are offered, passkeys only where passkeys
// are, a link only to a provider p declares, and a magic link to the
// account's verified address where p offers magic links AND its mail
// reaches someone (a paused or absent mailer sends no link). A credential
// the policy does not serve is no way in, however it is stored.
func (cr accountCredentials) usableWithout(p LoginPolicy, links []IDPLink, passkey string) int {
	n := 0
	if cr.password && (!p.Present || p.Offers(MethodPassword)) {
		n++
	}
	if p.Present && p.Offers(MethodPasskey) {
		for _, k := range cr.passkeys {
			if k.ID != passkey {
				n++
			}
		}
	}
	for _, l := range cr.links {
		removed := false
		for _, r := range links {
			removed = removed || (r.IdPID == l.IdPID && r.UserID == l.UserID)
		}
		if _, declared := p.SocialByIdPID(l.IdPID); declared && !removed {
			n++
		}
	}
	if p.Present && p.Offers(MethodMagicLink) && p.MailDelivers() && cr.emailVerified {
		n++
	}
	return n
}

// ── Sessions ───────────────────────────────────────────────────────────

// HandleSessions lists the places the account is signed in, the current one
// flagged.
func (a *AccountRoutes) HandleSessions(w http.ResponseWriter, req *http.Request) {
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	sessions, err := a.sessionsOf(req.Context(), s)
	if err != nil {
		a.unavailable(req.Context(), w, "account: list the sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// HandleSessionRevoke signs one of the account's OTHER sessions out. A handle
// that is not one of the account's answers 404, like an unknown one. The
// current session is refused: signing out is the broker's logout route.
func (a *AccountRoutes) HandleSessionRevoke(w http.ResponseWriter, req *http.Request) {
	if !a.admit(w, req) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok {
		return
	}
	ctx := req.Context()
	handle := strings.TrimSpace(req.PathValue("handle"))
	if handle == s.info.Handle {
		writeErr(w, http.StatusConflict, CodeCurrentSession, "That is this session: sign out instead.")
		return
	}
	switch err := a.cfg.Sessions.RevokeHandle(ctx, s.info.Subject, handle); {
	case errors.Is(err, session.ErrNoSession):
		writeErr(w, http.StatusNotFound, CodeNotLinked, "No such session.")
	case err != nil:
		a.unavailable(ctx, w, "account: revoke a session", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	}
}

// HandleRevokeOthers signs every other session of the account out. Recent
// proof.
func (a *AccountRoutes) HandleRevokeOthers(w http.ResponseWriter, req *http.Request) {
	if !a.admit(w, req) {
		return
	}
	s, ok := a.signedIn(w, req)
	if !ok || !a.requireRecent(w, req, s) {
		return
	}
	n, err := a.cfg.Sessions.RevokeOthers(req.Context(), s.info.Subject, s.id)
	if err != nil {
		a.unavailable(req.Context(), w, "account: revoke the other sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// ── Issuer calls ───────────────────────────────────────────────────────

// ChangeEmail sets a user's address, unverified, and has the issuer mail a
// verification code to it with urlTemplate ({{.Code}}, {{.UserID}}). CALL
// ONLY AFTER A PROOF for this user: the broker's credential needs none.
func (c *Client) ChangeEmail(ctx context.Context, userID, email, urlTemplate string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(email) == "" {
		return fmt.Errorf("a user id and an address are required")
	}
	body := map[string]any{"email": email}
	if urlTemplate != "" {
		body["sendCode"] = map[string]any{"urlTemplate": urlTemplate}
	} else {
		body["sendCode"] = map[string]any{}
	}
	if _, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/email", body); err != nil {
		return fmt.Errorf("change the address: %w", err)
	}
	return nil
}

// ChangeEmailReturnCode is ChangeEmail with the code returned instead of
// mailed, for a stack with no mail server. Never hand the code to the
// browser that asked: delivering it to the new address is the proof.
func (c *Client) ChangeEmailReturnCode(ctx context.Context, userID, email string) (string, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(email) == "" {
		return "", fmt.Errorf("a user id and an address are required")
	}
	raw, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/email", map[string]any{"email": email, "returnCode": map[string]any{}})
	if err != nil {
		return "", fmt.Errorf("change the address: %w", err)
	}
	var out struct {
		VerificationCode string `json:"verificationCode"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.VerificationCode == "" {
		return "", fmt.Errorf("the issuer returned no verification code")
	}
	return out.VerificationCode, nil
}

// RemoveIDPLink removes one identity-provider link from an account. A link
// that is gone already is not an error.
func (c *Client) RemoveIDPLink(ctx context.Context, userID string, link IDPLink) error {
	if strings.TrimSpace(userID) == "" || link.IdPID == "" || link.UserID == "" {
		return fmt.Errorf("a user id and a provider identity are required")
	}
	path := "/v2/users/" + url.PathEscape(userID) + "/links/" + url.PathEscape(link.IdPID) + "/" + url.PathEscape(link.UserID)
	if _, err := c.sendJSON(ctx, http.MethodDelete, path, map[string]any{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("remove the %s link: %w", link.IdPID, err)
	}
	return nil
}

// RemovePasskey removes one of an account's passkeys. One that is gone
// already is not an error.
func (c *Client) RemovePasskey(ctx context.Context, userID, passkeyID string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(passkeyID) == "" {
		return fmt.Errorf("a user id and a passkey id are required")
	}
	path := "/v2/users/" + url.PathEscape(userID) + "/passkeys/" + url.PathEscape(passkeyID)
	if _, err := c.sendJSON(ctx, http.MethodDelete, path, map[string]any{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("remove passkey %s: %w", passkeyID, err)
	}
	return nil
}
