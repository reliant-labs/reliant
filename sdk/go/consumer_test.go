// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package gosdk_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The SDK as a consumer sees it: a scratch module outside this repository that
// requires github.com/reliant-labs/reliant/sdk/go and builds against every
// public package. The consumer reaches the SDK by path here (a replace in the
// CONSUMER's go.mod, which is how `go get <module>@<version>` resolves it
// too: from the module's own go.mod and nothing else). So the build fails if
// the SDK needs a replace of its own (a consumer ignores a dependency's), needs
// the app module, or imports anything outside itself.
//
// The child `go build` reads the SDK's files; this test reads them too, so the
// test cache is keyed on them and a changed SDK runs it again. It compiles the
// SDK's whole dependency graph, so it is a full-tier test (`task test`, `task
// test:sdk:full`); TestSDKGoModStandsAlone is its fast half, in every tier.

const sdkModule = "github.com/reliant-labs/reliant/sdk/go"

const consumerMain = `package main

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/sdk/go/reliant"
	"github.com/reliant-labs/reliant/sdk/go/reliant/askuser"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

func main() {
	fmt.Println(reliant.New, reliant.NewWebhook, follow.Succeeded, follow.RenderText, askuser.IsAskUser,
		reliantv1connect.NewRunServiceClient, (*reliantv1.Run)(nil), (*follow.Engine)(nil))
}
`

// TestSDKGoModStandsAlone: what a consumer reads of this module is its go.mod,
// so it must be complete. A consumer ignores a dependency's replace
// directives, and `go install <module>@<version>` refuses a module that has
// one; and the SDK must not require the app module, which depends on it.
func TestSDKGoModStandsAlone(t *testing.T) {
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module "+sdkModule+"\n") {
		t.Errorf("go.mod declares the wrong module: %q", strings.SplitN(string(mod), "\n", 2)[0])
	}
	if regexp.MustCompile(`(?m)^\s*(replace\s+)?\S+(\s+v\S+)?\s+=>`).Match(mod) {
		t.Error("go.mod has a replace: consumers ignore it, so the module would not build for them")
	}
	if regexp.MustCompile(`(?m)^\s*(require\s+)?github\.com/reliant-labs/reliant\s+v`).Match(mod) {
		t.Error("go.mod requires the app module: the SDK must not depend on what depends on it")
	}
}

func TestSDKBuildsAsAnExternalDependency(t *testing.T) {
	if testing.Short() {
		t.Skip("slow test, skipped under -short: builds a scratch consumer module and the SDK's dependency graph with the go command; runs in the full tier (task test:sdk:full, task test)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH: the consumer build needs the go command")
	}
	sdkDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	readModuleFiles(t, sdkDir)

	sum, err := os.ReadFile(filepath.Join(sdkDir, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	consumer := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/sdkconsumer\n\ngo 1.27.0\n\n"+
		"require "+sdkModule+" v0.0.0\n\n"+
		"replace "+sdkModule+" => "+filepath.ToSlash(sdkDir)+"\n")
	// The SDK's own sums cover its whole graph: the consumer adds nothing.
	write("go.sum", string(sum))
	write("main.go", consumerMain)

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = consumer
		// Judged as a stranger's module: no workspace, and no -mod flags of
		// whoever runs the test. -mod=mod lets go fill in requirements the
		// SDK's graph implies, as `go get` would.
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -buildvcs=false")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("go %s in a scratch consumer module: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}
	run("build", "-o", os.DevNull, ".")

	// The app module must not be in a consumer's graph at all: the SDK is the
	// Apache-2.0 half, and the app depends on it, not the reverse.
	for _, line := range strings.Split(run("list", "-m", "all"), "\n") {
		if mod, _, _ := strings.Cut(line, " "); mod == strings.TrimSuffix(sdkModule, "/sdk/go") {
			t.Errorf("a consumer of the SDK pulls in the app module: %q", line)
		}
	}
}

// readModuleFiles reads every file the consumer build compiles, so `go test`'s
// cache sees them (it tracks only the files the test process opens).
func readModuleFiles(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") || name == "go.mod" || name == "go.sum" {
			_, err = os.ReadFile(path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
