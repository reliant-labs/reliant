package hostinfra

// Zitadel as a HOST PROCESS — the dev identity provider, run the same way
// postgres is, with no container runtime involved.
//
// # Why this exists
//
// The dev IdP was the last container in a scaffolded dev stack. Postgres
// went host-native first (see hostinfra.go), which left a project WITH a
// frontend needing docker for exactly one thing while a project without one
// needed none at all — a cliff, not a gradient, and it fell on precisely the
// workflow that wants a real browser sign-in.
//
// The reason given for keeping it containerized was that Zitadel is a heavy
// server with a schema and a bootstrap of its own. The bootstrap part is
// true and is preserved verbatim below; the heavy part was wrong about the
// thing that actually matters here. Zitadel is a single static Go binary —
// the same language forge is written in — published for darwin/linux/windows
// on amd64 and arm64. Supervising it is the same problem this package
// already solved for postgres: fetch a pinned archive once, cache it, verify
// it, launch it with explicit directories, wait until it is genuinely
// serving, and stop it by a pid we recorded.
//
// # What is preserved exactly
//
// The DECLARATIVE BOOTSTRAP is untouched. `idp-steps.yaml` still describes
// the instance, the org, the admin human and the service account, still
// arrives via `--steps`, and Zitadel still writes the service-account PAT to
// the path ZITADEL_FIRSTINSTANCE_PATPATH names — which is the file the
// identity step of `forge env up` reads. Only the EXECUTION changed from container to host
// process; every declaration, every env var and every file path is the same
// one the compose service used.
//
// # The database
//
// Zitadel needs postgres, and the host-native postgres this package already
// supervises is right there. It gets its OWN database on that server (the
// compose path gave it a whole second container for the same isolation),
// created on demand by ensureNamedDatabase. Two servers become one, and the
// IdP's schema still cannot collide with the app's.
//
// # What is not obvious, and cost time to learn
//
//   - `zitadel -v` is the version probe. There is no `version` SUBCOMMAND —
//     `zitadel version` errors with "unknown command" — so a naive probe
//     reports a broken download for a perfectly good binary.
//   - The archive extracts to a DIRECTORY (`zitadel-<os>-<arch>/zitadel`),
//     not a bare file, so extraction flattens it to a known path rather than
//     trusting the archive's shape at every later use.
//   - Zitadel resolves WHICH instance a request is for from the Host header,
//     matched against the instance's ExternalDomain — not from the socket.
//     That is why the port is deterministic (allocate_port) and why
//     everything, including this process's own readiness probe, reaches it at
//     the same localhost origin the browser uses.
//   - Readiness is NOT "the port accepts connections". On first boot the
//     declarative setup (schema, instance, org, admin user, PAT) runs before
//     it serves, and registering against a listening-but-unconverged IdP
//     fails. `/debug/ready` is Zitadel's own unauthenticated readiness route
//     and is what the compose healthcheck used; it is what this waits on too.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
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

// EngineZitadel is the second engine forge supervises natively. It matches
// the `engine` discriminator on the KCL HostInfra schema.
const EngineZitadel = "zitadel"

// ZitadelVersion is the pinned Zitadel release.
//
// KEEP THIS IN SYNC with the two other places the same version is pinned,
// or a dev stack and a deployed one disagree about what an IdP is:
//
//   - internal/templates/project/docker-compose.yml.tmpl — the `idp`
//     service's image tag, which is the containerized escape hatch for a
//     project that opts back out of the host path.
//   - control-plane's deploy/kcl/lib/infra.k — the in-cluster Zitadel
//     Deployment.
//
// Pinned MINOR.PATCH, never `latest`: an IdP that silently changes version
// under a dev stack turns "login broke today" into an archaeology exercise.
const ZitadelVersion = "v4.19.5"

