package serverkit_test

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// A real `go build -cover -covermode=atomic` server answers with the files
// `go tool covdata` reads, and resets in atomic mode; the same server built
// without -cover answers 404. The fake-driven tests
// (coverage_internal_test.go) pin every other answer.
func TestCoverageHandler_RealCoverBuild(t *testing.T) {
	testkit.Slow(t, "compiles testdata/covserver with and without -cover and runs it")
	dir := t.TempDir()

	cover := startCovServer(t, filepath.Join(dir, "cover"), "-cover", "-covermode=atomic")
	for _, target := range []string{serverkit.CoveragePath, serverkit.CoveragePath + "?reset=1"} {
		code, body := getOnce(t, "http://"+cover+target)
		if code != http.StatusOK {
			t.Fatalf("%s answered %d: %s", target, code, body)
		}
		var meta, counters bool
		tr := tar.NewReader(bytes.NewReader(body))
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s: not a tar: %v", target, err)
			}
			meta = meta || strings.HasPrefix(hdr.Name, "covmeta.")
			counters = counters || strings.HasPrefix(hdr.Name, "covcounters.")
		}
		if !meta || !counters {
			t.Errorf("%s: tar has meta=%v counters=%v, want both", target, meta, counters)
		}
	}

	plain := startCovServer(t, filepath.Join(dir, "plain"))
	if code, _ := getOnce(t, "http://"+plain+serverkit.CoveragePath); code != http.StatusNotFound {
		t.Errorf("binary built without -cover answered %d, want 404", code)
	}
}

// getOnce GETs url once. covserver prints its address only after its
// listener is bound, so the connection is accepted with no retry: the event
// the test waits on is that line, not a duration.
func getOnce(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// startCovServer builds testdata/covserver to bin with flags, runs it, and
// returns the address it serves on.
func startCovServer(t *testing.T, bin string, flags ...string) string {
	t.Helper()
	args := append(append([]string{"build"}, flags...), "-o", bin, "./testdata/covserver")
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	// The exit flush writes here; without it Go warns on stderr.
	t.Setenv("GOCOVERDIR", t.TempDir())
	cmd := exec.Command(bin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if addr, ok := strings.CutPrefix(sc.Text(), "ADDR "); ok {
			return addr
		}
	}
	t.Fatal("covserver printed no address")
	return ""
}
