package seamgenerated

// Impl implements Service.
type Impl struct{}

// Do is declared in the contract interface — allowed.
func (i *Impl) Do() string { return "ok" }

// Rogue is a HAND-WRITTEN exported method outside the contract — the
// single-seam rule stays strict on user code even though a sibling
// generated file is exempt.
func (i *Impl) Rogue() string { return "flagged" } // want `exported method Rogue on type Impl is not declared in the Service interface \(contract.go\)`
