package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// expressApp lays down an existing Express app with its own Dockerfile and
// no Go — the shape the `adopt` skill takes onto forge — and returns its root.
func expressApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"package.json": `{"name":"realworld","scripts":{"start":"node dist/main.js"},"dependencies":{"express":"^4"}}`,
		"Dockerfile":   "FROM node:20-slim\nWORKDIR /app\nCOPY . .\nEXPOSE 3000\nCMD [\"node\", \"dist/main.js\"]\n",
		"src/main.ts":  "import express from 'express';\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The service scaffold over an app with a Dockerfile and no Go module used
// to write ~40 Go files into it and break its image build. With no --kind it
// now stops before writing anything, and names the `adopt` skill (and
// --kind service for a Go scaffold).
func TestProjectNewInPlaceOverADockerfileAppPointsAtTheAdoptSkill(t *testing.T) {
	root := expressApp(t)
	err := runNew(t.Context(), "realworld", root, "", "", nil, nil, "", true, false, nil, "reliant", true, "local", "per-service", false, false, false, false, newFrontendOptions{})
	if err == nil || !strings.Contains(err.Error(), "skill load adopt") || !strings.Contains(err.Error(), "--kind service") {
		t.Fatalf("want a refusal naming the adopt skill (and --kind service for a Go server), got %v", err)
	}
	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 3 {
		t.Errorf("the refusal must come before anything is written; the app now holds %d entries", len(entries))
	}
}

// The memory of an adopted app (workloads.k, no go.mod, no module_path, no
// root frontend) is the any-language one, read off the tree, and it points
// at the skill the project was adopted by.
func TestProjectMemoryForAnAdoptedApp(t *testing.T) {
	root := expressApp(t)
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: realworld\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without workloads it is an adopted project that uses nothing of forge
	// yet: never the Go guide, never an app forge deploys.
	bare, err := RenderProjectMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if b := string(bare); strings.Contains(b, "adopted app") || strings.Contains(b, "This is a Go project") || !strings.Contains(b, "adopted project") {
		t.Fatalf("a bare forge.yaml in a Node app must read as an adopted project, not the Go guide or a deployed app:\n%s", b)
	}
	if err := os.MkdirAll(filepath.Join(root, "deploy", "kcl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "kcl", "workloads.k"), []byte("import forge\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := RenderProjectMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	memory := string(out)
	for _, want := range []string{"adopted app", "deploy/kcl/workloads.k", "skill load adopt", "env up dev", "obs check"} {
		if !strings.Contains(memory, want) {
			t.Errorf("adopted-app memory lacks %q", want)
		}
	}
	for _, banned := range []string{"This is a Go project", "## Architecture in one screen", "static site", "--kind container"} {
		if strings.Contains(memory, banned) {
			t.Errorf("adopted-app memory contains %q", banned)
		}
	}
}
