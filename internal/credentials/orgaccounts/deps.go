// forge:deps — consumer-declared interfaces for this package's Deps.
package orgaccounts

import "github.com/reliant-labs/reliant/internal/credentials/accountseal"

// Sealer is the slice of the credential vault a rotated key is sealed with. vault.Service
// satisfies it.
type Sealer = accountseal.Sealer
