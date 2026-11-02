package hostinfra

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// THE CACHE PATH IS A CONTRACT, not an implementation detail.
//
// control-plane's workspace image pre-caches this binary so a container
// start does not re-download 50 MB, and it can only do that by writing to
// the exact path forge reads. Changing the layout silently would not break
// correctness — forge would just download again — which is precisely why it
// needs a test: the regression is invisible except as a slow start nobody
// attributes to this.
func TestZitadelBinaryPath_IsTheDocumentedCacheContract(t *testing.T) {
	got := ZitadelBinaryPath()
	if got == "" {
		t.Skip("no user cache dir on this machine")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("UserCacheDir: %v", err)
	}
	want := filepath.Join(cache, "forge", "zitadel", ZitadelVersion,
		runtime.GOOS+"-"+runtime.GOARCH, zitadelBinaryName(runtime.GOOS))
	if got != want {
		t.Errorf("cache path drifted from the published contract\n got: %s\nwant: %s\n"+
			"anything that pre-caches this binary (control-plane's workspace image) writes to the "+
			"OLD path and its work is silently wasted", got, want)
	}
	// Version must be IN the path: two projects on different pins have to
	// coexist, and a bump must not need a manual cache wipe.
	if !strings.Contains(got, ZitadelVersion) {
		t.Errorf("cache path %q does not carry the version — a version bump would reuse the old binary", got)
	}
	// The filename is what a pre-caching image writes. Bare `zitadel`, no
	// version suffix, because the version is already a directory (plus the
	// .exe Windows requires to run it at all).
	if want := zitadelBinaryName(runtime.GOOS); filepath.Base(got) != want {
		t.Errorf("cached binary filename = %q, want %q", filepath.Base(got), want)
	}
}

// The version pin is what keeps a dev stack and a deployed one talking
// about the same IdP. It is duplicated (deliberately, in three files that
// cannot import each other) so its FORM is asserted here — a bare tag with
// no "v" would silently build a URL that 404s.
func TestZitadelVersion_IsPinnedTag(t *testing.T) {
	if !strings.HasPrefix(ZitadelVersion, "v") {
		t.Errorf("ZitadelVersion = %q, want a v-prefixed release tag (the download URL is built from it verbatim)", ZitadelVersion)
	}
	if strings.Contains(ZitadelVersion, "latest") {
		t.Errorf("ZitadelVersion = %q — never `latest`: an IdP that changes version under a dev stack "+
			"turns \"login broke today\" into archaeology", ZitadelVersion)
	}
}

// Every platform forge claims to support must have BOTH an asset name and
// a checksum, and the two must agree. A platform with an asset but no
// checksum would fail at download time with a mismatch against the empty
// string — a confusing way to say "unsupported".
func TestZitadelAsset_CoversEveryPublishedPlatform(t *testing.T) {
	supported := []struct{ goos, goarch string }{
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"linux", "amd64"}, {"linux", "arm64"},
		{"windows", "amd64"}, {"windows", "arm64"},
	}
	for _, p := range supported {
		asset, ok := zitadelAsset(p.goos, p.goarch)
		if !ok {
			t.Errorf("%s/%s reports unsupported, but forge claims to run the IdP there", p.goos, p.goarch)
			continue
		}
		sum, has := zitadelChecksums[asset]
		if !has || len(sum) != 64 {
			t.Errorf("%s: checksum is missing or not a sha256 (%q)", asset, sum)
		}
	}
	// An unpublished platform must report so rather than composing a URL
	// that 404s halfway through a `forge env up`.
	if _, ok := zitadelAsset("plan9", "riscv64"); ok {
		t.Error("plan9/riscv64 reported as supported")
	}
}

func TestZitadelDownloadURL_PointsAtThePinnedRelease(t *testing.T) {
	url := zitadelDownloadURL("zitadel-linux-amd64.tar.gz")
	for _, want := range []string{"github.com/zitadel/zitadel", ZitadelVersion, "zitadel-linux-amd64.tar.gz"} {
		if !strings.Contains(url, want) {
			t.Errorf("download URL %q is missing %q", url, want)
		}
	}
}

// An unknown engine must be REFUSED, not silently treated as postgres.
// Both engines write to a data directory and one of them would be the
// wrong server entirely.
func TestStart_RefusesUnknownEngine(t *testing.T) {
	err := Start(context.Background(), t.TempDir(), Spec{Name: "mystery", Engine: "cockroach", Port: 1})
	if err == nil {
		t.Fatal("an unsupported engine must fail rather than fall through to a default")
	}
	if !strings.Contains(err.Error(), "cockroach") {
		t.Errorf("the error must name the unsupported engine, got: %v", err)
	}
}

