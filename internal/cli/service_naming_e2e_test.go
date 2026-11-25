//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// Wire identifiers follow proto spelling; Forge's own helpers remain idiomatic
// Go. Exercise an existing owned service.go, not only a newly rendered template.
func TestE2EEquivalentServiceNamingGeneratedLint(t *testing.T) {
	t.Parallel()
	requireTool(t, "golangci-lint")
	bin := buildforgeBinary(t)
	contractDir := buildContractlintBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, bin, "project", "new", "namingapp", "--mod", "example.com/namingapp", "--service", "api_key")
	root := filepath.Join(dir, "namingapp")
	addCorpusForgePkgReplace(t, root)
	proto := filepath.Join(root, "proto/services/api_key/v1/api_key.proto")
	source := strings.ReplaceAll(readFileE2E(t, proto), "APIKeyService", "ApiKeyService")
	source = strings.Replace(source, "  // TODO: Add your RPC methods here.", "  rpc CreateApiKey(CreateApiKeyRequest) returns (CreateApiKeyResponse) {\n"+
		"    option (forge.v1.method) = {\n"+
		"      activity: {none: \"a naming fixture: it records nothing\"}\n"+
		"    };\n  }", 1)
	source += "\nmessage CreateApiKeyRequest {}\nmessage CreateApiKeyResponse {}\n"
	writeFileE2E(t, proto, source)
	// The proto-format lane gates at every pin. The rpc above is written as
	// forge's pinned buf formats it, but buf releases disagree on a
	// one-field option literal, and lint runs PATH's buf first: format the
	// test's own input with the buf lint will use, before forge touches it.
	runCmd(t, root, bin, "lint", "--proto-format", "--fix")
	runCmd(t, root, bin, "generate")
	runCmd(t, root, "go", "build", "./...")
	out, err := runLintE2E(t, root, contractDir, bin, "lint", "--no-fix")
	if err != nil {
		t.Fatalf("equivalent proto spelling must build and lint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ golangci-lint") {
		t.Fatalf("Go lint did not run:\n%s", out)
	}
}

// A service whose name merely starts with an initialism ("identity" starts
// with "id") must scaffold as IdentityService, generate and build.
func TestE2EInitialismPrefixServiceName(t *testing.T) {
	t.Parallel()
	bin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, bin, "project", "new", "idapp", "--mod", "example.com/idapp", "--service", "identity")
	root := filepath.Join(dir, "idapp")
	addCorpusForgePkgReplace(t, root)
	proto := readFileE2E(t, filepath.Join(root, "proto/services/identity/v1/identity.proto"))
	if !strings.Contains(proto, "service IdentityService") {
		t.Fatalf("proto must declare IdentityService:\n%s", proto)
	}
	runCmd(t, root, bin, "generate")
	runCmd(t, root, "go", "build", "./...")
}
