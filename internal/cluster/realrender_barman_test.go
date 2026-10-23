package cluster

import (
	"os"
	"strings"
	"testing"
)

// TestCollectCertManagerSecretNames_RealRenderedBundle runs the new supply
// collector over a REAL rendered manifest stream captured from
// `forge env render <env>`, when one is supplied via
// FORGE_TEST_RENDERED_BUNDLE. It is the end-to-end counterpart to the
// synthetic cert-manager fixtures: those prove the rule, this proves the rule
// matches the YAML the renderer actually emits (indentation, field ordering,
// the vendored plugin's exact apiVersion).
//
// It asserts on the SUPPLY collector rather than on CheckSecretSupply, because
// the full gate also consumes caller-side supply (KubeconfigSecret /
// ExternalSecret / secret-provider Secrets) that only the cli layer can
// project — a bundle checked with nil supply reports those as missing and
// would make this assertion meaningless.
//
// Skipped when the env var is unset, so it never gates CI on a file only a
// developer's machine has.
func TestCollectCertManagerSecretNames_RealRenderedBundle(t *testing.T) {
	path := os.Getenv("FORGE_TEST_RENDERED_BUNDLE")
	if path == "" {
		t.Skip("set FORGE_TEST_RENDERED_BUNDLE to a rendered manifest stream to run this")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	manifests := string(b)

	certSupplied := CollectCertManagerSecretNames(manifests)
	if len(certSupplied) == 0 {
		t.Skip("bundle renders no cert-manager Certificates — nothing for this check to prove")
	}

	// Every Secret a Certificate materialises must be recognised as supply,
	// so the gate cannot report it as an undeclared mount.
	misses := CheckSecretSupply(manifests, nil)
	for _, m := range misses {
		if _, isCert := certSupplied[m.Secret]; isCert {
			t.Errorf("Secret %q is materialised by a cert-manager Certificate in this bundle but was reported as an undeclared mount (by %s)",
				m.Secret, strings.Join(m.Workloads, ", "))
		}
	}
}
