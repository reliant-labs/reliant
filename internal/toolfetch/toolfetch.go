// Package toolfetch installs non-Go tools forge runs for itself — today syft
// (SBOM generation) and grype (vulnerability scanning) — as pinned upstream
// release binaries, each archive verified against a sha256 compiled into
// forge before a byte of it is extracted.
//
// # Using it
//
//	path, version, err := toolfetch.Ensure(ctx, toolfetch.Syft)
//	if err != nil {
//		// The tool is unavailable: whatever needed it did NOT run. Record
//		// "not scanned" / "no SBOM", never a clean result.
//	}
//	cmd := exec.CommandContext(ctx, path, "scan", ...)
//
// Ensure is idempotent after the first call: a trusted, verified entry in the
// cache is a hit with no network. The first call downloads the archive for
// this GOOS/GOARCH (tens of MB), prints one line to stderr saying so,
// verifies it and installs the binary. The returned path is safe to exec for
// as long as the threat model below holds; see "Who can change the binary". version is always the pinned release
// (templates.SyftVersion / templates.GrypeVersion), the string a scan record
// names as its scanner version.
//
// Find reports an installed tool without installing anything or touching the
// network (`forge doctor` uses it). MethodFor reports how this platform
// would get a tool: the verified release, `go install`, or not at all. A Fetcher
// (New) is the same API with every input injectable — cache
// directory, download base URL, HTTP client, platform, the `go install`
// fallback — for tests and for callers that need their own log writer.
//
// # Errors
//
// Every error from Ensure means the tool is not usable. Three are classifiable
// with errors.Is:
//
//   - ErrUnavailable: this platform has no published binary forge has a
//     checksum for, and there is no Go toolchain to build it with. Retrying
//     will not help.
//   - ErrChecksum: the download did not hash to the pinned value. Nothing was
//     installed; the bytes were corrupted or substituted.
//   - ErrUntrustedCache: a directory on the cache's path is writable by
//     another user. Reinstalling would not help; the error names the
//     directory and the fix.
//
// Anything else (no network, an HTTP error, a full disk) is transient.
//
// # Where it lives
//
//	$FORGE_TOOLS_CACHE (default <UserCacheDir>/forge/tools)/release/
//	  <name>@<version>-<goos>-<goarch>/
//	    <name>[.exe]          mode 0555 (0755 on Windows)
//	    toolfetch.json        bookkeeping: what was installed and from where
//
// It is an entry of forge's shared tool cache (internal/toolcache), so it is
// built in a private staging directory and published by one rename: a
// parallel forge sees no entry or a whole one, never a half-written binary.
// Two processes that miss at once both download, and the loser of the rename
// uses the winner's entry. Nothing is ever rewritten in place; a bump is a
// new entry beside the old.
//
// # Who can change the binary (the threat model)
//
// Ensure hands back a path, and the caller execs it later, so what matters
// is who could replace the file in between. Two checks answer it on every
// hit, each enough on its own against a substituted release binary:
//
//   - The bytes. The cached executable must hash to the sha256 compiled into
//     forge for that platform's executable (Tool.BinaryChecksums), beside the
//     archive's. Nothing in the cache vouches for anything: whoever could
//     write the binary could write the manifest beside it. (A `go install`
//     build has no compiled-in pin and relies on the second check alone.)
//   - Who can write the path, decided by ownership and permissions, below.
//
// Who can write the path:
//
//   - Another user: excluded. On Unix, every directory from the filesystem root down
//     to the release kind directory must be owned by this user or root and be
//     writable by nobody else (a sticky directory such as /tmp, and a
//     group-writable directory of the user's own private group, are the two
//     exceptions; both still stop others renaming our entries). The kind
//     directory, the entry, its manifest and its binary must be owned by this
//     user, writable by no one else, and not symlinks; the binary is 0555.
//     The path is resolved once and then walked with Lstat, component by
//     component, so a symlink anywhere fails the check. A violation in an
//     entry refuses it and reinstalls into a fresh staging directory that
//     replaces it by rename; a violation above it is ErrUntrustedCache. With
//     no writer but this user able to touch any component, nothing can change
//     between the check and the exec. On Windows, ACLs instead of mode bits;
//     see below.
//   - This user (any process running as the same uid): out of scope. Such a
//     process can already replace forge itself, edit PATH and shell startup
//     files, or ptrace forge, so no check inside forge stops it. (The hash
//     check still refuses a binary such a process changed between two calls;
//     it cannot stop a change between the check and the exec.)
//   - Root: out of scope, for the same reason.
//
// Copying the verified bytes into a per-process private directory and
// executing the copy would also close the race, and was rejected: it costs
// ~90 MB of I/O per call (SBOM generation calls Ensure per artifact), needs
// an executable private directory (/tmp is noexec on hardened hosts), leaks
// copies whenever a process dies before its cleanup, and protects only
// against writers the permission check already excludes.
//
// Windows: the cache's protection is its ACL, and forge checks it, failing
// closed. For every component it reads the owner and DACL
// (GetNamedSecurityInfo) and refuses the cache (ErrUntrustedCache, or a
// reinstall for a bad entry) unless the owner is this user, SYSTEM,
// Administrators or TrustedInstaller; the DACL is present and not NULL; and
// no ACE grants anyone else write, append, delete, delete-child, WRITE_DAC
// or WRITE_OWNER (generic rights mapped first). Directories above the cache
// may let others add new children (C:\ lets Authenticated Users create
// folders), never delete, rename or re-permission one. Deny ACEs are not
// credited, an ACE type forge cannot read refuses, and a host whose ACLs
// cannot be read refuses. The default location, %LocalAppData%\forge\tools,
// passes as it comes: the profile's inherited per-user ACL is private. The
// rule is evaluateACL, a pure function table-tested on every OS. Symlinks are
// refused as on Unix.
//
// # Where it downloads from
//
// https://github.com/<repo>/releases/download/<version>/<asset>. FORGE_TOOLS_MIRROR
// replaces https://github.com with a mirror that serves the same paths (an
// Artifactory or Nexus remote of github.com, an internal proxy). A mirror
// cannot substitute bytes: the pinned sha256 is checked either way.
//
// # When there is no binary
//
// A platform the upstream release does not publish (grype has no
// windows/arm64 build) falls back to `go install <GoPackage>@<Version>`,
// which the Go checksum database verifies, when a Go toolchain is on PATH.
// Otherwise Ensure returns ErrUnavailable.
//
// # Per-project versions
//
// A Tool is a plain value, so a caller that wants another version builds its
// own Tool with that version AND that version's checksums. There is
// deliberately no way to name a version without its checksums: a version
// forge has no sha256 for cannot be verified, and this package never
// installs a release binary it did not verify. (forge.yaml has no such
// override yet; the seam is here for when it does.)
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the download is the package's whole job, and every input it reaches out with (BaseURL, Client, GoInstall, LookPath, CacheDir) is already an injectable Fetcher field the tests point at an httptest server, as internal/cigate's Resolver does; a contract.go Service would be one implementation with no substitution point, and a consumer that wants a fake declares its own one-method interface over Ensure
//forge:exclude-contract: a filesystem tool cache with package-level operations, not an injected application component
package toolfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/toolcache"
)

