package vulnscan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

const (
	// EnvDir relocates the shared database: a CI runner that caches one
	// directory, a read-only home, or a test.
	EnvDir = "FORGE_VULN_DB_DIR"
	// EnvURL points the database refresh at a mirror that serves grype's
	// layout (<url>/v6/latest.json and the archives it names).
	EnvURL = "FORGE_VULN_DB_URL"
	// DefaultURL is grype's own database distribution.
	DefaultURL = "https://grype.anchore.io/databases"
	// SchemaMajor is the database schema the pinned grype reads.
	SchemaMajor = "v6"

	// RefreshAfter is how old the database may get before a command that
	// is allowed to fetch refreshes it. Upstream publishes at least daily.
	RefreshAfter = 24 * time.Hour
	// StaleAfter is how old a database may be before a verdict computed
	// against it is "stale". It is grype's own limit
	// (db.max-allowed-built-age), which forge never relaxes.
	StaleAfter = 120 * time.Hour
	// recheckAfter bounds how often a refresh asks the listing when the
	// database is past RefreshAfter but the listing has nothing newer
	// (upstream late), so every command does not pay a round-trip.
	recheckAfter = time.Hour
	// lockPoll is how often a waiter retries the database lock.
	lockPoll = 250 * time.Millisecond
	// listingTimeout bounds the listing read; it is a few hundred bytes.
	listingTimeout = 30 * time.Second
)

var (
	// ErrScannerMissing is grype not installed and not installable. A scan
	// that needed it did not run, which is "not scanned", never clean.
	ErrScannerMissing = errors.New("vulnerability scanner unavailable")
	// ErrNoDB is no vulnerability database on this machine.
	ErrNoDB = errors.New("no vulnerability database on this machine")
	// ErrStale is a database older than StaleAfter.
	ErrStale = errors.New("vulnerability database is stale")
)

// ToolFunc resolves the grype binary: its path and pinned version.
type ToolFunc func(ctx context.Context) (path, version string, err error)

// Command is one invocation of grype.
type Command struct {
	Path   string
	Args   []string
	Env    []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

// Runner runs a Command. The default runs the real binary; tests substitute
// a fake grype.
type Runner func(ctx context.Context, c Command) error

// Exec runs a Command as a real process: the default Runner.
func Exec(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...) //nolint:gosec // c.Path is the pinned grype binary
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = c.Env, c.Dir, c.Stdout, c.Stderr
	return cmd.Run()
}

// DefaultDir is the shared database's directory: $FORGE_VULN_DB_DIR, else
// <UserCacheDir>/forge/vulndb — beside forge's other machine-wide caches, and
// out of the home directory a backup would copy (it is 3.3 GiB).
func DefaultDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv(EnvDir)); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate the user cache directory for the vulnerability database: %w (set %s)", err, EnvDir)
	}
	return filepath.Join(base, "forge", "vulndb"), nil
}

// DefaultBaseURL is the database distribution: $FORGE_VULN_DB_URL, else
// DefaultURL.
func DefaultBaseURL() string {
	if u := strings.TrimSpace(os.Getenv(EnvURL)); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultURL
}

// DB is the machine's shared grype database. The zero value is not usable:
// set Dir and Grype (Open does both, and URL, from the environment).
type DB struct {
	// Dir is the database's directory (DefaultDir).
	Dir string
	// URL is the distribution base (DefaultBaseURL); "" is DefaultBaseURL.
	URL string
	// Grype resolves the scanner binary.
	Grype ToolFunc
	// Run runs grype; nil runs the real binary.
	Run Runner
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Log receives progress lines (a download, a wait for another
	// process's update); nil is os.Stderr.
	Log io.Writer

	// ReadOnly uses Dir as a database something else keeps fresh: a
	// machine's database mounted read-only into a gate. Nothing is written
	// into Dir. grype's config goes to ConfigDir, a read takes the
	// directory's lock only when its lock file already exists, and an
	// update or import is refused.
	ReadOnly bool
	// ConfigDir is where grype's (empty) config is written; "" is Dir. A
	// ReadOnly database needs one, since Dir cannot take the file.
	ConfigDir string
	// Offline forbids every network read: Ensure never reads the listing
	// or runs a download, whatever its options, and judges the database on
	// disk as it is. A missing or stale database is then ErrNoDB or
	// ErrStale, as with no network.
	Offline bool
}

// ErrOffline is a network read refused because the database is Offline.
var ErrOffline = errors.New("the vulnerability database is offline: no network read is allowed")

// Open returns the database at DefaultDir, resolving grype with tool.
func Open(tool ToolFunc) (*DB, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return &DB{Dir: dir, URL: DefaultBaseURL(), Grype: tool}, nil
}

func (d *DB) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *DB) log() io.Writer {
	if d.Log != nil {
		return d.Log
	}
	return os.Stderr
}

