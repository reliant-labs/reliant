package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
	Email     string
}

// FindUserByEmail resolves an email address to the one human user that has
// it, case-insensitively.
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
		Result []struct {
			UserID             string `json:"userId"`
			PreferredLoginName string `json:"preferredLoginName"`
			Human              *struct {
				Email struct {
					Email string `json:"email"`
				} `json:"email"`
			} `json:"human"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return User{}, fmt.Errorf("decode user search: %w", err)
	}

	var matches []User
	for _, r := range out.Result {
		if r.Human == nil || r.UserID == "" {
			continue // machine accounts have no email and cannot reset one
		}
		matches = append(matches, User{
			ID:        r.UserID,
			LoginName: r.PreferredLoginName,
			Email:     r.Human.Email.Email,
		})
	}
	if len(matches) != 1 {
		// Zitadel does not enforce email uniqueness by default. Zero and
		// "more than one" are both "no single account to act on".
		return User{}, ErrUserNotFound
	}
	return matches[0], nil
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
		User struct {
			UserID             string `json:"userId"`
			PreferredLoginName string `json:"preferredLoginName"`
			Human              *struct {
				Email struct {
					Email string `json:"email"`
				} `json:"email"`
			} `json:"human"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return User{}, fmt.Errorf("decode user: %w", err)
	}
	if out.User.Human == nil || out.User.UserID == "" {
		return User{}, ErrUserNotFound
	}
	return User{ID: out.User.UserID, LoginName: out.User.PreferredLoginName, Email: out.User.Human.Email.Email}, nil
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

	_, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/password", map[string]any{
		"newPassword": map[string]any{
			"password":       r.NewPassword,
			"changeRequired": false,
		},
		"verificationCode": code,
	})
	if err == nil {
		return nil
	}
	return classifyResetError(err)
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