// EnvMirror replaces https://github.com as the base of every release
// download. The mirror must serve GitHub's path layout:
// <mirror>/<owner>/<repo>/releases/download/<tag>/<asset>.
const EnvMirror = "FORGE_TOOLS_MIRROR"

// DefaultBaseURL is where release archives are downloaded from when
// FORGE_TOOLS_MIRROR is unset.
const DefaultBaseURL = "https://github.com"

// maxArchiveBytes bounds a download and an extracted binary. The pinned
// archives are ~30 MB and their binaries ~100 MB; a response that keeps
// going is not one of them.
const maxArchiveBytes = 1 << 30

// manifestName is the file in each entry recording what was installed.
const manifestName = "toolfetch.json"

var (
	// ErrUnavailable means the tool cannot be installed on this platform at
	// all: no published binary forge has a checksum for, and no Go toolchain.
	ErrUnavailable = errors.New("tool unavailable on this platform")
	// ErrChecksum means a downloaded archive did not match its pinned sha256.
	ErrChecksum = errors.New("checksum mismatch")
)

// Tool is a pinned upstream release of one binary.
type Tool struct {
	// Name is the binary's name (without .exe) and the cache entry's prefix.
	Name string
	// Version is the release tag, "v1.57.2". Asset names carry it without
	// the leading "v", as goreleaser writes them.
	Version string
	// Repo is the GitHub owner/repo the release is published under.
	Repo string
	// GoPackage is the main package `go install` builds when the platform
	// has no published binary.
	GoPackage string
	// Checksums maps a release asset's file name to its sha256, copied from
	// the release's checksums file. A platform whose asset is absent has no
	// verified binary.
	Checksums map[string]string
	// BinaryChecksums maps the same asset names to the sha256 of the
	// executable inside each archive. Every asset in Checksums must have
	// one. Every cache hit hashes the cached binary against this compiled-in
	// value, so nothing written to the cache (the manifest included) can
	// vouch for a binary.
	BinaryChecksums map[string]string
}

