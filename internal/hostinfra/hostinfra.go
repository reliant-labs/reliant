// Package hostinfra runs a project's declared dev infrastructure as HOST
// PROCESSES, with no container runtime involved.
//
// # Why this exists
//
// `forge env up` already runs the project's OWN code on the host — that is the
// whole point of the host dev loop. But the infrastructure underneath it
// (postgres, first and foremost) arrived as docker containers, so a dev loop
// that touched no container of its own still could not start without docker.
// On the small cloud VMs a lot of people actually develop on, that is a
// multi-hundred-megabyte resident cost paid before the project has done
// anything — and on some of them docker is not installable at all. A project
// whose code needs no container should need no container runtime.
//
// So the DEFAULT flipped: a `forge.HostInfra` declaration in an environment's
// KCL means forge fetches and supervises the server itself. `forge.Compose`
// did not go anywhere — a project that wants containerized infra says so, and
// gets exactly what it used to. The escape hatch stopped being the only door.
//
// # Postgres
//
// The postgres engine runs a REAL postgres binary, downloaded once and cached
// under the user cache dir, via github.com/fergusstrange/embedded-postgres.
// forge already depends on that library for generate-time schema
// introspection (pkg/pgtest), so the dev server and the shadow database used
// to project the ORM are the same postgres, behaving the same way. Migrations
// apply verbatim; there is no emulation layer to disagree with production.
//
// # What this package learned from pkg/pgtest, and where it deliberately differs
//
// pkg/pgtest boots EPHEMERAL servers for tests and generate runs: it hands out
// scratch databases, reference-counts users across processes, and tears the
// server down when the last one leaves. Its hard-won configuration —
// mmap-backed shared memory (System V shmget exhausts macOS's SHMMNI limit and
// then every initdb fails), a reused binary cache path, a start timeout
// generous enough for the first run's archive extraction, and reaping the
// instances that a SIGKILLed process left holding IPC resources — is exactly
// as necessary here, and is reproduced below rather than re-learned.
//
// Two things are the OPPOSITE of pgtest's, because a dev database is not a
// test database:
//
//   - The DATA DIRECTORY PERSISTS, under the project's own .forge/hostinfra/.
//     A developer's rows have to survive stopping the stack, the same way a
//     compose volume's do. (This is also why DataPath is configured
//     explicitly: embedded-postgres RemoveAll's its RuntimePath on every
//     Start, and when DataPath is unset the data directory lives INSIDE
//     RuntimePath — so an unconfigured instance silently wipes the database on
//     every boot.)
//   - The SERVER OUTLIVES THE PROCESS THAT STARTED IT. `forge env up` returns
//     while the stack keeps serving, exactly as a `docker compose up -d`
//     postgres does. There is no reference counting and no teardown-on-exit:
//     the server is stopped explicitly, by `forge env down`.
//
// Both differences point the same way — this is infrastructure with a
// lifecycle a human manages, not a fixture with a lifecycle a test manages.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the database/sql and TCP calls are readiness probes of the process hostinfra itself started; a contract.go split of the probe is tracked as CONTRACTS follow-up F2
//forge:exclude-contract: supervision of third-party server binaries forge fetches (a human-managed lifecycle, not an injected component)
package hostinfra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/lib/pq" // postgres driver, registered as "postgres"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// EnginePostgres is the first engine forge supervises natively (the others:
// zitadel.go, nats.go, temporal.go). It matches the `engine` discriminator on
// the KCL HostInfra schema.
const EnginePostgres = "postgres"

// DefaultDataDirRel is where a HostInfra instance keeps its state when the
// declaration names no data_dir: under the project's own .forge/, which is
// already gitignored and already where forge keeps machine-local state
// (the port store, the dev-stack block registry, the IdP's PAT).
//
// Project-local rather than a shared system directory ON PURPOSE. Two
// checkouts of the same project — the parallel-worktree case forge's
// allocate_port exists to serve — are two independent stacks with two
// independent databases, and a shared path would silently make them one.
const DefaultDataDirRel = ".forge/hostinfra"

