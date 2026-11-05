package hostinfra

// The SUPERVISED-BINARY engines: a third-party server that ships as one
// static executable in a release archive, which forge fetches once, verifies
// against a checksum pinned in this repo, launches detached, waits on until
// it is genuinely serving, and stops by the pid it recorded.
//
// # Why this is its own layer
//
// Zitadel was the first server shaped like this (zitadel.go), and NATS and
// Temporal's dev server are the same problem with different flags: a pinned
// archive, a binary inside it, an argv, a readiness question and a pidfile.
// Each one got its own copy of the download, the extraction, the pidfile and
// the stop path, the cost of a new dependency would be another 500 lines that
// drift from the last 500. So the mechanics live here once, and an engine is a
// descriptor: its release (what to fetch and how to tell it is the pinned
// version), its argv, and its readiness probe. nats.go and temporal.go are
// each little more than that descriptor.
//
// Zitadel predates this file and still carries its own copy of the same
// mechanics; folding it onto supervisedEngine is mechanical and left for its
// own change so this one does not move a working IdP.
//
// # What is deliberately the same as zitadel.go
//
//   - The cache path is versioned per engine and platform
//     (<UserCacheDir>/forge/<engine>/<version>/<goos>-<goarch>/<binary>), so
//     two projects on different pins coexist and a bump invalidates nothing.
//   - A cached binary is re-verified by RUNNING it and matching its version
//     output, not by its presence: a truncated extraction looks present.
//   - The archive's checksum is verified, against the table in git, before
//     anything is extracted; the binary is installed by rename, so a
//     concurrent run sees no binary or a whole one.
//   - "Ours" is decided by our own pidfile and the recorded process's
//     identity, never by what answers on the port: every NATS answers INFO the
//     same way, so the response cannot tell this project's server from
//     another's.
//   - The process gets its own session, so it outlives `forge env up`, and the
//     data directory persists until someone deletes it.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// binaryRelease is a pinned upstream release of one server binary.
type binaryRelease struct {
	// engine names the cache directory and appears in every message.
	engine string
	// version is the pinned release, as it appears in the version output.
	version string
	// checksums maps a release asset's file name to its sha256, copied from
	// the release's own checksum file. An asset absent here is a platform
	// forge does not run this engine on.
	checksums map[string]string
	// asset names the release archive for a GOOS/GOARCH pair.
	asset func(goos, goarch string) string
	// url is where an asset is published.
	url func(asset string) string
	// binary is the executable's file name inside the archive on goos.
	binary func(goos string) string
	// versionArgs is the argv that makes the binary print its version, and
	// versionNeedle what that output must contain.
	versionArgs   []string
	versionNeedle string
	// approxSize is printed with the one-time download notice.
	approxSize string
}

// assetFor reports the asset for goos/goarch, and whether it is published.
func (r binaryRelease) assetFor(goos, goarch string) (string, bool) {
	name := r.asset(goos, goarch)
	_, ok := r.checksums[name]
	return name, ok
}

// binaryPathFor is where the extracted binary is cached. "" when the user
// cache directory cannot be determined.
func (r binaryRelease) binaryPathFor(goos, goarch string) string {
	c, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(c, "forge", r.engine, r.version, goos+"-"+goarch, r.binary(goos))
}

// binaryPath is binaryPathFor this platform.
func (r binaryRelease) binaryPath() string {
	return r.binaryPathFor(runtime.GOOS, runtime.GOARCH)
}

// ensure returns the path of a verified binary, downloading it on first use.
func (r binaryRelease) ensure(ctx context.Context) (string, error) {
	bin := r.binaryPath()
	if bin == "" {
		return "", fmt.Errorf("cannot determine the user cache directory to store the %s binary in", r.engine)
	}
	if r.verify(bin) == nil {
		return bin, nil
	}
	asset, ok := r.assetFor(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return "", fmt.Errorf("%s %s publishes no binary for %s/%s that forge has a checksum for",
			r.engine, r.version, runtime.GOOS, runtime.GOARCH)
	}
	fmt.Printf("  fetching %s %s for %s/%s (%s, cached at %s)\n",
		r.engine, r.version, runtime.GOOS, runtime.GOARCH, r.approxSize, filepath.Dir(bin))
	if err := r.download(ctx, asset, bin); err != nil {
		return "", err
	}
	if err := r.verify(bin); err != nil {
		return "", fmt.Errorf("the downloaded %s binary did not verify: %w", r.engine, err)
	}
	return bin, nil
}

