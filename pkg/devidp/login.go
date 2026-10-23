package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The API-only sign-in flow: how an app authenticates a user WITHOUT ever
// sending the browser to the identity provider's own login pages.
//
// ── Why this exists ───────────────────────────────────────────────────
//
// The default OIDC authorization-code flow redirects the user agent to the
// issuer's /authorize endpoint, which answers with the IdP's OWN login UI.
// The user types their password on the issuer's origin, under the issuer's
// branding, and comes back with a code. That works, and for many products
// it is the right choice — an IdP-hosted page is one less place for a
// credential bug to live.
//
// It is the wrong choice when the sign-in screen is part of the PRODUCT.
// Leaving the app to authenticate is a visible seam: different typography,
// a different URL in the address bar, no way to put a product's own empty
// states or error copy on the most-seen screen it has. This package's job
// is to make the other option available without asking anyone to
// reverse-engineer it.
//
// ── The three calls, and the one that is easy to get wrong ────────────
//
// Zitadel's v2 API exposes the whole flow, so an app can drive it:
//
//  1. The browser hits /oauth/v2/authorize as usual. With the LoginV2
//     feature pointed at the app (SetLoginUI below), the issuer redirects
//     BACK INTO THE APP with an `authRequest` id instead of rendering its
//     own page.
//  2. The app collects credentials on its own screen and calls
//     CreateSession — the credential check happens here.
//  3. The app calls FinalizeAuthRequest with the resulting session, and
//     receives the callback URL carrying the authorization code. From
//     there the browser completes a standard PKCE token exchange; the
//     backend never handles the user's tokens.
//
// ── THE BYPASS THIS FILE EXISTS TO PREVENT ────────────────────────────
//
// Zitadel treats "which factors were checked" as the CALLER's decision. A
// session created with a user check and NO password check is a perfectly
// valid session, and it will finalize an auth request and yield real
// tokens. Verified against v4.19.4: a session with `checks: {user: {...}}`
// and nothing else produced a working access_token and id_token for an
// admin account, and the id_token carried NO `amr` claim — so a downstream
// validator cannot even tell the credential was never verified.
//
// A wrong password is correctly rejected (COMMAND-3M0fs). Omitting the
// password entirely is not rejected at all. The difference matters because
// it is the difference between a login form and an impersonation endpoint.
//
// So this package does NOT expose a "build your own checks" surface, and
// deliberately accepts no caller-supplied JSON for the checks object.
// Credentials is a closed set of typed fields, each mapping to exactly one
// factor, and buildChecks REFUSES to produce a checks object with no
// verifying factor in it. A handler that forwards a request body straight
// through cannot silently create a passwordless session, because there is
// no code path that accepts one.
//
// If you need a factor this does not model, add a field to Credentials and
// a case to buildChecks. Do not reach around them.
type Credentials struct {
	// LoginName identifies the account: the username, email or other
	// login name the issuer knows. Required.
	LoginName string

	// Password is the user's password. Required unless another verifying
	// factor is set — see buildChecks.
	Password string

	// TOTP is a time-based one-time code, when the account has TOTP
	// enrolled. Supplied ALONGSIDE Password: Zitadel records each factor
	// separately on the session, so a second factor adds to the password
	// check rather than replacing it.
	TOTP string
}

// verifying reports whether these credentials carry at least one factor
// that actually PROVES anything. A login name alone identifies an account;
// it does not authenticate one.
func (c Credentials) verifying() bool {
	return c.Password != "" || c.TOTP != ""
}

// Session is an authenticated session at the issuer: the handle that
// FinalizeAuthRequest exchanges for an authorization code.
//
// The token is a bearer credential for this session. It is returned so a
// caller can hold it across the two calls, and for no other reason — it
// must never be sent to a browser.
type Session struct {
	ID    string `json:"sessionId"`
	Token string `json:"sessionToken"`
}

