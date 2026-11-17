package devidp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Social sign-in (Google, GitHub) with the app's own OAuth app.
//
// The issuer does the OAuth code exchange itself: StartIDPIntent returns the
// provider's authorize URL with the issuer's own /idps/callback as the
// redirect, the issuer exchanges the code with the client secret it holds,
// and then sends the browser to the intent's success URL with ?id=&token=.
// The client secret therefore never reaches the app — only an intent id and
// token, which RetrieveIDPIntent turns into what the provider asserted.

// StartIDPIntent starts a social sign-in and returns the provider's authorize
// URL and the intent id (the URL's `state`), which the caller binds to the
// browser so the callback can prove it is the same browser.
func (c *Client) StartIDPIntent(ctx context.Context, idpID, successURL, failureURL string) (authURL, intentID string, err error) {
	if strings.TrimSpace(idpID) == "" || successURL == "" || failureURL == "" {
		return "", "", fmt.Errorf("an identity provider id and both return URLs are required")
	}
	body, err := c.postJSON(ctx, "/v2/idp_intents", map[string]any{
		"idpId": idpID,
		"urls":  map[string]any{"successUrl": successURL, "failureUrl": failureURL},
	})
	if err != nil {
		return "", "", fmt.Errorf("start a social sign-in: %w", err)
	}
	var out struct {
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("decode social sign-in: %w", err)
	}
	u, perr := url.Parse(out.AuthURL)
	if out.AuthURL == "" || perr != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", fmt.Errorf("the issuer returned no usable authorize URL")
	}
	intentID = u.Query().Get("state")
	if intentID == "" {
		return "", "", fmt.Errorf("the issuer's authorize URL carries no state (intent id)")
	}
	return out.AuthURL, intentID, nil
}

// IDPLink is a link between an account and a provider identity.
type IDPLink struct {
	IdPID    string
	UserID   string // the provider's user id
	UserName string
}

func (l IDPLink) body() map[string]any {
	return map[string]any{"idpId": l.IdPID, "userId": l.UserID, "userName": l.UserName}
}

// IDPResult is what the provider asserted, as the issuer reports it.
type IDPResult struct {
	// LinkedUserID is the account already linked to this provider identity,
	// "" when none is.
	LinkedUserID string
	// Link is the provider identity, for AddIDPLink or CreateUser.
	Link IDPLink
	// Email is the provider's address; EmailVerified is the provider's
	// assertion that it is verified. An EMPTY Email is never verified,
	// whatever the issuer says: Zitadel's GitHub provider reports every
	// address verified, even one it did not find.
	Email         string
	EmailVerified bool
	GivenName     string
	FamilyName    string
}

// RetrieveIDPIntent reads a completed intent. The token is the one the
// issuer appended to the success URL; it is useless without the intent id.
func (c *Client) RetrieveIDPIntent(ctx context.Context, intentID, token string) (IDPResult, error) {
	intentID, token = strings.TrimSpace(intentID), strings.TrimSpace(token)
	if intentID == "" || token == "" {
		return IDPResult{}, fmt.Errorf("an intent id and token are required")
	}
	body, err := c.postJSON(ctx, "/v2/idp_intents/"+url.PathEscape(intentID), map[string]any{"idpIntentToken": token})
	if err != nil {
		return IDPResult{}, fmt.Errorf("retrieve the social sign-in: %w", err)
	}
	var out struct {
		UserID         string `json:"userId"`
		IDPInformation struct {
			IdPID    string `json:"idpId"`
			UserID   string `json:"userId"`
			UserName string `json:"userName"`
		} `json:"idpInformation"`
		AddHumanUser *struct {
			Profile struct {
				GivenName  string `json:"givenName"`
				FamilyName string `json:"familyName"`
			} `json:"profile"`
			Email struct {
				Email      string `json:"email"`
				IsVerified bool   `json:"isVerified"`
			} `json:"email"`
		} `json:"addHumanUser"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return IDPResult{}, fmt.Errorf("decode social sign-in: %w", err)
	}
	if out.IDPInformation.IdPID == "" || out.IDPInformation.UserID == "" {
		return IDPResult{}, fmt.Errorf("the issuer reported no provider identity for the intent")
	}
	r := IDPResult{
		LinkedUserID: out.UserID,
		Link: IDPLink{IdPID: out.IDPInformation.IdPID, UserID: out.IDPInformation.UserID,
			UserName: out.IDPInformation.UserName},
	}
	if h := out.AddHumanUser; h != nil {
		r.Email = strings.TrimSpace(h.Email.Email)
		r.EmailVerified = h.Email.IsVerified && r.Email != ""
		r.GivenName, r.FamilyName = h.Profile.GivenName, h.Profile.FamilyName
	}
	return r, nil
}

// AddIDPLink links a provider identity to an existing account. Call it only
// when BOTH the provider's address and the account's are verified, and they
// are the same address: an unverified auto-link is an account takeover.
func (c *Client) AddIDPLink(ctx context.Context, userID string, link IDPLink) error {
	if strings.TrimSpace(userID) == "" || link.IdPID == "" || link.UserID == "" {
		return fmt.Errorf("a user id and a provider identity are required")
	}
	if _, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/links", map[string]any{"idpLink": link.body()}); err != nil {
		return fmt.Errorf("link the social account: %w", err)
	}
	return nil
}
