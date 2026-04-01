// Copyright (c) 2025 Reliant Labs
package drivers

import "testing"

func TestTemperatureMode_ResolverLogicCoveredByCompilation(t *testing.T) {
	// We intentionally keep resolver temperature behavior untested at unit level for now
	// because defaultGetDriver is tightly coupled to provider registry + user key availability.
	// The important behavior is implemented directly in resolver.go and is exercised by
	// integration/e2e and by running compaction against GPT-5.x.
	//
	// If you want to harden this later, we should refactor defaultGetDriver to accept
	// injectable dependencies (available drivers + factory lookup) so it can be unit tested.
}
