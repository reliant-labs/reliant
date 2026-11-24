package testhost

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestModuleDownloadFailedMatchesOnlyTransportFailures(t *testing.T) {
	for out, want := range map[string]bool{
		// forge#2479, verbatim: an HTTP/2 stream reset from the proxy.
		`github.com/reliant-labs/forge/pkg/contractkit: github.com/reliant-labs/forge/pkg@v0.1.15: read "https://proxy.golang.org/github.com/reliant-labs/forge/pkg/@v/v0.1.15.mod": stream error: stream ID 25; INTERNAL_ERROR; received from peer`: true,
		`connectrpc.com/connect@v1.19.0: Get "https://proxy.golang.org/connectrpc.com/connect/@v/v1.19.0.mod": net/http: TLS handshake timeout`:                                                                                                      true,
		`example.com/dep@v1.0.0: reading https://proxy.golang.org/example.com/dep/@v/v1.0.0.mod: 502 Bad Gateway`:                                                                                                                                    true,
		`example.com/dep@v1.0.0: reading https://proxy.golang.org/example.com/dep/@v/v1.0.0.mod: 404 Not Found`:                                                                                                                                      false,
		`internal/pipeline/mock_gen.go:12:2: undefined: db.Estimate`:                                                                                                                                                                                 false,
		`missing go.sum entry for module providing package example.com/dep`:                                                                                                                                                                          false,
	} {
		if got := ModuleDownloadFailed([]byte(out)); got != want {
			t.Errorf("ModuleDownloadFailed(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestPostgresDownloadFailedMatchesOnlyTransportFailures(t *testing.T) {
	wrap := func(fe *pgtest.FetchError) error {
		return fmt.Errorf("pgtest: start embedded postgres 18.3.0: %w", fe)
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"no answer":           {wrap(&pgtest.FetchError{URL: "https://repo1.maven.org/x.jar", Attempts: 4, Err: errors.New("dial tcp: i/o timeout")}), true},
		"5xx":                 {wrap(&pgtest.FetchError{URL: "https://repo1.maven.org/x.jar", Status: 503, Attempts: 4}), true},
		"body broke off":      {wrap(&pgtest.FetchError{URL: "https://repo1.maven.org/x.jar", Status: 200, Attempts: 4, Err: io.ErrUnexpectedEOF}), true},
		"404 is an answer":    {wrap(&pgtest.FetchError{URL: "https://repo1.maven.org/x.jar", Status: 404, Attempts: 1}), false},
		"checksum mismatch":   {wrap(&pgtest.FetchError{URL: "https://repo1.maven.org/x.jar", Status: 200, Attempts: 1, Err: &pgtest.ChecksumError{}}), false},
		"not a download":      {errors.New("pgtest: connect: connection refused"), false},
		"nil is not a reason": {nil, false},
	} {
		if got := PostgresDownloadFailed(tc.err); got != tc.want {
			t.Errorf("%s: PostgresDownloadFailed(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}
