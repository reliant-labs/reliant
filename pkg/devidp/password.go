package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Password reset for the API-only sign-in flow: redeeming a one-time code
// for a new password, with the browser still never contacting the issuer.
//
// ── Where the code comes from is NOT this file's business ────────────────
//
// Zitadel issues a password-reset code either by mailing it (its default,
// which needs SMTP) or, with `returnCode`, by handing it back to whoever
// asked. Who may ask is a deployment decision — a privileged operator
// command, a mail sender, a support tool — so this file only REDEEMS codes.
// Minting one belongs to whatever holds the management credential, never to
// a request path.
//
// ── THE HAZARD THIS FILE EXISTS TO CONTAIN ──────────────────────────────
//
// The login broker's credential (IAM_LOGIN_CLIENT — see EnsureLoginBroker)
// is allowed to call SetPassword WITHOUT a verification code, and Zitadel
// then simply overwrites the password. Verified against v4.19.4: the broker
// token set a new password on another user with no code in the body, HTTP
// 200. The same token can also mint a reset code with `returnCode`.
//
// So the broker credential can already take over any account, and a reset
// handler's safety rests ENTIRELY on the code it forwards being one the
// PERSON supplied. ResetPassword therefore refuses an empty code before any
// network call, exactly as buildChecks refuses a passwordless session: a
// handler that forwarded a blank field would otherwise turn a reset form
// into "set anyone's password", and the response would look like success.
//
// It also matters what Zitadel does NOT do: it does not burn a code after
// failed attempts (verified: eight wrong codes, then the right one still
// worked). A six-character code with unlimited guesses is a brute-force
// target, so an attempt cap is the CALLER's job.

// ErrUserNotFound is returned by FindUserByEmail when no human user has that
// address — or when more than one does, which is treated the same way: a
// reset keyed by an ambiguous email cannot know whose password it is
// changing.
var ErrUserNotFound = errors.New("no single user has that email address")

// ErrInvalidResetCode means the issuer rejected the reset code: wrong,
// expired, or already used. Deliberately one error for all three — a form
// that tells "expired" from "wrong" teaches a guesser when to stop.
var ErrInvalidResetCode = errors.New("the reset code is not valid for this user")

// PasswordPolicyError means the code was ACCEPTED but the new password
// failed the issuer's password policy. The code is still valid afterwards
// (verified), so the person should pick a different password, not ask for a
// new code — which is why a caller must be able to tell this apart from
// ErrInvalidResetCode.
type PasswordPolicyError struct {
	// Rule is the complexity rule the issuer reported, or "" when it
	// reported one this package does not know. The issuer names only the
	// first rule a password breaks; PasswordPolicy.Unmet lists them all.
	Rule PasswordRule
	// Reason is the issuer's own description ("Password is too short"),
	// safe and useful to show the person who typed it.
	Reason string
}

func (e *PasswordPolicyError) Error() string {
	return "the new password does not meet the password policy: " + e.Reason
}

// User is the subset of an issuer account a sign-in or reset flow needs.
type User struct {
	ID string
	// LoginName is what CreateSession identifies the account by, and it is
	// NOT necessarily the email. An account created with a bare username
	// gets a login name of `<username>@<org domain>` — the seeded admin of
	// a forge dev IdP is `admin@<org>.localhost` while its email is the
	// address people actually type. Resolve through FindUserByEmail
	// rather than passing a typed email to CreateSession.
	LoginName string
	// Username is the account's username, which its login name derives
	// from. Accounts the broker creates take the address as their username.
	Username string
	Email    string
	// EmailVerified is the issuer's own verdict on Email
	// (human.email.isVerified): true only once the address was proven
	// reachable, by a verification code or by whoever created the account
	// saying so. An account created through public sign-up holds an address
	// the person TYPED, so this is false until they verify it.
	//
	// Check it before binding anything to Email — an invitation, a tenant
	// membership, an account merge. Without it, registering someone else's
	// address is enough to claim whatever was waiting for them.
	EmailVerified bool
	// Locked: the issuer refuses every sign-in to the account.
	Locked bool
	// PasswordChanged is when the account's password was last set, as the
	// issuer records it (human.passwordChanged); zero when it has none or
	// the issuer did not say.
	PasswordChanged time.Time
}

