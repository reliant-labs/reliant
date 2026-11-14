package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Challenge sessions: the sign-in methods that answer a challenge rather than
// present a secret — a passkey assertion, an emailed one-time code.
//
// They take two calls. The first creates a session with a USER CHECK and a
// challenge; the second presents the answer. Between the two the session
// holds a user check and NOTHING ELSE — exactly the session the file header of
// login.go warns about, the one the issuer will finalize into real tokens for
// someone who proved nothing.
//
// So the two halves are kept apart by type. A ChallengeSession cannot be
// finalized: nothing accepts one. Only SetSession's answer turns it into a
// Session, and CompleteSignIn — the one way to finalize a session this file
// offers — reads the session back from the issuer and refuses it unless a
// verifying factor is recorded on it. A handler that skipped the answer, or
// forwarded a forged one the issuer rejected, gets an error, not tokens.

// ErrEmailUnverified means the credentials were right but the account's
// address is not verified, and the policy issues no session for one. The
// sign-in screen offers to resend the verification mail.
var ErrEmailUnverified = errors.New("the account's email address is not verified")

// ErrNoVerifiedFactor means a session reached CompleteSignIn without any
// verifying factor recorded on it: the bypass, refused.
var ErrNoVerifiedFactor = errors.New("refusing to finalize a session with no verified factor")

// ErrInvalidCode means the issuer rejected a one-time code: wrong, expired or
// already used. One error for all three, as for ErrInvalidResetCode.
var ErrInvalidCode = errors.New("the code is not valid")

// ChallengeSession is a session the issuer created with a user check and a
// challenge, waiting for its answer. It is NOT a Session: it cannot be
// finalized, and holding one proves nothing.
type ChallengeSession struct {
	ID    string
	Token string
	// WebAuthnOptions is the passkey challenge (the issuer's
	// publicKeyCredentialRequestOptions), for navigator.credentials.get.
	WebAuthnOptions json.RawMessage
	// OTPEmailCode is set only when the code was requested with ReturnCode.
	OTPEmailCode string
}

// UserVerificationRequired is the only user-verification requirement this
// package requests for a passkey: the authenticator must check its owner
// (PIN, biometric), so a passkey is a whole factor, not just possession.
const UserVerificationRequired = "USER_VERIFICATION_REQUIREMENT_REQUIRED"

// Challenge is what a challenge session asks for. Exactly one field is set.
type Challenge struct {
	// WebAuthnDomain requests a passkey assertion for this relying-party
	// id (the app's host or a parent of it — never the issuer's).
	WebAuthnDomain string
	// OTPEmailURLTemplate requests an emailed one-time code; the mail links
	// to this template ({{.Code}}, {{.UserID}}, {{.SessionID}} …).
	OTPEmailURLTemplate string
	// OTPEmailReturnCode requests the one-time code back instead of mailed.
	// Only for a caller that has ALREADY verified a proof in the same
	// request (an email-verification code the person presented): it is how
	// that proof becomes a session.
	OTPEmailReturnCode bool
}

func (ch Challenge) body() (map[string]any, error) {
	set := 0
	out := map[string]any{}
	if d := strings.TrimSpace(ch.WebAuthnDomain); d != "" {
		set++
		out["webAuthN"] = map[string]any{"domain": d, "userVerificationRequirement": UserVerificationRequired}
	}
	if t := ch.OTPEmailURLTemplate; t != "" {
		set++
		out["otpEmail"] = map[string]any{"sendCode": map[string]any{"urlTemplate": t}}
	}
	if ch.OTPEmailReturnCode {
		set++
		out["otpEmail"] = map[string]any{"returnCode": map[string]any{}}
	}
	if set != 1 {
		return nil, fmt.Errorf("a challenge session requests exactly one challenge, got %d", set)
	}
	return out, nil
}

