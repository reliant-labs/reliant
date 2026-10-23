//go:build !integration

package testkit

// integrationLane is true in a binary built with -tags=integration: the
// integration lane. This build has no tag; see lane_integration.go.
const integrationLane = false
