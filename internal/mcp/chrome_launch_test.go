// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeGlobalInstall lays out what `npm install -g chrome-devtools-mcp@<v>`
// leaves under a prefix — the bin symlink into the package, the package's own
// package.json, and a nested type-only package.json on the way up — and
// returns the bin path.
func fakeGlobalInstall(t *testing.T, version string) string {
	t.Helper()
	prefix := t.TempDir()
	pkg := filepath.Join(prefix, "lib", "node_modules", "chrome-devtools-mcp")
	script := filepath.Join(pkg, "build", "src", "bin", "chrome-devtools-mcp.js")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		script: "#!/usr/bin/env node\n",
		filepath.Join(pkg, "build", "package.json"): `{"type":"module"}`,
		filepath.Join(pkg, "package.json"):          `{"name":"chrome-devtools-mcp","version":"` + version + `"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(prefix, "bin", "chrome-devtools-mcp")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(script, bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

func withLookPath(t *testing.T, f func(string) (string, error)) {
	t.Helper()
	prev := lookPath
	lookPath = f
	t.Cleanup(func() { lookPath = prev })
}

// The browser MCP starts from the installed binary when it is the pinned
// version: npx's per-spawn resolution was what pushed its handshake past the
// start budget on a loaded machine. Anything else falls back to npx.
func TestChromeDevtoolsLaunch(t *testing.T) {
	npx := []string{"-y", "chrome-devtools-mcp@" + chromeDevtoolsMCPVersion}

	t.Run("pinned version installed: run it directly", func(t *testing.T) {
		bin := fakeGlobalInstall(t, chromeDevtoolsMCPVersion)
		withLookPath(t, func(string) (string, error) { return bin, nil })
		command, args := chromeDevtoolsLaunch()
		if command != bin || len(args) != 0 {
			t.Fatalf("launch = %q %v, want %q with no npx arguments", command, args, bin)
		}
	})

	t.Run("another version installed: npx the pinned one", func(t *testing.T) {
		bin := fakeGlobalInstall(t, "0.0.1")
		withLookPath(t, func(string) (string, error) { return bin, nil })
		command, args := chromeDevtoolsLaunch()
		if command != "npx" || !reflect.DeepEqual(args, npx) {
			t.Fatalf("launch = %q %v, want npx %v", command, args, npx)
		}
	})

	t.Run("not installed: npx", func(t *testing.T) {
		withLookPath(t, func(string) (string, error) { return "", exec.ErrNotFound })
		command, args := chromeDevtoolsLaunch()
		if command != "npx" || !reflect.DeepEqual(args, npx) {
			t.Fatalf("launch = %q %v, want npx %v", command, args, npx)
		}
	})
}

// The server flags follow whichever launch was chosen.
func TestBuiltinChromeDevtoolsUsesTheInstalledBinary(t *testing.T) {
	bin := fakeGlobalInstall(t, chromeDevtoolsMCPVersion)
	withLookPath(t, func(string) (string, error) { return bin, nil })

	server, ok := builtinMCPServers("http://host.k3d.internal:9222")[chromeDevtoolsServerName]
	if !ok {
		t.Fatal("chrome-devtools is not configured when a browser URL is set")
	}
	want := []string{"--isolated", "--browserUrl=http://host.k3d.internal:9222"}
	if server.Command != bin || !reflect.DeepEqual(server.Args, want) {
		t.Fatalf("server = %q %v, want %q %v", server.Command, server.Args, bin, want)
	}
}
