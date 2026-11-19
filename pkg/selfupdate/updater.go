package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// SwapMode is how the swapped binary starts.
type SwapMode int

const (
	// ExecInPlace execs the new binary in this process: same pid, so
	// systemd, launchd and a parent like Electron see no restart. Unix only.
	ExecInPlace SwapMode = iota
	// ExitForSupervisor swaps the binary, then exits with [ExitCodeRestart]
	// for a supervisor to start the new one.
	ExitForSupervisor
)

func (m SwapMode) String() string {
	switch m {
	case ExecInPlace:
		return "exec-in-place"
	case ExitForSupervisor:
		return "exit-for-supervisor"
	}
	return "unknown"
}

// ExitCodeRestart is the exit status of [ExitForSupervisor]: EX_TEMPFAIL,
// "try again", which a restart-on-failure supervisor restarts.
const ExitCodeRestart = 75

// Defaults for the zero fields of [Config].
const (
	DefaultConfirmWithin = 60 * time.Second
	DefaultProbeTimeout  = 10 * time.Second
	DefaultDrainTimeout  = 5 * time.Minute
	DefaultChecksums     = "checksums.txt"
	DefaultMaxDownload   = 512 << 20
)

// Config is an [Updater]'s whole configuration. Binary, Current and Source
// are required, and so is a Verifier unless InsecureAllowUnsigned says
// otherwise.
type Config struct {
	// Binary names the program, for the default state directory, the default
	// archive names and logs ("reliant").
	Binary string
	// Current is the running build.
	Current Build
	// Source finds the target release; Channel is passed to it.
	Source  Source
	Channel string

	// Verifier checks the downloaded files against the release's signature.
	// Required unless InsecureAllowUnsigned.
	Verifier Verifier
	// InsecureAllowUnsigned installs releases on the sha256 of an UNSIGNED
	// checksums.txt alone, which detects corruption but not tampering: whoever
	// can replace one release asset can replace both. Logged at every start.
	InsecureAllowUnsigned bool

	// Policy decides what to do about a newer release; default [StageOnly].
	Policy Policy
	// Drainer finishes in-flight work before a swap. Nil applies at once.
	Drainer Drainer
	// Shutdown asks the app to tear down (cancel its root context); after
	// teardown the app's main calls [Updater.ExecPending]. Required to apply.
	Shutdown func()

	// Probe is the argv (after the program) the staged binary must exit 0
	// on; default {"version"}.
	Probe        []string
	ProbeTimeout time.Duration
	// StateDir holds the lock, staged releases, the previous binary and
	// state.json. Machine-level, because several instances may share one
	// binary; default [DefaultStateDir].
	StateDir string
	// ConfirmWithin is how long a swapped binary has to call
	// [Pending.Confirm] before it is reverted; default 60s.
	ConfirmWithin time.Duration
	// DrainTimeout is the drain deadline when a Decision carries none;
	// default 5 minutes.
	DrainTimeout time.Duration
	SwapMode     SwapMode

	// ArchiveName names the release asset for a platform; default
	// "<Binary>_<goos>_<goarch>.tar.gz", forge's own naming. A name without
	// .tar.gz, .tgz or .zip is the binary itself.
	ArchiveName func(goos, goarch string) string
	// BinaryName is the binary's file name inside the archive; default
	// Binary, plus ".exe" on Windows.
	BinaryName string
	// ChecksumsName is the release's sha256 list; default "checksums.txt".
	ChecksumsName string
	// HTTPClient downloads; default an observe.NewHTTPClient with no overall
	// timeout (the context bounds each download). Only https URLs are fetched.
	HTTPClient       *http.Client
	MaxDownloadBytes int64

	// Managed is the app's own "something else runs me" predicate, passed to
	// [Detect].
	Managed func() (owner string, managed bool)
	Logger  *slog.Logger
}

// Staged is a release verified, extracted and probed under StateDir.
type Staged struct {
	Release Release
	// Path is the staged binary.
	Path   string
	SHA256 string
}

// Updater checks for, stages and applies releases of one binary.
type Updater struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	pending *Staged

	// Seams for tests: the binary being replaced, the clock, how to back it
	// up, and how to exec or exit.
	exePath  string
	now      func() time.Time
	backupFn func(src, dir, label string, now time.Time) (string, error)
	execFn   func(path string, argv, env []string) error
	exitFn   func(code int)
}

// DefaultStateDir is ~/.<binary>/upgrade.
func DefaultStateDir(binary string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("selfupdate: no home directory for the default state dir: %w", err)
	}
	return filepath.Join(home, "."+binary, "upgrade"), nil
}

// ErrNoVerifier is returned by [New] for a Config with neither a Verifier
// nor InsecureAllowUnsigned.
var ErrNoVerifier = errors.New("selfupdate: Config.Verifier is required: unsigned releases are refused unless Config.InsecureAllowUnsigned is set")