// Asset is the release archive name for goos/goarch, in goreleaser's
// <name>_<version>_<os>_<arch>.<ext> layout (.zip on Windows).
func (t Tool) Asset(goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", t.Name, strings.TrimPrefix(t.Version, "v"), goos, goarch, ext)
}

// Binary is the executable's file name on goos.
func (t Tool) Binary(goos string) string {
	if goos == "windows" {
		return t.Name + ".exe"
	}
	return t.Name
}

// binaryChecksum returns the pinned sha256 of the executable in
// goos/goarch's asset, if any.
func (t Tool) binaryChecksum(goos, goarch string) (string, bool) {
	sum, ok := t.BinaryChecksums[t.Asset(goos, goarch)]
	return sum, ok
}

// checksum returns the pinned sha256 of goos/goarch's asset, if any.
func (t Tool) checksum(goos, goarch string) (asset, sum string, ok bool) {
	asset = t.Asset(goos, goarch)
	sum, ok = t.Checksums[asset]
	return asset, sum, ok
}

// Source says how an installed entry was produced.
type Source string

const (
	// SourceRelease is a release archive verified against its pinned sha256.
	SourceRelease Source = "release"
	// SourceGoInstall is a `go install` build, verified by the Go checksum
	// database.
	SourceGoInstall Source = "go-install"
)

// Method is how a platform gets a tool.
type Method string

const (
	// MethodRelease downloads the verified release binary.
	MethodRelease Method = "release"
	// MethodGoInstall builds it with `go install` (no published binary).
	MethodGoInstall Method = "go-install"
	// MethodUnavailable means neither is possible.
	MethodUnavailable Method = "unavailable"
)

