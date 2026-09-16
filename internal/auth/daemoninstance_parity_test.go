// Copyright (c) 2025 Reliant Labs

package auth

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/daemoninstance"
)

// TestEndpointKeyParityWithDaemonInstance pins this package's endpointKey — the
// credentials-store key — against daemoninstance.OriginKey, the origin component
// of an instance key.
//
// The two are deliberately separate implementations: the instance key package is
// a leaf that must not depend on this one, since it is the credentials store
// that will come to reference an instance and not the reverse. That decision is
// only safe if the duplication cannot drift, which is what this test buys. If it
// ever fails, a daemon writes its runtime record under one instance while
// reading its credentials from another — split-brain that presents as "my PAT
// disappeared", with nothing in the logs naming the cause.
//
// electron/src/daemon-creds.js carries a third hand-mirrored copy, pinned by its
// own tests in electron/test/daemon-creds.test.js.
//
// The test lives here rather than in daemoninstance because endpointKey is
// unexported, and exporting it purely to be asserted against would widen this
// package's surface for a test's benefit.
func TestEndpointKeyParityWithDaemonInstance(t *testing.T) {
	inputs := []string{
		// Every entry present in a real ~/.reliant/daemon.json.
		"http://127.0.0.1:8123",
		"http://localhost:3090",
		"http://localhost:3091",
		"http://localhost:3123",
		"http://localhost:3691",
		"http://localhost:8090",
		"http://localhost:8690",
		"https://api.reliantapi.com",
		"https://preprod.reliantapi.com",

		// The shapes where two reasonable implementations disagree.
		"https://staging.reliantapi.com/grpc",
		"https://staging.reliantapi.com/api?q=1",
		"https://staging.reliantapi.com/grpc#frag",
		"HTTPS://API.Example.COM/path",
		"http://[::1]:8080",
		"//host-relative",

		// Refusal cases: both must decline, neither may default.
		"",
		"   ",
		"not-a-url",
		"https://",
	}

	for _, in := range inputs {
		want := endpointKey(in)
		got := daemoninstance.OriginKey(in)
		if got != want {
			t.Errorf("origin drift for %q: auth.endpointKey = %q, daemoninstance.OriginKey = %q", in, want, got)
		}
	}
}
