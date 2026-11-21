package devidp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Passkeys (WebAuthn) through the broker.
//
// ── THE CHECK THE ISSUER DOES NOT DO ─────────────────────────────────────
//
// Zitadel takes the relying-party id from the caller (`domain`) and, when it
// verifies an assertion or a registration, builds its WebAuthn configuration
// with the allowed origins set to WHATEVER ORIGIN THE CLIENT REPORTED in
// clientDataJSON (v4.19.5 internal/webauthn config(id, origin)). It verifies
// the signature and the challenge, never the origin. A phishing page that
// relays a victim's assertion would pass.
//
// So the broker checks it: CheckClientData refuses a credential whose
// clientDataJSON origin is not the app's own origin, whose type is not the
// ceremony's, or that was made cross-origin. Call it before every
// SetSession(WebAuthnAssertion) and VerifyPasskeyRegistration.

// WebAuthn ceremony types (clientDataJSON.type).
const (
	WebAuthnGet    = "webauthn.get"
	WebAuthnCreate = "webauthn.create"
)

// CheckClientData verifies the clientDataJSON inside a browser credential:
// its origin must equal origin exactly (scheme, host and port), its type
// must be ceremony, and it must not be a cross-origin (iframe) ceremony.
// credential is the PublicKeyCredential JSON with base64url fields.
func CheckClientData(credential json.RawMessage, origin, ceremony string) error {
	var cred struct {
		Type     string `json:"type"`
		Response struct {
			ClientDataJSON string `json:"clientDataJSON"`
		} `json:"response"`
	}
	if err := json.Unmarshal(credential, &cred); err != nil {
		return fmt.Errorf("the credential is not JSON: %w", err)
	}
	if cred.Type != "public-key" {
		return fmt.Errorf("the credential type is %q, not public-key", cred.Type)
	}
	raw, err := decodeB64URL(cred.Response.ClientDataJSON)
	if err != nil || len(raw) == 0 {
		return fmt.Errorf("the credential carries no readable clientDataJSON")
	}
	var cd struct {
		Type        string `json:"type"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(raw, &cd); err != nil {
		return fmt.Errorf("clientDataJSON is not JSON: %w", err)
	}
	want := strings.TrimRight(origin, "/")
	switch {
	case want == "":
		return fmt.Errorf("no app origin is configured to check passkeys against")
	case cd.Type != ceremony:
		return fmt.Errorf("clientDataJSON type is %q, want %q", cd.Type, ceremony)
	case cd.Origin != want:
		return fmt.Errorf("clientDataJSON origin %q is not the app's origin %q", cd.Origin, want)
	case cd.CrossOrigin:
		return fmt.Errorf("the passkey ceremony ran cross-origin")
	case cd.Challenge == "":
		return fmt.Errorf("clientDataJSON carries no challenge")
	}
	return nil
}

// decodeB64URL decodes base64url with or without padding, and tolerates
// standard base64 (some encoders emit it).
func decodeB64URL(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// PasskeyRegistrationCode authorizes registering a passkey for a user who
// has no session yet (an invitee, a just-verified sign-up). Minted by the
// broker right after the person presented a proof in the same request.
type PasskeyRegistrationCode struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

// MintPasskeyRegistrationCode asks the issuer for a passkey registration
// code and returns it instead of mailing it. LIKE A RESET CODE, IT IS A
// CREDENTIAL: call it only after a proof for this user succeeded in the same
// request, and never put it in a response body.
func (c *Client) MintPasskeyRegistrationCode(ctx context.Context, userID string) (PasskeyRegistrationCode, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return PasskeyRegistrationCode{}, fmt.Errorf("a user id is required")
	}
	if err := c.requireVerified(ctx, userID); err != nil {
		return PasskeyRegistrationCode{}, err
	}
	body, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/passkeys/registration_link", map[string]any{"returnCode": map[string]any{}})
	if err != nil {
		return PasskeyRegistrationCode{}, fmt.Errorf("mint a passkey registration code: %w", err)
	}
	var out struct {
		Code PasskeyRegistrationCode `json:"code"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return PasskeyRegistrationCode{}, fmt.Errorf("decode passkey registration code: %w", err)
	}
	if out.Code.ID == "" || out.Code.Code == "" {
		return PasskeyRegistrationCode{}, fmt.Errorf("the issuer returned no passkey registration code")
	}
	return out.Code, nil
}

