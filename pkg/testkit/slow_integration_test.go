//go:build integration

package testkit

import (
	"strings"
	"testing"
)

// Under -tags=integration the lane constant flips and Slow skips even without
// -short: the untagged tests this lane also compiles already ran in the full
// tier.
func TestSlowSkipsInTheIntegrationLane(t *testing.T) {
	withShort(t, false, func() {
		rec := &recordingTB{TB: t}
		Slow(rec, "builds the forge binary")
		if !strings.Contains(rec.skipped, "integration lane") || !strings.Contains(rec.skipped, "full tier") {
			t.Fatalf("Slow did not skip in the integration lane with a message saying where it runs: %q", rec.skipped)
		}
		if !SlowSkipped() {
			t.Error("SlowSkipped() = false in the integration lane")
		}
	})
}
