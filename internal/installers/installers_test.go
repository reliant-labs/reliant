package installers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// repoFile returns the repo-relative path's absolute form and reads it in the
// test process. Reading it here, not only in the child shell, is what keeps the
// tests below cache-sound under -short: Go's test cache tracks the files the
// test process opens, and an edit to install.sh must invalidate a cached PASS.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(p); err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return p
}

// hostAsset is the release asset name install.sh must pick on this machine.
func hostAsset() string {
	return fmt.Sprintf("forge_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fakeArchive returns a tar.gz holding an executable `forge` that prints
// "forge version <ver>", the same layout scripts/build-release-assets.sh packs.
func fakeArchive(t *testing.T, ver string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho 'forge version " + ver + "'\n")
	for _, f := range []struct {
		name string
		mode int64
		body []byte
	}{
		{"forge", 0o755, body},
		{"LICENSE", 0o644, []byte("license\n")},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
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

// release is a fixture GitHub-releases server. Each version serves its archive
// at /download/<tag>/<asset>, and the newest one is also at /latest/download/.
type release struct {
	srv *httptest.Server

	mu       sync.Mutex
	files    map[string][]byte // URL path -> body
	requests []string
}

func newRelease(t *testing.T) *release {
	t.Helper()
	r := &release{files: map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.URL.Path)
		body, ok := r.files[req.URL.Path]
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

// publish adds a release for tag. sumFor lets a test corrupt the checksum.
func (r *release) publish(t *testing.T, tag string, latest bool, sumFor func(asset string, archive []byte) string) {
	t.Helper()
	archive := fakeArchive(t, tag)
	asset := hostAsset()
	sums := fmt.Sprintf("%s  %s\n%s  forge_other_os.tar.gz\n", sumFor(asset, archive), asset, strings.Repeat("0", 64))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, prefix := range []string{"/download/" + tag} {
		r.files[prefix+"/"+asset] = archive
		r.files[prefix+"/checksums.txt"] = []byte(sums)
	}
	if latest {
		r.files["/latest/download/"+asset] = archive
		r.files["/latest/download/checksums.txt"] = []byte(sums)
	}
}

func goodSum(_ string, archive []byte) string { return sha256Hex(archive) }

type installResult struct {
	out    string
	err    error
	dir    string // install dir
	binary string
}

// runInstall runs install.sh under shell with a clean environment pointed at rel.
func runInstall(t *testing.T, shell string, rel *release, dir string, env ...string) installResult {
	t.Helper()
	script := repoFile(t, "install.sh")
	if dir == "" {
		dir = filepath.Join(t.TempDir(), "bin")
	}
	cmd := exec.CommandContext(context.Background(), shell, script)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
		"FORGE_RELEASE_BASE_URL=" + rel.srv.URL,
		"FORGE_INSTALL_DIR=" + dir,
	}, env...)
	out, err := cmd.CombinedOutput()
	return installResult{out: string(out), err: err, dir: dir, binary: filepath.Join(dir, "forge")}
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for unix; install.ps1 covers windows")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		if _, err := exec.LookPath("wget"); err != nil {
			t.Skip("neither curl nor wget on PATH")
		}
	}
}

func TestInstallSh_LatestInstallsVerifiedBinary(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)

	res := runInstall(t, "sh", rel, "")
	if res.err != nil {
		t.Fatalf("install failed: %v\n%s", res.err, res.out)
	}
	info, err := os.Stat(res.binary)
	if err != nil {
		t.Fatalf("binary not installed: %v\n%s", err, res.out)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	if !strings.Contains(res.out, "forge version v0.9.0") {
		t.Errorf("install did not run `forge version`:\n%s", res.out)
	}
	if !strings.Contains(res.out, "is not on your PATH") || !strings.Contains(res.out, res.dir) {
		t.Errorf("expected a PATH hint naming %s:\n%s", res.dir, res.out)
	}
	left, _ := filepath.Glob(filepath.Join(res.dir, ".forge.install.*"))
	if len(left) != 0 {
		t.Errorf("staging file left behind: %v", left)
	}
}

func TestInstallSh_NoPathHintWhenDirOnPath(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	dir := filepath.Join(t.TempDir(), "bin")

	res := runInstall(t, "sh", rel, dir, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if res.err != nil {
		t.Fatalf("install failed: %v\n%s", res.err, res.out)
	}
	if strings.Contains(res.out, "not on your PATH") {
		t.Errorf("PATH hint printed although %s is on PATH:\n%s", dir, res.out)
	}
}

func TestInstallSh_PinnedVersion(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	rel.publish(t, "v0.8.0", false, goodSum)

	for _, pin := range []string{"v0.8.0", "0.8.0"} {
		t.Run(pin, func(t *testing.T) {
			res := runInstall(t, "sh", rel, "", "FORGE_VERSION="+pin)
			if res.err != nil {
				t.Fatalf("install failed: %v\n%s", res.err, res.out)
			}
			if !strings.Contains(res.out, "forge version v0.8.0") {
				t.Errorf("pinned install did not install v0.8.0:\n%s", res.out)
			}
		})
	}
}

func TestInstallSh_UnknownVersionFailsAndInstallsNothing(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)

	res := runInstall(t, "sh", rel, "", "FORGE_VERSION=v9.9.9")
	if res.err == nil {
		t.Fatalf("installing a version that does not exist succeeded:\n%s", res.out)
	}
	if !strings.Contains(res.out, "v9.9.9") {
		t.Errorf("error should name the missing version:\n%s", res.out)
	}
	if _, err := os.Stat(res.binary); err == nil {
		t.Errorf("a binary was installed despite the failure")
	}
}

func TestInstallSh_ChecksumMismatchInstallsNothing(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, func(string, []byte) string { return strings.Repeat("ab", 32) })

	res := runInstall(t, "sh", rel, "")
	if res.err == nil {
		t.Fatalf("a checksum mismatch must fail the install:\n%s", res.out)
	}
	if !strings.Contains(res.out, "sha256 mismatch") {
		t.Errorf("error should say sha256 mismatch:\n%s", res.out)
	}
	if _, err := os.Stat(res.binary); err == nil {
		t.Errorf("an unverified binary was installed")
	}
}

