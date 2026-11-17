//go:build integration && !windows

package cli

// P15, the last hop: the key set `forge cluster connect` uploads is enough for
// a REAL OpenBao to log a pod in. A real kube-apiserver (envtest) signs a
// projected ServiceAccount token; connect registers the cluster's issuer and
// keys; the mount is configured exactly as the control plane configures one
// from that registration (jwt_validation_pubkeys as PEM, bound_issuer) with no
// route back into the cluster; and the login succeeds for the bound subject and
// audience and fails for every other.
//
// Needs docker (OpenBao) and KUBEBUILDER_ASSETS (kube-apiserver). Run:
//
//	KUBEBUILDER_ASSETS=... go test -tags=integration -count=1 -run TestSmoke_UploadedJWKSLogsInToARealOpenBao ./internal/cli/

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// pemKeysOf converts the uploaded key set the way the control plane does
// before it writes jwt_validation_pubkeys.
func pemKeysOf(t *testing.T, jwks string) []string {
	t.Helper()
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal([]byte(jwks), &set); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, raw := range set.Keys {
		var h struct{ Kid string }
		_ = json.Unmarshal(raw, &h)
		tok := &jwt.Token{Header: map[string]any{"kid": h.Kid}}
		key, err := uploadedKeyFunc(t, jwks)(tok)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(key)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	}
	return out
}

func TestSmoke_UploadedJWKSLogsInToARealOpenBao(t *testing.T) {
	ec := connectToEnvtest(t)
	b := startSmokeBao(t)

	const (
		aud  = "https://secrets.example.com/c/prod-us"
		mnt  = "jwt-prod-us"
		role = "env-aaaaaaaa-prod-us-bbbbbbbb"
	)
	b.root(http.MethodPut, "sys/auth/"+mnt, map[string]any{"type": "jwt"})
	b.root(http.MethodPut, "auth/"+mnt+"/config", map[string]any{
		"jwt_validation_pubkeys": pemKeysOf(t, ec.jwks), "bound_issuer": ec.issuer,
	})
	b.root(http.MethodPut, "sys/policies/acl/"+role, map[string]any{"policy": `path "secret/data/example/prod/X" { capabilities = ["read"] }`})
	b.root(http.MethodPut, "auth/"+mnt+"/role/"+role, map[string]any{
		"role_type": "jwt", "user_claim": "sub", "bound_audiences": []string{aud},
		"bound_subject":  "system:serviceaccount:default:reliant-secrets",
		"token_policies": []string{role}, "token_type": "batch", "token_ttl": 600, "token_max_ttl": 600,
	})

	login := func(token string) int {
		code, _, err := b.do(context.Background(), http.MethodPost, "auth/"+mnt+"/login", nil, "", map[string]any{"role": role, "jwt": token})
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	if code := login(mintAPIServerToken(t, ec, aud)); code != http.StatusOK {
		t.Fatalf("a token the cluster signed, for the bound audience and subject, was refused: HTTP %d", code)
	}
	if code := login(mintAPIServerToken(t, ec, "https://secrets.example.com/c/other")); code == http.StatusOK {
		t.Error("a token for another audience logged in")
	}
	if code := login("eyJhbGciOiJFUzI1NiJ9.e30.AAAA"); code == http.StatusOK {
		t.Error("a token no registered key signed logged in")
	}
}