// humanUser is the part of a v2 user resource User is read from, shared by
// the search and the get so the two cannot disagree about a field.
type humanUser struct {
	UserID             string `json:"userId"`
	State              string `json:"state"`
	PreferredLoginName string `json:"preferredLoginName"`
	Username           string `json:"username"`
	Human              *struct {
		Email struct {
			Email      string `json:"email"`
			IsVerified bool   `json:"isVerified"`
		} `json:"email"`
		PasswordChanged time.Time `json:"passwordChanged"`
	} `json:"human"`
}

// user converts a resource into a User; ok is false for a machine account
// (no human part) or one with no id.
func (h humanUser) user() (User, bool) {
	if h.Human == nil || h.UserID == "" {
		return User{}, false
	}
	return User{
		ID:              h.UserID,
		LoginName:       h.PreferredLoginName,
		Username:        h.Username,
		Email:           h.Human.Email.Email,
		EmailVerified:   h.Human.Email.IsVerified,
		Locked:          h.State == "USER_STATE_LOCKED",
		PasswordChanged: h.Human.PasswordChanged,
	}, true
}

// FindUserByEmail resolves an email address to the one human user that has
// it, case-insensitively; none, or several, is ErrUserNotFound. On a
// ProvenCredentialsOnly client, where several share it, the one whose address
// is VERIFIED is that user — a proven owner. Several share an address while a
// fresh account takes it over from one holding unproven credentials (see
// fresh_account.go); the proven one is the one to act on.
//
// The broker's login-client credential may search users, so this works with
// the same token SignIn uses.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return User{}, fmt.Errorf("an email address is required")
	}

	body, err := c.postJSON(ctx, "/v2/users", map[string]any{
		"queries": []any{
			map[string]any{"emailQuery": map[string]any{
				"emailAddress": email,
				"method":       "TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE",
			}},
		},
	})
	if err != nil {
		return User{}, fmt.Errorf("search users by email: %w", err)
	}

	var out struct {
		Result []humanUser `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return User{}, fmt.Errorf("decode user search: %w", err)
	}

	var matches []User
	for _, r := range out.Result {
		u, ok := r.user()
		if !ok {
			continue // machine accounts have no email and cannot reset one
		}
		matches = append(matches, u)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if !c.ProvenCredentialsOnly {
		return User{}, ErrUserNotFound
	}
	// Zitadel does not enforce email uniqueness by default. Zero, or
	// several with no single verified one, is "no single account to act on".
	var verified []User
	for _, u := range matches {
		if u.EmailVerified {
			verified = append(verified, u)
		}
	}
	if len(verified) == 1 {
		return verified[0], nil
	}
	return User{}, ErrUserNotFound
}

// FindUserByID resolves a user id to the human user it names. ErrUserNotFound
// when there is none, or when it is a machine account.
func (c *Client) FindUserByID(ctx context.Context, id string) (User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return User{}, fmt.Errorf("a user id is required")
	}
	body, err := c.getJSON(ctx, "/v2/users/"+url.PathEscape(id))
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status == 404 {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("get user: %w", err)
	}
	var out struct {
		User humanUser `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return User{}, fmt.Errorf("decode user: %w", err)
	}
	u, ok := out.User.user()
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

// loginNameFor resolves what a person typed into the login name the issuer
// creates sessions by.
//
// They are not always the same string. An account created with a bare
// username gets a login name of `<username>@<org domain>` — the seeded admin
// of a forge dev IdP is `admin@<org>.localhost` while its email is the
// address people actually type — and Zitadel answers a session for the
// EMAIL with "User could not be found (QUERY-Dfbg2)". Observed in prod: the
// only operator account could not sign in by email at all.
//
// Only something that looks like an email is looked up, and anything that
// does not resolve to exactly one account is passed through unchanged, so a
// login name typed directly still works. A failed lookup falls back the same
// way rather than failing the sign-in: the session check that follows is the
// authority, and it reports its own error.
func (c *Client) loginNameFor(ctx context.Context, typed string) string {
	typed = strings.TrimSpace(typed)
	if !strings.Contains(typed, "@") {
		return typed
	}
	user, err := c.FindUserByEmail(ctx, typed)
	if err != nil || user.LoginName == "" {
		return typed
	}
	return user.LoginName
}

