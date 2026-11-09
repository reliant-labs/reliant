package ledgerfile

import (
	"errors"
	"strings"
	"testing"
)

// withFSType swaps the statfs seam for one test. Not parallel-safe (it
// mutates a package var), so these tests do not call t.Parallel.
func withFSType(t *testing.T, name string) {
	t.Helper()
	prev := fsTypeOf
	fsTypeOf = func(string) (string, error) { return name, nil }
	t.Cleanup(func() { fsTypeOf = prev })
}

// TestNetworkFilesystemIsRefusedAtOpen is why the refusal exists: an
// advisory lock on NFS or SMB may be silently local to one client, so two
// machines sharing a $FORGE_LEDGER_HOME would both decide from the same
// history and both append — the exact race the lock is here to close. A
// ledger that LOOKS locked and is not is worse than no ledger, because the
// loss shows up long after the command succeeded.
//
// Driven through the injected fstype seam rather than a real mount: no test
// environment here has one, and the decision being tested is the refusal,
// not the syscall.
func TestNetworkFilesystemIsRefusedAtOpen(t *testing.T) {
	for _, fs := range []string{"nfs", "nfs4", "smbfs", "cifs", "smb2", "afpfs", "webdav", "9p"} {
		t.Run(fs, func(t *testing.T) {
			withFSType(t, fs)
			_, err := Open(t.TempDir(), "proj")
			if !errors.Is(err, ErrNetworkFilesystem) {
				t.Fatalf("a ledger home on %s must be refused at open, got %v", fs, err)
			}
			// The message has to name the remedy, not just the problem:
			// the supported way for a TEAM to share a ledger is a
			// control plane.
			if !strings.Contains(err.Error(), "FORGE_LEDGER_HOME") || !strings.Contains(err.Error(), "control plane") {
				t.Fatalf("the refusal must name both the knob and the supported alternative, got: %v", err)
			}
		})
	}
}

// TestLocalFilesystemIsAdmitted pins that the refusal is narrow: a normal
// local disk must open.
func TestLocalFilesystemIsAdmitted(t *testing.T) {
	for _, fs := range []string{"apfs", "ext4", "xfs", "btrfs", "zfs", "overlay", "tmpfs"} {
		t.Run(fs, func(t *testing.T) {
			withFSType(t, fs)
			if _, err := Open(t.TempDir(), "proj"); err != nil {
				t.Fatalf("a ledger home on %s must be admitted, got %v", fs, err)
			}
		})
	}
}

// TestUnknownFilesystemIsAdmitted pins the "could not tell" rule. A platform
// with no statfs here, or a path whose statfs fails, must not become a
// platform where forge refuses to keep a ledger at all — the refusal is for
// filesystems we can POSITIVELY identify as network ones.
func TestUnknownFilesystemIsAdmitted(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		withFSType(t, "")
		if _, err := Open(t.TempDir(), "proj"); err != nil {
			t.Fatalf("an unidentifiable filesystem must be admitted, got %v", err)
		}
	})

	t.Run("statfs error", func(t *testing.T) {
		prev := fsTypeOf
		fsTypeOf = func(string) (string, error) { return "", errors.New("statfs failed") }
		t.Cleanup(func() { fsTypeOf = prev })
		if _, err := Open(t.TempDir(), "proj"); err != nil {
			t.Fatalf("a failed statfs must be admitted, not fatal, got %v", err)
		}
	})
}

// TestRealFilesystemIsAdmitted exercises the ACTUAL platform statfs, so the
// per-platform code is covered rather than only the seam. A temp dir is a
// local disk (or tmpfs) on every machine this runs on.
func TestRealFilesystemIsAdmitted(t *testing.T) {
	t.Parallel()
	if _, err := Open(t.TempDir(), "proj"); err != nil {
		t.Fatalf("a temp dir must be a lockable filesystem, got %v", err)
	}
}
