// Copyright (c) 2025 Reliant Labs
package cptransport

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		// Prod, e2e and dev-k8s: control-plane's admin-server Service.
		{"http://admin-server.control-plane-prod.svc.cluster.local:8090", true},
		{"http://admin-server.control-plane-prod.svc:8090", true},
		{"http://ADMIN-SERVER.control-plane-prod.SVC.cluster.local.:8090", true},
		// Dev: host processes on loopback.
		{"http://127.0.0.1:8090", true},
		{"http://localhost:8090", true},
		{"http://[::1]:8090", true},
		{"https://controller.example", true},
		{"https://admin-server.control-plane-prod.svc.cluster.local", true},

		// Plaintext that can leave the machine or the cluster.
		{"http://external.example", false},
		{"http://cp.reliant.dev:8090", false},
		{"http://10.0.0.8:8090", false},
		{"http://admin-server:8090", false},              // search-domain resolution
		{"http://control-plane-prod.svc:8090", false},    // no namespace
		{"http://svc.cluster.local:8090", false},         // no service or namespace
		{"http://a.b.svc.cluster.local.evil.com", false}, // suffix spoof
		{"http://a.b.svc.example.com", false},
		{"http://a..svc", false},
		{"ftp://admin-server.ns.svc", false},
		{"http://", false},
		{"", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			require.NoError(t, err)
			if tc.ok {
				require.NoError(t, Check(u))
			} else {
				require.ErrorIs(t, Check(u), ErrInsecure)
			}
		})
	}
	require.ErrorIs(t, Check(nil), ErrInsecure)
}
