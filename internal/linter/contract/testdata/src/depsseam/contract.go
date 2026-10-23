// Package depsseam declares, beside its Service, narrow interfaces its Deps
// consumes. Its own types satisfy some of them (connauth's shape: a Brokers
// registry the package also takes through a Get-only seam).
package depsseam

// Service is the package's provided contract.
type Service interface {
	Name() string
}

// BrokerLookup is a seam Deps consumes. *Brokers satisfies it.
type BrokerLookup interface {
	Get(id string) (string, bool)
}

// Clock is a seam Deps holds in a slice.
type Clock interface {
	Now() int64
}

// Sink is named only as a func field's parameter: a value the package hands
// out, so it is not a seam and still binds.
type Sink interface {
	Write(s string) error
}

// Deps names BrokerLookup and Clock as requirements, and Service as the next
// implementation in a chain (the decorator shape): a Deps field typed as the
// provided contract does not exempt it.
type Deps struct {
	Brokers BrokerLookup
	Clocks  []Clock
	Next    Service
	OnEvent func(Sink)
}
