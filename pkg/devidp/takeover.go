package devidp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// ── Pre-registration takeover, ruled out by construction ────────────────
//
// THE ATTACK: someone registers victim@example.com with a password of their
// own, or links their own social account to it. The account is unverified.
// When the owner of the inbox later proves the address, the proof must not
// hand them — or leave the squatter — an account the squatter set up.
//
// The broker used to answer that by EVICTING: every proof took the account's
// credentials back, before the proof as well as after it, under a lock, a
// time fence and finally a cross-replica lock (forge#1821, #2098, #2166). A
// proof that FAILED still evicted, and a stale eviction could land on a
// credential the owner had just set (forge#2121). The class of bug existed
// because the state existed. Now it cannot:
//
//  1. NO CREDENTIAL IS EVER PUT ON AN UNVERIFIED ACCOUNT. An account may
//     exist before its address is proven — that is how the issuer mails the
//     code — but it holds nothing: a sign-up's password and an unverified
//     provider's link wait, sealed in the browser that chose them, until that
//     browser proves the address. The Client calls that add a credential
//     refuse an unverified account (ErrUnverifiedAccount), so no path can
//     slip one in.
//  2. A PROOF NEVER VERIFIES AN ACCOUNT THAT HOLDS A CREDENTIAL. Such an
//     account predates this rule, or is a contact-only account (a sign-up
//     where no mail could prove the address, MetaContactOnly). Its
//     credentials prove nothing, and taking them away needs calls the issuer
//     cannot aim (its TOTP removal names no factor). So it is left exactly as
//     it is, and refused at the proof (errHoldsCredentials). The owner
//     proves into a FRESH, credential-less account instead, which takes the
//     address over once proven (fresh_account.go). Nothing is deleted.
//
// So a proof's only write is the issuer's own verification of a
// credential-less account. A failed or abandoned proof changes nothing, and
// there is nothing to evict, lock, fence or serialise.

// maxPasswordBytes bounds a password the sign-up routes accept, so a sealed
// binding that carries one keeps its fixed size.
const maxPasswordBytes = 256

// errHoldsCredentials refuses a proof of an unverified account that holds a
// credential: nothing proves who set it, so the address is proven into a
// fresh account instead (or, for an invitation, by an administrator).
var errHoldsCredentials = errors.New("the account holds credentials its address never proved")

// ErrUnverifiedAccount is returned, on a ProvenCredentialsOnly client, by
// every call that would add a credential — a password where there was none,
// a passkey, a social link, a one-time-code method — to an account whose
// address is not verified. The call is refused before it writes anything.
var ErrUnverifiedAccount = errors.New("the account's address is not verified: no credential may be added to it")

// proveAddress runs proof — the issuer's check of an emailed code, which also
// verifies the address — for the account before was read as. It writes
// nothing of its own. A proof of an unverified account that holds any
// credential is refused with errHoldsCredentials before the issuer is asked:
// verifying it would make whatever was set on it before the proof usable.
//
// Nothing can add a credential between that read and the proof: every call
// that adds one refuses an unverified account.
//
// A supplied (deprecated) ProofLocker is taken around the proof of an
// unverified account, as it always was; one that cannot be taken refuses it.
func (r *SignInRoutes) proveAddress(ctx context.Context, before User, proof func() error) error {
	if before.EmailVerified {
		return proof()
	}
	release, err := r.lockAccount(ctx, before.ID)
	if err != nil {
		return err
	}
	defer release()
	return r.proveAddressHeld(ctx, before, proof)
}

// proveAddressHeld is proveAddress for a caller that already holds the
// account's proof lock (HandleVerify, whose hold covers the installs too).
func (r *SignInRoutes) proveAddressHeld(ctx context.Context, before User, proof func() error) error {
	if before.EmailVerified {
		return proof()
	}
	held, err := r.cfg.Client.credentialKinds(ctx, before.ID)
	if err != nil {
		return fmt.Errorf("read the account's credentials before its proof: %w", err)
	}
	if len(held) > 0 {
		r.cfg.Logger.InfoContext(ctx, "address proof refused: the unverified account holds credentials nobody proved", "user_id", before.ID, "credentials", held)
		return errHoldsCredentials
	}
	return proof()
}

// credentialKinds lists the sign-in methods an account holds, as the issuer
// names them (AUTHENTICATION_METHOD_TYPE_PASSWORD, _PASSKEY, _IDP, _TOTP,
// _U2F, _OTP_SMS, _OTP_EMAIL). Empty: a credential-less account.
func (c *Client) credentialKinds(ctx context.Context, userID string) ([]string, error) {
	body, err := c.getJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/authentication_methods")
	if err != nil {
		return nil, fmt.Errorf("list authentication methods: %w", err)
	}
	var out struct {
		AuthMethodTypes []string `json:"authMethodTypes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode authentication methods: %w", err)
	}
	return out.AuthMethodTypes, nil
}

// requireVerified refuses (ErrUnverifiedAccount) an account whose address is
// not verified, when the client is ProvenCredentialsOnly. Every Client call
// that adds a credential runs it first. A read that fails refuses too: an
// account nobody could read is not known to be verified.
func (c *Client) requireVerified(ctx context.Context, userID string) error {
	if !c.ProvenCredentialsOnly {
		return nil
	}
	u, err := c.FindUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("read the account before adding a credential: %w", err)
	}
	if !u.EmailVerified {
		return ErrUnverifiedAccount
	}
	return nil
}

// newNonce is 128 random bits, URL-safe.
func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// grantSignupRoleIfSelfSignup grants signup_role to an account public
// sign-up created (MetaSelfSignup), once (MetaSignupRoleGranted), and only
// when the policy still lets its now-verified address sign up. An invited or
// declared account, or a re-verification, gets nothing.
func (r *SignInRoutes) grantSignupRoleIfSelfSignup(ctx context.Context, userID, email string) {
	if r.cfg.Policy.SignupRole == "" || r.cfg.ProjectID == "" || !r.cfg.Policy.SignupAllows(email) {
		return
	}
	c := r.cfg.Client
	self, err := c.HasMetadata(ctx, userID, MetaSelfSignup)
	if err != nil || !self {
		if err != nil {
			r.cfg.Logger.ErrorContext(ctx, "could not read how the account was created; no sign-up role", "user_id", userID, "error", err)
		}
		return
	}
	if done, err := c.HasMetadata(ctx, userID, MetaSignupRoleGranted); err != nil || done {
		return
	}
	if !r.grantSignupRole(ctx, userID) {
		return
	}
	if err := c.SetMetadata(ctx, userID, MetaSignupRoleGranted, "1"); err != nil {
		r.cfg.Logger.WarnContext(ctx, "sign-up role granted but not recorded", "user_id", userID, "error", err)
	}
}