// Installed is a usable tool on disk.
type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Path is the executable's absolute path.
	Path   string `json:"-"`
	Source Source `json:"source"`
	// Platform is <goos>/<goarch>.
	Platform string `json:"platform"`
	// URL and ArchiveSHA256 are set for SourceRelease: what was downloaded
	// and the pinned hash it matched.
	URL           string `json:"url,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
	// GoPackage is the `go install` spec for SourceGoInstall.
	GoPackage string `json:"go_package,omitempty"`
	// BinarySHA256 is the executable's sha256 when it was installed. It is
	// bookkeeping only for a release: the hit check compares the binary to
	// the compiled-in pin (Tool.BinaryChecksums), never to this. A go
	// install build has no compiled-in pin, so for it this is all there is,
	// and it guards against corruption only; the cache's privacy (the
	// package doc's threat model) is what protects such an entry.
	BinarySHA256 string `json:"binary_sha256"`
}

// Fetcher installs tools. New returns the one forge uses, every field
// resolved from this process and its environment; tests start from New and
// override fields.
type Fetcher struct {
	// CacheDir is the directory entries live in: New sets the release kind
	// of forge's shared tool cache ($FORGE_TOOLS_CACHE/release). "" (that
	// cache could not be resolved) makes every install fail with the reason.
	CacheDir string
	// BaseURL replaces https://github.com: New sets $FORGE_TOOLS_MIRROR,
	// else DefaultBaseURL.
	BaseURL string
	// Client downloads archives: New sets a client with a 10-minute timeout
	// that honours the standard proxy environment.
	Client *http.Client
	// GOOS and GOARCH are the platform to install for: New sets this
	// process's.
	GOOS, GOARCH string
	// LookPath finds the Go toolchain for the fallback. nil is exec.LookPath.
	LookPath func(file string) (string, error)
	// GoInstall runs `go install spec` with GOBIN=gobin. nil runs the real
	// toolchain.
	GoInstall func(ctx context.Context, spec, gobin string) error
	// Log receives the one-line notice of a download. nil is os.Stderr.
	Log io.Writer
	// owner is the uid every directory and file forge created in the cache
	// must belong to (see the package doc's threat model): New sets this
	// process's. Use New; a zero Fetcher trusts only root-owned caches.
	owner int
	// hostOS is the OS of the filesystem the cache is on, which decides
	// whether owner and mode bits are checked (modesEnforced): New sets
	// runtime.GOOS. Distinct from GOOS, the platform a binary is installed
	// for.
	hostOS string
	// acl reads security descriptors when hostOS is Windows: New sets the
	// platform's reader and the current user's SID. Its zero value refuses
	// every Windows cache.
	acl aclSource
}

// New returns the Fetcher forge uses: forge's shared tool cache, the
// $FORGE_TOOLS_MIRROR (else GitHub), a 10-minute download client, and this
// process's platform.
func New() *Fetcher {
	// An unresolvable cache root leaves CacheDir "", and cacheDir reports
	// why when something needs it; Method needs no cache at all.
	cache, _ := toolcache.KindDir(toolcache.KindRelease)
	base := DefaultBaseURL
	if m := strings.TrimSpace(os.Getenv(EnvMirror)); m != "" {
		base = m
	}
	return &Fetcher{
		CacheDir: cache,
		BaseURL:  base,
		Client:   &http.Client{Timeout: 10 * time.Minute},
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		owner:    os.Getuid(),
		hostOS:   runtime.GOOS,
		acl:      platformACL(),
	}
}

// Ensure returns the path and pinned version of a usable t, installing it
// on first use with New's Fetcher. See the package doc.
func Ensure(ctx context.Context, t Tool) (path, version string, err error) {
	in, err := New().Ensure(ctx, t)
	if err != nil {
		return "", "", err
	}
	return in.Path, in.Version, nil
}

// Find reports t when it is installed in the default cache, without
// installing anything or using the network.
func Find(t Tool) (Installed, bool) { return New().Find(t) }

// MethodFor reports how New's Fetcher would get t on this platform.
func MethodFor(t Tool) Method { return New().Method(t) }

func (f *Fetcher) goos() string { return f.GOOS }

func (f *Fetcher) goarch() string { return f.GOARCH }

func (f *Fetcher) platform() string { return f.goos() + "/" + f.goarch() }

func (f *Fetcher) cacheDir() (string, error) {
	if f.CacheDir != "" {
		return f.CacheDir, nil
	}
	// Only a Fetcher whose cache root did not resolve gets here; resolving
	// again returns the reason.
	if _, err := toolcache.KindDir(toolcache.KindRelease); err != nil {
		return "", err
	}
	return "", errors.New("toolfetch: no cache directory")
}

func (f *Fetcher) baseURL() string { return strings.TrimRight(f.BaseURL, "/") }

func (f *Fetcher) client() *http.Client { return f.Client }

func (f *Fetcher) log() io.Writer {
	if f.Log != nil {
		return f.Log
	}
	return os.Stderr
}

func (f *Fetcher) hasGo() bool {
	look := f.LookPath
	if look == nil {
		look = exec.LookPath
	}
	_, err := look("go")
	return err == nil
}

// URL is where t's archive for the Fetcher's platform is downloaded from.
func (f *Fetcher) URL(t Tool) string {
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", f.baseURL(), t.Repo, t.Version, t.Asset(f.goos(), f.goarch()))
}

// Method reports how the Fetcher's platform gets t.
func (f *Fetcher) Method(t Tool) Method {
	if _, _, ok := t.checksum(f.goos(), f.goarch()); ok {
		return MethodRelease
	}
	if t.GoPackage != "" && f.hasGo() {
		return MethodGoInstall
	}
	return MethodUnavailable
}

// key is t's cache entry name. The platform is part of it because it
// decides the bytes, and a FORGE_TOOLS_CACHE can be shared.
func (f *Fetcher) key(t Tool) string {
	return fmt.Sprintf("%s@%s-%s-%s", t.Name, t.Version, f.goos(), f.goarch())
}

// Find reports t when its entry is installed and usable. No network.
func (f *Fetcher) Find(t Tool) (Installed, bool) {
	if f.validate(t) != nil {
		return Installed{}, false
	}
	dir, err := f.cacheDir()
	if err != nil {
		return Installed{}, false
	}
	if dir, err = f.trustedKindDir(dir); err != nil {
		return Installed{}, false
	}
	return f.ready(t, filepath.Join(dir, f.key(t)))
}

// ready decides whether the entry at dir is a usable t. dir must be under a
// kind directory trustedKindDir accepted. The entry, its manifest and its
// binary must pass checkOwned (this user's, writable by no one else, no
// symlink); the manifest must name t at its version on this platform, with
// the pinned archive sha256 for a release; and the binary must be a 0555-style
// executable (no write bit at all on a Unix host) whose sha256 is the
// compiled-in executable pin for a release (the manifest's record only for a
// go install build). See the package doc for why that is enough.
func (f *Fetcher) ready(t Tool, dir string) (Installed, bool) {
	if !f.ownedEntry(dir, true) {
		return Installed{}, false
	}
	manifest := filepath.Join(dir, manifestName)
	if !f.ownedEntry(manifest, false) {
		return Installed{}, false
	}
	data, err := os.ReadFile(manifest) // #nosec G304 -- forge's own cache entry, ownership checked
	if err != nil {
		return Installed{}, false
	}
	var in Installed
	if json.Unmarshal(data, &in) != nil || in.Name != t.Name || in.Version != t.Version || in.Platform != f.platform() {
		return Installed{}, false
	}
	switch in.Source {
	case SourceRelease:
		if _, sum, ok := t.checksum(f.goos(), f.goarch()); !ok || in.ArchiveSHA256 != sum {
			return Installed{}, false
		}
	case SourceGoInstall:
		if _, _, ok := t.checksum(f.goos(), f.goarch()); ok || in.GoPackage != t.GoPackage+"@"+t.Version || t.GoPackage == "" {
			return Installed{}, false
		}
	default:
		return Installed{}, false
	}
	bin := filepath.Join(dir, t.Binary(f.goos()))
	if !f.ownedEntry(bin, false) {
		return Installed{}, false
	}
	if info, err := os.Lstat(bin); err != nil || (f.enforcesModes() && (info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o222 != 0)) {
		return Installed{}, false
	}
	want := in.BinarySHA256
	if in.Source == SourceRelease {
		// Never the manifest's word: whoever could write the binary could
		// write the manifest beside it.
		pinned, ok := t.binaryChecksum(f.goos(), f.goarch())
		if !ok {
			return Installed{}, false
		}
		want = pinned
	}
	if got, err := hashFile(bin); err != nil || want == "" || got != want {
		return Installed{}, false
	}
	in.Path = bin
	return in, true
}

// ownedEntry reports whether path is a directory (wantDir) or a regular file
// that passes checkOwned.
func (f *Fetcher) ownedEntry(path string, wantDir bool) bool {
	info, err := os.Lstat(path)
	if err != nil || f.checkOwned(path, info) != nil {
		return false
	}
	if wantDir {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- a validated cache entry path
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, maxArchiveBytes+1))
	if err != nil || n > maxArchiveBytes {
		return "", fmt.Errorf("cannot hash cache file %s: size %d: %v", path, n, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validate rejects unsafe path and URL components before any cache access.
func (f *Fetcher) validate(t Tool) error {
	for _, value := range []string{t.Name, t.Version, f.goos(), f.goarch(), t.Asset(f.goos(), f.goarch())} {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\:?#%<>|"*`+"\x00\r\n\t") || strings.TrimSpace(value) != value || strings.HasSuffix(value, ".") {
			return fmt.Errorf("toolfetch: unsafe path component %q", value)
		}
	}
	for asset, checksum := range t.Checksums {
		if asset == "." || asset == ".." || asset == "" || strings.ContainsAny(asset, `/\\:?#%`+"\x00") || strings.TrimSpace(asset) != asset || strings.HasSuffix(asset, ".") {
			return fmt.Errorf("toolfetch: unsafe asset %q", asset)
		}
		decoded, err := hex.DecodeString(checksum)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("toolfetch: invalid checksum for %q", asset)
		}
		// A release with no compiled-in executable pin could only be
		// vouched for by the cache itself.
		decoded, err = hex.DecodeString(t.BinaryChecksums[asset])
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("toolfetch: no valid executable checksum for %q", asset)
		}
	}
	for asset := range t.BinaryChecksums {
		if _, ok := t.Checksums[asset]; !ok {
			return fmt.Errorf("toolfetch: executable checksum for %q, which has no archive checksum", asset)
		}
	}
	return nil
}

