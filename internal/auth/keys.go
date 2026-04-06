// Copyright (c) 2025 Reliant Labs
package auth

import "os"

// supabasePublicKeyPEM is the embedded ECDSA public key for JWT verification (ES256).
// This key is safe to embed in the binary as it's public information.
//
// To use your own Supabase instance, set the RELIANT_JWT_PUBLIC_KEY env var
// to your project's ES256 public key in PEM format.
//
// To get the default key:
// 1. Go to your Supabase project JWT Signing Keys settings
// 2. Rotate keys to activate ES256
// 3. Fetch the JWKS from: https://dash.reliantlabs.io/auth/v1/.well-known/jwks.json
// 4. Convert the JWKS to PEM format (see docs/JWT_PUBLIC_KEY_SETUP.md)
const supabasePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEjD9BuhsTjfsW5CIArFahaLAuAI4Q
EHOA77KrAWwjMB2/NaiW0lH+CXIgp719RiyFrdD2tiK+uPJvZhpUVp1qeA==
-----END PUBLIC KEY-----`

// GetJWTPublicKey returns the JWT public key for token verification.
// It checks the RELIANT_JWT_PUBLIC_KEY env var first, falling back to the
// embedded Supabase public key.
func GetJWTPublicKey() string {
	if key := os.Getenv("RELIANT_JWT_PUBLIC_KEY"); key != "" {
		return key
	}
	return supabasePublicKeyPEM
}