// CreateSession authenticates a user and returns the resulting session.
//
// The credential check happens INSIDE the issuer: this call ships the
// password to Zitadel, which verifies it against its own stored hash. This
// process never sees, stores or compares a password hash, which is the
// property that makes brokering the call acceptable in the first place.
//
// A bad password returns an error carrying Zitadel's own message
// (COMMAND-3M0fs, with a failedAttempts count) rather than a paraphrase:
// lockout policy is the issuer's to enforce and to report.
func (c *Client) CreateSession(ctx context.Context, creds Credentials) (Session, error) {
	checks, err := buildChecks(creds)
	if err != nil {
		return Session{}, err
	}

	body, err := c.postJSON(ctx, "/v2/sessions", map[string]any{"checks": checks})
	if err != nil {
		return Session{}, fmt.Errorf("authenticate %q: %w", creds.LoginName, err)
	}

	var out Session
	if err := json.Unmarshal(body, &out); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}
	if out.ID == "" || out.Token == "" {
		// Defence in depth. A session without both halves cannot finalize
		// an auth request, and returning one would surface later as an
		// opaque failure at the callback rather than here, where the
		// cause is visible.
		return Session{}, fmt.Errorf("the issuer returned an incomplete session for %q", creds.LoginName)
	}
	return out, nil
}

// buildChecks translates typed credentials into the issuer's checks object.
//
// THIS FUNCTION IS THE AUTHENTICATION BOUNDARY. It is the only thing in
// this package that produces a checks object, it takes no caller-supplied
// map, and it refuses the two shapes that would make the broker an
// impersonation endpoint:
//
//   - no login name — there is no account to check against;
//   - no verifying factor — the case that yields a real token for an
//     unverified user (see the file header).
//
// The refusals are errors rather than "add a default", because there is no
// safe default for "which credential did this person present".
func buildChecks(creds Credentials) (map[string]any, error) {
	if strings.TrimSpace(creds.LoginName) == "" {
		return nil, fmt.Errorf("a login name is required")
	}
	if !creds.verifying() {
		// The bypass, refused at the only place it could be constructed.
		return nil, fmt.Errorf(
			"refusing to create a session for %q with no verifying factor: "+
				"the issuer would accept it and mint real tokens for an unauthenticated user",
			creds.LoginName)
	}

	checks := map[string]any{
		"user": map[string]any{"loginName": creds.LoginName},
	}
	if creds.Password != "" {
		checks["password"] = map[string]any{"password": creds.Password}
	}
	if creds.TOTP != "" {
		checks["totp"] = map[string]any{"code": creds.TOTP}
	}
	return checks, nil
}

// FinalizeAuthRequest completes an OIDC authorization request with an
// authenticated session, and returns the callback URL the browser should be
// sent to. That URL carries the authorization code and the original
// `state`.
//
// authRequestID is the value the issuer put in the query string when it
// redirected into this app (see SetLoginUI). Pass it through unchanged,
// INCLUDING any prefix: Zitadel's v2 ids are prefixed (`V2_...`), and the
// prefix is part of the id.
//
// A v1 auth request cannot be finalized this way — it fails "Auth Request
// does not exist (COMMAND-jae5P)", which reads like a expiry bug but means
// the LoginV2 feature is not enabled for this instance. SetLoginUI is what
// makes the issuer mint v2 requests in the first place, so the two are a
// package: neither is useful alone.
func (c *Client) FinalizeAuthRequest(ctx context.Context, authRequestID string, session Session) (string, error) {
	id := strings.TrimSpace(authRequestID)
	if id == "" {
		return "", fmt.Errorf("an auth request id is required")
	}

	body, err := c.postJSON(ctx, "/v2/oidc/auth_requests/"+url.PathEscape(id), map[string]any{
		"session": map[string]any{
			"sessionId":    session.ID,
			"sessionToken": session.Token,
		},
	})
	if err != nil {
		return "", fmt.Errorf("finalize auth request %q: %w", id, err)
	}

	var out struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode callback: %w", err)
	}
	if out.CallbackURL == "" {
		return "", fmt.Errorf("the issuer accepted auth request %q but returned no callback URL", id)
	}
	return out.CallbackURL, nil
}

