package release

import (
	"errors"
	"testing"
)

// TestShape_ApplierValues: a shape records "forge", "flux" or nothing as its
// declared applier, and refuses any other word rather than guessing who
// applies the env.
func TestShape_ApplierValues(t *testing.T) {
	t.Parallel()
	for _, applier := range []string{"", ApplierForge, ApplierFlux} {
		if err := (Shape{Applier: applier}).Validate(); err != nil {
			t.Errorf("applier %q: %v", applier, err)
		}
	}
	if err := (Shape{Applier: "hub"}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("applier hub: Validate = %v, want ErrInvalid", err)
	}
}
