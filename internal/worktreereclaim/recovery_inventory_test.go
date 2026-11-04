// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryIndexEnvelope(t *testing.T) {
	empty := make([]byte, 32)
	copy(empty, "DIRC")
	binary.BigEndian.PutUint32(empty[4:8], 2)
	if e := recoveryCheckIndex(empty, 20); e != nil {
		t.Fatal(e)
	}
	version4 := append([]byte(nil), empty...)
	binary.BigEndian.PutUint32(version4[4:8], 4)
	if recoveryCheckIndex(version4, 20) == nil {
		t.Fatal("v4 accepted without serializer proof")
	}
	unknown := append([]byte(nil), empty[:12]...)
	unknown = append(unknown, []byte("UNKN\x00\x00\x00\x00")...)
	unknown = append(unknown, empty[12:]...)
	if recoveryCheckIndex(unknown, 20) == nil {
		t.Fatal("unknown extension accepted")
	}
	for n := 0; n < len(empty); n++ {
		if recoveryCheckIndex(empty[:n], 20) == nil {
			t.Fatalf("truncated index accepted at %d", n)
		}
	}
}

// Regression: a package/build declaration cannot establish immutable
// provenance for arbitrary manually authored leaves in an ignored directory.
// Before the fix, dist/notes.txt passed inventory and its bytes were omitted.
func TestRecoveryIgnoredAuthoredTextHeld(t *testing.T) {
	heavyUnderShort(t, "ignored authored-data regression uses local Git subprocesses")
	for _, dir := range []string{"dist", "node_modules", ".tools", ".next", ".turbo"} {
		t.Run(dir, func(t *testing.T) {
			root := recoveryTestRepo(t)
			for _, name := range []string{"package.json", "tsconfig.json"} {
				recoveryTestWrite(t, root, name, []byte("{}\n"), 0644)
			}
			recoveryTestWrite(t, root, "Taskfile.yml", []byte("version: '3'\n"), 0644)
			recoveryTestWrite(t, root, ".gitignore", []byte(dir+"/\n"), 0644)
			recoveryTestWrite(t, root, dir+"/notes.txt", []byte("my only copy of these research notes\n"), 0644)
			ctx := withGitObserver(context.Background(), func(args []string) {
				for _, arg := range args {
					if arg == "push" || arg == "fetch" || arg == "ls-remote" {
						t.Fatal("egress during ignored-content hold")
					}
				}
			})
			if _, e := RecoveryInventoryDigest(ctx, root); e == nil {
				t.Fatal("ignored manually authored bytes omitted from inventory")
			}
			if _, e := CaptureRecovery(ctx, root, recoveryTestDescriptor()); e == nil {
				t.Fatal("ignored manually authored bytes not preserved or held")
			}
			got, e := os.ReadFile(filepath.Join(root, dir, "notes.txt"))
			if e != nil || string(got) != "my only copy of these research notes\n" {
				t.Fatal("original ignored file changed")
			}
		})
	}
}

func TestRecoveryManifestRejectsOmittedBytes(t *testing.T) {
	oid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := RecoveryManifest{Version: RecoveryFormatVersion, SecretPolicy: RecoverySecretPolicy, CaptureID: "capture", WorktreeID: "worktree", RepoID: "repo", Rel: ".", HeadOID: oid, IndexTreeOID: oid, DiskTreeOID: oid, ObjectFormat: "sha1", HistoryTips: []string{oid}, OmittedDigest: recoveryDigest([]RecoveryEntry{{Path: "ZGlzdC9ub3Rlcy50eHQ=", Mode: "420", OID: oid}})}
	m.InventoryDigest = recoveryDigest(recoveryManifestInventory(m))
	if e := recoveryManifestCheck(m); e == nil {
		t.Fatal("manifest digest substituted for omitted original bytes")
	}
}

func TestRecoveryInventoryStableAfterMoveAndRecoveryFetch(t *testing.T) {
	heavyUnderShort(t, "inventory move proof uses local Git subprocesses")
	root := recoveryTestRepo(t)
	base := t.TempDir()
	checkout := filepath.Join(base, "linked")
	recoveryTestGit(t, root, "worktree", "add", "-q", "-b", "linked-test", checkout)
	c, e := CaptureRecovery(context.Background(), checkout, recoveryTestDescriptor())
	if e != nil {
		t.Fatal(e)
	}
	moved := filepath.Join(base, "quarantined")
	recoveryTestGit(t, root, "worktree", "move", checkout, moved)
	after, e := RecoveryInventoryDigest(context.Background(), moved)
	if e != nil || after != c.InventoryDigest {
		t.Fatalf("move changed inventory: %v", e)
	}
	recoveryTestGit(t, root, "update-ref", "refs/remotes/origin/reliant-recovery/worktree-test/capture-test/repo-test", c.CommitOID)
	after, e = RecoveryInventoryDigest(context.Background(), moved)
	if e != nil || after != c.InventoryDigest {
		t.Fatalf("recovery fetch changed inventory: %v", e)
	}
}

func TestRecoveryIgnoredInventory(t *testing.T) {
	heavyUnderShort(t, "ignored inventory uses local Git subprocesses")
	t.Run("generated-marker-is-not-provenance", func(t *testing.T) {
		root := recoveryTestRepo(t)
		recoveryTestWrite(t, root, "package.json", []byte("{}\n"), 0644)
		recoveryTestWrite(t, root, ".gitignore", []byte("dist/\n"), 0644)
		recoveryTestWrite(t, root, "dist/app.js", []byte("// Code generated DO NOT EDIT\nconsole.log('compiled');\n"), 0644)
		if _, e := CaptureRecovery(context.Background(), root, recoveryTestDescriptor()); e == nil {
			t.Fatal("generated marker incorrectly authorized omission")
		}
	})
	for _, tt := range []struct {
		name, file string
		data       []byte
		link       bool
	}{
		{"sensitive-path", "dist/.env", []byte("// Code generated DO NOT EDIT\n"), false},
		{"data-path", "dist/state.sqlite", []byte("plain data"), false},
		{"content-secret", "dist/app.js", []byte("password=compiled-credential"), false},
		{"symlink", "dist/link", nil, true},
		{"binary-holds-conservatively", "dist/app.bin", []byte{0, 1, 2}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := recoveryTestRepo(t)
			recoveryTestWrite(t, root, "package.json", []byte("{}\n"), 0644)
			recoveryTestWrite(t, root, ".gitignore", []byte("dist/\n"), 0644)
			if tt.link {
				if e := os.MkdirAll(filepath.Join(root, "dist"), 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink("../../outside", filepath.Join(root, tt.file)); e != nil {
					t.Fatal(e)
				}
			} else {
				recoveryTestWrite(t, root, tt.file, tt.data, 0644)
			}
			if _, e := CaptureRecovery(context.Background(), root, recoveryTestDescriptor()); e == nil {
				t.Fatal("unsafe omitted content accepted")
			}
		})
	}
}