// verify asserts the binary at p IS the pinned version, by running it.
func (r binaryRelease) verify(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !isExecutableFile(info) {
		return fmt.Errorf("%s is not executable", p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, r.versionArgs...).CombinedOutput() // #nosec G204 -- forge's own cache entry
	if err != nil {
		return fmt.Errorf("run %s %s: %w", p, strings.Join(r.versionArgs, " "), err)
	}
	if !strings.Contains(string(out), r.versionNeedle) {
		return fmt.Errorf("expected version %s, got %q", r.versionNeedle, strings.TrimSpace(string(out)))
	}
	return nil
}

// download fetches asset, verifies its checksum against the pinned table and
// extracts the binary to dest.
func (r binaryRelease) download(ctx context.Context, asset, dest string) error {
	want := r.checksums[asset]
	url := r.url(asset)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", url, err)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), r.engine+"-download-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf(
			"checksum mismatch for %s\n  expected %s\n  got      %s\n"+
				"  the download was corrupted or substituted; forge will not run an unverified binary",
			asset, want, got)
	}
	return extractMember(tmpName, asset, r.binary(runtime.GOOS), dest)
}

// extractMember pulls the regular file whose base name is member out of the
// archive (a .tar.gz or a .zip, decided by assetName) and installs it at dest,
// executable. The archive's directory layout is flattened: nothing downstream
// needs to know it, and a layout change fails here, once, by name.
func extractMember(archive, assetName, member, dest string) error {
	install := func(src io.Reader, size int64) error {
		tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+"-extract-*")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		tmpName := tmp.Name()
		// Bounded by the entry's declared size: an unbounded copy from an
		// archive lets a malformed entry write until the disk fills.
		if _, err := io.CopyN(tmp, src, size); err != nil && !errors.Is(err, io.EOF) {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return fmt.Errorf("extract %s: %w", member, err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("close extracted file: %w", err)
		}
		if err := os.Chmod(tmpName, 0o755); err != nil { // #nosec G302 -- an executable must be executable
			_ = os.Remove(tmpName)
			return fmt.Errorf("chmod extracted file: %w", err)
		}
		if err := os.Rename(tmpName, dest); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("install %s: %w", member, err)
		}
		return nil
	}

	if strings.HasSuffix(assetName, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		defer func() { _ = zr.Close() }()
		for _, f := range zr.File {
			if !f.Mode().IsRegular() || path.Base(f.Name) != member {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("read archive: %w", err)
			}
			err = install(rc, int64(f.UncompressedSize64)) // #nosec G115 -- a size from a checksum-verified archive
			_ = rc.Close()
			return err
		}
		return fmt.Errorf("archive %s contained no `%s` executable", assetName, member)
	}

	f, err := os.Open(archive) // #nosec G304 -- forge's own staged download
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != member {
			continue
		}
		return install(tr, hdr.Size)
	}
	return fmt.Errorf("archive %s contained no `%s` executable", assetName, member)
}

// supervisedEngine is one server forge runs from a binaryRelease.
type supervisedEngine struct {
	release binaryRelease
	// args is the argv after the binary, for this declaration.
	args func(spec Spec, dataDir string) []string
	// ready performs ONE readiness probe against a running instance.
	ready func(ctx context.Context, spec Spec, bin string) bool
	// readyTimeout bounds the wait for the first boot.
	readyTimeout time.Duration
}

func (e supervisedEngine) pidFile(dataDir string) string {
	return filepath.Join(dataDir, e.release.engine+".pid")
}

