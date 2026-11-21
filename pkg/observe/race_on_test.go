//go:build race

package observe

// raceEnabled reports whether the tests run under the race detector, which
// changes allocation counts (it drops sync.Pool entries at random and
// instruments the code), so an allocation assertion is not meaningful there.
const raceEnabled = true