// PasskeyRegistration is a started registration: the passkey id to verify
// and the creation options for navigator.credentials.create.
type PasskeyRegistration struct {
	PasskeyID string
	Options   json.RawMessage
}

// RegisterPasskey starts a passkey registration for userID under the
// relying-party id domain. code is nil for a signed-in user (the caller
// took userID from a validated session token, never from a request body),
// and the registration code otherwise.
func (c *Client) RegisterPasskey(ctx context.Context, userID, domain string, code *PasskeyRegistrationCode) (PasskeyRegistration, error) {
	userID, domain = strings.TrimSpace(userID), strings.TrimSpace(domain)
	if userID == "" || domain == "" {
		return PasskeyRegistration{}, fmt.Errorf("a user id and a relying-party id are required")
	}
	if err := c.requireVerified(ctx, userID); err != nil {
		return PasskeyRegistration{}, err
	}
	req := map[string]any{"domain": domain, "authenticator": "PASSKEY_AUTHENTICATOR_UNSPECIFIED"}
	if code != nil {
		req["code"] = map[string]any{"id": code.ID, "code": code.Code}
	}
	body, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/passkeys", req)
	if err != nil {
		return PasskeyRegistration{}, fmt.Errorf("start passkey registration: %w", err)
	}
	var out struct {
		PasskeyID string          `json:"passkeyId"`
		Options   json.RawMessage `json:"publicKeyCredentialCreationOptions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return PasskeyRegistration{}, fmt.Errorf("decode passkey registration: %w", err)
	}
	if out.PasskeyID == "" || len(out.Options) == 0 {
		return PasskeyRegistration{}, fmt.Errorf("the issuer returned an incomplete passkey registration")
	}
	return PasskeyRegistration{PasskeyID: out.PasskeyID, Options: out.Options}, nil
}

// VerifyPasskeyRegistration completes a registration with the browser's
// attestation. Check it with CheckClientData(…, WebAuthnCreate) first.
func (c *Client) VerifyPasskeyRegistration(ctx context.Context, userID, passkeyID string, credential json.RawMessage, name string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(passkeyID) == "" || len(credential) == 0 {
		return fmt.Errorf("a user id, a passkey id and a credential are required")
	}
	if err := c.requireVerified(ctx, userID); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	_, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/passkeys/"+url.PathEscape(passkeyID), map[string]any{
		"publicKeyCredential": credential,
		"passkeyName":         name,
	})
	if err != nil {
		return fmt.Errorf("verify passkey registration: %w", err)
	}
	return nil
}

// Passkey is one registered passkey.
type Passkey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListPasskeys returns a user's ready passkeys.
func (c *Client) ListPasskeys(ctx context.Context, userID string) ([]Passkey, error) {
	return c.listPasskeys(ctx, userID, false)
}

// listPasskeys returns a user's passkeys; all includes ones whose
// registration was started and never finished.
func (c *Client) listPasskeys(ctx context.Context, userID string, all bool) ([]Passkey, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("a user id is required")
	}
	// The issuer's passkey search takes no paging: it answers every passkey
	// at once, and searchAll fails if its total says otherwise.
	result, err := searchAll[struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}](ctx, c, "/v2/users/"+url.PathEscape(userID)+"/passkeys/_search", map[string]any{}, "result", false)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	keys := []Passkey{}
	for _, p := range result {
		if !all && p.State != "" && p.State != "AUTH_FACTOR_STATE_READY" {
			continue
		}
		keys = append(keys, Passkey{ID: p.ID, Name: p.Name})
	}
	return keys, nil
}
