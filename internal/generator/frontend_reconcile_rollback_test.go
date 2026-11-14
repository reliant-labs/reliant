package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestFrontendReconcileRollbackRestoresDirtyManifestAndPackageManagerWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell package-manager fixture")
	}
	for _, failTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "later generation failure", true: "package manager failure"}[failTool], func(t *testing.T) {
			root := t.TempDir()
			rel := "frontends/site"
			dir := filepath.Join(root, rel)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			manifest := []byte("{\n  \"name\": \"dirty-user-edit\",\n  \"dependencies\": {\"@reliantlabs/forge-web-runtime\": \"^0.0.1\"}\n}\n")
			lock := []byte("lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      '@reliantlabs/forge-web-runtime':\n        specifier: ^0.0.0\n        version: 0.0.0\n")
			for p, b := range map[string][]byte{"package.json": manifest, "pnpm-lock.yaml": lock} {
				if err := os.WriteFile(filepath.Join(dir, p), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			untouched := filepath.Join(dir, "user-untracked.txt")
			if err := os.WriteFile(untouched, []byte("mine"), 0600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf 'rewritten-lock\\n' > pnpm-lock.yaml\nprintf 'onlyBuiltDependencies: []\\n' > pnpm-workspace.yaml\n"
			if failTool {
				script += "exit 1\n"
			}
			if err := os.WriteFile(filepath.Join(bin, "pnpm"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
			checksums.ResetSkipWrite()
			checksums.BeginRollbackJournal(root)
			t.Cleanup(checksums.CommitRollback)
			EnsureBrowserTelemetryDependencies(root, rel, "site")
			if got, _ := os.ReadFile(filepath.Join(dir, "package.json")); bytes.Equal(got, manifest) {
				t.Fatal("fixture did not exercise manifest mutation")
			}
			err := SyncFrontendLock(root, rel)
			if (err != nil) != failTool {
				t.Fatalf("package manager result=%v want failure=%v", err, failTool)
			}
			if _, err := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); err != nil {
				t.Fatal("package manager did not create workspace sidefile")
			}
			checksums.RestoreRollback(root)
			for p, want := range map[string][]byte{"package.json": manifest, "pnpm-lock.yaml": lock} {
				got, err := os.ReadFile(filepath.Join(dir, p))
				if err != nil || !bytes.Equal(got, want) {
					t.Errorf("preexisting dirty %s not restored: err=%v", p, err)
				}
				info, err := os.Stat(filepath.Join(dir, p))
				if err == nil && info.Mode().Perm() != 0600 {
					t.Errorf("%s mode=%o want600", p, info.Mode().Perm())
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); !os.IsNotExist(err) {
				t.Error("new package manager workspace sidefile survived rollback")
			}
			if got, _ := os.ReadFile(untouched); string(got) != "mine" {
				t.Error("untouched user file changed")
			}
		})
	}
}