// zitadelChecksums is the sha256 of each published release archive for
// ZitadelVersion, copied from the release's own checksums.txt:
//
//	https://github.com/zitadel/zitadel/releases/download/v4.19.5/checksums.txt
//
// Verifying against a table pinned HERE rather than against the checksums
// file fetched alongside the archive is the whole point: a checksum
// downloaded from the same place as the artifact it certifies proves only
// that the two arrived from the same host. This table is in git, reviewed
// with the version bump, and a mismatch fails LOUDLY at download time —
// rather than at first sign-in, where "the IdP will not start" names
// nothing about a truncated fetch.
//
// Keyed by the archive's own asset name, so adding a platform is adding a
// line.
var zitadelChecksums = map[string]string{
	"zitadel-darwin-amd64.tar.gz": "a30f7bb010ccf064d414e3ea98d3a2e5684d5171cd24cb545cba911871b6ea0a",
	"zitadel-darwin-arm64.tar.gz": "e9de33e9278eed5d16cacc47c3ef5906d6e0bcaf2888b04a257ae0e6dc77f297",
	"zitadel-linux-amd64.tar.gz":  "6320c9783a807633f98d35d88c0e60e1a783daf7679aca09269da0aba083cd5d",
	"zitadel-linux-arm64.tar.gz":  "4a0c6429bed5a1c2256856710c4b68a75b571608ee69aaee2353ec278d1a48ed",

	"zitadel-windows-amd64.tar.gz": "d0a50c1279662c14372fe075bf155004f4c9c841c046081e7489cf13988f7c1e",
	"zitadel-windows-arm64.tar.gz": "d61acd4626ae0b11914d85955455c474252655995fbe047d0491f5813f81e524",
}

// zitadelAsset names the release archive for a GOOS/GOARCH pair, and
// reports whether this platform is published at all.
//
// Zitadel's asset names happen to use Go's own GOOS/GOARCH spelling, which
// is why this is a formatting function rather than a translation table —
// but it is a function so the ONE place that knows the naming scheme is
// also the one place a future rename is fixed.
func zitadelAsset(goos, goarch string) (string, bool) {
	name := fmt.Sprintf("zitadel-%s-%s.tar.gz", goos, goarch)
	if _, ok := zitadelChecksums[name]; !ok {
		return "", false
	}
	return name, true
}

// zitadelDownloadURL is where the pinned archive is published.
func zitadelDownloadURL(asset string) string {
	return fmt.Sprintf("https://github.com/zitadel/zitadel/releases/download/%s/%s", ZitadelVersion, asset)
}

// ZitadelBinaryPath is where forge caches the extracted zitadel binary,
// shared by every project on this machine.
//
// The layout is deliberately parallel to the postgres cache
// (<UserCacheDir>/forge/embedded-postgres): one forge-owned directory per
// third-party server, VERSIONED, so two projects on different pins coexist
// and a bump never has to invalidate anything by hand.
//
//	<UserCacheDir>/forge/zitadel/v4.19.5/<goos>-<goarch>/zitadel
//
// This path is a CONTRACT, not an implementation detail: an image that
// pre-caches the binary (control-plane's workspace image does) writes to
// exactly this path so a container start does not re-download 50 MB. Moving
// it silently would not break correctness — forge would just download again
// — but it would quietly undo that optimisation, so treat it as public.
//
// Returns "" when the user cache dir cannot be determined, which the caller
// reports rather than guessing at a fallback location.
func ZitadelBinaryPath() string {
	return zitadelBinaryPathFor(runtime.GOOS, runtime.GOARCH)
}

// zitadelBinaryName is the file name of the zitadel executable on goos —
// Windows only runs files named *.exe, and the release archive ships it so.
func zitadelBinaryName(goos string) string {
	if goos == "windows" {
		return "zitadel.exe"
	}
	return "zitadel"
}

// zitadelBinaryPathFor is ZitadelBinaryPath with the platform injectable.
func zitadelBinaryPathFor(goos, goarch string) string {
	c, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(c, "forge", "zitadel", ZitadelVersion,
		goos+"-"+goarch, zitadelBinaryName(goos))
}