// New validates cfg and fills its defaults.
func New(cfg Config) (*Updater, error) {
	switch {
	case cfg.Binary == "":
		return nil, errors.New("selfupdate: Config.Binary is required")
	case cfg.Source == nil:
		return nil, errors.New("selfupdate: Config.Source is required")
	case cfg.Current.Version == "" && cfg.Current.CommitTime.IsZero():
		return nil, errors.New("selfupdate: Config.Current needs a Version or a CommitTime")
	case cfg.Verifier == nil && !cfg.InsecureAllowUnsigned:
		return nil, ErrNoVerifier
	case cfg.Verifier != nil && cfg.InsecureAllowUnsigned:
		return nil, errors.New("selfupdate: Config.Verifier and Config.InsecureAllowUnsigned are both set; choose one")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Policy == nil {
		cfg.Policy = StageOnly{}
	}
	if len(cfg.Probe) == 0 {
		cfg.Probe = []string{"version"}
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = DefaultProbeTimeout
	}
	if cfg.ConfirmWithin <= 0 {
		cfg.ConfirmWithin = DefaultConfirmWithin
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = DefaultDrainTimeout
	}
	if cfg.StateDir == "" {
		dir, err := DefaultStateDir(cfg.Binary)
		if err != nil {
			return nil, err
		}
		cfg.StateDir = dir
	}
	if cfg.ArchiveName == nil {
		binary := cfg.Binary
		cfg.ArchiveName = func(goos, goarch string) string {
			return binary + "_" + goos + "_" + goarch + ".tar.gz"
		}
	}
	if cfg.BinaryName == "" {
		cfg.BinaryName = cfg.Binary
		if runtime.GOOS == "windows" {
			cfg.BinaryName += ".exe"
		}
	}
	if cfg.ChecksumsName == "" {
		cfg.ChecksumsName = DefaultChecksums
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = observe.NewHTTPClient(observe.HTTPClientOptions{})
	}
	if cfg.MaxDownloadBytes <= 0 {
		cfg.MaxDownloadBytes = DefaultMaxDownload
	}
	if cfg.SwapMode == ExecInPlace && runtime.GOOS == "windows" {
		cfg.Logger.Warn("selfupdate: exec in place is not available on windows; swaps are not supported yet", "binary", cfg.Binary)
	}
	if cfg.InsecureAllowUnsigned {
		cfg.Logger.Warn("selfupdate: INSECURE: release signatures are not verified (Config.InsecureAllowUnsigned); a tampered release would be installed",
			"binary", cfg.Binary)
	}
	return &Updater{cfg: cfg, log: cfg.Logger, now: time.Now, backupFn: backupBinary, execFn: execBinary, exitFn: os.Exit}, nil
}

// Check asks the Source for the channel's release and decides what to do.
// A release that is not newer than the running build, or that was reverted
// on this machine before, is skipped without asking the Policy.
func (u *Updater) Check(ctx context.Context) (Release, Decision, error) {
	rel, err := u.cfg.Source.Latest(ctx, u.cfg.Channel)
	if err != nil {
		return Release{}, Decision{}, fmt.Errorf("selfupdate: find the %q release: %w", u.cfg.Channel, err)
	}
	isNewer, known := newer(u.cfg.Current, rel)
	switch {
	case !known:
		return rel, Decision{Action: Skip, Reason: fmt.Sprintf("cannot order running %q against %q: neither semver nor commit times", u.cfg.Current.Version, rel.Version)}, nil
	case !isNewer:
		return rel, Decision{Action: Skip, Reason: "not newer than the running " + u.cfg.Current.Version}, nil
	}
	st, err := u.readState(ctx)
	if err != nil {
		return rel, Decision{}, err
	}
	if f, ok := st.failed(rel.Version); ok {
		return rel, Decision{Action: Skip, Reason: fmt.Sprintf("%s was reverted here at %s (%s); waiting for a newer release", rel.Version, f.At.Format(time.RFC3339), f.Reason)}, nil
	}
	return rel, u.cfg.Policy.Decide(ctx, u.cfg.Current, rel), nil
}

// detect runs Detect against the binary this Updater replaces.
func (u *Updater) detect() (Install, error) {
	return Detect(DetectOptions{Path: u.exePath, Managed: u.cfg.Managed})
}

// requireSelfOwned refuses an install something else owns.
func (u *Updater) requireSelfOwned() (Install, error) {
	in, err := u.detect()
	if err != nil {
		return Install{}, err
	}
	if !in.SelfOwned() {
		return in, &NotSelfOwnedError{Install: in}
	}
	return in, nil
}

func (u *Updater) readState(ctx context.Context) (stateFile, error) {
	var st stateFile
	err := withLock(ctx, u.cfg.StateDir, func() error {
		var err error
		st, err = loadState(u.cfg.StateDir)
		return err
	})
	return st, err
}
