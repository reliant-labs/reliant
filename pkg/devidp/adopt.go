package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrAccountHoldsCredentials is AdoptPendingAccount's refusal: the account's
// address is unverified and it holds a credential nobody proved — whoever
// registered the address first may have set it. It is never verified on an
// administrator's say-so; move it out of the address's way instead.
var ErrAccountHoldsCredentials = errors.New("the account's address is unverified and it holds credentials nobody proved")

// AdoptPendingAccount is the step an ADMINISTRATOR'S flow takes before it
// hands an existing account its first sign-in path — a declared member
// (forge's identity converge), a console invitation that issues a
// password-reset or return code. Such a flow asserts the address itself, just
// as it does for an account it creates with the address verified, so an
// account it adopts gets the same: its address is marked verified.
//
// Only an account holding NO credential is verified (a sign-up still waiting
// for its proof: nothing on it to inherit). One holding any credential is
// refused with ErrAccountHoldsCredentials and left exactly as it is. An
// account already verified is left alone (adopted false). A read that fails
// refuses (fail closed).
//
// Why it is needed: a broker client with ProvenCredentialsOnly — every
// scaffolded login broker — adds no credential to an unverified account. A
// first-password RESET code is the exception (ResetPassword: redeeming one
// proves the address), but a passkey code or a social link is not, so an
// administrator's flow adopts first whatever it issues.
//
// The address is verified the issuer's way: a verification code is minted
// (returned, never mailed) and redeemed at once, since Zitadel refuses to
// "set" an unchanged address as verified. adopted reports that this call
// verified it.
func (c *Client) AdoptPendingAccount(ctx context.Context, userID string) (adopted bool, err error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, fmt.Errorf("a user id is required")
	}
	u, err := c.FindUserByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("read the account to adopt: %w", err)
	}
	if u.EmailVerified {
		return false, nil
	}
	kinds, err := c.credentialKinds(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("read the sign-in methods of the account to adopt: %w", err)
	}
	if len(kinds) > 0 {
		return false, fmt.Errorf("%w (%s)", ErrAccountHoldsCredentials, strings.Join(kinds, ", "))
	}
	if err := c.markAddressVerified(ctx, userID); err != nil {
		return false, fmt.Errorf("verify the adopted address: %w", err)
	}
	return true, nil
}

// markAddressVerified marks the account's current address verified the
// issuer's way: a verification code is minted (returned, never mailed) and
// redeemed at once, since Zitadel refuses to "set" an unchanged address as
// verified. Call it only once the address is asserted or proven.
func (c *Client) markAddressVerified(ctx context.Context, userID string) error {
	body, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/email/resend", map[string]any{"returnCode": map[string]any{}})
	if err != nil {
		return fmt.Errorf("mint a verification code: %w", err)
	}
	var out struct {
		VerificationCode string `json:"verificationCode"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("decode the verification code: %w", err)
	}
	if out.VerificationCode == "" {
		return errors.New("the issuer returned no verification code")
	}
	return c.VerifyEmail(ctx, userID, out.VerificationCode)
}