func TestInstallSh_MissingChecksumEntryFails(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	rel.mu.Lock()
	rel.files["/latest/download/checksums.txt"] = []byte(strings.Repeat("0", 64) + "  forge_other_os.tar.gz\n")
	rel.mu.Unlock()

	res := runInstall(t, "sh", rel, "")
	if res.err == nil || !strings.Contains(res.out, "no entry for "+hostAsset()) {
		t.Fatalf("want a 'no entry' failure, got err=%v\n%s", res.err, res.out)
	}
}

func TestInstallSh_ReplacesExistingBinary(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge"), []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := runInstall(t, "sh", rel, dir)
	if res.err != nil {
		t.Fatalf("upgrade failed: %v\n%s", res.err, res.out)
	}
	if !strings.Contains(res.out, "forge version v0.9.0") {
		t.Errorf("old binary was not replaced:\n%s", res.out)
	}
}

func TestInstallSh_DefaultsToHomeLocalBin(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	home := t.TempDir()

	cmd := exec.CommandContext(context.Background(), "sh", repoFile(t, "install.sh"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(), "FORGE_RELEASE_BASE_URL=" + rel.srv.URL}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "forge")); err != nil {
		t.Errorf("default install dir is not $HOME/.local/bin: %v", err)
	}
}

func TestInstallSh_RejectsBadVersion(t *testing.T) {
	requireUnix(t)
	rel := newRelease(t)
	res := runInstall(t, "sh", rel, "", "FORGE_VERSION=v1.2.3/../../x")
	if res.err == nil || !strings.Contains(res.out, "FORGE_VERSION") {
		t.Fatalf("a path-like FORGE_VERSION must be rejected, got err=%v\n%s", res.err, res.out)
	}
	if len(rel.requests) != 0 {
		t.Errorf("a rejected version must not reach the network, saw %v", rel.requests)
	}
}

// install.sh is documented as POSIX sh; prove it under dash, which is stricter
// than bash about bashisms.
func TestInstallSh_RunsUnderDash(t *testing.T) {
	requireUnix(t)
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Skip("dash not installed")
	}
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	res := runInstall(t, dash, rel, "")
	if res.err != nil {
		t.Fatalf("install under dash failed: %v\n%s", res.err, res.out)
	}
}

func TestShellScriptsPassShellcheck(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed")
	}
	scripts := []string{"install.sh", "scripts/build-release-assets.sh", "scripts/render-homebrew-formula.sh", "scripts/release-forge.sh"}
	// Analyze the sourced helper too, relative to the repository rather
	// than this package's test working directory; do not suppress SC1091.
	args := []string{"--severity=style", "--external-sources", "--source-path=" + filepath.Dir(repoFile(t, "install.sh"))}
	for _, s := range scripts {
		args = append(args, repoFile(t, s))
	}
	if out, err := exec.CommandContext(context.Background(), sc, args...).CombinedOutput(); err != nil {
		t.Fatalf("shellcheck failed:\n%s", out)
	}
}

func TestInstallShIsPOSIXShebang(t *testing.T) {
	b, err := os.ReadFile(repoFile(t, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "#!/bin/sh\n") {
		t.Errorf("install.sh must start with #!/bin/sh (it is piped into sh), got %q", strings.SplitN(string(b), "\n", 2)[0])
	}
	info, err := os.Stat(repoFile(t, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Errorf("install.sh is not executable in git: %v", info.Mode())
	}
}
