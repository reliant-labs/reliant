package openbao

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fake API must let a host runner with a different UID read and remove its
// objects. Checking modes reproduces the CI boundary even when the local runner
// happens to have the same UID as the converge container (or is root).
func TestKubectlShimStateIgnoresClientUmask(t *testing.T) {
	if testing.Short() {
		t.Skip("executes the bash/jq kubectl shim; runs in task test")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	mustMkdir(t, state)
	shim := filepath.Join(dir, "kubectl")
	mustWrite(t, shim, strings.Replace(kubectlShim, "S=/state", `S="$1"; shift`, 1), 0o755)

	for _, verb := range []string{"create", "apply"} {
		t.Run(verb, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `umask 077; exec bash "$@"`, "shim", shim, state, verb, "-f", "-")
			cmd.Stdin = strings.NewReader(`{"kind":"Secret","metadata":{"name":"recovery","namespace":"openbao"},"data":{"init.json":"aW5pdA=="}}`)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("kubectl %s: %v\n%s", verb, err, out)
			}
			for _, rel := range []string{"openbao", "openbao/secret-recovery", "openbao/secret-recovery/.uid", "openbao/secret-recovery/init.json"} {
				info, err := os.Stat(filepath.Join(state, rel))
				if err != nil {
					t.Fatal(err)
				}
				// Other-UID directory write/search permits host cleanup; file read
				// permits the recovery material and ledger assertions.
				want := os.FileMode(0o004)
				if info.IsDir() {
					want = 0o007
				}
				if got := info.Mode().Perm(); got&want != want {
					t.Errorf("%s permissions %04o: need other-UID bits %04o for host assertions/cleanup", rel, got, want)
				}
			}
			if got, err := os.ReadFile(filepath.Join(state, "openbao", "secret-recovery", "init.json")); err != nil || string(got) != "aW5pdA==" {
				t.Fatalf("recovery data = %q, %v; want exact API payload", got, err)
			}
		})
	}
}
