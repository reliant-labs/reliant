package requiremarked

// The contract is derived from the marked type; no contract.go or
// contract_gen.go exists yet (before the first forge generate), and the
// package still declares one.
//
//forge:contract
type service struct{}

func (s *service) DoWork() error {
	return nil
}
