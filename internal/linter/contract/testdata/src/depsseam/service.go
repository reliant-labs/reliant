package depsseam

// Brokers is a registry with its own public API. It satisfies only the
// BrokerLookup seam, so Register is not a leak past the contract.
type Brokers struct{ m map[string]string }

func NewBrokers() *Brokers { return &Brokers{m: map[string]string{}} }

func (b *Brokers) Get(id string) (string, bool) { v, ok := b.m[id]; return v, ok }

func (b *Brokers) Register(id, v string) error { b.m[id] = v; return nil }

// fixedClock satisfies only the Clock seam.
type fixedClock struct{}

func (fixedClock) Now() int64 { return 0 }

func (fixedClock) Advance(d int64) {}

// service implements the provided contract, which binds even though Deps
// names it too.
type service struct{ deps Deps }

func New(deps Deps) Service { return &service{deps: deps} }

func (s *service) Name() string { return "depsseam" }

func (s *service) Leak() {} // want `exported method Leak on type service is not declared in the Service interface \(contract.go\)`

// both satisfies the Service AND a seam: it is bound, and the seam's method
// is still allowed on it.
type both struct{}

func (both) Name() string                 { return "both" }
func (both) Get(id string) (string, bool) { return "", false }
func (both) Extra()                       {} // want `exported method Extra on type both is not declared in any of the implemented contract interfaces \[BrokerLookup, Service\] \(contract.go\)`

// fileSink satisfies Sink, which Deps names only inside a func signature.
type fileSink struct{}

func (fileSink) Write(s string) error { return nil }

func (fileSink) Flush() {} // want `exported method Flush on type fileSink is not declared in the Sink interface \(contract.go\)`