func (d *DB) run(ctx context.Context, c Command) error {
	if d.Run != nil {
		return d.Run(ctx, c)
	}
	return Exec(ctx, c)
}

// BaseURL is the distribution base in use.
func (d *DB) BaseURL() string {
	if d.URL != "" {
		return strings.TrimRight(d.URL, "/")
	}
	return DefaultBaseURL()
}

// ListingURL is the listing a refresh reads.
func (d *DB) ListingURL() string { return d.BaseURL() + "/" + SchemaMajor + "/latest.json" }

// OnDisk reports whether a database file is in the directory at all,
// without running grype: a cheap "has this machine ever pulled one" for a
// caller that must not download the first one (a release cut). Status says
// whether grype can open it.
func (d *DB) OnDisk() bool {
	fi, err := os.Stat(filepath.Join(d.grypeDir(), strings.TrimPrefix(SchemaMajor, "v"), "vulnerability.db"))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

func (d *DB) grypeDir() string { return filepath.Join(d.Dir, "grype") }
func (d *DB) configPath() string {
	if d.ConfigDir != "" {
		return filepath.Join(d.ConfigDir, "grype.yaml")
	}
	return filepath.Join(d.Dir, "grype.yaml")
}
func (d *DB) statePath() string { return filepath.Join(d.Dir, "state.json") }
func (d *DB) lockPath() string  { return filepath.Join(d.Dir, "lock") }

// emptyConfig is the grype config every invocation reads instead of the
// user's or the project's: nothing in it, so grype's defaults and forge's
// environment are the whole configuration.
const emptyConfig = "# Written by forge. grype reads this file instead of ~/.grype.yaml or a\n" +
	"# project's .grype.yaml, so no local ignore rule changes a forge verdict.\n"

// prepare creates the directory and the empty config.
func (d *DB) prepare() error {
	if strings.TrimSpace(d.Dir) == "" {
		return errors.New("vulnerability database directory is not set")
	}
	if d.ReadOnly {
		// Dir is someone else's: create nothing in it. A directory that is
		// not there is not created either; grype then reports no database.
		if strings.TrimSpace(d.ConfigDir) == "" {
			return errors.New("a read-only vulnerability database needs a directory for grype's config (ConfigDir)")
		}
		if err := os.MkdirAll(d.ConfigDir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d.ConfigDir, err)
		}
	} else if err := os.MkdirAll(d.grypeDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", d.grypeDir(), err)
	}
	if b, err := os.ReadFile(d.configPath()); err == nil && string(b) == emptyConfig {
		return nil
	}
	return atomicfile.WriteFile(d.configPath(), []byte(emptyConfig), 0o644)
}

// env is the environment every grype invocation runs with: the caller's
// environment minus every GRYPE_* variable (a GRYPE_ONLY_FIXED left in a
// shell would silently change findings), plus forge's settings. Auto-update
// is always off — the refresh is forge's own step — and the age check is
// always on.
func (d *DB) env() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GRYPE_") {
			out = append(out, kv)
		}
	}
	return append(out,
		"GRYPE_DB_CACHE_DIR="+d.grypeDir(),
		"GRYPE_DB_UPDATE_URL="+d.BaseURL(),
		"GRYPE_DB_AUTO_UPDATE=false",
		"GRYPE_DB_VALIDATE_AGE=true",
		"GRYPE_DB_MAX_ALLOWED_BUILT_AGE="+StaleAfter.String(),
		"GRYPE_DB_REQUIRE_UPDATE_CHECK=false",
		"GRYPE_CHECK_FOR_APP_UPDATE=false",
	)
}

