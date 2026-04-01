// Copyright (c) 2025 Reliant Labs
package auth

// SupabasePublicKeyPEM is the embedded ECDSA public key for JWT verification (ES256)
// This key is safe to embed in the binary as it's public information
//
// To get your public key:
// 1. Go to your Supabase project JWT Signing Keys settings
// 2. Rotate keys to activate ES256
// 3. Fetch the JWKS from: https://dash.reliantlabs.io/auth/v1/.well-known/jwks.json
// 4. Convert the JWKS to PEM format (see docs/JWT_PUBLIC_KEY_SETUP.md)
//
// IMPORTANT: This is your Supabase ES256 public key
const SupabasePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEjD9BuhsTjfsW5CIArFahaLAuAI4Q
EHOA77KrAWwjMB2/NaiW0lH+CXIgp719RiyFrdD2tiK+uPJvZhpUVp1qeA==
-----END PUBLIC KEY-----`
