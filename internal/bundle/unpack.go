package bundle

// Defensive unpack — every measure, and the attack it closes.
//
// A tar archive is a sequence of ATTACKER-CHOSEN PATHS carrying
// ATTACKER-CHOSEN LENGTHS. Both must be bounded before anything touches the
// filesystem, and the bounds must hold even when the archive lies about
// itself (a header's Size field is a claim, not a fact). control-plane's
// buildservice reaches the same conclusion from the other direction: its
// pusher parses an OCI layout as a non-root, no-capabilities, read-only-
// rootfs container specifically because it "parses untrusted bytes, so it is
// treated as a parser of untrusted bytes". forge has no container to hide
// behind here, so the checks in this file are the whole boundary.
//
// A BUNDLE IS NOT LESS HOSTILE FOR BEING FORGE'S OWN. It is fetched from a
// registry, and a registry is reachable by anything that holds a push
// credential — which, for a BYO registry declared in KCL, is not necessarily
// the same set of people who can deploy. The digest proves the bytes are the
// ones recorded; it proves nothing about whether the recorder was honest.
//
//  1. PATH TRAVERSAL — "../../.ssh/authorized_keys". The classic. Closed by
//     resolving every entry against the destination root and REFUSING
//     anything that escapes it, rather than by scrubbing ".." out of the name
//     (a scrub is a blocklist, and blocklists lose).
//  2. ABSOLUTE PATHS — "/etc/cron.d/x". Closed by the same check;
//     filepath.Join would otherwise silently re-root them.
//  3. SYMLINK ESCAPE — the subtle one, and the reason a path check alone is
//     not enough. An archive writes `link -> /etc`, then writes
//     `link/passwd`; each entry passes a naive containment check because
//     neither NAME escapes, but the second write lands outside. Closed by
//     refusing symlinks and hardlinks ENTIRELY: a manifest layer has no need
//     for them, so allowing them buys nothing and costs the whole boundary.
//  4. DECOMPRESSION BOMB — a few KB of gzip expanding to terabytes. Closed
//     by a hard ceiling on TOTAL DECOMPRESSED BYTES, enforced by an
//     io.LimitedReader on the read side. The header's Size field is NOT
//     trusted for this: it is attacker-supplied, so a bomb would simply
//     declare a small size. The limit counts what was actually read.
//  5. ENTRY-COUNT EXHAUSTION — millions of empty files, which the byte
//     ceiling alone does not bound. Closed by a separate entry cap.
//  6. DEVICE / SETUID EXOTICA — block devices, fifos, setuid bits. Closed by
//     an ALLOW-LIST of entry types (regular files and directories only) and
//     by masking permissions to a fixed mode rather than honouring the
//     archive's.
//
// The caps below are generous for a bundle and tiny compared to a disk. Doc
// §4.1 measures control-plane prod at 246 KB compressed, 2.34 MB
// uncompressed, so these are three orders of magnitude of headroom. They
// exist to make the failure LOUD AND EARLY rather than to be tuned.

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxLayerBytes caps total decompressed output per layer.
	MaxLayerBytes int64 = 64 << 20
	// MaxEntries caps the entry count, which the byte ceiling does not
	// bound (a million zero-length files cost no bytes).
	MaxEntries = 4096
	// MaxFileBytes caps any SINGLE file. Redundant against MaxLayerBytes
	// for a one-file bomb and not redundant for the shape that matters: it
	// localises the error to the offending entry instead of reporting "the
	// archive is too big" after a well-formed archive was half written.
	MaxFileBytes int64 = 16 << 20
	// MaxManifestBytes caps an OCI manifest and a config blob. The server
	// bounds both at 1 MiB (doc §6.3's RecordBundle), and forge refuses
	// the same size rather than a larger one: a bundle forge would push
	// and the server would refuse is a build that fails at the last step.
	MaxManifestBytes int64 = 1 << 20

	// unpackFileMode / unpackDirMode are the FIXED modes every extracted
	// entry gets. The archive's own mode bits are discarded rather than
	// masked, because honouring them is how a setuid or world-writable
	// file arrives, and nothing in a bundle needs to be executable.
	unpackFileMode os.FileMode = 0o644
	unpackDirMode  os.FileMode = 0o755
)

// ErrUnsafeArchive is the sentinel every defensive refusal wraps, so a caller
// can distinguish "this bundle is hostile or corrupt" from an ordinary IO
// failure.
var ErrUnsafeArchive = errors.New("forge: refusing to unpack bundle")