// Spec is one declared host-infra instance: the rendered projection of a
// `forge.HostInfra` deploy block plus the workload name that carries it.
type Spec struct {
	// Name is the workload name (e.g. "postgres"). It names the instance in
	// logs and, by default, its data directory.
	Name string
	// Engine selects the implementation: EnginePostgres, EngineZitadel,
	// EngineNATS or EngineTemporal.
	Engine string
	// Port is the loopback port the server binds — the same number the app's
	// DSN dials, which is why the environment declares it once and hands it
	// to both.
	Port int
	// Database is created on first boot; User/Password are the credentials
	// the DSN carries.
	Database string
	User     string
	Password string
	// DataDir is where the cluster lives. Relative paths resolve against the
	// project root; empty means DefaultDataDirRel/<Name>.
	DataDir string
	// Version pins the engine major version. Empty means forge's default.
	Version string

	// ── EngineZitadel only ────────────────────────────────────────────
	//
	// The dev IdP needs a handful of facts the postgres engine has no use
	// for. They live on the one Spec rather than in a second type because
	// they arrive the same way (one rendered `forge.HostInfra` block) and
	// go to the same place (Start), and a parallel type would double every
	// mapping between KCL and here for two fields' worth of difference.

	// IDPDatabase is the database Zitadel creates its schema in, on the
	// postgres server IDPDatabasePort names. Its OWN database — the
	// container path spent a second postgres container on the same
	// isolation.
	IDPDatabase string
	// IDPDatabasePort is where that postgres server listens. It is the
	// APP's host-native postgres, so this is the same number the app's DSN
	// carries, handed over from the one place the environment declares it.
	IDPDatabasePort int
	// IDPMasterKey is Zitadel's encryption key for the credentials it
	// stores. Exactly 32 characters, per Zitadel.
	IDPMasterKey string
	// IDPStepsFile is the declarative bootstrap passed as `--steps`
	// (idp-steps.yaml). Relative to the project root; this file IS the
	// instance's setup.
	IDPStepsFile string
	// IDPPATPath is where Zitadel writes the service-account personal
	// access token on first boot. It must match what the identity step of `forge env up`
	// reads — the two are one declaration.
	IDPPATPath string

	// ── EngineNATS only ───────────────────────────────────────────────

	// NATSMonitorPort is the HTTP monitoring port (`--http_port`). Zero
	// means no monitoring endpoint.
	NATSMonitorPort int

	// ── EngineTemporal only ───────────────────────────────────────────

	// TemporalNamespaces are created at launch; readiness waits for every
	// one of them. Empty means just Temporal's own "default".
	TemporalNamespaces []string
	// TemporalUIPort is the Web UI's port. Zero means headless.
	TemporalUIPort int
}

// unsupportedEngine is Start/StatusOf's answer for an engine forge has no
// implementation of.
func unsupportedEngine(spec Spec) error {
	return fmt.Errorf("host-infra %s: unsupported engine %q (forge supervises %q, %q, %q and %q natively): %w",
		spec.Name, spec.Engine, EnginePostgres, EngineZitadel, EngineNATS, EngineTemporal, ErrUnsupportedEngine)
}

// dataDir resolves the instance's data directory against the project root.
func (s Spec) dataDir(projectDir string) string {
	dir := s.DataDir
	if dir == "" {
		dir = filepath.Join(DefaultDataDirRel, s.Name)
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(projectDir, dir)
}

// DSN is the connection string for this instance's database.
func (s Spec) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@localhost:%d/%s?sslmode=disable",
		s.User, s.Password, s.Port, s.Database)
}

// baseDSN is the MAINTENANCE connection — the same server, the always-present
// `postgres` database. Creating another database requires being connected to
// one already, so this is what the ensure-database path dials.
//
// It carries connect_timeout, and that is not a tuning knob — it is a
// hang fix. See [probeConnectTimeoutSeconds].
func (s Spec) baseDSN() string {
	return fmt.Sprintf("postgres://%s:%s@localhost:%d/postgres?sslmode=disable&connect_timeout=%d",
		s.User, s.Password, s.Port, probeConnectTimeoutSeconds)
}

