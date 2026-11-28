// Copyright (c) 2026 Reliant Labs

package heavyjob

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/forgetest"
)

func TestKCLDaemonBoundsEnvironment(t *testing.T) {
	forge := forgetest.Pinned(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Forge resolves KCL inputs from the project root, not cmd.Dir.
	cmd := forge.Command(ctx, "env", "kcl", "eval", "deploy/kcl/heavy_bounds_test.k")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("KCL daemon bounds mapping: %v\n%s", err, out)
	}
}
