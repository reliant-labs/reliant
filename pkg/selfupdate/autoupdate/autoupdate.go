// Package autoupdate is the opt-in, default-off wiring of [selfupdate] for a
// long-lived forge-built binary: one [Updater] built from a few [Settings]
// fields that runs Boot at construction, confirms a freshly swapped binary
// once the app reports ready, checks for a newer signed release on an
// interval, stages and applies it through the app's graceful shutdown, and
// execs the new binary after that shutdown.
//
// A server on pkg/serverkit hands the Updater to Server.SelfUpdate and
// serverkit does the rest: readiness is its /readyz verdict, the drain is its
// graceful shutdown, and it calls ExecPending after its teardown. Any other
// binary runs [Updater.Serve] beside its own loop and calls
// [Updater.ExecPending] as the last act of main.
//
// Settings are plain values so the app decides where they come from — its
// typed config (flags, environment, config file), never ambient variables
// read here. The design is docs/proposals/self-upgrade.md, §3 and §12.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/reliant-labs/forge/pkg/selfupdate/sigstore"
)

// Defaults for the zero fields of [Settings].
const (
	DefaultInterval = 6 * time.Hour
	DefaultChannel  = "latest"
	// MinInterval is the shortest check interval Settings accepts.
	MinInterval = time.Minute
)

// firstCheck is how long after Serve starts the first check runs: soon
// enough that a long-lived process picks up a release without waiting a
// whole interval, late enough that a crash loop does not hammer the source.
const firstCheck = 2 * time.Minute

// confirmPoll is how often Serve asks the app's readiness while a swapped
// binary waits for confirmation.
const confirmPoll = time.Second

// Settings is the whole opt-in. The zero value is off.
type Settings struct {
	// Enabled turns self-update on. Off, the Updater still confirms a binary
	// an earlier run swapped in (so turning it off never strands a pending
	// revert) but never checks for, downloads or applies a release.
	Enabled bool
	// Source is where releases are found: an https URL of a channel manifest
	// (selfupdate.ManifestSource, the schema reliant's daemon-channels.json
	// uses), or a GitHub repository "owner/name" whose releases are read.
	Source string
	// Channel is the manifest's channel, or GitHub's "latest" (default) or
	// "prerelease".
	Channel string
	// Interval between checks; default 6h, at least 1m. Each wait is
	// jittered by up to a tenth so a fleet does not check in step.
	Interval time.Duration

	// SignerRepository is the "owner/name" whose release workflow signs the
	// releases, and which the signed predicate must name. Default: Source,
	// when Source is a GitHub repository; required with a manifest.
	SignerRepository string
	// SignerWorkflow is the release workflow's file under
	// .github/workflows/ ("release.yml"). The certificate must be that
	// workflow at a semver release tag. Required unless SignerIdentity or
	// InsecureAllowUnsigned is set.
	SignerWorkflow string
	// SignerIdentity overrides the identity SignerWorkflow derives: an
	// anchored (^…$) regular expression the certificate's subject must match.
	SignerIdentity string
	// PredicateType is the in-toto predicate type the workflow signs;
	// default https://github.com/<SignerRepository>/attestations/release-assets/v1,
	// the convention forge's and reliant's release workflows use.
	PredicateType string
	// Bundle names a GitHub release's signature asset; default
	// "<binary>-release.sigstore.json". A manifest names its own.
	Bundle string

	// InsecureAllowUnsigned installs releases without verifying any
	// signature: only the sha256 of an unsigned checksums.txt is checked,
	// which detects corruption and not tampering. Logged at every start.
	// Never set it together with a signer.
	InsecureAllowUnsigned bool
}

// Options is what the app supplies beside its Settings.
type Options struct {
	Settings Settings
	// Binary names the program ("acme"): state paths, archive names, logs.
	Binary string
	// Current is the running build, from the binary's version stamp. A
	// build with neither a semver Version nor a CommitTime ("dev") is never
	// upgraded.
	Current selfupdate.Build
	// StateDir is selfupdate's state directory; default
	// selfupdate.DefaultStateDir(Binary).
	StateDir string
	Logger   *slog.Logger

	// Policy decides about each newer release; default: apply it at
	// routine urgency (opting in means applying).
	Policy selfupdate.Policy
	// Drainer finishes the app's in-flight work before the swap. A
	// serverkit server leaves it nil: its graceful shutdown is the drain.
	Drainer selfupdate.Drainer
	// Managed is the app's own "something else runs me" predicate.
	Managed func() (owner string, managed bool)
	// HTTPClient downloads; default selfupdate's.
	HTTPClient *http.Client
	// TUFCachePath caches the Sigstore trusted root's TUF metadata.
	TUFCachePath string
	// Tune, when set, edits the selfupdate.Config just before it is built:
	// archive naming, swap mode, confirm window, probe — everything Settings
	// does not name.
	Tune func(*selfupdate.Config)
}

