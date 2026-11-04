package serverkit_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// Run must actually SERVE profiles on PprofAddr, and must not serve them on
// the public Addr.
//
// This is the property the whole always-on default exists for, and it is
// worth pinning rather than inferring from "the listener bound": a mux with
// the wrong prefix, or a pprof handler mounted on the public edge, both look
// identical in the logs and are both wrong. The heap body is decoded here
// because "200 OK with an empty body" is the failure mode that would still
// leave you unable to answer what a process is holding.
func TestRun_ServesHeapProfileOnItsOwnListener(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM through shutdownAndWait.
	mainAddr := freeAddr(t)
	pprofAddr := freeAddr(t)

	cfg := baseConfig(mainAddr)
	cfg.PprofAddr = pprofAddr
	errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: emptyHandler()})
	waitReady(t, mainAddr, 2*time.Second)

	// A heap profile is a gzipped protobuf. Decoding the gzip stream proves
	// this is a real profile rather than an error page with a 200 on it.
	body := getWithin(t, "http://"+pprofAddr+"/debug/pprof/heap", 2*time.Second)
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("/debug/pprof/heap did not return a gzipped profile (%v); first bytes: %q", err, head(body))
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress heap profile: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("heap profile decompressed to nothing — a 200 with no profile in it")
	}

	// The index is what `forge env status`'s pprof check parses.
	index := getWithin(t, "http://"+pprofAddr+"/debug/pprof/", 2*time.Second)
	for _, want := range []string{"heap", "goroutine", "allocs"} {
		if !bytes.Contains(index, []byte(want)) {
			t.Errorf("/debug/pprof/ index does not list %q:\n%s", want, head(index))
		}
	}

	// SEPARATE listener: the public edge must not serve pprof. Its endpoints
	// can leak memory contents and stall the process, and the public port is
	// the one with a k8s Service in front of it.
	resp, err := http.Get("http://" + mainAddr + "/debug/pprof/heap")
	if err != nil {
		t.Fatalf("probe public addr: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("public listener answered /debug/pprof/heap with %d — pprof must never ride the public edge", resp.StatusCode)
	}

	if runErr := shutdownAndWait(t, errCh, 5*time.Second); runErr != nil {
		t.Fatalf("Run returned %v on shutdown, want nil", runErr)
	}
}

// getWithin GETs url, retrying until it succeeds with a 200 or the deadline
// passes. The pprof listener starts on its own goroutine, so the first
// request can land before Serve is accepting.
func getWithin(t *testing.T, url string, within time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		b, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return b
		}
		lastErr = errStatus(resp.StatusCode)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s did not return 200 within %s (last: %v)", url, within, lastErr)
	return nil
}

type errStatus int

func (e errStatus) Error() string { return "status " + http.StatusText(int(e)) }

// head trims a body for an error message.
func head(b []byte) []byte {
	if len(b) > 200 {
		return b[:200]
	}
	return b
}
