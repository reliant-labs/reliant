package sbom

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// ErrNotArchive is a file that is not an image archive syft can read.
var ErrNotArchive = errors.New("not an image archive")

// IsTar reports whether head, the first bytes of a file, is a POSIX tar
// header (the "ustar" magic at offset 257). It says nothing about whether
// the tar holds an image: ArchiveScheme does.
func IsTar(head []byte) bool {
	return len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar"))
}

// IsGzip reports whether head starts with gzip's magic: a compressed archive,
// which syft is not given.
func IsGzip(head []byte) bool { return len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b }

// ArchiveScheme is the syft source scheme for an image archive at p:
// "docker-archive" for `docker save` output (it has a top-level
// manifest.json; docker 25+ also writes an OCI layout beside it, and either
// reading names the same image), "oci-archive" for an OCI image layout with
// no docker manifest. Anything else is ErrNotArchive, saying why.
//
// Only headers are read; the reader seeks over each entry's data, so an
// archive of gigabytes costs its entry count, not its size.
func ArchiveScheme(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // the caller names the build output it scans
	if err != nil {
		return "", fmt.Errorf("read %s: %v", p, err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if IsGzip(head) {
		return "", fmt.Errorf("%w: %s is gzip-compressed; decompress it to the tar `docker save` writes", ErrNotArchive, p)
	}
	if !IsTar(head) {
		return "", fmt.Errorf("%w: %s is not a tar file", ErrNotArchive, p)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read %s: %v", p, err)
	}
	var dockerManifest, ociLayout bool
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrNotArchive, p, err)
		}
		switch strings.TrimPrefix(path.Clean(h.Name), "./") {
		case "manifest.json":
			dockerManifest = true
		case "oci-layout":
			ociLayout = true
		}
	}
	switch {
	case dockerManifest:
		return "docker-archive", nil
	case ociLayout:
		return "oci-archive", nil
	}
	return "", fmt.Errorf("%w: %s is a tar with neither a docker manifest.json nor an oci-layout file (`docker save` writes one)", ErrNotArchive, p)
}