// Ensure returns a usable t, installing it when its entry is missing or
// unusable. See the package doc for the error contract.
func (f *Fetcher) Ensure(ctx context.Context, t Tool) (Installed, error) {
	if err := f.validate(t); err != nil {
		return Installed{}, err
	}
	kindDir, err := f.cacheDir()
	if err != nil {
		return Installed{}, fmt.Errorf("%s %s: %w", t.Name, t.Version, err)
	}
	if err := os.MkdirAll(kindDir, 0o755); err != nil {
		return Installed{}, fmt.Errorf("%s %s: %w", t.Name, t.Version, err)
	}
	if kindDir, err = f.trustedKindDir(kindDir); err != nil {
		return Installed{}, fmt.Errorf("%s %s: %w", t.Name, t.Version, err)
	}
	if in, ok := f.ready(t, filepath.Join(kindDir, f.key(t))); ok {
		return in, nil
	}

	var build func(ctx context.Context, stage string) error
	switch f.Method(t) {
	case MethodRelease:
		build = func(ctx context.Context, stage string) error { return f.installRelease(ctx, t, stage) }
	case MethodGoInstall:
		build = func(ctx context.Context, stage string) error { return f.installGo(ctx, t, stage) }
	default:
		return Installed{}, fmt.Errorf(
			"%s %s: no published binary for %s that forge has a checksum for, and no Go toolchain on PATH to build %s@%s with: %w\n"+
				"  anything that needs %s reports \"not scanned\", never clean — install Go, or run forge on a platform %s publishes",
			t.Name, t.Version, f.platform(), t.GoPackage, t.Version, ErrUnavailable, t.Name, t.Name)
	}

	readyFn := func(dir string) bool { _, ok := f.ready(t, dir); return ok }
	dir, err := toolcache.Ensure(ctx, kindDir, f.key(t), readyFn, build)
	if err != nil {
		return Installed{}, err
	}
	in, ok := f.ready(t, dir)
	if !ok {
		return Installed{}, fmt.Errorf("%s %s: the installed entry %s is not usable", t.Name, t.Version, dir)
	}
	return in, nil
}

