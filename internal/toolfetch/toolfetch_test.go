package toolfetch

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/toolpins"
)

const fakeBinary = "#!/bin/sh\necho fake 1.2.3\n"

// tarGz builds a goreleaser-shaped .tar.gz: docs beside the binary.
func tarGz(t *testing.T, member, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct{ name, body string }{{"LICENSE", "license"}, {member, content}, {"README.md", "readme"}} {
		if e.name == "" {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
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

func zipOf(t *testing.T, member, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// release is a fake GitHub release server: it serves assets at
// /<repo>/releases/download/<tag>/<asset> and counts requests.
type release struct {
	srv    *httptest.Server
	hits   atomic.Int64
	mu     sync.Mutex
	assets map[string][]byte
}

func newRelease(t *testing.T) *release {
	t.Helper()
	r := &release{assets: map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		r.mu.Lock()
		body, ok := r.assets[req.URL.Path]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *release) serve(tool Tool, goos, goarch string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.assets["/"+tool.Repo+"/releases/download/"+tool.Version+"/"+tool.Asset(goos, goarch)] = body
}

// fakeTool is a Tool whose linux/amd64 and windows/amd64 archives are pinned
// to the given bodies, and each archive's executable to the member it holds.
func fakeTool(linux, windows []byte) Tool {
	t := Tool{Name: "faketool", Version: "v1.2.3", Repo: "acme/faketool", GoPackage: "example.com/faketool/cmd/faketool",
		Checksums: map[string]string{}, BinaryChecksums: map[string]string{}}
	for _, p := range []struct {
		goos string
		body []byte
	}{{"linux", linux}, {"windows", windows}} {
		asset := t.Asset(p.goos, "amd64")
		t.Checksums[asset] = sum(p.body)
		t.BinaryChecksums[asset] = sum(memberOf(p.body, p.goos == "windows", t.Binary(p.goos)))
	}
	return t
}

// memberOf is the named member of an in-memory .tar.gz or .zip, or nil.
func memberOf(archive []byte, isZip bool, name string) []byte {
	if isZip {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil
		}
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil
				}
				defer func() { _ = rc.Close() }()
				b, _ := io.ReadAll(rc)
				return b
			}
		}
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return nil
		}
		if hdr.Name == name {
			b, _ := io.ReadAll(tr)
			return b
		}
	}
}

func noGo(string) (string, error) { return "", errors.New("not found") }

func (r *release) fetcher(t *testing.T, goos string) *Fetcher {
	return testFetcher(t.TempDir(), r.srv.URL, goos)
}

// testFetcher is New's Fetcher pointed at cache, base and goos/amd64, with
// no Go toolchain and no download notices.
func testFetcher(cache, base, goos string) *Fetcher {
	f := New()
	f.CacheDir, f.BaseURL, f.GOOS, f.GOARCH = cache, base, goos, "amd64"
	f.LookPath, f.Log = noGo, io.Discard
	return f
}

func TestEnsureInstallsTheVerifiedBinary(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	f := rel.fetcher(t, "linux")

	in, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	if in.Version != "v1.2.3" || in.Source != SourceRelease || in.ArchiveSHA256 != sum(archive) || in.Platform != "linux/amd64" {
		t.Errorf("installed = %+v", in)
	}
	// The fetcher returns the cache's canonical path; compare canonical to
	// canonical (on Windows t.TempDir() can be an 8.3 short name).
	if want := filepath.Join(canonical(t, f.CacheDir), "faketool@v1.2.3-linux-amd64", "faketool"); in.Path != want {
		t.Errorf("path = %s, want %s", in.Path, want)
	}
	got, err := os.ReadFile(in.Path)
	if err != nil || string(got) != fakeBinary {
		t.Fatalf("binary = %q, %v; want the archive's member", got, err)
	}
	// Windows has no exec bit; the hash check above holds everywhere.
	if info, _ := os.Stat(in.Path); runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("binary mode %v is not executable", info.Mode())
	}
	// The archive itself is not kept, and no staging directory is left.
	entries, _ := os.ReadDir(filepath.Dir(in.Path))
	for _, e := range entries {
		if e.Name() != "faketool" && e.Name() != manifestName {
			t.Errorf("entry holds %s besides the binary and its manifest", e.Name())
		}
	}
	assertNoStaging(t, f.CacheDir)
}

func TestEnsureCacheHitUsesNoNetwork(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	f := rel.fetcher(t, "linux")

	first, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	rel.srv.Close() // any request now fails
	second, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatalf("a cached tool must not need the network: %v", err)
	}
	if second.Path != first.Path || rel.hits.Load() != 1 {
		t.Errorf("second Ensure: path %s (first %s), %d requests; want the same entry and 1 request", second.Path, first.Path, rel.hits.Load())
	}
	if in, ok := f.Find(tool); !ok || in.Path != first.Path {
		t.Errorf("Find = %+v, %v; want the installed entry", in, ok)
	}
}

