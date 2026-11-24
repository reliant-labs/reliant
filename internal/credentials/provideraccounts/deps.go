// forge:deps — consumer-declared interfaces for this package's Deps.
package provideraccounts

import "github.com/reliant-labs/reliant/internal/credentials/accountseal"

// Sealer is the slice of the credential vault the secret columns are sealed with.
// vault.Service satisfies it.
type Sealer = accountseal.Sealer