// installRelease downloads t's archive into stage, verifies it against the
// pinned sha256, extracts the binary and writes the manifest.
func (f *Fetcher) installRelease(ctx context.Context, t Tool, stage string) error {
	asset, want, _ := t.checksum(f.goos(), f.goarch())
	url := f.URL(t)
	fmt.Fprintf(f.log(), "fetching %s %s for %s (%s, verified against forge's pinned sha256)\n", t.Name, t.Version, f.platform(), url)

	archive := filepath.Join(stage, asset)
	got, err := f.download(ctx, url, archive)
	if err != nil {
		return fmt.Errorf("%s %s: %w", t.Name, t.Version, err)
	}
	if got != want {
		return fmt.Errorf("%s %s: %w for %s\n  expected %s\n  got      %s\n"+
			"  the download was corrupted or substituted; forge will not run an unverified binary",
			t.Name, t.Version, ErrChecksum, asset, want, got)
	}
	bin := filepath.Join(stage, t.Binary(f.goos()))
	if err := extractMember(archive, asset, t.Binary(f.goos()), bin, f.executableMode()); err != nil {
		return fmt.Errorf("%s %s: %w", t.Name, t.Version, err)
	}
	binSum, err := hashFile(bin)
	if err != nil {
		return err
	}
	if wantBin, _ := t.binaryChecksum(f.goos(), f.goarch()); binSum != wantBin {
		return fmt.Errorf("%s %s: %w for the %s executable in %s\n  expected %s\n  got      %s\n"+
			"  the archive matched its pin but its executable did not: forge's two pins disagree",
			t.Name, t.Version, ErrChecksum, t.Binary(f.goos()), asset, wantBin, binSum)
	}
	if err := os.Remove(archive); err != nil {
		return err
	}
	return writeManifest(stage, Installed{
		Name: t.Name, Version: t.Version, Source: SourceRelease, Platform: f.platform(),
		URL: url, ArchiveSHA256: want, BinarySHA256: binSum,
	})
}