// EXTRACTION FLATTENS THE ARCHIVE. Zitadel ships the binary inside a
// per-platform DIRECTORY (`zitadel-darwin-arm64/zitadel`), so an extractor
// that assumed a bare file at the root would find nothing and report an
// empty archive for a perfectly good download.
func TestExtractZitadel_FindsTheBinaryInsideThePlatformDirectory(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "zitadel.tar.gz")
	writeTarGz(t, archive, map[string]string{
		"zitadel-darwin-arm64/LICENSE":   "license text",
		"zitadel-darwin-arm64/README.md": "readme",
		"zitadel-darwin-arm64/zitadel":   "#!/bin/sh\necho zitadel version v4.19.5\n",
	})

	dest := filepath.Join(dir, "out", "zitadel")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	// The darwin layout this archive mimics; extractZitadelFor's windows case
	// (member zitadel.exe) is pinned separately below.
	if err := extractZitadelFor(archive, dest, "darwin"); err != nil {
		t.Fatalf("extractZitadel: %v", err)
	}

	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("the binary was not written to the flattened path: %v", err)
	}
	if !strings.Contains(string(body), "zitadel version") {
		t.Errorf("extracted the wrong archive member: %q", body)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	// Not executable means the whole download succeeded and the server
	// still cannot start — with an error that names permissions, not this.
	// (A POSIX bit: Windows runs a file by its .exe name, and keeps none.)
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Errorf("extracted binary is not executable (mode %v)", info.Mode())
	}
}

func TestExtractZitadel_ReportsAnArchiveWithNoBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "empty.tar.gz")
	writeTarGz(t, archive, map[string]string{"zitadel-linux-amd64/README.md": "no binary here"})

	err := extractZitadel(archive, filepath.Join(dir, "zitadel"))
	if err == nil {
		t.Fatal("an archive with no zitadel executable must fail loudly")
	}
	if !strings.Contains(err.Error(), "zitadel") {
		t.Errorf("the error should say what was missing, got: %v", err)
	}
}

// A CACHED BINARY IS RE-VERIFIED BY RUNNING IT, not by its presence. A
// truncated extraction or a leftover from another pin is on disk and
// unusable, and every one of those failures otherwise surfaces at first
// sign-in with nothing pointing back at the cache.
//
// The stand-ins below reproduce the REAL binary's CLI surface, and one
// detail of it is the whole reason this test exists: `zitadel -v` prints
// the version, while `zitadel version` exits non-zero with "unknown
// command" — there is no version SUBCOMMAND. A probe written the obvious
// way therefore condemns a perfectly good binary, and does it at the point
// where the only symptom is "the IdP will not start".
func TestVerifyZitadelBinary_RejectsAWrongVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("runs stand-in zitadel binaries as shell subprocesses; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-in is POSIX-only")
	}
	dir := t.TempDir()

	right := filepath.Join(dir, "right")
	writeExecutable(t, right, zitadelCLIStub(ZitadelVersion))
	if err := verifyZitadelBinary(right); err != nil {
		t.Errorf("a binary reporting the pinned version must verify: %v\n"+
			"the probe must use `-v`; `zitadel version` is not a subcommand and always fails", err)
	}

	wrong := filepath.Join(dir, "wrong")
	writeExecutable(t, wrong, zitadelCLIStub("v3.0.0"))
	if err := verifyZitadelBinary(wrong); err == nil {
		t.Error("a binary reporting a DIFFERENT version must not verify")
	}

	if err := verifyZitadelBinary(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing binary must not verify")
	}

	notExec := filepath.Join(dir, "plain")
	if err := os.WriteFile(notExec, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyZitadelBinary(notExec); err == nil {
		t.Error("a non-executable file must not verify")
	}
}

// zitadelCLIStub is a shell stand-in that answers the way the real zitadel
// binary does: `-v` / `--version` print the version, and anything else —
// including `version` — is an unknown command.
func zitadelCLIStub(version string) string {
	return "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  -v|--version) echo \"zitadel version " + version + "\" ;;\n" +
		"  *) echo \"Error: unknown command \\\"$1\\\" for \\\"zitadel\\\"\" >&2; exit 1 ;;\n" +
		"esac\n"
}

