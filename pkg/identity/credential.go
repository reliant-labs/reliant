package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Credential authenticates the library to the instance. Use PAT for a personal
// access token (the dev bootstrap) and MachineKey for an instance-owner machine
// key (the control plane). Both must carry IAM_OWNER: the library edits
// instance-level policies and SMTP as well as the project.
type Credential interface {
	bearer(ctx context.Context, c *client) (string, error)
}

// CredentialFunc adapts a function (a token minted elsewhere, rotated by its
// owner) to a Credential. It is called for every request, so it should cache.
type CredentialFunc func(ctx context.Context) (string, error)

func (f CredentialFunc) bearer(ctx context.Context, _ *client) (string, error) { return f(ctx) }

type patCredential string

// PAT authenticates with a personal access token, which is already a bearer
// token for every API: no exchange happens.
func PAT(token string) Credential { return patCredential(strings.TrimSpace(token)) }

// PATFile reads a personal access token from path on every request, so a
// token the owner rotates in place is picked up without a restart.
func PATFile(path string) Credential {
	return CredentialFunc(func(context.Context) (string, error) {
		raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied credential path; reading it is the point.
		if err != nil {
			return "", fmt.Errorf("read the personal access token from %s: %w", path, err)
		}
		t := strings.TrimSpace(string(raw))
		if t == "" {
			return "", fmt.Errorf("the personal access token at %s is empty (the instance may still be initialising)", path)
		}
		return t, nil
	})
}

func (p patCredential) bearer(context.Context, *client) (string, error) {
	if p == "" {
		return "", errors.New("no personal access token supplied")
	}
	return string(p), nil
}

// MachineKeyDoc is Zitadel's machine-key JSON document: the shape its own SDKs
// and the JWT-profile grant consume, and the value stored verbatim in a
// KeyStore.
type MachineKeyDoc struct {
	Type   string `json:"type"`
	KeyID  string `json:"keyId"`
	Key    string `json:"key"`
	UserID string `json:"userId"`
}

// ParseMachineKey decodes a key document and checks it is usable.
func ParseMachineKey(raw []byte) (MachineKeyDoc, error) {
	var k MachineKeyDoc
	if err := json.Unmarshal(raw, &k); err != nil {
		return MachineKeyDoc{}, fmt.Errorf("machine key is not JSON: %w", err)
	}
	if k.KeyID == "" || k.UserID == "" || !strings.Contains(k.Key, "PRIVATE KEY") {
		return MachineKeyDoc{}, errors.New("machine key document is missing keyId, userId or a private key")
	}
	if _, err := k.privateKey(); err != nil {
		return MachineKeyDoc{}, err
	}
	return k, nil
}

func (k MachineKeyDoc) privateKey() (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(k.Key))
	if block == nil {
		return nil, fmt.Errorf("the machine key for user %q is not PEM-encoded", k.UserID)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the machine key for user %q is neither PKCS#1 nor PKCS#8: %w", k.UserID, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the machine key for user %q is a %T; Zitadel machine keys are RSA", k.UserID, parsed)
	}
	return key, nil
}

// assertion builds the RS256 JWT-profile assertion proving possession of the
// key: header kid = key id, iss = sub = the machine user's id, aud = issuer.
// It is exchanged at the token endpoint and never sent anywhere else.
func (k MachineKeyDoc) assertion(issuer string, now time.Time) (string, error) {
	key, err := k.privateKey()
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.KeyID})
	claims, _ := json.Marshal(map[string]any{
		"iss": k.UserID, "sub": k.UserID, "aud": []string{issuer},
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign the assertion for machine user %q: %w", k.UserID, err)
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// apiScope asks for a token the instance's own management API accepts. The
// reserved "zitadel" project alias resolves to the ZITADEL project whatever
// its generated id.
const apiScope = "openid urn:zitadel:iam:org:project:id:zitadel:aud"

type machineKeyCredential struct {
	doc MachineKeyDoc

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// MachineKey authenticates as a machine user through the JWT-profile grant: a
// signed assertion is exchanged for an access token, which is cached until
// shortly before it expires. raw is the key document Zitadel returned when the
// key was created.
func MachineKey(raw []byte) (Credential, error) {
	doc, err := ParseMachineKey(raw)
	if err != nil {
		return nil, err
	}
	return &machineKeyCredential{doc: doc}, nil
}

// MachineKeyFile is MachineKey for a key document on disk (a projected Secret).
func MachineKeyFile(path string) (Credential, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied credential path; reading it is the point.
	if err != nil {
		return nil, fmt.Errorf("read the machine key from %s: %w", path, err)
	}
	return MachineKey(raw)
}

func (m *machineKeyCredential) bearer(ctx context.Context, c *client) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && c.now().Before(m.expiry.Add(-time.Minute)) {
		return m.token, nil
	}
	tok, exp, err := exchangeKey(ctx, c, m.doc, apiScope)
	if err != nil {
		return "", err
	}
	m.token, m.expiry = tok, exp
	return tok, nil
}

// exchangeKey redeems a machine key for an access token.
func exchangeKey(ctx context.Context, c *client, doc MachineKeyDoc, scope string) (string, time.Time, error) {
	issuer, err := c.resolveIssuer(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	assertion, err := doc.assertion(issuer, c.now())
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
		"scope":      {scope},
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/oauth/v2/token"), strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.host != "" {
		req.Host = c.host
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		reason := out.Error
		if out.Description != "" {
			reason += ": " + out.Description
		}
		if reason == "" {
			reason = strings.TrimSpace(string(raw))
		}
		return "", time.Time{}, fmt.Errorf("the instance refused machine key %s of user %s (HTTP %d): %s",
			doc.KeyID, doc.UserID, resp.StatusCode, reason)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return out.AccessToken, c.now().Add(ttl), nil
}
