package installers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The MCP registry listing (npm/server.json) and the Smithery config
// (npm/smithery.yaml) describe the npm shim run as `forge mcp`. The registry
// verifies an npm package by the package.json `mcpName` matching the
// listing's name, and serves the package version the listing names, so those
// are held equal here; scripts/release-forge.sh moves both versions together.
func TestMCPRegistryListingMatchesTheNpmPackage(t *testing.T) {
	var server struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Packages    []struct {
			RegistryType     string                `json:"registryType"`
			RegistryBaseURL  string                `json:"registryBaseUrl"`
			Identifier       string                `json:"identifier"`
			Version          string                `json:"version"`
			Transport        struct{ Type string } `json:"transport"`
			PackageArguments []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"packageArguments"`
		} `json:"packages"`
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		MCPName string `json:"mcpName"`
	}
	for rel, v := range map[string]any{"npm/server.json": &server, "npm/package.json": &pkg} {
		data, err := os.ReadFile(repoFile(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	if server.Name != "io.github.reliant-labs/forge" || pkg.MCPName != server.Name {
		t.Errorf("server.json name %q, package.json mcpName %q: the registry needs both io.github.reliant-labs/forge", server.Name, pkg.MCPName)
	}
	if n := len(server.Description); n == 0 || n > 100 {
		t.Errorf("description is %d characters; the registry takes 1-100", n)
	}
	if server.Version != pkg.Version {
		t.Errorf("server.json version %s, npm/package.json %s", server.Version, pkg.Version)
	}
	if len(server.Packages) != 1 {
		t.Fatalf("packages = %d, want the npm shim alone", len(server.Packages))
	}
	p := server.Packages[0]
	if p.RegistryType != "npm" || p.RegistryBaseURL != "https://registry.npmjs.org" || p.Identifier != pkg.Name ||
		p.Version != pkg.Version || p.Transport.Type != "stdio" {
		t.Errorf("package = %+v, want npm %s@%s over stdio", p, pkg.Name, pkg.Version)
	}
	if len(p.PackageArguments) != 1 || p.PackageArguments[0].Type != "positional" || p.PackageArguments[0].Value != "mcp" {
		t.Errorf("packageArguments = %+v, want the one positional `mcp`", p.PackageArguments)
	}

	smithery, err := os.ReadFile(repoFile(t, "npm/smithery.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(smithery), "type: stdio") || !strings.Contains(string(smithery), "args: ['-y', '"+pkg.Name+"', 'mcp']") {
		t.Errorf("npm/smithery.yaml does not start `npx -y %s mcp` over stdio:\n%s", pkg.Name, smithery)
	}
}
