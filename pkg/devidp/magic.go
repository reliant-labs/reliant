package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Email proofs: magic links, address verification, invitations.
//
// Each proof here is a one-time code the issuer mailed to an address. Holding
// one proves the holder reads that inbox, and the broker's credential may
// then do privileged things for that account (mark it verified, mint a code,
// set a password). EVERY privileged call in the broker follows one of these
// checks succeeding for the same user IN THE SAME REQUEST — see the route
// handlers in broker.go.

// otpEmailExistsID is "the OTP-email method is already set up".
const otpEmailExistsID = "COMMAND-MKL2s"

// EnsureOTPEmail gives a user the OTP-email method, which a magic link's
// one-time code is checked against. Idempotent. The issuer refuses it for an
// unverified address (COMMAND-KLJ2d), so call it only for a verified one.
func (c *Client) EnsureOTPEmail(ctx context.Context, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("a user id is required")
	}
	_, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/otp_email", map[string]any{})
	if err != nil && !strings.Contains(err.Error(), otpEmailExistsID) && !isNoChangesErr(err) {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status == 409 {
			return nil
		}
		return fmt.Errorf("add the OTP-email method: %w", err)
	}
	return nil
}

// VerifyEmail redeems an email-verification code. A wrong, expired or used
// code is ErrInvalidCode. On success the address is verified.
func (c *Client) VerifyEmail(ctx context.Context, userID, code string) error {
	return c.redeemCode(ctx, userID, code, "/email/verify", "verify the email address")
}

// VerifyInviteCode redeems an invitation code. Single use; on success the
// issuer marks the address verified. A rejected code is ErrInvalidCode.
func (c *Client) VerifyInviteCode(ctx context.Context, userID, code string) error {
	return c.redeemCode(ctx, userID, code, "/invite_code/verify", "verify the invitation")
}

func (c *Client) redeemCode(ctx context.Context, userID, code, suffix, what string) error {
	userID, code = strings.TrimSpace(userID), strings.TrimSpace(code)
	if userID == "" || code == "" {
		// Refused before the issuer: no code is not "check nothing".
		return fmt.Errorf("refusing to %s with no user id or no code", what)
	}
	_, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+suffix, map[string]any{"verificationCode": code})
	if err == nil {
		return nil
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status >= 400 && apiErr.status < 500 {
		return fmt.Errorf("%w: %s", ErrInvalidCode, err.Error())
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ResendEmailCode mails a fresh email-verification code, linking to
// urlTemplate ({{.UserID}}, {{.Code}}, {{.OrgID}}).
func (c *Client) ResendEmailCode(ctx context.Context, userID, urlTemplate string) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("a user id is required")
	}
	send := map[string]any{}
	if urlTemplate != "" {
		send["urlTemplate"] = urlTemplate
	}
	if _, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/email/resend", map[string]any{"sendCode": send}); err != nil {
		return fmt.Errorf("resend the email-verification code: %w", err)
	}
	return nil
}

// NewUser is an account the broker creates from a sign-up: the address is
// UNVERIFIED unless EmailVerified (asserted by a social provider), and a
// verification code is mailed to VerifyURLTemplate when it is not.
type NewUser struct {
	Email      string
	GivenName  string
	FamilyName string
	// Password is optional: a passkey- or link-only account has none.
	Password string
	// EmailVerified is true only when a provider asserted it.
	EmailVerified bool
	// VerifyURLTemplate is where the verification mail links; "" mails the
	// issuer's default link. Ignored when EmailVerified.
	VerifyURLTemplate string
	// IDPLinks links social accounts at creation.
	IDPLinks []IDPLink
	// SelfSignup marks the account as created by public sign-up
	// (MetaSelfSignup), which is what entitles it to signup_role.
	SelfSignup bool
}

// CreateUser creates a human account and returns its id. A rejected
// password is a *PasswordPolicyError.
func (c *Client) CreateUser(ctx context.Context, u NewUser) (string, error) {
	email := strings.TrimSpace(u.Email)
	if email == "" {
		return "", fmt.Errorf("an email address is required")
	}
	emailBody := map[string]any{"email": email}
	if u.EmailVerified {
		emailBody["isVerified"] = true
	} else {
		send := map[string]any{}
		if u.VerifyURLTemplate != "" {
			send["urlTemplate"] = u.VerifyURLTemplate
		}
		emailBody["sendCode"] = send
	}
	req := map[string]any{
		"username": email,
		"profile":  profileFor(Registration{GivenName: u.GivenName, FamilyName: u.FamilyName}, email),
		"email":    emailBody,
	}
	if u.Password != "" {
		req["password"] = map[string]any{"password": u.Password, "changeRequired": false}
	}
	if u.SelfSignup {
		req["metadata"] = []any{metadataEntry(MetaSelfSignup, "1")}
	}
	if len(u.IDPLinks) > 0 {
		links := make([]any, 0, len(u.IDPLinks))
		for _, l := range u.IDPLinks {
			links = append(links, l.body())
		}
		req["idpLinks"] = links
	}
	body, err := c.postJSON(ctx, "/v2/users/human", req)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			if policy := policyErrorFrom(apiErr); policy != nil {
				return "", fmt.Errorf("create account: %w", policy)
			}
		}
		return "", fmt.Errorf("create account: %w", err)
	}
	var out struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode created user: %w", err)
	}
	if out.UserID == "" {
		return "", fmt.Errorf("the issuer created an account but returned no user id")
	}
	if u.SelfSignup {
		method := observe.AuthMethodOther
		switch {
		case u.Password != "":
			method = observe.AuthMethodPassword
		case len(u.IDPLinks) > 0:
			method = observe.AuthMethodSocial
		}
		reportSignUp(ctx, out.UserID, method, u.EmailVerified)
	}
	return out.UserID, nil
}

// HasPassword reports whether a user has a password set.
func (c *Client) HasPassword(ctx context.Context, userID string) (bool, error) {
	body, err := c.getJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/authentication_methods")
	if err != nil {
		return false, fmt.Errorf("list authentication methods: %w", err)
	}
	var out struct {
		AuthMethodTypes []string `json:"authMethodTypes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("decode authentication methods: %w", err)
	}
	for _, m := range out.AuthMethodTypes {
		if m == "AUTHENTICATION_METHOD_TYPE_PASSWORD" {
			return true, nil
		}
	}
	return false, nil
}

// GrantProjectRole grants role on projectID to a user, additively. An
// existing grant is left as it is.
//
// This is the management API (user.grant.write). A hosted identity gateway
// that does not route /management/v1 answers 404; the caller logs it, and the
// app grants the role itself (`forge cloud identity grant`) until it does.
func (c *Client) GrantProjectRole(ctx context.Context, userID, projectID, role string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(role) == "" {
		return fmt.Errorf("a user id, a project id and a role are required")
	}
	_, err := c.postJSON(ctx, "/management/v1/users/"+url.PathEscape(userID)+"/grants", map[string]any{
		"projectId": projectID,
		"roleKeys":  []string{role},
	})
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status == 409 {
			return nil
		}
		return fmt.Errorf("grant %q: %w", role, err)
	}
	return nil
}
