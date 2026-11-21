package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionReceiptChecksActualPinNotCwdHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("temporary git source check; runs in task test")
	}
	root := t.TempDir()
	initGitRepo(t, root, map[string]string{"go.mod": "module example/project\n\ngo 1.23\n\nrequire example/reliant v0.0.0-20260101000000-aaaaaaaaaaaa\n"})
	d := "sha256:" + strings.Repeat("b", 64)
	inputs := release.BuildInputs{Recipe: "image", Sources: []release.Source{{Repo: "example/reliant", Commit: strings.Repeat("a", 40)}}, ContextDigest: d, RecipeDigest: d, ConfigDigest: d, Tools: map[string]string{"go": d}, Platforms: []string{"linux/amd64"}, Outputs: []string{"image"}}
	digest, err := release.BuildInputDigest(inputs)
	if err != nil {
		t.Fatal(err)
	}
	receipt := release.BuildReceipt{Inputs: inputs, InputDigest: digest, Outputs: map[string]string{"image": d}}
	w := WorkloadEntity{Name: "worker", Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "export-pin"}}}
	if err := validateCompositionReceiptSource(context.Background(), root, w, receipt, &KCLEntities{}); err != nil {
		t.Fatal(err)
	}
	receipt.Inputs.Sources[0].Commit = strings.Repeat("c", 40)
	receipt.InputDigest, _ = release.BuildInputDigest(receipt.Inputs)
	if err := validateCompositionReceiptSource(context.Background(), root, w, receipt, &KCLEntities{}); err == nil {
		t.Fatal("wrong exported pin accepted")
	}
	// Input file reading itself is strict and side-effect free.
	data, _ := json.Marshal(inputs)
	if err := os.WriteFile(filepath.Join(root, "inputs.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
