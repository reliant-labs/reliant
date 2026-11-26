package cli

import (
	"context"
	"strings"
	"testing"
)

// prepareComposition refuses a malformed flag combination before it renders,
// reads a ledger or touches a registry, so every refusal below is reachable with
// no project at all.
func TestCovC_PrepareCompositionRefusesBadFlagCombinationsFirst(t *testing.T) {
	tests := []struct {
		name      string
		opts      buildOptions
		base      string
		bootstrap bool
		want      string
	}{
		{"needs a release", buildOptions{env: "prod"}, "promo-1", false, "snapshot composition requires --release"},
		{"bootstrap and base are exclusive", buildOptions{env: "prod", release: "v1"}, "promo-1", true, "--snapshot-bootstrap and --base-promotion are mutually exclusive"},
		{"one of base or bootstrap is required", buildOptions{env: "prod", release: "v1"}, "", false, "requires --base-promotion or --snapshot-bootstrap"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			err := prepareComposition(context.Background(), &opts, tc.base, tc.bootstrap, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
			if opts.composition != nil || opts.targets != nil {
				t.Errorf("a refused composition must not leave half-built state: %+v", opts)
			}
		})
	}
}

func TestCovC_PrepareCompositionReturnsARenderFailureUnchanged(t *testing.T) {
	prev := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = prev })
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return nil, errCovCRender }
	opts := buildOptions{env: "prod", release: "v1"}
	if err := prepareComposition(context.Background(), &opts, "", true, nil); err == nil || !strings.Contains(err.Error(), errCovCRender.Error()) {
		t.Fatalf("err = %v", err)
	}
}

var errCovCRender = &covCErr{"kcl render exploded"}

type covCErr struct{ s string }

func (e *covCErr) Error() string { return e.s }