// start brings the instance up, or confirms it already is, returning only
// once it is ready. See Start for the contract.
func (e supervisedEngine) start(ctx context.Context, projectDir string, spec Spec) error {
	name := e.release.engine
	if spec.Version != "" && spec.Version != e.release.version {
		return fmt.Errorf("host-infra %s: declares %s version %q, but this forge pins %s %s (the version is checksum-pinned in forge, not chosen per project); remove `version`, or set it to %q",
			spec.Name, name, spec.Version, name, e.release.version, e.release.version)
	}
	dataDir := spec.dataDir(projectDir)

	pid, port, recorded := readPIDFile(e.pidFile(dataDir))
	if recorded && processIsAlive(pid) {
		if verifyRecordedPid(pid, e.pidFile(dataDir), e.release.binaryPath()) == verdictOurs {
			if port != spec.Port {
				return fmt.Errorf("host-infra %s: this project's %s is already running on :%d, but the environment now declares :%d\n"+
					"  stop it first (`forge env down`), then bring the stack up again", spec.Name, name, port, spec.Port)
			}
			bin := e.release.binaryPath()
			if err := e.waitReady(ctx, spec, bin, pid); err != nil {
				return fmt.Errorf("host-infra %s: %w", spec.Name, err)
			}
			fmt.Printf("  %s: already ready on :%d (host process)\n", spec.Name, spec.Port)
			return nil
		}
	}
	// Whatever the pidfile named is gone (or was never ours): a crashed
	// predecessor must not make this start look like an already-running one.
	_ = os.Remove(e.pidFile(dataDir))

	if portListening(spec.Port) {
		return fmt.Errorf(
			"host-infra %s: port %d is held by something this project did not start\n"+
				"  forge will not adopt it: this project's processes would talk to another stack's %s.\n"+
				"  Fix — free the port, or move this one: the port is declared in deploy/kcl/<env>/main.k",
			spec.Name, spec.Port, name)
	}

	bin, err := e.release.ensure(ctx)
	if err != nil {
		return fmt.Errorf("host-infra %s: %w", spec.Name, err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("host-infra %s: create data dir: %w", spec.Name, err)
	}
	logPath := filepath.Join(dataDir, name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return fmt.Errorf("host-infra %s: open log: %w", spec.Name, err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(bin, e.args(spec, dataDir)...) // #nosec G204 -- binary is forge's own verified cache entry; args derive from the project's declaration
	cmd.Dir = dataDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("host-infra %s: start %s: %w", spec.Name, name, err)
	}
	pid = cmd.Process.Pid
	if err := writePIDFile(e.pidFile(dataDir), pid, spec.Port); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("host-infra %s: record pid: %w", spec.Name, err)
	}
	_ = cmd.Process.Release()

	fmt.Printf("  %s: %s %s starting on :%d (host process, data %s)\n",
		spec.Name, name, e.release.version, spec.Port, shortPath(projectDir, dataDir))
	if err := e.waitReady(ctx, spec, bin, pid); err != nil {
		return fmt.Errorf("host-infra %s: %w\n  logs: %s", spec.Name, err, shortPath(projectDir, logPath))
	}
	fmt.Printf("  %s: ready on :%d (pid %d)\n", spec.Name, spec.Port, pid)
	return nil
}

// waitReady blocks until the instance is ready, its process dies, or the
// deadline passes. Noticing the process is gone reports the real event at
// once instead of a timeout minutes later.
func (e supervisedEngine) waitReady(ctx context.Context, spec Spec, bin string, pid int) error {
	deadline := time.Now().Add(e.readyTimeout)
	for {
		if !processIsAlive(pid) {
			return fmt.Errorf("%s exited during startup (pid %d)", e.release.engine, pid)
		}
		if e.ready(ctx, spec, bin) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not report ready on :%d within %s", e.release.engine, spec.Port, e.readyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// stop shuts the instance down and reports whether one was running. SIGTERM,
// then SIGKILL after a grace period; only a process provably ours is signalled.
func (e supervisedEngine) stop(projectDir string, spec Spec) (bool, error) {
	dataDir := spec.dataDir(projectDir)
	pidFile := e.pidFile(dataDir)
	pid, _, ok := readPIDFile(pidFile)
	if !ok || !processIsAlive(pid) {
		_ = os.Remove(pidFile)
		return false, nil
	}
	switch verifyRecordedPid(pid, pidFile, e.release.binaryPath()) {
	case verdictForeign:
		_ = os.Remove(pidFile)
		return false, nil
	case verdictUnknown:
		return false, fmt.Errorf("host-infra %s: %s names pid %d but its identity could not be verified; not killing it",
			spec.Name, filepath.Base(pidFile), pid)
	case verdictOurs:
	}
	if err := terminateProcess(pid); err != nil {
		if isProcessGone(err) {
			_ = os.Remove(pidFile)
			return false, nil
		}
		return false, fmt.Errorf("host-infra %s: stop %s (pid %d): %w", spec.Name, e.release.engine, pid, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !processIsAlive(pid) {
			_ = os.Remove(pidFile)
			return true, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if verifyRecordedPid(pid, pidFile, e.release.binaryPath()) == verdictOurs {
		_ = killProcess(pid)
	}
	_ = os.Remove(pidFile)
	return true, nil
}

// status reports the instance without changing anything.
func (e supervisedEngine) status(ctx context.Context, spec Spec, dataDir string) Status {
	st := Status{DataDir: dataDir, Version: e.release.version}
	pid, port, recorded := readPIDFile(e.pidFile(dataDir))
	if recorded && processIsAlive(pid) {
		st.Running = true
		st.Port = port
		probe := spec
		probe.Port = port
		st.Ready = e.ready(ctx, probe, e.release.binaryPath())
		return st
	}
	if portListening(spec.Port) {
		st.Foreign = true
	}
	return st
}

// writePIDFile records "<pid> <port>": the port beside the pid, so an
// instance on a port the declaration has since moved off is still found, and
// reported, as this one.
func writePIDFile(p string, pid, port int) error {
	return atomicfile.WriteFile(p, []byte(fmt.Sprintf("%d %d\n", pid, port)), 0o644) // #nosec G306 -- a pid is not a secret
}

func readPIDFile(p string) (pid, port int, ok bool) {
	b, err := os.ReadFile(p) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, 0, false
	}
	pid, err1 := strconv.Atoi(fields[0])
	port, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || pid <= 0 {
		return 0, 0, false
	}
	return pid, port, true
}
