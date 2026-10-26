//go:build linux

package worktreereclaim

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Deterministically substitute after enumeration (the exact former Lstat/read
// race), not a probabilistic writer loop. No returned bytes may enter hash-object
// and therefore the upload closure, even for plaintext outside secret patterns.
func TestRecoveryReadRegularSubstitution(t *testing.T) {
	for _, kind := range []string{"file-symlink", "ancestor-symlink", "fifo", "regular-inode"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "dir")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "notes")
			original := []byte("ordinary captured notes")
			secret := bytes.Repeat([]byte("private outside plaintext"), 2)
			outside := t.TempDir()
			outsideFile := filepath.Join(outside, "notes")
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outsideFile, secret, 0600); err != nil {
				t.Fatal(err)
			}
			st, err := os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "ancestor-symlink":
				if err := os.Rename(dir, dir+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Rename(file, file+"-original"); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "file-symlink":
					err = os.Symlink(outsideFile, file)
				case "fifo":
					err = unix.Mkfifo(file, 0600)
				case "regular-inode":
					err = os.WriteFile(file, secret, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			type result struct {
				data []byte
				err  error
			}
			done := make(chan result, 1)
			go func() { data, err := recoveryReadRegular(file, st); done <- result{data, err} }()
			select {
			case got := <-done:
				if got.err == nil || len(got.data) != 0 {
					t.Fatalf("substituted content eligible for upload: %q, %v", got.data, got.err)
				}
			case <-time.After(time.Second):
				// Unblock a regressed ordinary Open so the test leaves no stuck goroutine.
				if kind == "fifo" {
					fd, e := unix.Open(file, unix.O_WRONLY|unix.O_NONBLOCK, 0)
					if e == nil {
						unix.Close(fd)
					}
				}
				t.Fatal("capture blocked on substituted nonregular file")
			}
		})
	}
}

func TestRecoveryReadRegularExactAndBounded(t *testing.T) {
	file := filepath.Join(t.TempDir(), "raw")
	data := []byte("raw\r\nbytes\x00\xff")
	if err := os.WriteFile(file, data, 0755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := recoveryReadRegular(file, st)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("raw bytes changed: %q %v", got, err)
	}
	if err := os.Truncate(file, recoveryMaxObject+1); err != nil {
		t.Fatal(err)
	}
	st, err = os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := recoveryReadRegular(file, st); err == nil || len(got) != 0 {
		t.Fatal("oversize file accepted")
	}
}
