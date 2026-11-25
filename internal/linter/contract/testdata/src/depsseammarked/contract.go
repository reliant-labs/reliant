// Package depsseammarked provides a marked, non-Service contract and also
// takes the next one in a chain through Deps.
package depsseammarked

// Gateway is the package's provided contract under a role name.
//
//forge:contract
type Gateway interface {
	Send(msg string) error
}

type Deps struct {
	Next Gateway
}
