package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/reliant-labs/forge/pkg/apikey"
)

// A key the KeyValidator accepts yields claims marked as an API key, with
// the key's lookup prefix — whatever the validator itself returned.
func TestAuthenticator_KeyClaimsCarryTheAPIKeyCredential(t *testing.T) {
	t.Parallel()
	key, store := newKey(t)
	a := NewAuthenticator(nil)
	a.UseKeyValidator(store)

	c, err := a.Authenticate(context.Background(), key)
	if err != nil {
		t.Fatalf("authenticate key: %v", err)
	}
	prefix, _ := apikey.LookupPrefix(key)
	if !c.IsAPIKey() || c.Credential.Kind() != CredentialAPIKey || c.Credential.KeyPrefix() != prefix {
		t.Fatalf("credential = %v (prefix %q), want an API key with prefix %q", c.Credential.Kind(), c.Credential.KeyPrefix(), prefix)
	}
}

// The Authenticator stamps the credential itself, so a key validator that
// sets nothing, or the wrong thing, cannot mislabel its claims, and the
// claims it returned are not written to.
func TestAuthenticator_StampsOverWhatTheValidatorReturned(t *testing.T) {
	t.Parallel()
	returned := &Claims{UserID: "key-owner", Credential: TokenCredential()}
	a := NewAuthenticator(nil)
	a.UseKeyValidator(KeyValidatorFunc(func(context.Context, string) (*Claims, error) { return returned, nil }))
	key, _ := newKey(t)
	prefix, _ := apikey.LookupPrefix(key)

	c, err := a.Authenticate(context.Background(), key)
	if err != nil {
		t.Fatalf("authenticate key: %v", err)
	}
	if !c.IsAPIKey() || c.Credential.KeyPrefix() != prefix {
		t.Fatalf("credential = %v, want an API key", c.Credential.Kind())
	}
	if returned.Credential.Kind() != CredentialToken {
		t.Fatalf("the validator's own claims were modified: %v", returned.Credential.Kind())
	}
}

// A token validator cannot produce an API-key credential, even one whose
// claims say they are a key: a bearer token is a token.
func TestAuthenticator_TokenPathAlwaysSaysToken(t *testing.T) {
	t.Parallel()
	a := NewAuthenticator(func(string) (*Claims, error) {
		return &Claims{UserID: "u", Credential: APIKeyCredential("forged12")}, nil
	})
	c, err := a.Authenticate(context.Background(), "eyJhbGciOiJIUzI1NiJ9.e30.sig")
	if err != nil {
		t.Fatalf("authenticate token: %v", err)
	}
	if c.IsAPIKey() || c.Credential.Kind() != CredentialToken || c.Credential.KeyPrefix() != "" {
		t.Fatalf("credential = %v (prefix %q), want a token", c.Credential.Kind(), c.Credential.KeyPrefix())
	}
}

// forgedClaimShapes are JWT payloads that try, every way a JSON claim
// could, to say "this was an API key".
func forgedClaimShapes() map[string]jwt.MapClaims {
	key := map[string]any{"kind": 2, "Kind": 2, "keyPrefix": "forged12", "KeyPrefix": "forged12", "key_prefix": "forged12"}
	return map[string]jwt.MapClaims{
		"credential object":     {"credential": key},
		"Credential object":     {"Credential": key},
		"credential string":     {"credential": "api_key"},
		"credential kind int":   {"credential": 2},
		"auth_method":           {"auth_method": "api_key"},
		"raw nesting":           {"raw": map[string]any{"credential": key, "auth_method": "api_key"}},
		"top-level fields":      {"kind": 2, "keyPrefix": "forged12", "key_prefix": "forged12"},
		"fk_ subject and email": {"sub": "fk_forged12abcdefghijklmnopqrstuvwxyz", "email": "fk_forged12@x"},
	}
}

// jsonResolver decodes the whole token payload into Claims with
// encoding/json — the most a resolver could hand a token's JSON.
type jsonResolver struct{}