// startZitadel brings the IdP up, or confirms it is already up, and returns
// only once it is READY — converged and serving, not merely listening.
//
// The already-up / foreign-holder distinction mirrors the postgres path's,
// and for the same reason: adopting a server this project did not start
// would register this project's application against another stack's
// identity provider, silently and successfully.
func startZitadel(ctx context.Context, projectDir string, spec Spec) error {
	dataDir := spec.dataDir(projectDir)

	switch identifyZitadelHolder(ctx, spec, dataDir) {
	case holderOurs:
		fmt.Printf("  %s: already ready on :%d (host process)\n", spec.Name, spec.Port)
		return nil
	case holderForeign:
		return fmt.Errorf(
			"host-infra %s: port %d is held by something this project did not start\n"+
				"  forge will not adopt it: registering this project's application against another\n"+
				"  stack's identity provider would succeed, and mint tokens the wrong issuer signed.\n"+
				"  Fix — free the port, or move this one: the port is declared in deploy/kcl/<env>/main.k",
			spec.Name, spec.Port)
	}

	// A recorded pid that is alive but NOT ready yet means a previous run
	// left one mid-boot; a recorded pid that is dead is a crash to clean up
	// after. Either way the pidfile is the handle, so reap before starting.
	reapDeadZitadel(dataDir)

	bin, err := ensureZitadelBinary(ctx)
	if err != nil {
		return fmt.Errorf("host-infra %s: %w", spec.Name, err)
	}

	// Zitadel's own database on the app's host-native postgres server. The
	// compose path spent a whole second container on this isolation; a
	// separate DATABASE on one server buys the same thing.
	if err := ensureNamedDatabase(spec.zitadelDSNBase(), spec.IDPDatabase); err != nil {
		return fmt.Errorf("host-infra %s: prepare the IdP's database: %w\n"+
			"  the dev IdP stores its state in postgres, which this environment declares as its own\n"+
			"  host-infra instance — so a postgres that did not come up takes the IdP with it",
			spec.Name, err)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("host-infra %s: create data dir: %w", spec.Name, err)
	}
	// The PAT the declarative boot writes, which the idp-provision job then
	// reads. Zitadel does not create the parent directory.
	patPath := spec.PATPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(patPath), 0o755); err != nil {
		return fmt.Errorf("host-infra %s: create PAT dir: %w", spec.Name, err)
	}

	stepsPath := spec.idpStepsPath(projectDir)
	if _, err := os.Stat(stepsPath); err != nil {
		return fmt.Errorf(
			"host-infra %s: the declarative bootstrap %s is missing: %w\n"+
				"  this file IS the IdP's setup (instance, org, admin user, service account); without it\n"+
				"  Zitadel boots an instance with no way to sign in and no PAT for the provisioning job.\n"+
				"  It is scaffolded with the project — restore it from git, or re-run `forge scaffold`",
			spec.Name, shortPath(projectDir, stepsPath), err)
	}

	logPath := filepath.Join(dataDir, "zitadel.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return fmt.Errorf("host-infra %s: open log: %w", spec.Name, err)
	}
	defer func() { _ = logFile.Close() }()

	// `start-from-init` is the cold start: set up the schema, run the
	// declarative steps, then serve. It is idempotent across restarts —
	// an instance that already exists is left alone — which is what lets
	// this be the only start path rather than a first-boot special case.
	//
	// The flags are the same ones the compose service passed as `command:`;
	// everything else is env, below, exactly as the container had it.
	args := []string{
		"start-from-init",
		"--masterkey", spec.IDPMasterKey,
		"--tlsMode", "disabled",
		"--steps", stepsPath,
	}
	cmd := exec.Command(bin, args...) // #nosec G204 -- binary is forge's own verified cache entry; args derive from the project's declaration
	cmd.Dir = projectDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), spec.zitadelEnv(patPath)...)
	// Its OWN session, so it OUTLIVES the `forge env up` that started it — the
	// same lifecycle postgres has (pg_ctl daemonizes to get there). Without
	// this it would die with the launching shell's process group, and the
	// dev stack would not survive the command returning.
	//
	// The attribute that achieves that is per-platform (setsid on unix, a
	// detached process group on Windows); see proc_unix.go / proc_windows.go.
	detachProcess(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("host-infra %s: start zitadel: %w", spec.Name, err)
	}
	pid := cmd.Process.Pid
	if err := writeZitadelPID(dataDir, pid, spec.Port); err != nil {
		// Started but unrecordable: stop it rather than leaking a server
		// no later `forge env down` could find.
		_ = cmd.Process.Kill()
		return fmt.Errorf("host-infra %s: record pid: %w", spec.Name, err)
	}
	// Release the handle without waiting: this process is not the
	// supervisor, the pidfile is.
	_ = cmd.Process.Release()

	fmt.Printf("  %s: zitadel %s starting on :%d (host process, data %s)\n",
		spec.Name, ZitadelVersion, spec.Port, shortPath(projectDir, dataDir))

	if err := waitZitadelReady(ctx, spec, pid, zitadelReadyTimeout); err != nil {
		return fmt.Errorf("host-infra %s: %w\n  logs: %s", spec.Name, err, shortPath(projectDir, logPath))
	}
	fmt.Printf("  %s: ready on :%d (pid %d)\n", spec.Name, spec.Port, pid)
	return nil
}

