//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ESharedProtoPackageScaffoldBufLint(t *testing.T) {
	t.Parallel()
	requireTool(t, "buf")
	bin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, bin, "project", "new", "sharedapp", "--mod", "example.com/sharedapp", "--service", "trading")
	root := filepath.Join(dir, "sharedapp")
	addCorpusForgePkgReplace(t, root)
	proto := filepath.Join(root, "proto/services/trading/v1/trading.proto")
	writeFileE2E(t, proto, strings.Replace(readFileE2E(t, proto), "package services.trading.v1;", "package trader.v1;", 1))
	// The author's existing non-directory package convention already needs
	// this rule disabled. Adding another service must preserve that choice,
	// and add only the two rules specific to sharing across directories.
	bufPath := filepath.Join(root, "buf.yaml")
	writeFileE2E(t, bufPath, strings.Replace(readFileE2E(t, bufPath), "lint:\n", "lint:\n  except: [PACKAGE_DIRECTORY_MATCH] # existing project policy\n", 1))
	runCmd(t, root, "buf", "lint")
	out := runCmdOutput(t, root, bin, "scaffold", "service", "access")
	if !strings.Contains(out, "buf.yaml lint.except updated") {
		t.Fatalf("missing shared package notice:\n%s", out)
	}
	runCmd(t, root, "buf", "lint")
	runCmd(t, root, "go", "build", "./...")
	before := readFileE2E(t, filepath.Join(root, "buf.yaml"))
	runCmd(t, root, bin, "scaffold", "service", "access", "--resume")
	if got := readFileE2E(t, filepath.Join(root, "buf.yaml")); got != before {
		t.Fatal("resume rewrote buf.yaml")
	}
	runCmd(t, root, "buf", "lint")
}