// loginClientRole is the instance-level role that permits finalizing
// another user's auth request — the "act as the login UI" privilege.
//
// IAM_OWNER is NOT enough, and the failure is unhelpful: without this role
// FinalizeAuthRequest returns "No matching permissions found (AUTH-AWfge)"
// after the credential check has already succeeded, which reads like a
// broken session rather than a missing grant. Verified against v4.19.4.
const loginClientRole = "IAM_LOGIN_CLIENT"

// EnsureLoginClientRole grants the service account behind this client the
// role the API-only sign-in flow requires, preserving whatever roles it
// already holds.
//
// THIS IS A PRIVILEGED GRANT. IAM_LOGIN_CLIENT permits completing an
// authorization request on behalf of any user in the instance, so the
// credential holding it is as sensitive as the sign-in flow itself. It
// belongs to the one backend that brokers logins — never in a frontend
// bundle, a build environment, or a shared operator token.
//
// userID is the service account's own user id. Roles are reconciled as a
// SET: the existing roles are read and this one added, so a re-run is a
// no-op and an unrelated role is never dropped.
func (c *Client) EnsureLoginClientRole(ctx context.Context, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("a user id is required to grant %s", loginClientRole)
	}

	roles, existing, err := c.instanceMemberRoles(ctx, userID)
	if err != nil {
		return err
	}
	for _, role := range roles {
		if role == loginClientRole {
			return nil // already granted; nothing to converge
		}
	}

	// ADD vs UPDATE are different endpoints, and picking the wrong one
	// fails in a way that reads like the user does not exist: PUT against a
	// user who holds no instance membership yet answers
	// "Errors.NotFound (INSTANCE-D8JxR)" — which names the INSTANCE, not
	// the missing membership, and sends you looking at the wrong thing.
	// A freshly created service account is always in that state, so this
	// is the normal path, not an edge case.
	if existing {
		_, err = c.putJSON(ctx, "/admin/v1/members/"+url.PathEscape(userID), map[string]any{
			"roles": append(roles, loginClientRole),
		})
	} else {
		_, err = c.postJSON(ctx, "/admin/v1/members", map[string]any{
			"userId": userID,
			"roles":  []string{loginClientRole},
		})
	}
	if err != nil && !isNoChangesErr(err) {
		return fmt.Errorf("grant %s to %q: %w", loginClientRole, userID, err)
	}
	return nil
}

