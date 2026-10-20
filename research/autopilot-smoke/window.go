// Package autopilotsmoke is a throwaway file for an Autopilot PR-review smoke
// test. This PR will be closed without merging.
package autopilotsmoke

// LastN returns the last n items of xs, or all of xs when it has fewer.
func LastN(xs []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(xs) < n {
		return xs
	}
	return xs[len(xs)-n-1:]
}
