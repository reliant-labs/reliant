// Package a holds the analyzer's main fixture, in a_test.go.
package a

import "os/exec"

// gitHead is production code of the package under test. A test that calls it
// is not judged by what it execs: production branches are many and mostly
// cold, and the timing guard measures what a test actually did.
func gitHead() string {
	out, _ := exec.Command("git", "rev-parse", "HEAD").Output()
	return string(out)
}