// probeConnectTimeoutSeconds bounds how long a probe waits for a postgres
// HANDSHAKE, as distinct from how long it waits for a query.
//
// The two are different, and assuming otherwise hangs forge forever.
// lib/pq documents that a zero or unspecified connect_timeout means WAIT
// INDEFINITELY, and the deadline it sets from connect_timeout is the only
// thing bounding the startup handshake — a context passed to
// QueryRowContext applies to the QUERY, which never begins. So a
// context.WithTimeout around the query does not save a connection that is
// stuck mid-handshake.
//
// That is not a hypothetical. identifyHolder dials the declared port
// whenever forge cannot find an instance of its own there, and on a shared
// dev box that port is routinely held by something that is NOT postgres.
// A TCP server that accepts the connection and then says nothing — any
// non-postgres service — leaves pq blocked in recvMessage with no
// deadline, so `forge env up` hangs with no output rather than reporting
// the port collision it is one step away from detecting.
//
// Five seconds, matching the query timeout beside it: a loopback postgres
// completes its handshake in single-digit milliseconds, so this is three
// orders of magnitude of headroom for the legitimate case and a bounded
// failure for the broken one.
const probeConnectTimeoutSeconds = 5

// Start brings the instance up, or confirms it is already up, and returns
// only once it is actually SERVING — not merely spawned. It is idempotent:
// running `forge env up` twice in a row does not start a second postgres, and
// does not fail the second time.
//
// The already-up check dials the port and authenticates rather than looking
// for a pid file, because those answer different questions. A pid file says a
// process was started; a successful connection says the thing the app is
// about to dial will answer it. On a shared dev machine the port may also be
// held by something that is NOT this instance — another project's postgres,
// or a system one — and that case has to be reported, not adopted: a stack
// that silently attaches to the wrong database is the failure this whole path
// exists to make impossible (see internal/devpg for the incident).
func Start(ctx context.Context, projectDir string, spec Spec) error {
	switch spec.Engine {
	case EnginePostgres:
		return startPostgres(ctx, projectDir, spec)
	case EngineZitadel:
		return startZitadel(ctx, projectDir, spec)
	case EngineNATS:
		return natsEngine.start(ctx, projectDir, spec)
	case EngineTemporal:
		return temporalEngine.start(ctx, projectDir, spec)
	default:
		return unsupportedEngine(spec)
	}
}

// startPostgres is the postgres engine's half of Start. See Start for the
// contract every engine honours (idempotent, returns only once SERVING).
func startPostgres(ctx context.Context, projectDir string, spec Spec) error {
	dataDir := spec.dataDir(projectDir)

	// Is OUR OWN cluster already running? Ask the data directory before
	// probing the port, because the data directory is what identifies this
	// instance — the port is just where it currently answers.
	//
	// Asking in this order also produces the right message for the one
	// genuinely confusing case: our cluster running, but on a DIFFERENT
	// port than the declaration now names. Probing the port first would
	// find it quiet, try to start a second postmaster over the same files,
	// and fail with postgres's own "lock file postmaster.pid already
	// exists" — which describes the mechanism and hides the cause.
	if running, alive := runningPort(dataDir); alive {
		if running == spec.Port {
			fmt.Printf("  %s: already running on :%d (host process)\n", spec.Name, spec.Port)
			return nil
		}
		return fmt.Errorf(
			"host-infra %s: already running on port %d, but this environment now declares port %d\n"+
				"  the data directory (%s) can only be served by one postmaster, so forge will not start a second.\n"+
				"  Fix — stop the running one (`forge env down <env>`) and re-run, which brings it back up on %d",
			spec.Name, running, spec.Port, shortPath(projectDir, dataDir), spec.Port)
	}

	// Nothing of ours is running. Is something ELSE on the port?
	switch identifyHolder(ctx, spec, dataDir) {
	case holderOurs:
		fmt.Printf("  %s: already running on :%d (host process)\n", spec.Name, spec.Port)
		return nil
	case holderForeign:
		return fmt.Errorf(
			"host-infra %s: port %d is held by something this project did not start\n"+
				"  forge will not adopt it: connecting anyway would run this project's migrations and\n"+
				"  seed data inside another stack's database, silently and successfully.\n"+
				"  Fix — free the port, or move this one: the port is declared in deploy/kcl/<env>/main.k",
			spec.Name, spec.Port)
	}

	// Reap the leftovers of instances whose supervising process was killed
	// before it could stop them. They keep SysV IPC resources allocated, and
	// once the kernel's tables fill (macOS defaults to 32 shared-memory
	// segments) EVERY subsequent initdb fails with "No space left on
	// device" — a message that names neither the cause nor this project.
	reapStaleRuntimes(projectDir)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("host-infra %s: create data dir: %w", spec.Name, err)
	}

	// initdb's and pg_ctl's own output goes to a log beside the data, the way
	// the IdP's does (zitadel.log). Left on the library's default — stdout —
	// a first `forge env up` printed initdb's whole transcript into the
	// terminal: absolute paths, the locale banner, and a "you can now start
	// the database server using: pg_ctl … start" hint telling the reader to
	// do by hand what forge had just done for them.
	logPath := filepath.Join(dataDir, "postgres.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return fmt.Errorf("host-infra %s: open log: %w", spec.Name, err)
	}
	defer func() { _ = logFile.Close() }()

	// pgtest.StartEmbedded fetches a cold cache's binary with retry and
	// reports a failed download as one, rather than as the library's
	// "no version found matching X" (pkg/pgtest/fetch.go).
	ep, err := pgtest.StartEmbedded(postgresConfig(spec, dataDir).Logger(logFile))
	if err != nil {
		return fmt.Errorf("host-infra %s: start postgres on :%d: %w (initdb/pg_ctl output: %s)",
			spec.Name, spec.Port, err, shortPath(projectDir, logPath))
	}
	// The server outlives this process and is stopped later by pg_ctl, never
	// through this handle, so nothing will flush the library's temp log again:
	// release the file this Start created (and only that one).
	ep.Detach()

	// The server now OUTLIVES this process by design (see the package doc):
	// `forge env up` returns while the stack keeps serving. embedded-postgres
	// starts postgres via `pg_ctl start`, which daemonizes — so there is no
	// child of ours to detach, and nothing to do here but stop holding the
	// handle. Teardown goes through Stop (`forge env down`), which finds the
	// server by its data directory rather than by a handle no later process
	// could hold anyway.

	if err := ensureDatabase(spec); err != nil {
		return fmt.Errorf("host-infra %s: %w", spec.Name, err)
	}
	fmt.Printf("  %s: postgres serving on :%d (host process, data %s)\n",
		spec.Name, spec.Port, shortPath(projectDir, dataDir))
	return nil
}