// core is the part of *selfupdate.Updater Serve drives; a seam for tests.
type core interface {
	Check(ctx context.Context) (selfupdate.Release, selfupdate.Decision, error)
	Prepare(ctx context.Context, rel selfupdate.Release) (selfupdate.Staged, error)
	Request(ctx context.Context, s selfupdate.Staged, d selfupdate.Decision) error
	ExecPending() error
}

// Updater runs one binary's self-update. Build it with [New].
type Updater struct {
	log      *slog.Logger
	binary   string
	settings Settings

	core    core // nil when off
	pending *selfupdate.Pending
	// swapped and confirm are the pending swap Boot found; seams for tests.
	swapped bool
	confirm func() error
	detect  func() (selfupdate.Install, error)

	confirmed atomic.Bool
	shutdown  atomic.Pointer[func()]
	serving   sync.Mutex

	after  func(time.Duration) <-chan time.Time
	jitter func(time.Duration) time.Duration
}

// New runs selfupdate.Boot (so a swapped binary's revert watchdog starts as
// early as the app builds this), validates the Settings and builds the
// Updater. With Settings.Enabled false it never fails on the other fields
// and returns an Updater that only confirms. Call it as early in main as the
// Settings are known, before slow composition.
func New(opts Options) (*Updater, error) {
	if opts.Binary == "" {
		return nil, errors.New("autoupdate: Options.Binary is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	log := opts.Logger
	p := selfupdate.Boot(selfupdate.BootOptions{Binary: opts.Binary, StateDir: opts.StateDir, Logger: log})
	u := &Updater{
		log:      log,
		binary:   opts.Binary,
		settings: opts.Settings,
		pending:  p,
		swapped:  p.Swapped,
		confirm:  p.Confirm,
		detect: func() (selfupdate.Install, error) {
			return selfupdate.Detect(selfupdate.DetectOptions{Managed: opts.Managed})
		},
		after:  time.After,
		jitter: jitterTenth,
	}
	if !opts.Settings.Enabled {
		return u, nil
	}
	cfg, err := opts.config()
	if err != nil {
		return nil, err
	}
	// Shutdown is Serve's: it calls the shutdown func the app handed it.
	cfg.Shutdown = u.requestShutdown
	c, err := selfupdate.New(cfg.Config)
	if err != nil {
		return nil, fmt.Errorf("autoupdate: %w", err)
	}
	u.core = c
	u.settings.Channel, u.settings.Interval = cfg.Channel, cfg.interval
	return u, nil
}

// built carries the interval beside the selfupdate.Config.
type built struct {
	selfupdate.Config
	interval time.Duration
}

func (o Options) config() (built, error) {
	s := o.Settings
	if s.Channel == "" {
		s.Channel = DefaultChannel
	}
	interval := s.Interval
	switch {
	case interval == 0:
		interval = DefaultInterval
	case interval < MinInterval:
		return built{}, fmt.Errorf("autoupdate: Settings.Interval %s is below the %s minimum", interval, MinInterval)
	}
	src, ghRepo, err := s.source(o.Binary)
	if err != nil {
		return built{}, err
	}
	cfg := selfupdate.Config{
		Binary:                o.Binary,
		Current:               o.Current,
		Source:                src,
		Channel:               s.Channel,
		InsecureAllowUnsigned: s.InsecureAllowUnsigned,
		Policy:                o.Policy,
		Drainer:               o.Drainer,
		StateDir:              o.StateDir,
		HTTPClient:            o.HTTPClient,
		Managed:               o.Managed,
		Logger:                o.Logger,
	}
	if cfg.Policy == nil {
		cfg.Policy = applyNewer{}
	}
	signed := s.SignerWorkflow != "" || s.SignerIdentity != ""
	switch {
	case s.InsecureAllowUnsigned && signed:
		return built{}, errors.New("autoupdate: Settings.InsecureAllowUnsigned is set together with a signer; choose one")
	case !s.InsecureAllowUnsigned && !signed:
		return built{}, errors.New("autoupdate: Settings.SignerWorkflow (or SignerIdentity) is required: unsigned releases are refused unless Settings.InsecureAllowUnsigned is set")
	case signed:
		v, err := s.verifier(ghRepo, o.TUFCachePath)
		if err != nil {
			return built{}, err
		}
		cfg.Verifier = v
	}
	if o.Tune != nil {
		o.Tune(&cfg)
	}
	return built{Config: cfg, interval: interval}, nil
}

var (
	repoRE     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	workflowRE = regexp.MustCompile(`^[A-Za-z0-9._-]+\.ya?ml$`)
)

// source builds the selfupdate.Source Settings.Source names, and returns the
// GitHub repository when it is one.
func (s Settings) source(binary string) (selfupdate.Source, string, error) {
	switch src := strings.TrimSpace(s.Source); {
	case src == "":
		return nil, "", errors.New("autoupdate: Settings.Source is required: a channel manifest's https URL or a GitHub repository owner/name")
	case strings.HasPrefix(src, "https://"):
		repo := s.SignerRepository
		if !repoRE.MatchString(repo) {
			return nil, "", fmt.Errorf("autoupdate: Settings.SignerRepository must be owner/name with a manifest source (the repository the manifest must name), got %q", repo)
		}
		return selfupdate.ManifestSource{URL: src, Repository: repo}, "", nil
	case strings.Contains(src, "://"):
		return nil, "", fmt.Errorf("autoupdate: Settings.Source %q: a manifest URL must be https", src)
	case repoRE.MatchString(src):
		bundle := s.Bundle
		if bundle == "" {
			bundle = binary + "-release.sigstore.json"
		}
		return selfupdate.GitHubSource{Repo: src, Bundle: bundle}, src, nil
	default:
		return nil, "", fmt.Errorf("autoupdate: Settings.Source %q is neither an https manifest URL nor a GitHub repository owner/name", src)
	}
}

// verifier builds the Sigstore verifier for the configured signer.
func (s Settings) verifier(ghRepo, tufCache string) (selfupdate.Verifier, error) {
	repo := s.SignerRepository
	if repo == "" {
		repo = ghRepo
	}
	if !repoRE.MatchString(repo) {
		return nil, fmt.Errorf("autoupdate: Settings.SignerRepository must be owner/name, got %q", repo)
	}
	identity := s.SignerIdentity
	if identity == "" {
		wf := strings.TrimPrefix(s.SignerWorkflow, ".github/workflows/")
		if !workflowRE.MatchString(wf) {
			return nil, fmt.Errorf("autoupdate: Settings.SignerWorkflow must be a workflow file name such as release.yml, got %q", s.SignerWorkflow)
		}
		identity = SignerIdentity(repo, wf)
	}
	predicate := s.PredicateType
	if predicate == "" {
		predicate = "https://github.com/" + repo + "/attestations/release-assets/v1"
	}
	v, err := sigstore.New(sigstore.Config{Repo: repo, Identity: identity, PredicateType: predicate, TUFCachePath: tufCache})
	if err != nil {
		return nil, fmt.Errorf("autoupdate: %w", err)
	}
	return v, nil
}

// SignerIdentity is the certificate identity Settings.SignerWorkflow
// derives: workflow (a file under .github/workflows/) of repo, run at a
// semver release tag.
func SignerIdentity(repo, workflow string) string {
	return `^https://github\.com/` + regexp.QuoteMeta(repo) + `/\.github/workflows/` + regexp.QuoteMeta(workflow) +
		`@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
}

// applyNewer is the default Policy: opting in means applying.
type applyNewer struct{}

func (applyNewer) Decide(context.Context, selfupdate.Build, selfupdate.Release) selfupdate.Decision {
	return selfupdate.Decision{Action: selfupdate.Apply, Urgency: selfupdate.Routine, Reason: "self-update is enabled"}
}

// Enabled reports whether the Settings turned self-update on.
func (u *Updater) Enabled() bool { return u.core != nil }

// Pending is what Boot found when New ran.
func (u *Updater) Pending() *selfupdate.Pending { return u.pending }

func (u *Updater) requestShutdown() {
	if f := u.shutdown.Load(); f != nil {
		(*f)()
	}
}

// Serve runs until ctx ends, or until it applied a release and called
// shutdown. It confirms a freshly swapped binary once ready returns nil
// (nil ready: at once), and, when enabled on an install this binary owns,
// checks for a newer release on the interval: a Stage decision stages it,
// an Apply decision stages it, drains through Options.Drainer and calls
// shutdown, after which the app tears down and calls [Updater.ExecPending].
// Errors are logged and retried at the next interval; Serve itself only
// returns nil. It never runs twice at once.
func (u *Updater) Serve(ctx context.Context, ready func(context.Context) error, shutdown func()) error {
	u.serving.Lock()
	defer u.serving.Unlock()
	if shutdown != nil {
		u.shutdown.Store(&shutdown)
	}
	var wg sync.WaitGroup
	if u.swapped {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.confirmWhenReady(ctx, ready)
		}()
	} else {
		u.confirmed.Store(true)
	}
	defer wg.Wait()

	if u.core == nil {
		<-ctx.Done()
		return nil
	}
	in, err := u.detect()
	switch {
	case err != nil:
		u.log.WarnContext(ctx, "selfupdate: off: cannot inspect the running binary", "binary", u.binary, "err", err)
		<-ctx.Done()
		return nil
	case !in.SelfOwned():
		attrs := []any{"binary", u.binary, "path", in.Path, "owner_kind", in.Kind.String(), "reason", in.Reason}
		if in.Owner != "" {
			attrs = append(attrs, "owner", in.Owner)
		}
		if in.Hint != "" {
			attrs = append(attrs, "update_with", in.Hint)
		}
		u.log.InfoContext(ctx, "selfupdate: off: something else owns this install and updates it", attrs...)
		<-ctx.Done()
		return nil
	}
	u.log.InfoContext(ctx, "selfupdate: on", "binary", u.binary, "source", u.settings.Source,
		"channel", u.settings.Channel, "interval", u.settings.Interval, "path", in.Path)

	wait := firstCheck
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-u.after(u.jitter(wait)):
		}
		wait = u.settings.Interval
		if !u.confirmed.Load() {
			continue // never stack an upgrade on an unconfirmed one
		}
		applied, stop := u.attempt(ctx)
		if applied || stop {
			if stop {
				<-ctx.Done()
			}
			return nil
		}
	}
}

// attempt runs one check. applied: a swap is armed and shutdown was called.
// stop: this install can never upgrade in this process; stop checking.
func (u *Updater) attempt(ctx context.Context) (applied, stop bool) {
	rel, d, err := u.core.Check(ctx)
	if err != nil {
		u.log.WarnContext(ctx, "selfupdate: check failed; retrying at the next interval", "binary", u.binary, "err", err)
		return false, false
	}
	if d.Action == selfupdate.Skip {
		u.log.DebugContext(ctx, "selfupdate: nothing to do", "binary", u.binary, "release", rel.Version, "reason", d.Reason)
		return false, false
	}
	staged, err := u.core.Prepare(ctx, rel)
	if err != nil {
		if ctx.Err() == nil {
			u.log.WarnContext(ctx, "selfupdate: staging failed; retrying at the next interval", "binary", u.binary, "release", rel.Version, "err", err)
		}
		return false, errors.Is(err, selfupdate.ErrNotSelfOwned)
	}
	if d.Action != selfupdate.Apply {
		u.log.InfoContext(ctx, "selfupdate: staged", "binary", u.binary, "release", rel.Version, "reason", d.Reason)
		return false, false
	}
	if err := u.core.Request(ctx, staged, d); err != nil {
		unsupported := errors.Is(err, selfupdate.ErrUnsupported) || errors.Is(err, selfupdate.ErrNotSelfOwned)
		u.log.WarnContext(ctx, "selfupdate: apply declined", "binary", u.binary, "release", rel.Version, "err", err, "will_retry", !unsupported)
		return false, unsupported
	}
	return true, false
}

// confirmWhenReady confirms the swapped binary once ready passes. If it never
// does, Boot's watchdog reverts the swap.
func (u *Updater) confirmWhenReady(ctx context.Context, ready func(context.Context) error) {
	for {
		if ready == nil || ready(ctx) == nil {
			// A readiness check may ignore cancellation and return success late.
			// Leave the swap unconfirmed so the watchdog can still revert it.
			if ctx.Err() != nil {
				return
			}
			err := u.confirm()
			if err == nil {
				u.confirmed.Store(true)
				return
			}
			if ctx.Err() == nil {
				u.log.WarnContext(ctx, "selfupdate: confirm failed; retrying", "binary", u.binary, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-u.after(confirmPoll):
		}
	}
}

// ExecPending performs a swap Serve armed; call it after the app's
// teardown, as the last act of main. It returns nil at once when nothing is
// armed and otherwise returns only on failure (the old binary is back).
func (u *Updater) ExecPending() error {
	if u.core == nil {
		return nil
	}
	return u.core.ExecPending()
}

// jitterTenth spreads d by up to ±10%.
func jitterTenth(d time.Duration) time.Duration {
	span := int64(d) / 10
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*span+1)-span)
}