// grype runs grype with args under forge's config and environment, from the
// database directory (so no .grype.yaml in the caller's directory is read).
func (d *DB) grype(ctx context.Context, stdout io.Writer, args ...string) (version string, err error) {
	if d.Grype == nil {
		return "", fmt.Errorf("%w: no grype resolver configured", ErrScannerMissing)
	}
	path, version, err := d.Grype(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrScannerMissing, err)
	}
	if err := d.prepare(); err != nil {
		return version, err
	}
	var stderr bytes.Buffer
	full := append([]string{"-c", d.configPath(), "-q"}, args...)
	dir := d.Dir
	if d.ReadOnly {
		// Dir may not even exist (no database mounted); the private config
		// directory holds no .grype.yaml either.
		dir = d.ConfigDir
	}
	err = d.run(ctx, Command{Path: path, Args: full, Env: d.env(), Dir: dir, Stdout: stdout, Stderr: &stderr})
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return version, fmt.Errorf("grype %s: %w: %s", strings.Join(args, " "), err, lastLines(msg, 5))
		}
		return version, fmt.Errorf("grype %s: %w", strings.Join(args, " "), err)
	}
	return version, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Status is the database as it is on disk now.
type Status struct {
	// Dir is the database directory.
	Dir string `json:"dir"`
	// URL is the distribution base a refresh reads.
	URL string `json:"url"`
	// Present reports a database grype can open.
	Present bool `json:"present"`
	// Schema is its schema version ("v6.1.10").
	Schema string `json:"schema,omitempty"`
	// BuiltAt is when it was built: the freshness of every scan against it.
	BuiltAt time.Time `json:"built_at,omitzero"`
	// Source is where it was downloaded or imported from.
	Source string `json:"source,omitempty"`
	// CheckedAt is when a refresh last read the listing, and LatestBuiltAt
	// the build the listing named then.
	CheckedAt     time.Time `json:"checked_at,omitzero"`
	LatestBuiltAt time.Time `json:"latest_built_at,omitzero"`
	// Problem is why grype could not open it, when Present is false.
	Problem string `json:"problem,omitempty"`
}

// Age is how old the database is at now.
func (s Status) Age(now time.Time) time.Duration {
	if !s.Present {
		return 0
	}
	return now.Sub(s.BuiltAt)
}

// Fresh reports a database younger than RefreshAfter.
func (s Status) Fresh(now time.Time) bool { return s.Present && s.Age(now) < RefreshAfter }

// Stale reports a database older than StaleAfter, or none at all.
func (s Status) Stale(now time.Time) bool { return !s.Present || s.Age(now) > StaleAfter }

// CacheKey is the key a CI cache of the database directory should use: it
// changes exactly when the database build does.
func (s Status) CacheKey() string {
	if !s.Present {
		return ""
	}
	return "forge-vulndb-" + SchemaMajor + "-" + s.BuiltAt.UTC().Format("2006-01-02")
}

// state is forge's record of its last listing check.
type state struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestBuiltAt time.Time `json:"latest_built_at,omitzero"`
}

