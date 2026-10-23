package pointer

// impl uses pointer receiver — *impl implements Service, but impl does not.
// The linter must detect this via pointer receiver check.
type impl struct{}

func (i *impl) Run() error { return nil }

func (i *impl) Oops() {} // want `exported method Oops on type impl is not declared in the Service interface \(contract.go\)`

// valImpl uses value receiver — both valImpl and *valImpl implement Service.
type valImpl struct{}

func (v valImpl) Run() error { return nil }

func (v valImpl) Extra() {} // want `exported method Extra on type valImpl is not declared in the Service interface \(contract.go\)`
