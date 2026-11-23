// forge:deps — consumer-declared interfaces for this package's Deps.
package automationtokens

import (
	"context"

	"github.com/reliant-labs/reliant/internal/vault"
)

// Sealer is the slice of the credential vault this package seals and opens with.
// vault.Service satisfies it.
type Sealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}