func (d *DB) readState() state {
	var st state
	if b, err := os.ReadFile(d.statePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (d *DB) writeState(st state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(d.statePath(), append(b, '\n'), 0o644)
}

// Status reads the database's status from grype (`grype db status`), with
// no network. It needs grype: a missing scanner is ErrScannerMissing.
func (d *DB) Status(ctx context.Context) (Status, error) {
	unlock, err := d.lock(ctx, false)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	return d.statusLocked(ctx)
}

func (d *DB) statusLocked(ctx context.Context) (Status, error) {
	st := Status{Dir: d.Dir, URL: d.BaseURL()}
	s := d.readState()
	st.CheckedAt, st.LatestBuiltAt = s.CheckedAt, s.LatestBuiltAt
	var out bytes.Buffer
	_, runErr := d.grype(ctx, &out, "db", "status", "-o", "json")
	if errors.Is(runErr, ErrScannerMissing) {
		return st, runErr
	}
	// `grype db status` exits 1 when there is no usable database, and
	// still prints the status that says why; only output that does not
	// parse is an error.
	var gs grypeDBStatus
	if err := json.Unmarshal(out.Bytes(), &gs); err != nil {
		if runErr != nil {
			return st, runErr
		}
		return st, fmt.Errorf("read `grype db status` output: %w", err)
	}
	st.Schema, st.Source = gs.SchemaVersion, gs.From
	if !gs.Valid {
		st.Problem = gs.Error
		if st.Problem == "" {
			st.Problem = "grype reports the database invalid"
		}
		return st, nil
	}
	built, err := parseGrypeTime(gs.Built)
	if err != nil {
		st.Problem = fmt.Sprintf("the database names no build time (%q)", gs.Built)
		return st, nil
	}
	st.Present, st.BuiltAt = true, built.UTC()
	return st, nil
}

// Listing is the distribution's newest database for SchemaMajor.
type Listing struct {
	Status        string    `json:"status"`
	SchemaVersion string    `json:"schemaVersion"`
	Built         time.Time `json:"built"`
	Path          string    `json:"path"`
	Checksum      string    `json:"checksum"`
}

// Latest reads the listing.
func (d *DB) Latest(ctx context.Context) (Listing, error) {
	var l Listing
	if d.Offline {
		return l, ErrOffline
	}
	ctx, cancel := context.WithTimeout(ctx, listingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.ListingURL(), nil)
	if err != nil {
		return l, err
	}
	resp, err := (&http.Client{Timeout: listingTimeout}).Do(req)
	if err != nil {
		return l, fmt.Errorf("read the vulnerability database listing %s: %w", d.ListingURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return l, fmt.Errorf("read the vulnerability database listing %s: HTTP %d", d.ListingURL(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return l, fmt.Errorf("read the vulnerability database listing %s: %w", d.ListingURL(), err)
	}
	if err := json.Unmarshal(body, &l); err != nil {
		return l, fmt.Errorf("the vulnerability database listing %s is not a grype listing: %w", d.ListingURL(), err)
	}
	if l.Built.IsZero() || l.Path == "" {
		return l, fmt.Errorf("the vulnerability database listing %s names no database", d.ListingURL())
	}
	return l, nil
}

// EnsureOptions says what Ensure may do.
type EnsureOptions struct {
	// Fetch allows a download when the database is missing or past
	// RefreshAfter. Without it Ensure only reports.
	Fetch bool
	// Force reads the listing now, whatever the database's age and the
	// last check, and updates when it names a newer build (`db pull`).
	Force bool
}

// Ensure returns a database fit to scan with, refreshing it when allowed and
// due.
//
// A refresh that fails (no network, a mirror down) is not an error while the
// database on disk is still within StaleAfter: it says so on Log and the
// scan goes ahead with what is here. With no database, or a stale one, it is
// ErrNoDB or ErrStale wrapping the cause.
//
// Concurrent forge processes are serialized by the directory's lock: one
// refreshes while the others wait for it (and say so), then use its result.
//
// An Offline or ReadOnly database is never refreshed, whatever opts say: it
// is judged as it is on disk.
func (d *DB) Ensure(ctx context.Context, opts EnsureOptions) (Status, error) {
	st, err := d.Status(ctx)
	if err != nil {
		return st, err
	}
	if d.Offline || d.ReadOnly {
		opts = EnsureOptions{}
	}
	now := d.now()
	if !d.refreshDue(st, opts, now) {
		return st, usable(st, now, nil)
	}
	unlock, err := d.lock(ctx, true)
	if err != nil {
		return st, err
	}
	defer unlock()
	// Another process may have refreshed while this one waited.
	if st, err = d.statusLocked(ctx); err != nil {
		return st, err
	}
	now = d.now()
	if !d.refreshDue(st, opts, now) {
		return st, usable(st, now, nil)
	}
	latest, lerr := d.Latest(ctx)
	if lerr != nil {
		if st.Present {
			_, _ = fmt.Fprintf(d.log(), "forge: could not check for a newer vulnerability database (%v); using the one built %s\n", lerr, st.BuiltAt.Format(time.RFC3339))
		}
		return st, usable(st, now, lerr)
	}
	if err := d.writeState(state{CheckedAt: now, LatestBuiltAt: latest.Built.UTC()}); err != nil {
		return st, err
	}
	st.CheckedAt, st.LatestBuiltAt = now, latest.Built.UTC()
	if st.Present && !latest.Built.After(st.BuiltAt) {
		return st, usable(st, now, nil)
	}
	_, _ = fmt.Fprintf(d.log(), "forge: downloading the vulnerability database (grype %s, built %s, ~200 MB, once per machine per day) into %s\n",
		latest.SchemaVersion, latest.Built.UTC().Format(time.RFC3339), d.Dir)
	if _, err := d.grype(ctx, io.Discard, "db", "update"); err != nil {
		if errors.Is(err, ErrScannerMissing) {
			return st, err
		}
		if st.Present {
			_, _ = fmt.Fprintf(d.log(), "forge: the vulnerability database download failed (%v); using the one built %s\n", err, st.BuiltAt.Format(time.RFC3339))
		}
		return st, usable(st, now, err)
	}
	next, err := d.statusLocked(ctx)
	if err != nil {
		return next, err
	}
	return next, usable(next, now, nil)
}

// refreshDue reports whether Ensure should read the listing.
func (d *DB) refreshDue(st Status, opts EnsureOptions, now time.Time) bool {
	if opts.Force {
		return true
	}
	if !opts.Fetch || st.Fresh(now) {
		return false
	}
	// Past RefreshAfter (or absent): ask, unless the listing was asked
	// recently and named nothing newer than what is here.
	if st.Present && now.Sub(st.CheckedAt) < recheckAfter && !st.LatestBuiltAt.After(st.BuiltAt) {
		return false
	}
	return true
}

// usable is Ensure's verdict on st: nil when it can be scanned with, else
// ErrNoDB or ErrStale, carrying cause.
func usable(st Status, now time.Time, cause error) error {
	switch {
	case !st.Present:
		msg := "run `forge vuln db pull` (or `forge vuln db import <file>` offline)"
		if st.Problem != "" {
			msg = st.Problem + "; " + msg
		}
		if cause != nil {
			return fmt.Errorf("%w (%s): %v", ErrNoDB, msg, cause)
		}
		return fmt.Errorf("%w (%s)", ErrNoDB, msg)
	case st.Stale(now):
		msg := fmt.Sprintf("built %s, older than %s; run `forge vuln db pull`", st.BuiltAt.Format(time.RFC3339), humanDuration(StaleAfter))
		if cause != nil {
			return fmt.Errorf("%w (%s): %v", ErrStale, msg, cause)
		}
		return fmt.Errorf("%w (%s)", ErrStale, msg)
	default:
		return nil
	}
}

// Import loads a database archive from a local file (`grype db import`):
// the offline path. The archive is the file the listing names, downloaded
// on a machine with network.
func (d *DB) Import(ctx context.Context, file string) (Status, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return Status{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Status{}, fmt.Errorf("import the vulnerability database: %w", err)
	}
	if fi.IsDir() {
		return Status{}, fmt.Errorf("import the vulnerability database: %s is a directory, not a database archive", abs)
	}
	unlock, err := d.lock(ctx, true)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	_, _ = fmt.Fprintf(d.log(), "forge: importing the vulnerability database from %s into %s (about two minutes)\n", abs, d.Dir)
	if _, err := d.grype(ctx, io.Discard, "db", "import", abs); err != nil {
		return Status{}, err
	}
	return d.statusLocked(ctx)
}

// lock takes the database lock: exclusive for a refresh or an import,
// shared for a read. A waiter that cannot take it at once says so on Log,
// then blocks until it can or ctx ends.
func (d *DB) lock(ctx context.Context, exclusive bool) (func(), error) {
	if d.ReadOnly && exclusive {
		return nil, fmt.Errorf("the vulnerability database in %s is read-only here: update or import it where it is kept", d.Dir)
	}
	if err := d.prepare(); err != nil {
		return nil, err
	}
	opts := []flock.Option{}
	if d.ReadOnly {
		// Share the keeper's lock when its lock file is there, opened
		// without O_CREATE so nothing is written. When it is not (a copy,
		// a mount without it), read unlocked: a database swapped mid-read
		// makes grype fail, which is "not scanned", never a pass.
		if _, err := os.Stat(d.lockPath()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return func() {}, nil
			}
			// Any other failure (permission, I/O) is no proof the keeper is
			// not swapping the database: refuse, so the scan is unscanned.
			return nil, fmt.Errorf("lock the vulnerability database %s: %w", d.lockPath(), err)
		}
		opts = append(opts, flock.SetFlag(os.O_RDONLY))
	}
	l := flock.New(d.lockPath(), opts...)
	try := l.TryRLock
	tryCtx := l.TryRLockContext
	if exclusive {
		try, tryCtx = l.TryLock, l.TryLockContext
	}
	ok, err := try()
	if err != nil {
		// Read-only or not, a lock that cannot be taken is not a lock: the
		// caller reports the scan unscanned rather than read a database
		// that may be mid-replacement.
		return nil, fmt.Errorf("lock the vulnerability database %s: %w", d.lockPath(), err)
	}
	if !ok {
		_, _ = fmt.Fprintf(d.log(), "forge: waiting for another forge process using the vulnerability database in %s\n", d.Dir)
		if ok, err = tryCtx(ctx, lockPoll); err != nil || !ok {
			if err == nil {
				err = ctx.Err()
			}
			return nil, fmt.Errorf("lock the vulnerability database %s: %w", d.lockPath(), err)
		}
	}
	return func() { _ = l.Unlock() }, nil
}

// humanDuration spells a whole number of days or hours.
func humanDuration(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%d hours", int(d/time.Hour))
}
