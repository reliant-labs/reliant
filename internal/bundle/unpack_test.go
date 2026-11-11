package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// hostileArchive builds a tar.gz from raw headers, so a test can write the
// entry a well-behaved writer would refuse to produce.
func hostileArchive(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, hdr := range entries {
		body := strings.Repeat("x", int(hdr.Size))
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func reg(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size, Format: tar.FormatPAX}
}

// PATH TRAVERSAL, and the two shapes of it. Closed by asking "is the result
// inside the root", which is decidable — not by scrubbing ".." out of a name,
// which is a blocklist and loses to an encoding nobody enumerated.
func TestUnpackRefusesPathTraversal(t *testing.T) {
	for name, entry := range map[string]string{
		"parent escape":        "../escaped.yaml",
		"deep parent escape":   "manifests/../../escaped.yaml",
		"absolute path":        "/etc/cron.d/forge",
		"absolute with dots":   "/../../etc/passwd",
		"sneaky middle escape": "manifests/a/../../../escaped.yaml",
	} {
		dest := t.TempDir()
		_, err := Unpack(bytes.NewReader(hostileArchive(t, reg(entry, 4))), dest)
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("%s (%q): want ErrUnsafeArchive, got %v", name, entry, err)
			continue
		}
		// And nothing landed. A refusal that wrote the file first would
		// be a log line, not a boundary.
		escaped := filepath.Join(filepath.Dir(dest), "escaped.yaml")
		if _, serr := os.Stat(escaped); serr == nil {
			t.Errorf("%s: the entry was written outside the destination anyway", name)
		}
	}
}

// SYMLINK ESCAPE — the subtle one, and the reason a path check alone is not
// enough. `link -> /etc` then `link/passwd`: each entry passes a naive
// containment check because neither NAME escapes, but the second write lands
// outside. Closed by refusing link types entirely.
func TestUnpackRefusesLinks(t *testing.T) {
	for name, hdr := range map[string]*tar.Header{
		"symlink": {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc", Format: tar.FormatPAX},
		"hardlink": {Name: "link", Typeflag: tar.TypeLink, Linkname: "manifests/a.yaml",
			Format: tar.FormatPAX},
		"fifo":   {Name: "pipe", Typeflag: tar.TypeFifo, Format: tar.FormatPAX},
		"device": {Name: "dev", Typeflag: tar.TypeBlock, Format: tar.FormatPAX},
	} {
		_, err := Unpack(bytes.NewReader(hostileArchive(t, hdr)), t.TempDir())
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("%s: want ErrUnsafeArchive, got %v", name, err)
		}
	}
}

// ENTRY-COUNT EXHAUSTION, which the byte ceiling does not bound: a million
// zero-length files cost no bytes.
func TestUnpackRefusesTooManyEntries(t *testing.T) {
	headers := make([]*tar.Header, 0, MaxEntries+1)
	for i := 0; i <= MaxEntries; i++ {
		headers = append(headers, reg("manifests/f"+strings.Repeat("0", 4)+strconv.Itoa(i)+".yaml", 0))
	}
	_, err := Unpack(bytes.NewReader(hostileArchive(t, headers...)), t.TempDir())
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive past %d entries, got %v", MaxEntries, err)
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("the refusal should name the entry cap: %v", err)
	}
}

// A DECOMPRESSION BOMB, counted on the READ side. The header's Size is
// attacker-supplied, so a bomb declares a small one — which is exactly what
// this archive does: it claims one byte and streams far more.
func TestUnpackRefusesADecompressionBomb(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// A header that LIES: Size 1, body far larger. tar.Writer enforces the
	// declared size, so the bomb is built as a raw gzip stream of a tar
	// whose single entry declares more than the ceiling.
	if err := tw.WriteHeader(reg("manifests/bomb.yaml", MaxLayerBytes+1)); err != nil {
		t.Fatal(err)
	}
	// Written in chunks so the test does not hold 64 MiB as one string.
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	for written := int64(0); written <= MaxLayerBytes; written += int64(len(chunk)) {
		if _, err := tw.Write(chunk); err != nil {
			break
		}
	}
	_ = tw.Close()
	_ = gz.Close()

	_, err := Unpack(bytes.NewReader(buf.Bytes()), t.TempDir())
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive for a bomb, got %v", err)
	}
}

// A SINGLE oversized file. Redundant against the layer ceiling for a one-file
// bomb, and not redundant for the shape that matters: it localises the error
// to the offending entry instead of reporting "the archive is too big" after
// a well-formed archive was half written.
func TestUnpackRefusesAnOversizedFile(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(reg("manifests/big.yaml", MaxFileBytes+16)); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	for written := int64(0); written < MaxFileBytes+16; written += int64(len(chunk)) {
		if _, err := tw.Write(chunk); err != nil {
			break
		}
	}
	_ = tw.Close()
	_ = gz.Close()

	_, err := Unpack(bytes.NewReader(buf.Bytes()), t.TempDir())
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive for an oversized file, got %v", err)
	}
}

// A DUPLICATE ENTRY. O_EXCL makes the second copy a hard error rather than a
// silent overwrite: otherwise a reviewer auditing the first occurrence would
// be auditing bytes that never landed.
func TestUnpackRefusesADuplicateEntry(t *testing.T) {
	archive := hostileArchive(t, reg("manifests/a.yaml", 4), reg("manifests/a.yaml", 4))
	_, err := Unpack(bytes.NewReader(archive), t.TempDir())
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive for a duplicate entry, got %v", err)
	}
}

func TestUnpackRefusesNonGzip(t *testing.T) {
	_, err := Unpack(strings.NewReader("this is not a gzip stream"), t.TempDir())
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive, got %v", err)
	}
}

// The happy path, and the two properties it has to carry: the extracted paths
// come back in apply order, and the archive's mode bits are DISCARDED rather
// than honoured (honouring them is how a setuid or world-writable file
// arrives, and nothing in a bundle needs to be executable).
func TestUnpackExtractsAndFixesModes(t *testing.T) {
	// What the fixed mode READS BACK as. Windows keeps no POSIX mode bits —
	// only a read-only attribute — and os.Stat reports any writable file as
	// 0666, so the property that survives there is "writable, nothing more";
	// an archive's 0777 would read back the same, and the executable bit
	// the check guards against does not exist on that OS.
	wantMode := unpackFileMode
	if runtime.GOOS == "windows" {
		wantMode = 0o666
	}
	built := mustBuild(t, buildFixture())
	layer, _ := built.Layer(release.BundleManifestsLayer)
	dest := t.TempDir()
	files, err := Unpack(bytes.NewReader(layer), dest)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("Unpack returned no files")
	}
	for i := 1; i < len(files); i++ {
		if files[i] <= files[i-1] {
			t.Fatalf("Unpack did not return paths in order: %v", files)
		}
	}
	for _, rel := range files {
		info, serr := os.Stat(filepath.Join(dest, rel))
		if serr != nil {
			t.Fatalf("%s: %v", rel, serr)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Errorf("%s has mode %v, want the fixed %v", rel, got, wantMode)
		}
	}

	// An executable bit in the ARCHIVE must not survive.
	hdr := reg("manifests/x/000-configmap-a.yaml", 4)
	hdr.Mode = 0o777
	execDest := t.TempDir()
	if _, err := Unpack(bytes.NewReader(hostileArchive(t, hdr)), execDest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(execDest, hdr.Name))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Errorf("an archive's 0777 survived as %v", got)
	}
}