// Unpack extracts a gzipped tar layer into destDir, enforcing every measure
// documented above. It creates destDir if absent.
//
// Returns the extracted paths, relative to destDir and slash-separated, so a
// caller can see what a bundle actually contained without walking the tree
// again — and, for the manifest layer, apply them in that order.
func Unpack(r io.Reader, destDir string) ([]string, error) {
	root, err := filepath.Abs(destDir)
	if err != nil {
		return nil, fmt.Errorf("resolve destination %q: %w", destDir, err)
	}
	if err := os.MkdirAll(root, unpackDirMode); err != nil {
		return nil, fmt.Errorf("create destination %q: %w", root, err)
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: not a gzip stream: %w", ErrUnsafeArchive, err)
	}
	defer func() { _ = gz.Close() }()

	// THE BOMB CEILING. Counting on the READ side, not from header Size
	// fields, is the entire point: a header's Size is attacker-supplied,
	// so a bomb declares a small one. +1 so hitting the limit exactly is
	// distinguishable from exceeding it.
	limited := &io.LimitedReader{R: gz, N: MaxLayerBytes + 1}
	tr := tar.NewReader(limited)

	// exhausted reports whether the total-bytes ceiling has been hit.
	//
	// This has to be consulted on EVERY error path, not only after the
	// loop. When the ceiling trips mid-entry the underlying reader simply
	// stops producing bytes, so the tar reader reports `unexpected EOF` —
	// a generic IO error that does NOT wrap ErrUnsafeArchive. A caller
	// matching the sentinel would therefore classify a decompression bomb
	// as an ordinary transport failure and, plausibly, retry it forever.
	// Checking the limit first turns that back into the refusal it is.
	exhausted := func() bool { return limited.N <= 0 }
	bombErr := func() error {
		return fmt.Errorf("%w: decompressed output exceeds %d bytes", ErrUnsafeArchive, MaxLayerBytes)
	}

	var written []string
	entries := 0
	for {
		hdr, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			if exhausted() {
				return nil, bombErr()
			}
			return nil, fmt.Errorf("%w: malformed tar: %w", ErrUnsafeArchive, nerr)
		}
		entries++
		if entries > MaxEntries {
			return nil, fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, MaxEntries)
		}

		target, terr := safeJoin(root, hdr.Name)
		if terr != nil {
			return nil, terr
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, unpackDirMode); err != nil {
				return nil, fmt.Errorf("create dir %q: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if err := writeRegular(tr, target); err != nil {
				if exhausted() {
					return nil, bombErr()
				}
				return nil, err
			}
			rel, _ := filepath.Rel(root, target)
			written = append(written, filepath.ToSlash(rel))
		default:
			// ALLOW-LIST, not a blocklist. Symlinks and hardlinks are
			// the escape described in (3) above; devices, fifos and the
			// rest have no business in a bundle. Refusing loudly rather
			// than skipping silently matters: a skipped entry produces a
			// bundle that unpacked "successfully" while missing an
			// object a deploy would then never apply.
			return nil, fmt.Errorf("%w: entry %q has disallowed type %q "+
				"(only regular files and directories are extracted; symlinks in particular can escape "+
				"the destination even when their own name does not)",
				ErrUnsafeArchive, hdr.Name, string(hdr.Typeflag))
		}
	}

	if exhausted() {
		return nil, bombErr()
	}
	return written, nil
}

// safeJoin resolves one archive entry name against the destination root and
// refuses anything that escapes it.
//
// The containment test is on the RESOLVED path against the resolved root,
// with a separator appended to the prefix — not a string search for "..". A
// blocklist of dangerous substrings loses to encodings nobody enumerated;
// asking "is the result inside the root" is the property that actually
// matters, and it is decidable.
func safeJoin(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: entry with empty name", ErrUnsafeArchive)
	}
	// Reject absolute names before joining: filepath.Join would quietly
	// re-root "/etc/passwd" under the destination, which HAPPENS to be
	// safe but hides the fact that the archive asked for something it
	// should never ask for.
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: entry %q is an absolute path", ErrUnsafeArchive, name)
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	cleanRoot := filepath.Clean(root)
	if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: entry %q escapes the destination directory", ErrUnsafeArchive, name)
	}
	return target, nil
}

// writeRegular extracts one regular file under the per-file ceiling.
//
// O_EXCL refuses to overwrite an existing file, which closes the
// duplicate-entry shape: an archive listing the same path twice would
// otherwise have its second copy win, so a reviewer auditing the first
// occurrence would be auditing bytes that never landed.
func writeRegular(tr io.Reader, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), unpackDirMode); err != nil {
		return fmt.Errorf("create parent of %q: %w", target, err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, unpackFileMode)
	if err != nil {
		return fmt.Errorf("%w: create %q: %w", ErrUnsafeArchive, target, err)
	}
	defer func() { _ = f.Close() }()

	// Per-file ceiling, again counted on the read side. +1 to tell
	// "exactly at the limit" from "over it".
	n, cerr := io.Copy(f, io.LimitReader(tr, MaxFileBytes+1))
	if cerr != nil {
		return fmt.Errorf("write %q: %w", target, cerr)
	}
	if n > MaxFileBytes {
		return fmt.Errorf("%w: file %q exceeds %d bytes", ErrUnsafeArchive, target, MaxFileBytes)
	}
	return nil
}
