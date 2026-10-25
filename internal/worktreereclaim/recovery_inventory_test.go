// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
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

func TestRecoveryRebuildableOwnership(t *testing.T) {
	root := t.TempDir()
	if recoveryRebuildableDirectory(root, "node_modules/package/index.js") {
		t.Fatal("unowned node_modules allowed")
	}
	recoveryTestWrite(t, root, "package.json", []byte("{}"), 0644)
	if !recoveryRebuildableDirectory(root, "node_modules/package/index.js") || !recoveryRebuildableDirectory(root, "dist/app.js") {
		t.Fatal("owned build conventions rejected")
	}
	for _, p := range []string{"data/cache.json", "tmp/file.txt", "logs/run.log", ".forge/state/file.json", "unknown/index.js"} {
		if recoveryRebuildableDirectory(root, p) {
			t.Errorf("unsupported ignored path allowed: %s", p)
		}
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
	t.Run("owned-generated-text", func(t *testing.T) {
		root := recoveryTestRepo(t)
		recoveryTestWrite(t, root, "package.json", []byte("{}\n"), 0644)
		recoveryTestWrite(t, root, ".gitignore", []byte("dist/\n"), 0644)
		recoveryTestWrite(t, root, "dist/app.js", []byte("console.log('compiled');\n"), 0644)
		c, e := CaptureRecovery(context.Background(), root, recoveryTestDescriptor())
		if e != nil {
			t.Fatal(e)
		}
		m, e := ReadRecoveryManifest(context.Background(), root, c)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range m.DiskEntries {
			if strings.Contains(entry.Path, "dist/") {
				t.Fatal("omitted build leaf uploaded")
			}
		}
		before := c.InventoryDigest
		recoveryTestWrite(t, root, "dist/app.js", []byte("console.log('changed');\n"), 0644)
		after, e := RecoveryInventoryDigest(context.Background(), root)
		if e != nil || after == before {
			t.Fatal("ignored mutation missed by full inventory")
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
