//go:build !race

package testkit

// raceEnabled is true in a binary built with -race; this build has no race
// detector. See race.go.
const raceEnabled = false