// download writes url's body to dest and returns its sha256.
func (f *Fetcher) download(ctx context.Context, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request for %s: %w", url, err)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if f.baseURL() != DefaultBaseURL {
			hint = fmt.Sprintf(" (from the mirror %s=%s)", EnvMirror, f.baseURL())
		}
		return "", fmt.Errorf("download %s: HTTP %d%s", url, resp.StatusCode, hint)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a staging path forge chose
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxArchiveBytes {
		return "", fmt.Errorf("download %s: larger than %d bytes, which no pinned archive is", url, maxArchiveBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// installGo builds t with `go install` into stage.
func (f *Fetcher) installGo(ctx context.Context, t Tool, stage string) error {
	spec := t.GoPackage + "@" + t.Version
	fmt.Fprintf(f.log(), "building %s %s for %s with go install %s (no published binary for this platform)\n", t.Name, t.Version, f.platform(), spec)
	install := f.GoInstall
	if install == nil {
		install = goInstall
	}
	if err := install(ctx, spec, stage); err != nil {
		return fmt.Errorf("%s %s: go install %s: %w", t.Name, t.Version, spec, err)
	}
	bin := filepath.Join(stage, t.Binary(f.goos()))
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("%s %s: go install %s produced no %s: %w", t.Name, t.Version, spec, t.Binary(f.goos()), err)
	}
	if err := os.Chmod(bin, f.executableMode()); err != nil {
		return err
	}
	binSum, err := hashFile(bin)
	if err != nil {
		return err
	}
	return writeManifest(stage, Installed{
		Name: t.Name, Version: t.Version, Source: SourceGoInstall, Platform: f.platform(), GoPackage: spec,
		BinarySHA256: binSum,
	})
}

// goInstall runs the real `go install spec` into gobin, outside any module
// (spec@version ignores the current one anyway) and with cgo off, as the
// upstream release builds are.
func goInstall(ctx context.Context, spec, gobin string) error {
	cmd := exec.CommandContext(ctx, "go", "install", spec) // #nosec G204 -- a pinned package path and version
	cmd.Dir = gobin
	cmd.Env = goexec.Env("GOBIN="+gobin, "GOWORK=off", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeManifest(stage string, in Installed) error {
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(stage, manifestName), append(data, '\n'), 0o644) // #nosec G306 -- a shared cache may serve several users
}
