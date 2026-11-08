// A package that LIVES under internal/operators/ but is not an operator: no
// SetupWithManager. The operator exemption is structural, so the rule still
// applies. Finding expected.
package notanoperator // want "package notanoperator has exported methods but no contract.go"

// Helper is an ordinary behavioral surface.
type Helper struct{}

// Compute does domain work.
func (h *Helper) Compute() error { return nil }
