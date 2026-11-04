package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestBuildReceiptCommandStrictTypedOutput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	d := "sha256:" + strings.Repeat("a", 64)
	input := release.BuildInputs{Recipe: "unit", Sources: []release.Source{{Repo: "example/source", Commit: strings.Repeat("b", 40)}}, ContextDigest: d, RecipeDigest: d, ConfigDigest: d, Tools: map[string]string{"go": d}, Platforms: []string{"linux/amd64"}, Outputs: []string{"image"}}
	raw, _ := json.Marshal(input)
	path := filepath.Join(dir, "inputs.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "receipt.json")
	t.Setenv(buildtarget.ReceiptPathEnv, destination)
	cmd := newBuildReceiptCmd()
	cmd.SetArgs([]string{"--inputs", path, "--output", "image=" + d})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := buildtarget.ReadReceipt(destination, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Inputs.Sources[0].Commit != input.Sources[0].Commit {
		t.Fatal("replaced actual exported source with cwd")
	}
}