// instanceMemberRoles reads the instance-level roles a user currently
// holds, and reports whether the user has an instance membership AT ALL.
//
// The second return value is what decides ADD vs UPDATE in the caller, and
// it is genuinely distinct from "holds no roles": a user with no membership
// must be POSTed (creating one), while PUT is for a membership that exists.
func (c *Client) instanceMemberRoles(ctx context.Context, userID string) (roles []string, member bool, err error) {
	body, err := c.postJSON(ctx, "/admin/v1/members/_search", map[string]any{})
	if err != nil {
		return nil, false, fmt.Errorf("list instance members: %w", err)
	}
	var out struct {
		Result []struct {
			UserID string   `json:"userId"`
			Roles  []string `json:"roles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, false, fmt.Errorf("decode instance members: %w", err)
	}
	for _, m := range out.Result {
		if m.UserID == userID {
			return m.Roles, true, nil
		}
	}
	return nil, false, nil
}

// Registration is a new account's details, as a sign-up form collects them.
//
// EmailVerified is deliberately a caller decision rather than a fixed
// value. A dev stack has no mail server, so an unverified address strands
// the account behind a verification step nothing can deliver — the exact
// dead end that makes a scaffolded sign-up feel broken. A production
// deployment with real mail should set it false and let the issuer send
// the verification.
type Registration struct {
	Email      string
	Password   string
	GivenName  string
	FamilyName string
	// EmailVerified marks the address trusted without a mail round trip.
	EmailVerified bool
}

// Register creates a new human user at the issuer and returns its id.
//
// WHY THIS IS A SERVER CALL. Creating a user needs `user.write`, which a
// browser cannot hold — the same reason the session and callback calls are
// brokered. The credential is the login broker's, so registration and
// sign-in share one account and one blast radius rather than adding a
// second privileged token.
//
// The issuer enforces its own password policy and uniqueness; a rejection
// comes back as its own message rather than a paraphrase, because "must
// contain a symbol" is exactly what the person filling in the form needs
// to read.
func (c *Client) Register(ctx context.Context, reg Registration) (string, error) {
	email := strings.TrimSpace(reg.Email)
	if email == "" {
		return "", fmt.Errorf("an email address is required")
	}
	if reg.Password == "" {
		// Refused here rather than sent: an account created with no
		// credential cannot sign in, and would need an invite code to
		// recover — which is a worse outcome than a clear error.
		return "", fmt.Errorf("a password is required to create an account")
	}

	profile := profileFor(reg, email)
	body, err := c.postJSON(ctx, "/v2/users/human", map[string]any{
		"username": email,
		"profile":  profile,
		"email": map[string]any{
			"email":      email,
			"isVerified": reg.EmailVerified,
		},
		"password": map[string]any{
			"password":       reg.Password,
			"changeRequired": false,
		},
	})
	if err != nil {
		// A rejected password comes back as a *PasswordPolicyError the
		// caller can branch on, rather than as a paraphrase to parse.
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			if policy := policyErrorFrom(apiErr); policy != nil {
				return "", fmt.Errorf("create account for %q: %w", email, policy)
			}
		}
		return "", fmt.Errorf("create account for %q: %w", email, err)
	}

	var out struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode created user: %w", err)
	}
	if out.UserID == "" {
		return "", fmt.Errorf("the issuer accepted the registration for %q but returned no user id", email)
	}
	return out.UserID, nil
}

// profileFor derives the given/family names the issuer REQUIRES on a human
// user, falling back to the address's local part when a form did not ask
// for them. Refusing to register someone for want of a surname would be
// worse than a placeholder they can correct later.
func profileFor(reg Registration, email string) map[string]any {
	given := strings.TrimSpace(reg.GivenName)
	family := strings.TrimSpace(reg.FamilyName)
	if given == "" || family == "" {
		local := email
		if at := strings.IndexByte(local, '@'); at > 0 {
			local = local[:at]
		}
		if given == "" {
			given = local
		}
		if family == "" {
			family = local
		}
	}
	return map[string]any{"givenName": given, "familyName": family}
}

// BrokerCredential is the service account the login broker authenticates
// as, and the token it presents.
//
// UserID is returned alongside the token because the role grant is keyed
// by user id, not by token — a caller that needs to re-grant (or audit)
// the role has the handle without another lookup.
type BrokerCredential struct {
	UserID string
	Token  string
}

// EnsureLoginBroker provisions the DEDICATED service account the login
// broker runs as, grants it the login-client role, and returns a token for
// it.
//
// WHY A SEPARATE ACCOUNT rather than reusing the provisioning credential.
// The provisioner's token is an IAM_OWNER credential that exists to
// register applications at deploy time; the broker's token exists to
// complete logins at request time, lives in a long-running server, and is
// reachable from the request path. Those are different lifetimes and very
// different blast radii, and folding them into one token means a
// compromise of the busiest surface in the app yields the credential that
// administers the whole instance.
//
// Splitting them costs one more account in the dev bootstrap and buys the
// ability to rotate or revoke the login path without touching provisioning.
//
// NOT IDEMPOTENT IN THE USUAL SENSE, and deliberately: Zitadel issues a
// personal access token exactly once and never returns it again, so a
// re-run against an existing account cannot recover the previous token and
// mints a NEW one instead. The old token stays valid until it expires or is
// removed — so callers should provision once and persist the result, rather
// than calling this on every boot. ProvisionLoginBroker is that caller: it
// keeps a stored token the issuer still accepts and mints only when it must.
func (c *Client) EnsureLoginBroker(ctx context.Context, username string) (BrokerCredential, error) {
	userID, err := c.ensureLoginBrokerAccount(ctx, username)
	if err != nil {
		return BrokerCredential{}, err
	}
	return c.mintBrokerToken(ctx, username, userID)
}

// ensureLoginBrokerAccount converges the broker's machine user and its
// login-client role, and returns the user id. Both steps read before they
// write, so this half of provisioning is idempotent; only the token is not.
func (c *Client) ensureLoginBrokerAccount(ctx context.Context, username string) (string, error) {
	if strings.TrimSpace(username) == "" {
		return "", fmt.Errorf("a username is required for the login broker account")
	}
	userID, err := c.ensureMachineUser(ctx, username)
	if err != nil {
		return "", err
	}
	if err := c.EnsureLoginClientRole(ctx, userID); err != nil {
		return "", err
	}
	return userID, nil
}

// mintBrokerToken issues a new personal access token for the broker account.
func (c *Client) mintBrokerToken(ctx context.Context, username, userID string) (BrokerCredential, error) {
	body, err := c.postJSON(ctx, "/management/v1/users/"+url.PathEscape(userID)+"/pats", map[string]any{})
	if err != nil {
		return BrokerCredential{}, fmt.Errorf("mint a token for %q: %w", username, err)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return BrokerCredential{}, fmt.Errorf("decode token: %w", err)
	}
	if out.Token == "" {
		return BrokerCredential{}, fmt.Errorf("the issuer accepted the token request for %q but returned no token", username)
	}
	return BrokerCredential{UserID: userID, Token: out.Token}, nil
}

// ensureMachineUser finds a machine user by username, creating it if
// absent. Read-before-write, so re-running adopts the existing account
// rather than failing on the uniqueness constraint.
func (c *Client) ensureMachineUser(ctx context.Context, username string) (string, error) {
	body, err := c.postJSON(ctx, "/v2/users", map[string]any{
		"queries": []any{
			map[string]any{"userNameQuery": map[string]any{"userName": username}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("search for the login broker account: %w", err)
	}
	var found struct {
		Result []struct {
			UserID string `json:"userId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &found); err != nil {
		return "", fmt.Errorf("decode user search: %w", err)
	}
	if len(found.Result) > 0 {
		return found.Result[0].UserID, nil
	}

	created, err := c.postJSON(ctx, "/management/v1/users/machine", map[string]any{
		"userName":        username,
		"name":            username,
		"description":     "Completes browser sign-in via the API-only login flow (forge)",
		"accessTokenType": "ACCESS_TOKEN_TYPE_BEARER",
	})
	if err != nil {
		return "", fmt.Errorf("create the login broker account %q: %w", username, err)
	}
	var out struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(created, &out); err != nil {
		return "", fmt.Errorf("decode created user: %w", err)
	}
	if out.UserID == "" {
		return "", fmt.Errorf("the issuer created %q but returned no user id", username)
	}
	return out.UserID, nil
}

// SetLoginUI points the instance's login UI at loginURI — the app's OWN
// sign-in route — so /oauth/v2/authorize redirects there instead of
// rendering the issuer's bundled pages.
//
// This is the switch that makes the whole flow possible, and it is why a
// project that merely calls CreateSession still ends up on the issuer's
// login screen. Empty loginURI restores the issuer-hosted UI.
//
// Enabling it also changes the SHAPE of the auth request the issuer mints
// (v2 rather than v1), which is what FinalizeAuthRequest requires. That
// coupling is the reason both live in this file.
func (c *Client) SetLoginUI(ctx context.Context, loginURI string) error {
	feature := map[string]any{"required": loginURI != ""}
	if loginURI != "" {
		feature["baseUri"] = loginURI
	}

	_, err := c.putJSON(ctx, "/v2/features/instance", map[string]any{"loginV2": feature})
	if err != nil && !isNoChangesErr(err) {
		return fmt.Errorf("point the login UI at %q: %w", loginURI, err)
	}
	return nil
}