// CreateChallengeSession creates a session for a user (by login name or id)
// and requests one challenge.
func (c *Client) CreateChallengeSession(ctx context.Context, loginName, userID string, ch Challenge) (ChallengeSession, error) {
	user, err := userCheck(loginName, userID)
	if err != nil {
		return ChallengeSession{}, err
	}
	challenges, err := ch.body()
	if err != nil {
		return ChallengeSession{}, err
	}
	body, err := c.postJSON(ctx, "/v2/sessions", map[string]any{
		"checks":     map[string]any{"user": user},
		"challenges": challenges,
	})
	if err != nil {
		return ChallengeSession{}, fmt.Errorf("start a challenge session: %w", err)
	}
	var out struct {
		SessionID    string `json:"sessionId"`
		SessionToken string `json:"sessionToken"`
		Challenges   struct {
			WebAuthN *struct {
				Options json.RawMessage `json:"publicKeyCredentialRequestOptions"`
			} `json:"webAuthN"`
			OTPEmail string `json:"otpEmail"`
		} `json:"challenges"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ChallengeSession{}, fmt.Errorf("decode challenge session: %w", err)
	}
	if out.SessionID == "" || out.SessionToken == "" {
		return ChallengeSession{}, fmt.Errorf("the issuer returned an incomplete challenge session")
	}
	cs := ChallengeSession{ID: out.SessionID, Token: out.SessionToken, OTPEmailCode: out.Challenges.OTPEmail}
	if out.Challenges.WebAuthN != nil {
		cs.WebAuthnOptions = out.Challenges.WebAuthN.Options
	}
	if ch.WebAuthnDomain != "" && len(cs.WebAuthnOptions) == 0 {
		return ChallengeSession{}, fmt.Errorf("the issuer returned no passkey challenge")
	}
	if ch.OTPEmailReturnCode && cs.OTPEmailCode == "" {
		return ChallengeSession{}, fmt.Errorf("the issuer returned no one-time code")
	}
	return cs, nil
}

// Answer is the response to a challenge. Exactly one field is set.
type Answer struct {
	// WebAuthnAssertion is the browser's PublicKeyCredential as JSON. The
	// CALLER must have checked its clientDataJSON origin and type first
	// (CheckClientData): the issuer verifies the signature and the
	// challenge, but accepts whatever origin the client reports.
	WebAuthnAssertion json.RawMessage
	// OTPEmailCode is the emailed one-time code.
	OTPEmailCode string
}

// SetSession presents the answer to a challenge session and returns the
// session it became. A rejected one-time code is ErrInvalidCode.
func (c *Client) SetSession(ctx context.Context, cs ChallengeSession, a Answer) (Session, error) {
	if cs.ID == "" || cs.Token == "" {
		return Session{}, fmt.Errorf("a challenge session is required")
	}
	checks := map[string]any{}
	switch {
	case len(a.WebAuthnAssertion) > 0 && a.OTPEmailCode == "":
		checks["webAuthN"] = map[string]any{"credentialAssertionData": a.WebAuthnAssertion}
	case strings.TrimSpace(a.OTPEmailCode) != "" && len(a.WebAuthnAssertion) == 0:
		checks["otpEmail"] = map[string]any{"code": strings.TrimSpace(a.OTPEmailCode)}
	default:
		// No answer is not "check nothing": it is refused.
		return Session{}, fmt.Errorf("refusing to update a challenge session with no answer")
	}
	body, err := c.sendJSON(ctx, http.MethodPatch, "/v2/sessions/"+url.PathEscape(cs.ID), map[string]any{
		"sessionToken": cs.Token,
		"checks":       checks,
	})
	if err != nil {
		var apiErr *apiError
		if a.OTPEmailCode != "" && errors.As(err, &apiErr) && apiErr.status < 500 {
			return Session{}, fmt.Errorf("%w: %s", ErrInvalidCode, err.Error())
		}
		return Session{}, fmt.Errorf("answer the challenge: %w", err)
	}
	var out struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}
	if out.SessionToken == "" {
		return Session{}, fmt.Errorf("the issuer accepted the answer but returned no session token")
	}
	return Session{ID: cs.ID, Token: out.SessionToken}, nil
}

// SessionFactors is what the issuer recorded on a session.
type SessionFactors struct {
	UserID string
	// Verified names the verifying factors with a verification time:
	// password, webAuthN, intent, totp, otpEmail, otpSms.
	Verified []string
}

// GetSessionFactors reads a session back from the issuer.
func (c *Client) GetSessionFactors(ctx context.Context, s Session) (SessionFactors, error) {
	body, err := c.getJSON(ctx, "/v2/sessions/"+url.PathEscape(s.ID)+"?sessionToken="+url.QueryEscape(s.Token))
	if err != nil {
		return SessionFactors{}, fmt.Errorf("read session: %w", err)
	}
	type factor struct {
		VerifiedAt string `json:"verifiedAt"`
	}
	var out struct {
		Session struct {
			Factors struct {
				User *struct {
					ID         string `json:"id"`
					VerifiedAt string `json:"verifiedAt"`
				} `json:"user"`
				Password *factor `json:"password"`
				WebAuthN *factor `json:"webAuthN"`
				Intent   *factor `json:"intent"`
				TOTP     *factor `json:"totp"`
				OTPEmail *factor `json:"otpEmail"`
				OTPSMS   *factor `json:"otpSms"`
			} `json:"factors"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return SessionFactors{}, fmt.Errorf("decode session: %w", err)
	}
	f := out.Session.Factors
	var sf SessionFactors
	if f.User != nil {
		sf.UserID = f.User.ID
	}
	for name, v := range map[string]*factor{
		"password": f.Password, "webAuthN": f.WebAuthN, "intent": f.Intent,
		"totp": f.TOTP, "otpEmail": f.OTPEmail, "otpSms": f.OTPSMS,
	} {
		if v != nil && v.VerifiedAt != "" {
			sf.Verified = append(sf.Verified, name)
		}
	}
	return sf, nil
}