// The pidfile is how `forge env down` finds a server that OUTLIVES the
// process that started it. Round-tripping the port alongside the pid is
// what lets forge tell "ours, where we expect it" from "ours, somewhere
// else" — the same distinction postgres's postmaster.pid buys.
func TestZitadelPIDFile_RoundTripsPIDAndPort(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := readZitadelPID(dir); ok {
		t.Fatal("an empty data dir must report no recorded instance")
	}
	if err := writeZitadelPID(dir, 4242, 8080); err != nil {
		t.Fatal(err)
	}
	pid, port, ok := readZitadelPID(dir)
	if !ok || pid != 4242 || port != 8080 {
		t.Fatalf("readZitadelPID = (%d, %d, %v), want (4242, 8080, true)", pid, port, ok)
	}
	removeZitadelPID(dir)
	if _, _, ok := readZitadelPID(dir); ok {
		t.Error("the pidfile must be gone after removeZitadelPID")
	}
}

// A crashed predecessor must not read as "already running" — that would
// make the next `forge env up` skip the start and then fail against an IdP
// that is not there.
func TestReapDeadZitadel_ClearsAStalePIDFile(t *testing.T) {
	dir := t.TempDir()
	// PID 1 is alive but is not ours; a pid that cannot exist is the
	// portable way to stand in for a dead one.
	if err := writeZitadelPID(dir, 0x7FFFFFFE, 8080); err != nil {
		t.Fatal(err)
	}
	reapDeadZitadel(dir)
	if _, _, ok := readZitadelPID(dir); ok {
		t.Error("a pidfile naming a dead process must be cleared")
	}
}

// The zitadel engine's env is the CONTRACT with the server: these are the
// same variables the compose service set, and each one is load-bearing.
// Asserting them here is what keeps a refactor from dropping the one that
// makes sign-in work.
func TestZitadelEnv_CarriesTheLoadBearingSettings(t *testing.T) {
	spec := Spec{
		Name: "idp", Engine: EngineZitadel, Port: 8180,
		User: "postgres", Password: "postgres",
		IDPDatabase: "zitadel", IDPDatabasePort: 5433,
	}
	env := map[string]string{}
	for _, kv := range spec.zitadelEnv("/tmp/pat.txt") {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}

	want := map[string]string{
		// The BROWSER-facing origin: what Zitadel mints tokens under and
		// routes requests by. Wrong here means "Instance not found".
		"ZITADEL_EXTERNALDOMAIN": "localhost",
		"ZITADEL_EXTERNALPORT":   "8180",
		"ZITADEL_PORT":           "8180",
		// Plain HTTP — both are required; ExternalSecure alone still
		// serves TLS.
		"ZITADEL_EXTERNALSECURE": "false",
		"ZITADEL_TLS_ENABLED":    "false",
		// The declarative boot writes the PAT the idp-provision job reads.
		"ZITADEL_FIRSTINSTANCE_PATPATH": "/tmp/pat.txt",
		// The v1 sign-in pages. True routes /oauth/v2/authorize at a login
		// UI that ships separately; absent, the sign-in page 404s and the
		// redirect flow dead-ends after a correct-looking request.
		"ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED": "false",
		// No passkey/2FA enrollment interstitial. Both are needed and they
		// are not the same knob.
		"ZITADEL_DEFAULTINSTANCE_LOGINPOLICY_PASSWORDLESSTYPE":    "0",
		"ZITADEL_DEFAULTINSTANCE_LOGINPOLICY_MFAINITSKIPLIFETIME": "0s",
		// Its own database, on the app's host-native postgres.
		"ZITADEL_DATABASE_POSTGRES_HOST":     "localhost",
		"ZITADEL_DATABASE_POSTGRES_PORT":     "5433",
		"ZITADEL_DATABASE_POSTGRES_DATABASE": "zitadel",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

// Paths in the declaration are PROJECT-RELATIVE, and the PAT path in
// particular must resolve to the same file the idp-provision job reads
// (its --pat-file default). If these two ever disagree the IdP boots fine,
// the job runs fine, and the job cannot find the credential.
func TestZitadelSpecPaths_ResolveAgainstTheProjectRoot(t *testing.T) {
	spec := Spec{Name: "idp", Engine: EngineZitadel}
	root := filepath.Join("/projects", "acme")

	if got, want := spec.idpStepsPath(root), filepath.Join(root, "idp-steps.yaml"); got != want {
		t.Errorf("default steps path = %q, want %q", got, want)
	}
	if got, want := spec.PATPath(root), filepath.Join(root, ".forge", "idp", "pat.txt"); got != want {
		t.Errorf("default PAT path = %q, want %q\n"+
			"this must match the idp-provision job's --pat-file default, or the job cannot authenticate", got, want)
	}

	// Host-absolute: "/etc/steps.yaml" is only ROOTED on Windows (no volume),
	// so it would be joined onto the project root there.
	absDir := t.TempDir()
	stepsAbs, patAbs := filepath.Join(absDir, "steps.yaml"), filepath.Join(absDir, "pat.txt")
	abs := Spec{IDPStepsFile: stepsAbs, IDPPATPath: patAbs}
	if got := abs.idpStepsPath(root); got != stepsAbs {
		t.Errorf("an absolute steps path must pass through, got %q", got)
	}
	if got := abs.PATPath(root); got != patAbs {
		t.Errorf("an absolute PAT path must pass through, got %q", got)
	}
}

// helpers

func writeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		mode := int64(0o644)
		if filepath.Base(name) == "zitadel" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
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
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // a test stand-in for an executable
		t.Fatal(err)
	}
}

