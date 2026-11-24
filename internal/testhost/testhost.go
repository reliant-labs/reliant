// Package testhost turns a download the host could not complete into a skip
// that names what was missing, for the tests that fetch something from the
// network as a side effect: a module from the Go proxy, the embedded-postgres
// archive from Maven Central.
//
// WHY. A registry or network failure says nothing about the code under test,
// yet it failed tests on forge main, and each failure filed a flake:suspected
// issue (forge#2479: a go build's module fetch hit "stream error ... INTERNAL_ERROR"
// from proxy.golang.org; forge#2360-#2363: the embedded-postgres jar fetch
// failed). The owner's rule for a failure the host causes is a t.Skip naming
// the missing resource, never a failure. Only transport failures qualify: a
// 404, a checksum mismatch or any other answer that is about the request
// itself still fails the test.
//
// Test-only by intent: nothing outside _test.go files imports it.
//
//forge:exclude-contract: test-only: skips tests whose network download failed; imported only by _test.go files
package testhost

import (
	"errors"
	"net/http"
	"regexp"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// moduleTransportFailure matches cmd/go's report of a module download that
// failed in transport: the request never got an answer (`Get "<url>": …`,
// `read "<url>": …` for an HTTP/2 stream reset), or the proxy answered 5xx.
// A 404 or 410 is the proxy's answer about the module and stays a failure.
var moduleTransportFailure = regexp.MustCompile(`(?:Get|read) "https?://[^"]+": |reading https?://\S+: 5\d\d `)

// ModuleDownloadFailed reports whether out, the combined output of a go
// command, shows a module download that failed in transport.
func ModuleDownloadFailed(out []byte) bool {
	return moduleTransportFailure.Match(out)
}

// SkipIfModuleDownloadFailed skips t when out, the combined output of a go
// command that failed, shows a module download that failed in transport.
// It returns when it does not, so the caller goes on to fail the test.
func SkipIfModuleDownloadFailed(t testing.TB, out []byte) {
	t.Helper()
	if m := moduleTransportFailure.Find(out); m != nil {
		t.Skipf("the Go module proxy could not be reached (%s…): a host network failure, not a defect; the test needs the module downloads to complete", m)
	}
}

// PostgresDownloadFailed reports whether err, from pgtest, is the embedded
// postgres archive download failing in transport: no answer, a 5xx, or a
// body that broke off. A 404 or a checksum mismatch is not.
func PostgresDownloadFailed(err error) bool {
	var fe *pgtest.FetchError
	if !errors.As(err, &fe) {
		return false
	}
	var ce *pgtest.ChecksumError
	if errors.As(err, &ce) {
		return false
	}
	switch {
	case fe.Status == 0, fe.Status >= http.StatusInternalServerError:
		return true
	case fe.Status == http.StatusOK:
		return fe.Err != nil // the read of the archive failed
	}
	return false
}

// SkipIfPostgresDownloadFailed skips t when err, from pgtest, is the embedded
// postgres archive download failing in transport. It returns when it is not,
// so the caller goes on to fail the test.
func SkipIfPostgresDownloadFailed(t testing.TB, err error) {
	t.Helper()
	if PostgresDownloadFailed(err) {
		t.Skipf("the embedded postgres archive could not be downloaded: a host network failure, not a defect; set FORGE_TEST_POSTGRES_URL to use a server of your own (%v)", err)
	}
}
