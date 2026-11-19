package depsseammarked

type gateway struct{ deps Deps }

func New(deps Deps) Gateway { return &gateway{deps: deps} }

func (g *gateway) Send(msg string) error { return nil }

func (g *gateway) Retry() {} // want `exported method Retry on type gateway is not declared in the Gateway interface \(contract.go\)`