// postgresConfig is the embedded-postgres configuration for a dev server.
// The comments explain the settings that are NOT obvious; the obvious ones
// (user, password, port) are just the spec.
func postgresConfig(spec Spec, dataDir string) embeddedpostgres.Config {
	cfg := embeddedpostgres.DefaultConfig().
		Username(spec.User).
		Password(spec.Password).
		// The maintenance database, not the app's. embedded-postgres creates
		// this one on first boot; the app's own database is created by
		// ensureDatabase below, which also covers the SECOND database a
		// project needs later (the dev IdP's) without re-initializing the
		// cluster.
		Database("postgres").
		// initdb names its locale instead of deriving one from LANG/LC_*.
		// Left to the environment, a shell exporting LC_ALL=C.UTF-8 on macOS
		// (which has no such locale) failed every first `forge env up` with
		// initdb's "invalid locale settings; check LANG and LC_* environment
		// variables". C exists on every platform postgres runs on — the same
		// choice pgtest makes for the embedded test databases — and UTF8
		// keeps the encoding production's, where C alone means SQL_ASCII.
		// Only a fresh data directory is initialized, so an existing dev
		// cluster keeps whatever locale it was created with.
		Locale("C").
		Encoding("UTF8").
		Port(uint32(spec.Port)). //nolint:gosec // the KCL schema bounds port to 1..65535
		// DATA and RUNTIME are separate directories, and that is load-bearing:
		// Start() does RemoveAll(RuntimePath) every time, and an unset DataPath
		// defaults to a subdirectory OF RuntimePath — so leaving it unset would
		// silently wipe the developer's database on every `forge env up`.
		DataPath(filepath.Join(dataDir, "data")).
		RuntimePath(filepath.Join(dataDir, "runtime")).
		StartParameters(postgresStartParameters(runtime.GOOS)).
		// Generous because the FIRST run extracts the postgres archive before
		// it can start anything. Later runs take a second or two.
		StartTimeout(90 * time.Second)
	if cache := cacheDir(); cache != "" {
		// One cache for every instance on this machine: the archive is ~30 MB
		// and identical, so a second project should not re-download it.
		cfg = cfg.CachePath(cache)
	}
	if spec.Version != "" {
		cfg = cfg.Version(postgresRelease(spec.Version))
	}
	return cfg
}

