package embedded

type svc struct{}

func (s *svc) Start() error { return nil }
func (s *svc) Stop() error  { return nil }

// With per-method enforcement (bug #20 fix), the embedded-superset pruning
// that used to collapse Base+Service into a single Service report is gone.
// The diagnostic now lists every implemented contract interface so the
// reader can see the full set the method was checked against.
func (s *svc) Leak() {} // want `exported method Leak on type svc is not declared in any of the implemented contract interfaces \[(Service, Base|Base, Service)\] \(contract.go\)`