func TestEnsureRefusesAChecksumMismatch(t *testing.T) {
	t.Parallel()
	pinned := tarGz(t, "faketool", fakeBinary)
	substituted := tarGz(t, "faketool", "#!/bin/sh\necho evil\n")
	tool := fakeTool(pinned, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", substituted)
	f := rel.fetcher(t, "linux")

	_, err := f.Ensure(context.Background(), tool)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	for _, want := range []string{sum(pinned), sum(substituted), "will not run an unverified binary"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
	if _, ok := f.Find(tool); ok {
		t.Error("a mismatched download was installed")
	}
	if _, statErr := os.Stat(filepath.Join(f.CacheDir, "faketool@v1.2.3-linux-amd64")); !os.IsNotExist(statErr) {
		t.Errorf("an entry exists after a refused download: %v", statErr)
	}
	assertNoStaging(t, f.CacheDir)
}

func TestEnsureWindowsZip(t *testing.T) {
	t.Parallel()
	archive := zipOf(t, "faketool.exe", "MZ fake")
	tool := fakeTool(nil, archive)
	rel := newRelease(t)
	rel.serve(tool, "windows", "amd64", archive)
	f := rel.fetcher(t, "windows")

	in, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(in.Path) != "faketool.exe" {
		t.Errorf("path = %s, want faketool.exe", in.Path)
	}
	if got, _ := os.ReadFile(in.Path); string(got) != "MZ fake" {
		t.Errorf("binary = %q", got)
	}
}

func TestEnsureFailsOnHTTPErrorAndMissingMember(t *testing.T) {
	t.Parallel()
	t.Run("404", func(t *testing.T) {
		t.Parallel()
		archive := tarGz(t, "faketool", fakeBinary)
		tool := fakeTool(archive, nil)
		rel := newRelease(t) // serves nothing
		f := rel.fetcher(t, "linux")
		_, err := f.Ensure(context.Background(), tool)
		if err == nil || !strings.Contains(err.Error(), "HTTP 404") || errors.Is(err, ErrChecksum) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want a transient HTTP 404", err)
		}
		assertNoStaging(t, f.CacheDir)
	})
	t.Run("no binary in the archive", func(t *testing.T) {
		t.Parallel()
		archive := tarGz(t, "", "")
		tool := fakeTool(archive, nil)
		rel := newRelease(t)
		rel.serve(tool, "linux", "amd64", archive)
		f := rel.fetcher(t, "linux")
		_, err := f.Ensure(context.Background(), tool)
		if err == nil || !strings.Contains(err.Error(), "contains no faketool executable") {
			t.Fatalf("err = %v, want the missing member named", err)
		}
		if _, ok := f.Find(tool); ok {
			t.Error("an archive without the binary installed an entry")
		}
	})
}

// A platform the release does not publish builds with go install when Go is
// present, and is ErrUnavailable — "not scanned" — when it is not.
func TestEnsureUnsupportedPlatform(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)

	t.Run("go install fallback", func(t *testing.T) {
		t.Parallel()
		f := rel.fetcher(t, "plan9")
		f.LookPath = func(string) (string, error) { return "/usr/bin/go", nil }
		var gotSpec string
		f.GoInstall = func(_ context.Context, spec, gobin string) error {
			gotSpec = spec
			return os.WriteFile(filepath.Join(gobin, "faketool"), []byte("built"), 0o755)
		}
		if m := f.Method(tool); m != MethodGoInstall {
			t.Fatalf("Method = %s, want go-install", m)
		}
		in, err := f.Ensure(context.Background(), tool)
		if err != nil {
			t.Fatal(err)
		}
		if gotSpec != "example.com/faketool/cmd/faketool@v1.2.3" || in.Source != SourceGoInstall || in.GoPackage != gotSpec {
			t.Errorf("spec %q, installed %+v", gotSpec, in)
		}
		if rel.hits.Load() != 0 {
			t.Error("the go install fallback downloaded a release asset")
		}
	})
	t.Run("no go", func(t *testing.T) {
		t.Parallel()
		f := rel.fetcher(t, "plan9")
		if m := f.Method(tool); m != MethodUnavailable {
			t.Fatalf("Method = %s, want unavailable", m)
		}
		_, err := f.Ensure(context.Background(), tool)
		if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "not scanned") {
			t.Fatalf("err = %v, want ErrUnavailable saying it is not scanned", err)
		}
	})
}