// postgresReleases maps a postgres MAJOR to the exact release the embedded
// binaries publish for it. HostInfra.version is documented as a major ("16"),
// and that is what a project means: prod runs "postgres 16", not a patch
// release. embedded-postgres fetches by full release ("16.9.0"), and handed a
// bare "16" it asks Maven for an artifact that does not exist — so the major
// is resolved here, to the release the library itself pins for it.
var postgresReleases = map[string]embeddedpostgres.PostgresVersion{
	"18": embeddedpostgres.V18,
	"17": embeddedpostgres.V17,
	"16": embeddedpostgres.V16,
	"15": embeddedpostgres.V15,
	"14": embeddedpostgres.V14,
	"13": embeddedpostgres.V13,
	"12": embeddedpostgres.V12,
}

// postgresRelease resolves a declared version: a known major to its pinned
// release, anything else (an exact "16.9.0") verbatim.
func postgresRelease(v string) embeddedpostgres.PostgresVersion {
	if r, ok := postgresReleases[strings.TrimSpace(v)]; ok {
		return r
	}
	return embeddedpostgres.PostgresVersion(v)
}

// PostgresMajor is the major of a declared or reported postgres version:
// "16" for "16", "16.9.0" or "16.9". A data directory records only its
// major (PG_VERSION), so that is the unit two versions are compared in.
func PostgresMajor(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}

// postgresStartParameters is a dev server's postgresql.conf overrides for a
// host running goos.
//
// On POSIX: mmap-backed shared memory instead of System V. The sysv default
// exhausts the kernel's SHMMNI limit on macOS ("could not create shared
// memory segment: No space left on device") once a few instances accumulate,
// and a developer machine running several stacks is exactly where they
// accumulate.
//
// On Windows: no shared-memory override. Its postgres build implements one
// type per setting, "windows", and refuses to start on anything else
// ("invalid value for parameter dynamic_shared_memory_type: mmap") — so
// carrying the POSIX fix across made every `forge env up` postgres fail to
// boot there. Windows has no SHMMNI table for the default to exhaust.
func postgresStartParameters(goos string) map[string]string {
	params := map[string]string{
		"shared_buffers":  "64MB",
		"max_connections": "100",
	}
	if goos != "windows" {
		params["dynamic_shared_memory_type"] = "mmap"
		params["shared_memory_type"] = "mmap"
	}
	return params
}

// Stop shuts the instance down cleanly and leaves the DATA in place.
//
// Clean matters beyond tidiness: an orderly `pg_ctl stop` releases the SysV
// semaphore sets and the shared-memory interlock segment, while a SIGKILL
// leaks both. Leak enough of them and no postgres starts on this machine
// again until they are reclaimed by hand.
//
// The data directory SURVIVES. `forge env down` is "stop the stack", not
// "throw away my rows" — the compose story it replaces kept its volume too.
// Deleting the data directory is the (explicit, manual) way to start clean.
func Stop(projectDir string, spec Spec) error {
	_, err := StopReport(projectDir, spec)
	return err
}

