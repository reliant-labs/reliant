package disjoint

// svc satisfies BOTH Reader and Writer with no method overlap.
// All exported methods are accounted for across the union of interfaces,
// so the analyzer should report no diagnostics.
type svc struct{}

func (s *svc) Read() string          { return "" }
func (s *svc) Write(in string) error { return nil }

// Leak() is in NEITHER interface, so it should be reported as a violation
// against the union [Reader, Writer].
func (s *svc) Leak() {} // want `exported method Leak on type svc is not declared in any of the implemented contract interfaces \[Reader, Writer\] \(contract.go\)`
