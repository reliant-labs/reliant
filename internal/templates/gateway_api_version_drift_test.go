package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gatewayAPIVersionRef matches a Gateway API release version wherever it is
// written as part of an upstream release URL or an install-bundle reference —
// the two shapes a hardcoded copy actually takes.
var gatewayAPIVersionRef = regexp.MustCompile(`gateway-api/releases/download/(v[0-9]+\.[0-9]+\.[0-9]+)/`)

// pinnedGatewayAPIVersion reads gateway_api= out of the embedded ingress
// VERSION file — the single source of truth for which Gateway API bundle
// forge installs and applies (internal/templates/ingress/envoy/VERSION).
func pinnedGatewayAPIVersion(t *testing.T) string {
	t.Helper()
	b, err := IngressTemplates().Get("envoy/VERSION")
	if err != nil {
		t.Fatalf("read pinned ingress VERSION: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "gateway_api" {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal("ingress VERSION declares no gateway_api= pin")
	return ""
}

// TestNoShippedFileHardcodesAGatewayAPIVersion is the drift guard.
//
// forge pins the Gateway API bundle ONCE, in internal/templates/ingress/envoy/
// VERSION, and installs it declaratively for every env (the `gateway-api` CRD
// bundle on a forge.HelmChart). A second copy of the version written into a
// scaffolded template, a skill, or a doc is not a pin — it is a copy that
// drifts silently, because nothing fails when the two disagree.
//
// That is not hypothetical. control-plane's CI hand-applied
// gateway-api/releases/download/v1.2.1/standard-install.yaml from two
// workflow files while forge pinned v1.6.2. v1.2.1 predates
// Gateway.spec.allowedListeners entirely, so the k3d smoke test failed every
// run with
//
//	.spec.allowedListeners: field not declared in schema
//
// on a cluster four minor versions behind the one prod runs. Nobody noticed
// for as long as it took to matter, because a stale literal in a YAML file
// has no guard. This test is that guard for forge's OWN shipped surface:
// scaffold a hardcoded version into a template or document one in a skill and
// the build fails here, naming the pin to use instead.
//
// Scoped to the version-bearing shapes, not to the string "gateway-api":
// prose may reference the project freely, and the URL BUILDER
// (internal/cli/dev_cluster_ingress.go, which interpolates the pinned
// version) is the correct way to name a release.
func TestNoShippedFileHardcodesAGatewayAPIVersion(t *testing.T) {
	pinned := pinnedGatewayAPIVersion(t)

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// Trees forge SHIPS: scaffolded templates (including the project skills),
	// the KCL library every project imports, and the documentation a user
	// follows. A stale version in any of these reaches a user verbatim.
	shipped := []string{
		filepath.Join(root, "internal", "templates"),
		filepath.Join(root, "kcl"),
		filepath.Join(root, "docs"),
	}

	var violations []string
	for _, tree := range shipped {
		err := filepath.WalkDir(tree, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "node_modules" || name == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			// Go tests are allowed to name a version as INPUT — asserting the
			// URL builder interpolates correctly requires a literal.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, m := range gatewayAPIVersionRef.FindAllStringSubmatch(string(b), -1) {
				if m[1] != pinned {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, fmt.Sprintf("%s hardcodes Gateway API %s", rel, m[1]))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("shipped file(s) hardcode a Gateway API version other than the %s pin "+
			"in internal/templates/ingress/envoy/VERSION:\n  %s\n\n"+
			"Reference the pinned bundle instead of a literal: forge installs it declaratively "+
			"via the `gateway-api` CRD bundle on a forge.HelmChart, so a project needs no "+
			"hand-rolled kubectl apply at all.",
			pinned, strings.Join(violations, "\n  "))
	}
}
