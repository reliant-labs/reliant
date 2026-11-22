package cli

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/toolpins"
)

// TestToolPinsNameForgesGolangciLint: `forge tools pins golangci-lint` is
// how a CI workflow reads the golangci-lint `forge lint` gates with, so it
// must be the very constant forge lint builds and installs.
func TestToolPinsNameForgesGolangciLint(t *testing.T) {
	pins := toolPins()
	if got := pins["golangci-lint"]; got != templates.GolangciLintVersion {
		t.Errorf("pins[golangci-lint] = %q, want %q", got, templates.GolangciLintVersion)
	}
	if got := pins["buf"]; got != templates.BufVersion {
		t.Errorf("pins[buf] = %q, want %q", got, templates.BufVersion)
	}
	if got := pins["syft"]; got != templates.SyftVersion {
		t.Errorf("pins[syft] = %q, want %q", got, templates.SyftVersion)
	}
	if got := pins["grype"]; got != templates.GrypeVersion {
		t.Errorf("pins[grype] = %q, want %q", got, templates.GrypeVersion)
	}

	cmd := newToolsPinsCmd()
	cmd.SetArgs([]string{"no-such-tool"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "golangci-lint") {
		t.Errorf("an unknown tool must fail naming the known ones, got %v", err)
	}
}

// TestToolsInstallKnowsTheLintTools: golangci-lint and buf are installable by
// name, and an unknown name fails before anything is installed.
func TestToolsInstallKnowsTheLintTools(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	err := runToolsInstall(context.Background(), "", false, "no-such-tool")
	if err == nil {
		t.Fatal("an unknown tool name installed nothing and passed")
	}
	for _, want := range []string{"golangci-lint", "buf", "protoc-gen-go", "syft", "grype"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must list %q among the installable tools: %v", want, err)
		}
	}
}

// TestToolPinsArchives: `forge tools pins grype --archives` is pkg/toolpins'
// grype, with each archive's URL — what a Dockerfile installs from.
func TestToolPinsArchives(t *testing.T) {
	var runErr error
	out := captureStdout(t, func() {
		cmd := newToolsPinsCmd()
		cmd.SetArgs([]string{"grype", "--archives"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var got struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Archives []struct {
			GOOS, GOARCH, Name, SHA256, URL string
		} `json:"archives"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	want := toolpins.Grype()
	if got.Name != "grype" || got.Version != want.Version || len(got.Archives) != len(want.Archives) {
		t.Fatalf("got %+v, want toolpins.Grype()", got)
	}
	for _, a := range got.Archives {
		sum, _ := want.SHA256(a.GOOS, a.GOARCH)
		url, _ := want.ReleaseURL(a.GOOS, a.GOARCH)
		if a.SHA256 != sum || a.URL != url || a.Name == "" {
			t.Errorf("%s/%s: %+v, want sha256 %s url %s", a.GOOS, a.GOARCH, a, sum, url)
		}
	}

	cmd := newToolsPinsCmd()
	cmd.SetArgs([]string{"buf", "--archives"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "grype, syft") {
		t.Errorf("a tool with no archives must fail naming the ones with archives, got %v", err)
	}
}
