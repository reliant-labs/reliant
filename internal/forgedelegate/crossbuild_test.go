package forgedelegate

import (
	"os"
	"os/exec"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestBuildsOnTargetsWithoutFlock pins the lock files' build constraints:
// the package (and pkg/hostslot under it) must compile where x/sys/unix has
// no flock(2), falling back to lock_other.go, as well as where it has one.
// wasm is not a target: the package runs other programs (go install, exec of
// the pinned forge), which no wasm port can.
//
// The primary targets are vetted, test files included. AIX and Plan 9 are
// built, not vetted: the tests' own dependencies (pkg/testkit, and through
// it connect and pgtest) do not compile there, and no test runs there. What
// must hold on them is that forge itself builds.
func TestBuildsOnTargetsWithoutFlock(t *testing.T) {
	testkit.Slow(t, "execs go vet / go build to cross-compile the package once per target",
		testkit.Inputs("internal/forgedelegate/**", "pkg/hostslot/**", "go.mod", "go.sum"))
	for _, target := range []struct{ goos, goarch, verb string }{
		{"linux", "amd64", "vet"}, {"darwin", "arm64", "vet"}, {"windows", "amd64", "vet"},
		{"aix", "ppc64", "build"}, {"plan9", "amd64", "build"},
	} {
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			t.Parallel()
			args := []string{target.verb, "."}
			if target.verb == "build" {
				args = []string{"build", "-o", os.DevNull, "."}
			}
			cmd := exec.Command("go", args...)
			cmd.Env = append(os.Environ(), "GOOS="+target.goos, "GOARCH="+target.goarch, "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go %s for %s/%s: %v\n%s", target.verb, target.goos, target.goarch, err, out)
			}
		})
	}
}