// StopReport is Stop, and also reports whether a server was actually
// running — so `forge env down` can say what it stopped instead of
// claiming to have stopped something that was already down.
//
// The server is located by its DATA DIRECTORY, not by the declared port.
// An instance running on a port the declaration has since moved off is
// still this instance, and still the one that must be stopped; finding it
// by port would leave it running and report success.
func StopReport(projectDir string, spec Spec) (stopped bool, err error) {
	switch spec.Engine {
	case EngineZitadel:
		return stopZitadel(projectDir, spec)
	case EngineNATS:
		return natsEngine.stop(projectDir, spec)
	case EngineTemporal:
		return temporalEngine.stop(projectDir, spec)
	}
	dataDir := spec.dataDir(projectDir)
	pgCtl := filepath.Join(dataDir, "runtime", "bin", "pg_ctl")
	dataPath := filepath.Join(dataDir, "data")
	if _, statErr := os.Stat(pgCtl); statErr != nil {
		return false, nil // never started here, or already cleaned up
	}
	if _, alive := postmaster(filepath.Join(dataPath, "postmaster.pid")); !alive {
		return false, nil
	}
	// -m fast rolls back open transactions and shuts down promptly rather
	// than waiting for clients to disconnect, which on a dev box means
	// waiting for an app the developer just Ctrl-C'd.
	out, runErr := exec.Command(pgCtl, "stop", "-w", "-D", dataPath, "-m", "fast").CombinedOutput() // #nosec G204 -- path derived from the project's own declaration
	if runErr != nil {
		return false, fmt.Errorf("host-infra %s: stop postgres: %w: %s", spec.Name, runErr, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// holder classifies what, if anything, is answering on the instance's port.
type holder int

const (
	// holderNone: nothing is listening. Start it.
	holderNone holder = iota
	// holderOurs: this instance is already up and serving. No-op.
	holderOurs
	// holderForeign: something is listening that is not this instance —
	// another project's database, or a system postgres. Refuse.
	holderForeign
)

// identifyHolder decides which of the three cases the port is in.
//
// The distinction between "ours" and "foreign" is drawn from the DATA
// DIRECTORY, not from the connection: forge asks the running server where its
// files are (`SHOW data_directory`) and compares that against the path this
// declaration names. Credentials cannot make that call — a scaffolded project
// uses postgres/postgres, so does every other one on the machine, and a
// successful login proves only that the passwords match. The data directory is
// what actually distinguishes one database from another.
func identifyHolder(ctx context.Context, spec Spec, dataDir string) holder {
	if !portListening(spec.Port) {
		return holderNone
	}
	db, err := sql.Open("postgres", spec.baseDSN())
	if err != nil {
		return holderForeign
	}
	defer func() { _ = db.Close() }()

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var running string
	if err := db.QueryRowContext(probeCtx, "SHOW data_directory").Scan(&running); err != nil {
		// Listening, but not a postgres that will talk to us on these
		// credentials. Whatever it is, it is not this instance.
		return holderForeign
	}
	ours := filepath.Join(dataDir, "data")
	if sameDir(running, ours) {
		return holderOurs
	}
	return holderForeign
}

// sameDir compares two directory paths, resolving symlinks so that a macOS
// /var/... vs /private/var/... spelling of one directory does not read as two.
// Falls back to a lexical comparison when a path cannot be resolved.
func sameDir(a, b string) bool {
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra == nil && errb == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// portListening reports whether anything holds the loopback port.
func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ensureDatabase creates the instance's application database when it does not
// exist yet.
//
// embedded-postgres creates a database on FIRST boot only (it skips the whole
// init path when it finds a valid data directory), so a project that later
// declares a second database — or renames the first — would otherwise get a
// server with no database to connect to and a `FATAL: database "x" does not
// exist` from the app rather than from the thing that owns the database.
func ensureDatabase(spec Spec) error {
	base, err := sql.Open("postgres", spec.baseDSN())
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer func() { _ = base.Close() }()
	// The server has just reported ready via pg_ctl -w, but the first
	// connection can still land microseconds early on a cold start.
	var pingErr error
	for i := 0; i < 40; i++ {
		if pingErr = base.Ping(); pingErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if pingErr != nil {
		return fmt.Errorf("connect to postgres on :%d: %w", spec.Port, pingErr)
	}
	var exists bool
	if err := base.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", spec.Database,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check database %q: %w", spec.Database, err)
	}
	if exists {
		return nil
	}
	// CREATE DATABASE takes no bind parameters, so the name is interpolated.
	// Quote it as an identifier: a project named `note-app` is a perfectly
	// legal database name but not a bare identifier.
	if _, err := base.Exec("CREATE DATABASE " + quoteIdent(spec.Database)); err != nil {
		// A concurrent creator winning the race satisfies the post-condition.
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("create database %q: %w", spec.Database, err)
	}
	return nil
}

// quoteIdent renders a string as a quoted SQL identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// staleRuntimeAge is how long a runtime directory must sit with no live
// postmaster before it is treated as abandoned. Generous enough that an
// instance mid-boot (archive extraction, initdb) is never swept out from
// under itself.
const staleRuntimeAge = 30 * time.Minute

// reapStaleRuntimes reclaims the SysV shared-memory segments left behind by
// instances whose postmaster died without an orderly shutdown.
//
// Postgres always creates one small System V interlock segment per instance
// even under shared_memory_type=mmap, and records its id in postmaster.pid
// precisely so a replacement postmaster can detect and remove a stale one. A
// process that was SIGKILLed never gets to; the segments accumulate, and once
// the kernel table is full (macOS defaults to 32) every subsequent initdb on
// the machine fails. Reclaiming on the way IN — rather than hoping every exit
// path is clean — is what pkg/pgtest learned to do, for the same reason.
//
// Best-effort throughout: this is hygiene, not a precondition, and an error
// reclaiming someone else's leftovers must never stop this project's boot.
func reapStaleRuntimes(projectDir string) {
	root := filepath.Join(projectDir, DefaultDataDirRel)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pidFile := filepath.Join(root, e.Name(), "data", "postmaster.pid")
		if _, alive := postmaster(pidFile); alive {
			continue // a live instance — another env, or another stack
		}
		info, statErr := os.Stat(pidFile)
		if statErr != nil || time.Since(info.ModTime()) < staleRuntimeAge {
			continue
		}
		reclaimShmSegment(pidFile)
	}
}

// runningPort reports the port THIS instance's cluster is currently
// serving on, and whether its postmaster is alive.
//
// Postgres records the port on the 4th line of postmaster.pid
// (LOCK_FILE_LINE_PORT) precisely so tooling can find a running server
// without guessing. Reading it is what lets forge tell "our database is up
// where we expect it" apart from "our database is up somewhere else",
// which are the same observation from the port's point of view and
// completely different problems.
func runningPort(dataDir string) (port int, alive bool) {
	pidFile := filepath.Join(dataDir, "data", "postmaster.pid")
	if _, live := postmaster(pidFile); !live {
		return 0, false
	}
	b, err := os.ReadFile(pidFile) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(b), "\n")
	const portLine = 3 // 0-based index of the 4th line
	if len(lines) <= portLine {
		// A postmaster is alive but has not written its port yet (it is
		// mid-boot). Report alive with an unknown port rather than 0, which
		// would read as "not running" and race a second start.
		return -1, true
	}
	p, err := strconv.Atoi(strings.TrimSpace(lines[portLine]))
	if err != nil || p <= 0 {
		return -1, true
	}
	return p, true
}

// postmaster reads a postmaster.pid file and reports the server pid plus
// whether that process is alive (a signal-0 probe). A missing or unparseable
// file reads as "not running".
func postmaster(pidFile string) (pid int, alive bool) {
	b, err := os.ReadFile(pidFile) // #nosec G304 -- path is derived from the project's own data dir
	if err != nil {
		return 0, false
	}
	first := string(b)
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	pid, err = strconv.Atoi(strings.TrimSpace(first))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, processAlive(pid)
}

// reclaimShmSegment removes the SysV shared-memory segment named in a stale
// pid file. Postgres writes "<shmkey> <shmid>" on the 7th line of
// postmaster.pid (LOCK_FILE_LINE_SHMEM_KEY); this reuses that contract.
func reclaimShmSegment(pidFile string) {
	b, err := os.ReadFile(pidFile) // #nosec G304 -- path is derived from the project's own data dir
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	const shmemLine = 6 // 0-based index of the 7th line
	if len(lines) <= shmemLine {
		return
	}
	fields := strings.Fields(lines[shmemLine])
	if len(fields) < 2 {
		return
	}
	id, err := strconv.Atoi(fields[1])
	if err != nil || id <= 0 {
		return
	}
	// removeShmSegment is a no-op on Windows, which has no SysV shm.
	removeShmSegment(id)
}

// cacheDir is where the downloaded postgres archive is kept, shared across
// every project on this machine. Empty falls back to the library's default.
func cacheDir() string {
	c, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(c, "forge", "embedded-postgres")
}

// shortPath renders a path relative to the project root for log output, so a
// launch line reads `.forge/hostinfra/postgres` rather than an absolute path
// wide enough to wrap the terminal.
func shortPath(projectDir, path string) string {
	if rel, err := filepath.Rel(projectDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// ErrUnsupportedEngine is returned for an engine forge has no implementation
// for. The KCL schema check rejects these at render time, so reaching this is
// a forge bug or a hand-built Spec.
var ErrUnsupportedEngine = errors.New("hostinfra: unsupported engine")
