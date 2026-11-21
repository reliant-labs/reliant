//go:build integration

package testkit

// integrationLane is true in a binary built with -tags=integration: the
// integration lane, which also compiles every untagged test of the package —
// tests the full tier has already run, so Slow skips them here.
const integrationLane = true