// DeleteSession ends a session at the issuer. Best effort: a caller that
// refuses a session deletes it so nothing can finalize it later.
func (c *Client) DeleteSession(ctx context.Context, s Session) error {
	_, err := c.sendJSON(ctx, http.MethodDelete, "/v2/sessions/"+url.PathEscape(s.ID), map[string]any{"sessionToken": s.Token})
	return err
}

// CompleteSignIn turns an authenticated session into tokens: it begins an
// auth request, checks the session, finalizes and redeems.
//
// THE CHECK is the point. The session is read back from the issuer and
// refused (ErrNoVerifiedFactor) unless a verifying factor is recorded on it.
// With cfg.RequireVerifiedEmail it is also refused (ErrEmailUnverified) while
// the account's address is unverified. Both refusals delete the session.
func (c *Client) CompleteSignIn(ctx context.Context, cfg FlowConfig, s Session) (Tokens, error) {
	if s.ID == "" || s.Token == "" {
		return Tokens{}, fmt.Errorf("a session is required")
	}
	factors, err := c.GetSessionFactors(ctx, s)
	if err != nil {
		return Tokens{}, err
	}
	if len(factors.Verified) == 0 {
		_ = c.DeleteSession(ctx, s)
		return Tokens{}, ErrNoVerifiedFactor
	}
	if err := c.gate(ctx, cfg, s, factors.UserID); err != nil {
		return Tokens{}, err
	}
	flow, err := c.BeginFlow(ctx, cfg)
	if err != nil {
		return Tokens{}, err
	}
	return c.finish(ctx, cfg, flow, s, methodFromFactors(factors.Verified))
}

// gate applies the verified-email rule to a session's user.
func (c *Client) gate(ctx context.Context, cfg FlowConfig, s Session, userID string) error {
	if !cfg.RequireVerifiedEmail {
		return nil
	}
	if userID == "" {
		factors, err := c.GetSessionFactors(ctx, s)
		if err != nil {
			return err
		}
		userID = factors.UserID
	}
	user, err := c.FindUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("check the account's address: %w", err)
	}
	if !user.EmailVerified {
		_ = c.DeleteSession(ctx, s)
		return ErrEmailUnverified
	}
	return nil
}

// finish finalizes the auth request with the session and redeems the code.
// A redeemed code is a sign-in: it records auth_signed_in (auth_events.go),
// with the method WithAuthMethod named, else the one method says.
func (c *Client) finish(ctx context.Context, cfg FlowConfig, flow Flow, s Session, method observe.AuthMethod) (Tokens, error) {
	callback, err := c.FinalizeAuthRequest(ctx, flow.AuthRequestID, s)
	if err != nil {
		return Tokens{}, err
	}
	code, err := codeFromCallback(callback)
	if err != nil {
		return Tokens{}, err
	}
	tokens, err := c.RedeemCode(ctx, cfg, code, flow.PKCE)
	if err != nil {
		return Tokens{}, err
	}
	reportSignIn(ctx, tokens, method)
	return tokens, nil
}

// UserInfo is the subject of an access token, as the issuer's userinfo
// endpoint reports it.
//
// The profile claims (Name through Picture) are filled when the token's
// scopes grant them (`profile`); each is empty otherwise. They are for
// display: what the person, or an administrator, typed.
type UserInfo struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"-"`

	Name              string `json:"name,omitempty"`
	GivenName         string `json:"given_name,omitempty"`
	FamilyName        string `json:"family_name,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Picture           string `json:"picture,omitempty"`
}

// UserInfo asks the issuer who an access token belongs to. The issuer
// validates the token; an invalid or expired one is an error. This is how a
// signed-in-only route learns its user: from the token, never a request body.
func (c *Client) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return UserInfo{}, fmt.Errorf("an access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Base, "/")+"/oidc/v1/userinfo", nil)
	if err != nil {
		return UserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	body, err := c.do(c.withHost(req))
	if err != nil {
		return UserInfo{}, fmt.Errorf("userinfo: %w", err)
	}
	var out struct {
		UserInfo
		EmailVerified any `json:"email_verified"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return UserInfo{}, fmt.Errorf("decode userinfo: %w", err)
	}
	if out.Subject == "" {
		return UserInfo{}, fmt.Errorf("the issuer's userinfo named no subject")
	}
	info := out.UserInfo
	info.EmailVerified = out.EmailVerified == true || out.EmailVerified == "true"
	return info, nil
}