func TestZitadelWindows_AssetsAndHashes(t *testing.T) {
	want := map[string]string{
		"amd64": "d0a50c1279662c14372fe075bf155004f4c9c841c046081e7489cf13988f7c1e",
		"arm64": "d61acd4626ae0b11914d85955455c474252655995fbe047d0491f5813f81e524",
	}
	for arch, sum := range want {
		asset, ok := zitadelAsset("windows", arch)
		if !ok || asset != "zitadel-windows-"+arch+".tar.gz" || zitadelChecksums[asset] != sum {
			t.Errorf("windows/%s: asset=%q ok=%v sum=%q", arch, asset, ok, zitadelChecksums[asset])
		}
	}
}

func TestZitadelBinaryName_AndPathPerPlatform(t *testing.T) {
	if zitadelBinaryName("windows") != "zitadel.exe" || zitadelBinaryName("darwin") != "zitadel" || zitadelBinaryName("linux") != "zitadel" {
		t.Error("binary name wrong")
	}
	if p := zitadelBinaryPathFor("windows", "amd64"); p != "" {
		if filepath.Base(p) != "zitadel.exe" || filepath.Base(filepath.Dir(p)) != "windows-amd64" {
			t.Errorf("windows path = %s", p)
		}
	}
	if p := zitadelBinaryPathFor("linux", "arm64"); p != "" && filepath.Base(p) != "zitadel" {
		t.Errorf("linux path = %s", p)
	}
}

func TestExtractZitadel_WindowsMemberIsExe(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "z.tar.gz")
	writeTarGz(t, archive, map[string]string{
		"zitadel-windows-amd64/LICENSE":     "l",
		"zitadel-windows-amd64/zitadel.exe": "MZ-fake",
	})
	dest := filepath.Join(dir, "zitadel.exe")
	if err := extractZitadelFor(archive, dest, "windows"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "MZ-fake" {
		t.Errorf("wrong member extracted: %q", b)
	}
	if err := extractZitadelFor(archive, filepath.Join(dir, "x"), "linux"); err == nil {
		t.Error("linux extraction must not match zitadel.exe")
	}
}

// The compose `idp` service is the container escape hatch for the same IdP
// the host path runs; a bump that moves one pin and not the other leaves a
// dev stack and its escape hatch disagreeing about what an IdP is.
func TestZitadelVersion_MatchesComposeTemplate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "templates", "project", "docker-compose.yml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	// The compose image is also pinned by digest; the version tag before it
	// is what must match.
	want := regexp.MustCompile(`image: ghcr\.io/zitadel/zitadel:` + regexp.QuoteMeta(ZitadelVersion) + `(@sha256:[0-9a-f]{64})?\n`)
	if !want.Match(b) {
		t.Errorf("docker-compose.yml.tmpl does not pin ghcr.io/zitadel/zitadel:%s; ZitadelVersion and the compose image must move together", ZitadelVersion)
	}
}

// Smoke for the pin: fetches the REAL upstream archive for this platform into
// a throwaway cache, so a wrong checksum in zitadelChecksums, a renamed asset
// or a retagged release fails here instead of at a developer's first
// `forge env up`. Then runs the unmodified binary and asserts its version.
func TestEnsureZitadelBinary_RealUpstreamArchiveMatchesPin(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads the real ~50 MB release archive; runs in task test")
	}
	if _, ok := zitadelAsset(runtime.GOOS, runtime.GOARCH); !ok {
		t.Skipf("no zitadel build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache) // linux
	t.Setenv("HOME", cache)           // darwin: ~/Library/Caches
	t.Setenv("LocalAppData", cache)   // windows

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	bin, err := ensureZitadelBinary(ctx)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			t.Skipf("release host unreachable from this machine: %v", err)
		}
		t.Fatalf("ensureZitadelBinary: %v", err)
	}
	if err := verifyZitadelBinary(bin); err != nil {
		t.Fatalf("the downloaded binary is not %s: %v", ZitadelVersion, err)
	}
}
