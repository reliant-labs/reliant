// Copyright (c) 2025 Reliant Labs

package vault

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/crypto"
)

// EnvKey is the environment variable holding the key-encryption key (KEK) as a
// forge keyring: "id:base64,id:base64", first entry primary.
const EnvKey = "RELIANT_VAULT_KEY"

// KeyWrapper wraps and unwraps per-tenant DEKs. It is the seam a KMS or Bao
// Transit implementation would later fill without touching stored secrets.
type KeyWrapper interface {
	// Wrap seals dek under the current primary KEK and reports which KEK id did.
	Wrap(dek, aad []byte) (wrapped []byte, kekID string, err error)
	Unwrap(wrapped, aad []byte) ([]byte, error)
}

// envKeyWrapper is the one v1 implementation: a forge keyring from EnvKey.
type envKeyWrapper struct{ ring *crypto.Keyring }

// NewEnvKeyWrapper builds a KeyWrapper from a parsed keyring.
func NewEnvKeyWrapper(ring *crypto.Keyring) KeyWrapper { return &envKeyWrapper{ring: ring} }

func (w *envKeyWrapper) Wrap(dek, aad []byte) ([]byte, string, error) {
	wrapped, err := w.ring.Seal(dek, aad)
	if err != nil {
		return nil, "", fmt.Errorf("wrapping dek: %w", err)
	}
	return wrapped, w.ring.PrimaryKeyID(), nil
}

func (w *envKeyWrapper) Unwrap(wrapped, aad []byte) ([]byte, error) {
	dek, err := w.ring.Open(wrapped, aad)
	if err != nil {
		return nil, fmt.Errorf("unwrapping dek: %w", err)
	}
	return dek, nil
}