// SendPasswordResetLink asks the issuer to EMAIL the user a link to set a new
// password. urlTemplate is the link's shape, with the issuer's placeholders:
//
//	https://app.example.com/auth/reset?userID={{.UserID}}&code={{.Code}}&orgID={{.OrgID}}
//
// ── This sends mail only if the instance has an SMTP provider ───────────
//
// The call SUCCEEDS either way. An instance with no SMTP configuration (every
// customer instance, unless its environment declares `smtp`) accepts the
// request, queues a notification nothing can deliver, and tells the caller
// nothing. A nil error therefore means "the issuer accepted the request", never
// "an email arrived".
//
// The code travels only in the email. This function never returns it, so a
// request handler that calls it cannot leak one to the person asking.
func (c *Client) SendPasswordResetLink(ctx context.Context, userID, urlTemplate string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("a user id is required to request a password reset")
	}
	link := map[string]any{"notificationType": "NOTIFICATION_TYPE_Email"}
	if urlTemplate != "" {
		link["urlTemplate"] = urlTemplate
	}
	if _, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/password_reset", map[string]any{"sendLink": link}); err != nil {
		return fmt.Errorf("request password reset link: %w", err)
	}
	return nil
}

// MintPasswordResetCode asks the issuer for a reset code and returns it to
// the CALLER instead of mailing it.
//
// THE RETURNED CODE IS THE CREDENTIAL. Whoever holds it can set the user's
// password. It belongs in a server log in development, or in a channel the
// account's owner controls (an SMS, a chat message, an admin screen only they
// can see) — never in a response to the request that asked for it, which is
// anyone on the internet. A handler that returned it from a "forgot password"
// endpoint would turn that endpoint into "take over any account".
func (c *Client) MintPasswordResetCode(ctx context.Context, userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("a user id is required to request a password reset")
	}
	body, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/password_reset", map[string]any{"returnCode": map[string]any{}})
	if err != nil {
		return "", fmt.Errorf("mint password reset code: %w", err)
	}
	var out struct {
		VerificationCode string `json:"verificationCode"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode password reset code: %w", err)
	}
	if out.VerificationCode == "" {
		return "", fmt.Errorf("the issuer accepted the reset request for user %q but returned no code", userID)
	}
	return out.VerificationCode, nil
}

// PasswordReset is one redemption of a reset code. Every field is required.
type PasswordReset struct {
	UserID      string
	Code        string
	NewPassword string
}

// ResetPassword sets a user's password using a one-time reset code.
//
// A missing field is refused BEFORE any network call — above all a missing
// Code. See the file header: with the broker's credential, the issuer would
// accept a code-less request and overwrite the password of whichever user
// is named. There is no safe default for "which code did the person
// present".
//
// Errors a caller should branch on:
//   - ErrInvalidResetCode (wrapped): wrong, expired or used code;
//   - *PasswordPolicyError: code fine, password rejected — the code is
//     still usable.
//
// Anything else is the issuer or the credential failing, and is returned
// with the issuer's own message.
func (c *Client) ResetPassword(ctx context.Context, r PasswordReset) error {
	return c.resetPassword(ctx, r, true)
}

// resetPassword redeems r. proves says whether the code is one a person
// presented or the broker minted after a proof of the address: only then does
// the redemption mark an unverified account's address verified. SetPassword
// passes false, because the code it redeems is its own and proves nothing.
func (c *Client) resetPassword(ctx context.Context, r PasswordReset, proves bool) error {
	userID := strings.TrimSpace(r.UserID)
	code := strings.TrimSpace(r.Code)
	switch {
	case userID == "":
		return fmt.Errorf("a user id is required to reset a password")
	case code == "":
		return fmt.Errorf("refusing to reset the password of user %q with no reset code: "+
			"the issuer would accept it and overwrite the password without proof of anything", userID)
	case r.NewPassword == "":
		return fmt.Errorf("a new password is required")
	}
	gate, err := c.passwordGate(ctx, userID)
	if err != nil {
		return err
	}
	// firstPassword: this redemption installs the account's FIRST password,
	// or completes one whose verification failed after it was installed.
	// Only then may a presented code verify the address: the account held
	// nothing nobody proved. See firstPasswordAttempt.
	firstPassword, attempt := false, ""
	switch {
	case gate == gateFirstPassword && proves:
		disqualified, derr := c.firstPasswordDisqualified(ctx, userID)
		if derr != nil {
			return derr
		}
		if disqualified {
			break // installs the password; verifies nothing
		}
		if attempt, err = c.recordFirstPasswordAttempt(ctx, userID); err != nil {
			return err
		}
		firstPassword = true
	case gate == gateUnverifiedPassword && proves:
		var disqualify bool
		if firstPassword, disqualify, err = c.passwordIsAFirstPasswordAttempt(ctx, userID); err != nil {
			return err
		}
		if disqualify {
			// Before the password is replaced, or a later reset would read
			// the replacement as a first password (forge#2179 review).
			if err := c.disqualifyFirstPassword(ctx, userID); err != nil {
				return err
			}
		}
	}

	_, err = c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/password", map[string]any{
		"newPassword": map[string]any{
			"password":       r.NewPassword,
			"changeRequired": false,
		},
		"verificationCode": code,
	})
	if err != nil {
		err = classifyResetError(err)
		if attempt != "" && errors.Is(err, ErrInvalidResetCode) {
			// The issuer refused this code outright, so THIS attempt
			// installed nothing: its own record goes. Another request's
			// record is never touched, and an ambiguous failure (a timeout,
			// a lost answer, a 5xx) keeps this one: the password may be in.
			_ = c.DeleteMetadata(context.WithoutCancel(ctx), userID, attempt)
		}
		return err
	}
	if gate == gateOpen || !proves {
		return nil
	}
	if !firstPassword {
		// A password-only account this broker did not give its first
		// password, or one disqualified for having held such a password:
		// whoever registered the address set it, and may hold a session it
		// passed before this reset. Replacing the password is the holder's
		// recovery; the address is never verified on it, which would make
		// that session usable (forge#2179 review). Its owner proves into a
		// fresh account instead.
		return nil
	}
	// The issuer accepted the code, and a reset code reaches a person only
	// in a mail to the account's address or from an administrator who
	// issued it (a console invitation): either way the address is now
	// proven, so it is marked verified, as AdoptPendingAccount would.
	//
	// The password is already set, so this must not be lost to the request
	// going away: it runs without the caller's cancellation (each issuer call
	// keeps its own timeout). If it fails anyway, the account is unverified
	// and holds only a password, which passwordGate reports as
	// gateUnverifiedPassword: redeeming a fresh reset code on the SAME
	// account marks it verified then, so nothing is stranded (forge#2121).
	if err := c.markAddressVerified(context.WithoutCancel(ctx), userID); err != nil {
		return fmt.Errorf("the password was set, but the address its reset code proved could not be marked verified "+
			"(redeeming a new reset code completes it): %w", err)
	}
	// Inert on a verified account if this fails.
	c.clearFirstPasswordAttempts(context.WithoutCancel(ctx), userID)
	return nil
}

// ── First-password install attempts ─────────────────────────────────────
//
// A presented reset code redeemed on an unverified, credential-less account
// installs its first password and then verifies the address. If the
// verification fails, or the issuer's answer to the password write is lost,
// the account is left unverified holding a password — indistinguishable,
// by its credentials alone, from a squatter's pre-registered account, which
// a reset must never verify. So each install records an ATTEMPT first, under
// its own key (MetaFirstPasswordPrefix + nonce, value its start in unix
// milliseconds): "this broker began installing a first password here".
//
// A later presented code verifies a password-only account only when an
// attempt exists AND the account's password was set at or after the
// EARLIEST attempt began (the issuer's passwordChanged, less
// firstPasswordClockSkew). There is no expiry: an invitee whose verification
// failed during an outage and who comes back days later must still recover
// the SAME account, with its roles. Safety rests on two facts instead:
//
//   - An attempt is recorded only while the account holds NO credential, so
//     a password set before it — a squatter's, from a pre-registration —
//     predates every attempt and never qualifies.
//   - Once recorded, a password reaches the account only through a reset
//     code, which is mailed to the address or handed out by an
//     administrator; no forge path sets a password on an existing unverified
//     account otherwise (ProvenCredentialsOnly refuses a broker-minted one
//     for a credential-less account). So any password set after an attempt
//     is the address owner's, and verifying it is what the owner asked for.
//
// A reset that finds attempt records beside a password that predates them
// all does not qualify, and it must not make the NEXT reset qualify: its
// replacement password is dated after the attempts, so judged by date alone
// it would pass. So it first marks the account disqualified
// (MetaFirstPasswordDisqualified), and only then replaces the password. A
// marked account is never verified by a reset again, and no attempt is
// recorded on it. The mark needs no lock across broker replicas: a reset
// reads the account (its passwordChanged) BEFORE its records, so one that
// sees a disqualifying reset's replacement password also sees the mark that
// was written before it. It never removes another request's attempt record,
// and a reset that qualifies never writes it, so a wrong code cannot plant it
// on an account a first password was installed on.
//
// A record is removed by the request that made it only when the issuer
// refused that request's code outright (it installed nothing); an ambiguous
// failure keeps it, since the password may be in. Every record goes once the
// address is verified. A malformed record, or one dated in the future past
// the skew, is treated as absent.

// firstPasswordClockSkew allows for the broker's clock running ahead of the
// issuer's. A variable so a test can move it.
var firstPasswordClockSkew = time.Minute

// recordFirstPasswordAttempt writes this request's attempt and returns its
// key.
func (c *Client) recordFirstPasswordAttempt(ctx context.Context, userID string) (string, error) {
	key := MetaFirstPasswordPrefix + newNonce()
	if err := c.SetMetadata(ctx, userID, key, strconv.FormatInt(c.clock().UnixMilli(), 10)); err != nil {
		return "", fmt.Errorf("record the first-password attempt before setting it: %w", err)
	}
	return key, nil
}

// firstPasswordRecords is the prefix of every key below: the attempts
// (MetaFirstPasswordPrefix) and the disqualifying mark, read in one search.
const firstPasswordRecords = "forge.first_password"

// passwordIsAFirstPasswordAttempt reports whether an unverified,
// password-only account's password is one a recorded first-password attempt
// installed (see above). disqualify reports that it is not, though attempts
// were recorded, and the account is not marked yet: the caller marks it
// before replacing the password. It fails closed on a read that fails.
//
// The account is read BEFORE its records: that order is what makes the mark
// work without a lock (see above).
func (c *Client) passwordIsAFirstPasswordAttempt(ctx context.Context, userID string) (qualifies, disqualify bool, err error) {
	u, err := c.FindUserByID(ctx, userID)
	if err != nil {
		return false, false, fmt.Errorf("read when the account's password was set: %w", err)
	}
	records, err := c.MetadataWithPrefix(ctx, userID, firstPasswordRecords)
	if err != nil {
		return false, false, fmt.Errorf("read the account's first-password attempts: %w", err)
	}
	if _, marked := records[MetaFirstPasswordDisqualified]; marked {
		return false, false, nil
	}
	now := c.clock()
	var earliest time.Time
	for key, v := range records {
		if !strings.HasPrefix(key, MetaFirstPasswordPrefix) {
			continue
		}
		ms, perr := strconv.ParseInt(v, 10, 64)
		start := time.UnixMilli(ms)
		if perr != nil || ms <= 0 || start.After(now.Add(firstPasswordClockSkew)) {
			continue // malformed, or from the future: absent
		}
		if earliest.IsZero() || start.Before(earliest) {
			earliest = start
		}
	}
	if earliest.IsZero() {
		return false, false, nil
	}
	qualifies = !u.PasswordChanged.IsZero() && !u.PasswordChanged.Before(earliest.Add(-firstPasswordClockSkew))
	return qualifies, !qualifies, nil
}

// firstPasswordDisqualified reports whether the account carries
// MetaFirstPasswordDisqualified. It fails closed on a read that fails.
func (c *Client) firstPasswordDisqualified(ctx context.Context, userID string) (bool, error) {
	_, marked, err := c.Metadata(ctx, userID, MetaFirstPasswordDisqualified)
	if err != nil {
		return false, fmt.Errorf("read whether the account may take a first password: %w", err)
	}
	return marked, nil
}

// disqualifyFirstPassword writes MetaFirstPasswordDisqualified. A reset
// that cannot write it does not replace the password.
func (c *Client) disqualifyFirstPassword(ctx context.Context, userID string) error {
	if err := c.SetMetadata(ctx, userID, MetaFirstPasswordDisqualified, strconv.FormatInt(c.clock().UnixMilli(), 10)); err != nil {
		return fmt.Errorf("mark the account's password as no first password before replacing it: %w", err)
	}
	return nil
}

// clearFirstPasswordAttempts removes every attempt record of a verified
// account (inert there if one stays).
func (c *Client) clearFirstPasswordAttempts(ctx context.Context, userID string) {
	attempts, err := c.MetadataWithPrefix(ctx, userID, MetaFirstPasswordPrefix)
	if err != nil {
		return
	}
	for key := range attempts {
		_ = c.DeleteMetadata(ctx, userID, key)
	}
}

// mayAddPassword refuses, on a ProvenCredentialsOnly client, to give a
// password to an unverified account that has none (ErrUnverifiedAccount):
// that is a credential added. Replacing the password a contact-only account
// was created with is its holder's own recovery, and stays allowed.
//
// SetPassword runs it: the code it redeems is one the broker minted itself,
// which proves nothing about the address.
func (c *Client) mayAddPassword(ctx context.Context, userID string) error {
	gate, err := c.passwordGate(ctx, userID)
	if err != nil {
		return err
	}
	if gate == gateFirstPassword {
		return ErrUnverifiedAccount
	}
	return nil
}

// passwordState is what passwordGate found about an account.
type passwordState int

const (
	// gateOpen: nothing to verify — the address is verified, the client
	// does not require proven credentials, or the account holds a password
	// beside another credential. Setting a password changes nothing else.
	gateOpen passwordState = iota
	// gateFirstPassword: unverified, holding NO credential. Only a reset
	// code the person presented may set its password, and that marks the
	// address verified.
	gateFirstPassword
	// gateUnverifiedPassword: unverified, holding a password and nothing
	// else — a contact-only or pre-registered account created with one, or
	// a first password whose verification failed after it was set. Its
	// holder may replace it. A presented reset code marks the address
	// verified ONLY for the last (a first-password attempt; see
	// passwordIsAFirstPasswordAttempt): any other password was
	// set by whoever registered the address, who may hold a session it
	// passed.
	gateUnverifiedPassword
)

// passwordGate is the ProvenCredentialsOnly check before a password is set.
// It refuses (ErrUnverifiedAccount) an unverified account holding a
// credential other than a password, and otherwise reports the account's
// passwordState: a presented reset code redeemed on an unverified account
// marks the address verified — a reset code is delivered only to the address
// or by an administrator who issued it (forge#2121).
//
// It fails CLOSED: an account that cannot be read, or whose sign-in methods
// cannot be, is refused with that error — nothing is minted or redeemed for
// it. An unknown account answers as a wrong code does (ErrInvalidResetCode),
// so a reset form says nothing about which accounts exist.
func (c *Client) passwordGate(ctx context.Context, userID string) (passwordState, error) {
	if !c.ProvenCredentialsOnly {
		return gateOpen, nil
	}
	u, err := c.FindUserByID(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		return gateOpen, fmt.Errorf("%w: no such account", ErrInvalidResetCode)
	}
	if err != nil {
		return gateOpen, fmt.Errorf("read the account before setting its password: %w", err)
	}
	if u.EmailVerified {
		return gateOpen, nil
	}
	kinds, err := c.credentialKinds(ctx, userID)
	if err != nil {
		return gateOpen, fmt.Errorf("read the account's sign-in methods before setting its password: %w", err)
	}
	if len(kinds) == 0 {
		return gateFirstPassword, nil
	}
	hasPassword, onlyPassword := false, true
	for _, k := range kinds {
		if k == "AUTHENTICATION_METHOD_TYPE_PASSWORD" {
			hasPassword = true
		} else {
			onlyPassword = false
		}
	}
	switch {
	case onlyPassword:
		return gateUnverifiedPassword, nil
	case hasPassword:
		// A password beside a credential nobody proved: replacing the
		// password stays the holder's recovery, but nothing is verified,
		// which would vouch for the other credential.
		return gateOpen, nil
	}
	return gateOpen, ErrUnverifiedAccount
}

// Zitadel's stable ids for the two rejections a reset form must tell apart.
//
//   - `CODE-…` comes from the code verifier itself. CODE-woT0xc ("Code is
//     invalid") is verified against v4.19.4; an expired code is rejected by
//     the same verifier, under the same prefix.
//   - COMMAND-2M9fs is "Code not found" (verified): no outstanding code,
//     which is what a used or never-issued code looks like.
//   - `DOMAIN-…` is the password-complexity policy. DOMAIN-HuJf6 ("Password
//     is too short") is verified; its upper/lower/number/symbol siblings
//     live in the same package and share the prefix.
const (
	codeErrorPrefix   = "CODE-"
	codeNotFoundID    = "COMMAND-2M9fs"
	policyErrorPrefix = "DOMAIN-"
)

func classifyResetError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("reset password: %w", err)
	}
	id, _ := apiErr.errorID()
	if policy := policyErrorFrom(apiErr); policy != nil {
		return policy
	}
	switch {
	case strings.HasPrefix(id, codeErrorPrefix), id == codeNotFoundID:
		return fmt.Errorf("%w: %s", ErrInvalidResetCode, err.Error())
	default:
		return fmt.Errorf("reset password: %w", err)
	}
}