// zitadelReadyTimeout bounds the FIRST boot, which runs the whole
// declarative setup — schema creation, instance, org, admin user, PAT —
// before it serves. Later boots take a few seconds. Generous for the same
// reason the postgres start timeout is: the slow case is real work, not a
// hang, and failing early on it would make the first run of a fresh
// project look broken.
const zitadelReadyTimeout = 3 * time.Minute

// waitZitadelReady blocks until the instance reports READY, the process
// dies, or the deadline passes.
//
// Readiness is Zitadel's own /debug/ready, not a TCP dial: on first boot it
// listens well before the declarative setup has converged, and an
// application registered against a half-initialized instance fails in ways
// that name neither the instance nor the setup.
//
// The LIVENESS check is what turns the common failure into a useful
// message. A Zitadel that exits during setup (an unreachable database, a
// bad masterkey) would otherwise be reported as a timeout three minutes
// later — the least informative version of the truth. Noticing the process
// is gone reports the actual event immediately.
func waitZitadelReady(ctx context.Context, spec Spec, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/debug/ready", spec.Port)
	for {
		if !processIsAlive(pid) {
			return fmt.Errorf("zitadel exited during startup (pid %d)", pid)
		}
		if zitadelReady(ctx, client, url) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("zitadel did not report ready on :%d within %s", spec.Port, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// zitadelReady performs one readiness probe. Any error, and any non-200,
// reads as "not yet".
func zitadelReady(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// stopZitadel shuts the instance down and reports whether one was running.
//
// SIGTERM, then SIGKILL after a grace period. Zitadel keeps its state in
// postgres and holds no IPC resources of its own, so an ungraceful stop
// costs nothing the way a SIGKILLed postgres does — but it is still given
// the chance to close its database connections rather than leaving the
// server to time them out.
func stopZitadel(projectDir string, spec Spec) (bool, error) {
	dataDir := spec.dataDir(projectDir)
	pid, _, ok := readZitadelPID(dataDir)
	if !ok || !processIsAlive(pid) {
		removeZitadelPID(dataDir)
		return false, nil
	}
	// The pid may have been recycled since the pidfile was written. Only kill
	// a process that is provably our zitadel; a recycled pid means ours is
	// already gone (clear the stale pidfile), unreadable means touch nothing.
	switch verifyRecordedPid(pid, filepath.Join(dataDir, zitadelPIDFile), ZitadelBinaryPath()) {
	case verdictForeign:
		removeZitadelPID(dataDir)
		return false, nil
	case verdictUnknown:
		return false, fmt.Errorf("host-infra %s: zitadel.pid names pid %d but its identity could not be verified; not killing it", spec.Name, pid)
	case verdictOurs:
	}
	if err := terminateProcess(pid); err != nil {
		if isProcessGone(err) {
			removeZitadelPID(dataDir)
			return false, nil
		}
		return false, fmt.Errorf("host-infra %s: stop zitadel (pid %d): %w", spec.Name, pid, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processIsAlive(pid) {
			removeZitadelPID(dataDir)
			return true, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Would not go quietly. The state that matters is in postgres and is
	// already durable, so this is safe — and leaving it running would hold
	// the port against the next `forge env up`.
	if verifyRecordedPid(pid, filepath.Join(dataDir, zitadelPIDFile), ZitadelBinaryPath()) == verdictOurs {
		_ = killProcess(pid)
	}
	removeZitadelPID(dataDir)
	return true, nil
}

// identifyZitadelHolder classifies what, if anything, holds the port.
//
// "Ours" is decided by our OWN pidfile plus a liveness probe, not by the
// response on the port: every Zitadel answers /debug/ready identically, so
// the response cannot distinguish this project's instance from another's.
// The pidfile is the only thing that knows which process this project
// started.
func identifyZitadelHolder(ctx context.Context, spec Spec, dataDir string) holder {
	pid, port, recorded := readZitadelPID(dataDir)
	if recorded && processIsAlive(pid) && port == spec.Port {
		client := &http.Client{Timeout: 2 * time.Second}
		if zitadelReady(ctx, client, fmt.Sprintf("http://localhost:%d/debug/ready", spec.Port)) {
			return holderOurs
		}
		// Ours, alive, but not ready — it is mid-boot from a previous
		// invocation. Treat the port as ours and let the caller's readiness
		// wait converge rather than starting a second one that cannot bind.
		return holderOurs
	}
	if !portListening(spec.Port) {
		return holderNone
	}
	return holderForeign
}

// zitadelPIDFile is where the running instance's pid and port are recorded.
// Named for what it is rather than reusing postgres's postmaster.pid
// convention, because nothing but forge writes or reads it.
const zitadelPIDFile = "zitadel.pid"

// writeZitadelPID records "<pid> <port>". The PORT is recorded alongside
// the pid for the same reason postgres writes its own into postmaster.pid:
// an instance running on a port the declaration has since moved off is
// still this instance, and reporting that clearly is the difference
// between a fixable message and a mystery.
func writeZitadelPID(dataDir string, pid, port int) error {
	return atomicfile.WriteFile(filepath.Join(dataDir, zitadelPIDFile),
		[]byte(fmt.Sprintf("%d %d\n", pid, port)), 0o644) // #nosec G306 -- a pid is not a secret
}

func readZitadelPID(dataDir string) (pid, port int, ok bool) {
	b, err := os.ReadFile(filepath.Join(dataDir, zitadelPIDFile)) // #nosec G304 -- path derived from the project's own data dir
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

func removeZitadelPID(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, zitadelPIDFile))
}

// reapDeadZitadel clears the pidfile of an instance that is no longer
// running, so a crashed predecessor does not make the next start look like
// an already-running one.
func reapDeadZitadel(dataDir string) {
	if pid, _, ok := readZitadelPID(dataDir); ok && !processIsAlive(pid) {
		removeZitadelPID(dataDir)
	}
}

// processIsAlive reports whether pid names a live process (per-OS probe).
func processIsAlive(pid int) bool { return processAlive(pid) }

// ensureZitadelBinary returns the path to a verified zitadel binary,
// downloading and extracting it on first use and reusing the cache after.
//
// The cached binary is re-verified by asking it its VERSION rather than by
// trusting its presence. A truncated extraction, a half-written file from
// an interrupted run, or a cache entry from a different pin all present as
// "the file is there", and all of them fail later — at first sign-in, where
// nothing points back here.
func ensureZitadelBinary(ctx context.Context) (string, error) {
	bin := ZitadelBinaryPath()
	if bin == "" {
		return "", fmt.Errorf("cannot determine the user cache directory to store the zitadel binary in")
	}
	if verifyZitadelBinary(bin) == nil {
		return bin, nil
	}

	asset, ok := zitadelAsset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return "", fmt.Errorf(
			"zitadel %s publishes no binary for %s/%s\n"+
				"  the dev IdP can still run as a container on this platform — in deploy/kcl/dev/main.k:\n"+
				"      deploy = forge.Compose {service = \"idp\", env = {\"IDP_PORT\": str(_idp_port)}}",
			ZitadelVersion, runtime.GOOS, runtime.GOARCH)
	}

	fmt.Printf("  fetching zitadel %s for %s/%s (~50 MB, cached at %s)\n",
		ZitadelVersion, runtime.GOOS, runtime.GOARCH, filepath.Dir(bin))
	if err := downloadZitadel(ctx, asset, bin); err != nil {
		return "", err
	}
	if err := verifyZitadelBinary(bin); err != nil {
		return "", fmt.Errorf("the downloaded zitadel binary did not verify: %w", err)
	}
	return bin, nil
}

// verifyZitadelBinary asserts the binary at path IS the pinned version, by
// running it.
//
// `zitadel -v` is the probe. There is NO `version` subcommand — `zitadel
// version` exits non-zero with "unknown command" — so a probe written the
// obvious way condemns a perfectly good binary.
func verifyZitadelBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !isExecutableFile(info) {
		return fmt.Errorf("%s is not executable", path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-v").CombinedOutput() // #nosec G204 -- forge's own cache entry
	if err != nil {
		return fmt.Errorf("run %s -v: %w", path, err)
	}
	if !strings.Contains(string(out), ZitadelVersion) {
		return fmt.Errorf("expected version %s, got %q", ZitadelVersion, strings.TrimSpace(string(out)))
	}
	return nil
}

// downloadZitadel fetches, checksum-verifies and extracts the release
// archive to dest.
//
// The checksum is verified over the WHOLE archive before anything is
// extracted, against the table pinned in this file. A mismatch is fatal and
// says so — the failure mode this prevents is a partial or substituted
// download surfacing much later as an IdP that will not start.
func downloadZitadel(ctx context.Context, asset, dest string) error {
	want := zitadelChecksums[asset]
	url := zitadelDownloadURL(asset)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", url, err)
	}
	// Generous: this is a ~50 MB download over whatever link the developer
	// has, and it happens once per version per machine.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	// Staged in a temp file next to the destination: the archive is
	// verified before it is extracted, and a failed download never leaves a
	// half-written binary at the path the next run will trust.
	tmp, err := os.CreateTemp(filepath.Dir(dest), "zitadel-download-*.tar.gz")
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
	return extractZitadel(tmpName, dest)
}

// extractZitadel pulls the single `zitadel` executable out of the release
// archive and writes it to dest.
//
// The archive contains a DIRECTORY (`zitadel-<os>-<arch>/{zitadel,LICENSE,
// README.md}`), so this selects the entry by base name and flattens it —
// which means nothing downstream has to know the archive's internal layout,
// and a future layout change breaks here, once, with a clear message.
func extractZitadel(archive, dest string) error {
	return extractZitadelFor(archive, dest, runtime.GOOS)
}

// extractZitadelFor is extractZitadel with the target platform injectable,
// because the archive member is named zitadel.exe on windows.
func extractZitadelFor(archive, dest, goos string) error {
	memberName := zitadelBinaryName(goos)
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
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != memberName {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), "zitadel-extract-*")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		tmpName := tmp.Name()
		// Bounded copy: a tar entry declares its own size, and an
		// unbounded io.Copy from an archive would let a malformed one
		// write until the disk fills. The size is the header's, and the
		// checksum above already certified the archive.
		if _, err := io.CopyN(tmp, tr, hdr.Size); err != nil && !errors.Is(err, io.EOF) {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return fmt.Errorf("extract zitadel: %w", err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("close extracted file: %w", err)
		}
		if err := os.Chmod(tmpName, 0o755); err != nil { // #nosec G302 -- an executable must be executable
			_ = os.Remove(tmpName)
			return fmt.Errorf("chmod extracted file: %w", err)
		}
		// Rename last: the destination path only ever exists complete and
		// executable, so a concurrent forge run either finds no binary or
		// finds a whole one — never a partial file it would then try to run.
		if err := os.Rename(tmpName, dest); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("install zitadel binary: %w", err)
		}
		return nil
	}
	return fmt.Errorf("archive %s contained no `%s` executable", archive, memberName)
}

// zitadelEnv is the process environment for the IdP — the SAME variables
// the compose service set, with the same values, so the container path and
// the host path configure one server identically.
//
// The comments that explain WHY each of these is set live in
// docker-compose.yml.tmpl's `idp` service, which remains the escape hatch;
// duplicating them here would create two copies to keep in sync. What is
// worth repeating: EXTERNALDOMAIN/EXTERNALPORT are the address the BROWSER
// uses and what Zitadel mints tokens under, LOGINV2_REQUIRED=false selects
// the built-in sign-in pages (the v2 UI ships as a separate service and its
// absence dead-ends the redirect flow), and the two login-policy zeros
// suppress the passkey/2FA enrollment interstitials that a local instance
// has no way to complete.
func (s Spec) zitadelEnv(patPath string) []string {
	return []string{
		"ZITADEL_DATABASE_POSTGRES_HOST=localhost",
		fmt.Sprintf("ZITADEL_DATABASE_POSTGRES_PORT=%d", s.IDPDatabasePort),
		"ZITADEL_DATABASE_POSTGRES_DATABASE=" + s.IDPDatabase,
		"ZITADEL_DATABASE_POSTGRES_USER_USERNAME=" + s.User,
		"ZITADEL_DATABASE_POSTGRES_USER_PASSWORD=" + s.Password,
		"ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE=disable",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME=" + s.User,
		"ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD=" + s.Password,
		"ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE=disable",
		"ZITADEL_EXTERNALSECURE=false",
		"ZITADEL_TLS_ENABLED=false",
		"ZITADEL_EXTERNALDOMAIN=localhost",
		fmt.Sprintf("ZITADEL_EXTERNALPORT=%d", s.Port),
		fmt.Sprintf("ZITADEL_PORT=%d", s.Port),
		"ZITADEL_FIRSTINSTANCE_PATPATH=" + patPath,
		"ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED=false",
		"ZITADEL_DEFAULTINSTANCE_LOGINPOLICY_PASSWORDLESSTYPE=0",
		"ZITADEL_DEFAULTINSTANCE_LOGINPOLICY_MFAINITSKIPLIFETIME=0s",
	}
}

// zitadelDSNBase is the MAINTENANCE connection to the postgres server the
// IdP's database lives on — the always-present `postgres` database, which
// is what creating another database requires being connected to.
func (s Spec) zitadelDSNBase() string {
	return fmt.Sprintf("postgres://%s:%s@localhost:%d/postgres?sslmode=disable&connect_timeout=%d",
		s.User, s.Password, s.IDPDatabasePort, probeConnectTimeoutSeconds)
}

// idpStepsPath resolves the declarative bootstrap file against the project
// root.
func (s Spec) idpStepsPath(projectDir string) string {
	p := s.IDPStepsFile
	if p == "" {
		p = "idp-steps.yaml"
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectDir, p)
}

// PATPath resolves the service-account token path against the project
// root. It must match what the identity step of `forge env up` reads (its --pat-file
// default, .forge/idp/pat.txt) — the two are the same declaration.
func (s Spec) PATPath(projectDir string) string {
	p := s.IDPPATPath
	if p == "" {
		p = filepath.Join(".forge", "idp", "pat.txt")
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectDir, p)
}

// ensureNamedDatabase creates a database on an already-running server when
// it does not exist yet.
//
// Separate from ensureDatabase (which serves the postgres engine's own
// spec-declared database) because this one is told BOTH the server to dial
// and the database to make: the IdP's database lives on the APP's postgres
// instance, so the connection and the name come from two different
// declarations.
func ensureNamedDatabase(baseDSN, name string) error {
	if name == "" {
		return fmt.Errorf("no database name declared")
	}
	db, err := sql.Open("postgres", baseDSN)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer func() { _ = db.Close() }()

	var pingErr error
	for i := 0; i < 40; i++ {
		if pingErr = db.Ping(); pingErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if pingErr != nil {
		return fmt.Errorf("connect to postgres: %w", pingErr)
	}
	var exists bool
	if err := db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check database %q: %w", name, err)
	}
	if exists {
		return nil
	}
	if _, err := db.Exec("CREATE DATABASE " + quoteIdent(name)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("create database %q: %w", name, err)
	}
	return nil
}