// An entry whose manifest does not carry the pinned checksum (a different
// pin's leftovers, a hand edit) is replaced, not served.
func TestEnsureReplacesAnEntryWithTheWrongChecksum(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	f := rel.fetcher(t, "linux")

	dir := filepath.Join(f.CacheDir, "faketool@v1.2.3-linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "faketool"), []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(dir, Installed{Name: "faketool", Version: "v1.2.3", Source: SourceRelease, Platform: "linux/amd64", ArchiveSHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Find(tool); ok {
		t.Fatal("Find served an entry with the wrong checksum")
	}
	in, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(in.Path); string(got) != fakeBinary {
		t.Errorf("binary = %q, want the verified one", got)
	}
}

// Parallel forge processes installing the same tool all get one whole entry.
func TestEnsureConcurrentInstallsConverge(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	cache := t.TempDir()

	const n = 8
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := testFetcher(cache, rel.srv.URL, "linux")
			in, err := f.Ensure(context.Background(), tool)
			paths[i], errs[i] = in.Path, err
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("install %d: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Errorf("install %d got %s, install 0 got %s", i, paths[i], paths[0])
		}
	}
	if got, _ := os.ReadFile(paths[0]); string(got) != fakeBinary {
		t.Errorf("binary = %q", got)
	}
	assertNoStaging(t, cache)
}

// FORGE_TOOLS_MIRROR replaces https://github.com and keeps GitHub's paths.
func TestMirrorReplacesGitHub(t *testing.T) {
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	t.Setenv(EnvMirror, rel.srv.URL+"/")

	// New reads the mirror; the trailing "/" is trimmed.
	f := New()
	f.CacheDir, f.GOOS, f.GOARCH, f.LookPath, f.Log = t.TempDir(), "linux", "amd64", noGo, io.Discard
	if want := rel.srv.URL + "/acme/faketool/releases/download/v1.2.3/faketool_1.2.3_linux_amd64.tar.gz"; f.URL(tool) != want {
		t.Errorf("URL = %s, want %s", f.URL(tool), want)
	}
	if _, err := f.Ensure(context.Background(), tool); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvMirror, "")
	def := New()
	def.GOOS, def.GOARCH = "linux", "amd64"
	if got := def.URL(Syft); got != "https://github.com/anchore/syft/releases/download/"+templates.SyftVersion+"/syft_"+strings.TrimPrefix(templates.SyftVersion, "v")+"_linux_amd64.tar.gz" {
		t.Errorf("default URL = %s", got)
	}
}

// The pins are pkg/toolpins' (which holds the platform-coverage and
// upstream-checksum tests): every pinned archive resolves here under the
// same asset name, to the same archive and executable sums, and nothing
// else does.
func TestToolsArePkgToolpins(t *testing.T) {
	t.Parallel()
	pins := toolpins.All()
	if len(Tools()) != len(pins) {
		t.Fatalf("toolfetch has %d tools, toolpins %d", len(Tools()), len(pins))
	}
	for _, tool := range Tools() {
		p, ok := toolpins.Lookup(tool.Name)
		if !ok || p.Version != tool.Version || p.Repo != tool.Repo || p.GoPackage != tool.GoPackage {
			t.Errorf("%s: toolfetch %+v does not match toolpins %+v", tool.Name, tool, p)
			continue
		}
		if len(tool.Checksums) != len(p.Archives) {
			t.Errorf("%s: %d checksums, toolpins has %d archives", tool.Name, len(tool.Checksums), len(p.Archives))
		}
		for _, a := range p.Archives {
			asset, sum, ok := tool.checksum(a.GOOS, a.GOARCH)
			bin, binOK := tool.binaryChecksum(a.GOOS, a.GOARCH)
			if !binOK || bin != a.BinarySHA256 {
				t.Errorf("%s %s/%s: toolfetch's executable pin %s, toolpins pins %s", tool.Name, a.GOOS, a.GOARCH, bin, a.BinarySHA256)
			}
			if !ok || asset != a.Name || sum != a.SHA256 || tool.Binary(a.GOOS) != a.Binary {
				t.Errorf("%s %s/%s: toolfetch resolves %s %s, toolpins pins %s %s", tool.Name, a.GOOS, a.GOARCH, asset, sum, a.Name, a.SHA256)
			}
		}
	}
	if Syft.Version != templates.SyftVersion || Grype.Version != templates.GrypeVersion {
		t.Errorf("tool versions %s/%s are not templates' pins", Syft.Version, Grype.Version)
	}
}

// canonical is dir with every link and short name resolved: the form the
// fetcher returns paths in.
func canonical(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func assertNoStaging(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("staging left behind: %s", e.Name())
		}
	}
}
