//go:build windows

package debug

import (
	"os"
	"strings"
	"testing"
)

func TestProcessCommandLineSelf(t *testing.T) {
	got, err := processCommandLine(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(got), ".test") {
		t.Fatalf("command line %q does not look like the test binary", got)
	}
}