func (jsonResolver) Resolve(mc map[string]any) (*Claims, error) {
	b, err := json.Marshal(mc)
	if err != nil {
		return nil, err
	}
	var c Claims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// No JWT, whatever its claims, authenticates as an API key: through
// Validate, through the legacy header path, and through an Authenticator,
// with the built-in claim mapping and with a resolver that JSON-decodes the
// payload straight into Claims.
func TestJWT_NoClaimShapeProducesAnAPIKeyCredential(t *testing.T) {
	t.Parallel()
	for _, resolver := range []UserResolver{nil, jsonResolver{}} {
		v, err := NewValidator(Config{
			Provider:     ProviderJWT,
			JWT:          JWTConfig{SigningMethod: "HS256", Secret: "topsecret"},
			UserResolver: resolver,
		})
		if err != nil {
			t.Fatalf("new validator: %v", err)
		}
		a := NewAuthenticator(v.Validate)
		for name, mc := range forgedClaimShapes() {
			payload := jwt.MapClaims{"sub": "user-1", "exp": time.Now().Add(time.Hour).Unix()}
			for k, val := range mc {
				payload[k] = val
			}
			tok := signHS256(t, "topsecret", payload)

			h := http.Header{}
			h.Set("Authorization", "Bearer "+tok)
			viaHeaders, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
			if err != nil {
				t.Fatalf("%s (resolver %T): headers: %v", name, resolver, err)
			}
			viaValidate, err := v.Validate(tok)
			if err != nil {
				t.Fatalf("%s (resolver %T): validate: %v", name, resolver, err)
			}
			viaAuthenticator, err := a.Authenticate(context.Background(), tok)
			if err != nil {
				t.Fatalf("%s (resolver %T): authenticator: %v", name, resolver, err)
			}
			for path, c := range map[string]*Claims{"headers": viaHeaders, "validate": viaValidate, "authenticator": viaAuthenticator} {
				if c.IsAPIKey() || c.Credential.Kind() != CredentialToken || c.Credential.KeyPrefix() != "" {
					t.Errorf("%s (resolver %T) via %s: credential = %v (prefix %q), want a token",
						name, resolver, path, c.Credential.Kind(), c.Credential.KeyPrefix())
				}
			}
		}
	}
}

// Claims decoded from JSON never carry a credential, and encoding claims
// never writes one, so no JSON round trip can carry or mint it.
func TestClaims_CredentialIsNotJSON(t *testing.T) {
	t.Parallel()
	var c Claims
	in := `{"user_id":"u","credential":{"kind":2,"keyPrefix":"forged12"},"Credential":{"Kind":2,"KeyPrefix":"forged12"}}`
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.IsAPIKey() || c.Credential != (Credential{}) {
		t.Fatalf("decoded a credential from JSON: %v", c.Credential.Kind())
	}

	out, err := json.Marshal(&Claims{UserID: "u", Credential: APIKeyCredential("abcd1234")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Claims
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Credential != (Credential{}) {
		t.Fatalf("credential survived a JSON round trip: %s", out)
	}
}

// The legacy Validator's api_key provider marks its claims too.
func TestValidator_APIKeyProviderMarksTheCredential(t *testing.T) {
	t.Parallel()
	key, store := newKey(t)
	v, err := NewValidator(Config{Provider: ProviderAPIKey, KeyValidator: store})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	h := http.Header{}
	h.Set(v.cfg.APIKey.EffectiveHeader(), key)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	prefix, _ := apikey.LookupPrefix(key)
	if !c.IsAPIKey() || c.Credential.KeyPrefix() != prefix {
		t.Fatalf("credential = %v (prefix %q), want an API key with prefix %q", c.Credential.Kind(), c.Credential.KeyPrefix(), prefix)
	}
}

func TestClaims_IsAPIKeyOnNil(t *testing.T) {
	t.Parallel()
	var c *Claims
	if c.IsAPIKey() {
		t.Fatal("nil claims are not an API key")
	}
	if (&Claims{}).IsAPIKey() || (&Claims{}).Credential.Kind() != CredentialUnknown {
		t.Fatal("zero claims carry no credential")
	}
}
