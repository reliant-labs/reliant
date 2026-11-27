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
	cmd := forge.Command(ctx, "env", "kcl", "eval", "heavy_bounds_test.k")
	cmd.Dir = "../../deploy/kcl"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("KCL daemon bounds mapping: %v\n%s", err, out)
	}
}
