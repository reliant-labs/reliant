// Mirrors forge's app composition seam: package internal/app carrying
// the forge seam files (compose.go, providers.go). The package has
// exported methods (DefaultClient, Worker accessors) but NO contract.go,
// and must NOT be flagged by the require-contract rule — internal/app is
// the composition site forge's architecture defines, not a
// contract-bearing service package. Zero findings expected.
package app

// Components is the typed per-binary component bag.
type Components struct {
	Widget *widgetService
}

type widgetService struct{}

// NewComponents constructs the component closure from the owned *Infra.
func NewComponents(infra *Infra) (*Components, error) {
	return &Components{Widget: &widgetService{}}, nil
}
