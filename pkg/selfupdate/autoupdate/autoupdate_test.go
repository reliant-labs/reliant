package autoupdate

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// serverkit declares what it needs of a self-updater; Updater is it.
var _ serverkit.SelfUpdater = (*Updater)(nil)

// fakeClock hands out timers the test fires by hand: every After call is
// reported on asked, and its channel fires only when the test fires it.
type fakeClock struct{ asked chan timer }

type timer struct {
	d  time.Duration
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{asked: make(chan timer, 64)} }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	tm := timer{d: d, ch: make(chan time.Time, 1)}
	c.asked <- tm
	return tm.ch
}

// next waits for the next After call and returns its duration and a func
// that fires it.
func (c *fakeClock) next(t *testing.T) (time.Duration, func()) {
	t.Helper()
	select {
	case tm := <-c.asked:
		return tm.d, func() { tm.ch <- time.Time{} }
	case <-time.After(10 * time.Second):
		t.Fatal("Serve never waited on the clock")
		return 0, nil
	}
}

// fakeCore records what Serve asked of selfupdate.
type fakeCore struct {
	mu       sync.Mutex
	decision selfupdate.Decision
	checkErr error
	prepErr  error
	reqErr   error
	checks   int
	prepares int
	requests int
	execs    int
	shutdown func()
}

func (f *fakeCore) Check(context.Context) (selfupdate.Release, selfupdate.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return selfupdate.Release{Version: "2.0.0", Tag: "v2.0.0"}, f.decision, f.checkErr
}

func (f *fakeCore) Prepare(_ context.Context, rel selfupdate.Release) (selfupdate.Staged, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	return selfupdate.Staged{Release: rel}, f.prepErr
}

func (f *fakeCore) Request(context.Context, selfupdate.Staged, selfupdate.Decision) error {
	f.mu.Lock()
	f.requests++
	err, shut := f.reqErr, f.shutdown
	f.mu.Unlock()
	if err == nil && shut != nil {
		shut() // what selfupdate.Updater.Request does: Config.Shutdown
	}
	return err
}

func (f *fakeCore) ExecPending() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs++
	return nil
}

func (f *fakeCore) counts() (checks, prepares, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.prepares, f.requests
}

func selfOwned() (selfupdate.Install, error) {
	return selfupdate.Install{Path: "/home/u/.local/bin/app", Kind: selfupdate.SelfOwned}, nil
}

// testUpdater is an enabled Updater over fakes.
func testUpdater(t *testing.T, c *fakeCore, clk *fakeClock) *Updater {
	t.Helper()
	u := &Updater{
		log:      testkit.DiscardLogger(),
		binary:   "app",
		settings: Settings{Enabled: true, Source: "o/r", Channel: "latest", Interval: time.Hour},
		core:     c,
		confirm:  func() error { return nil },
		detect:   selfOwned,
		after:    clk.After,
		jitter:   func(d time.Duration) time.Duration { return d },
	}
	c.shutdown = u.requestShutdown
	return u
}

func serve(u *Updater, ready func(context.Context) error, shutdown func()) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- u.Serve(ctx, ready, shutdown) }()
	return cancel, done
}

func waitDone(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestServeAppliesANewerRelease(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	c := &fakeCore{decision: selfupdate.Decision{Action: selfupdate.Apply}}
	u := testUpdater(t, c, clk)
	shut := make(chan struct{})
	cancel, done := serve(u, nil, func() { close(shut) })
	defer cancel()

	d, fire := clk.next(t)
	if d != firstCheck {
		t.Fatalf("first wait = %s, want %s", d, firstCheck)
	}
	fire()
	<-shut // Request called the app's shutdown
	waitDone(t, done)
	if checks, prepares, requests := c.counts(); checks != 1 || prepares != 1 || requests != 1 {
		t.Fatalf("checks=%d prepares=%d requests=%d, want 1 each", checks, prepares, requests)
	}
	if err := u.ExecPending(); err != nil || c.execs != 1 {
		t.Fatalf("ExecPending: err=%v execs=%d", err, c.execs)
	}
}

func TestServeStagesAndRetriesOnTheInterval(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	c := &fakeCore{decision: selfupdate.Decision{Action: selfupdate.Stage}, checkErr: nil}
	u := testUpdater(t, c, clk)
	cancel, done := serve(u, nil, func() { t.Error("a Stage decision must not shut down") })

	_, fire := clk.next(t)
	fire()
	d, fire := clk.next(t)
	if d != time.Hour {
		t.Fatalf("second wait = %s, want the interval", d)
	}
	c.mu.Lock()
	c.checkErr = errors.New("source down")
	c.mu.Unlock()
	fire()
	_, _ = clk.next(t) // a failed check waits for the next interval too
	cancel()
	waitDone(t, done)
	if checks, prepares, requests := c.counts(); checks != 2 || prepares != 1 || requests != 0 {
		t.Fatalf("checks=%d prepares=%d requests=%d, want 2/1/0", checks, prepares, requests)
	}
}

func TestServeStopsWhenTheSwapCannotHappenHere(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	c := &fakeCore{decision: selfupdate.Decision{Action: selfupdate.Apply}, reqErr: selfupdate.ErrUnsupported}
	u := testUpdater(t, c, clk)
	cancel, done := serve(u, nil, func() { t.Error("an unsupported swap must not shut down") })
	_, fire := clk.next(t)
	fire()
	for {
		if _, _, requests := c.counts(); requests == 1 {
			break
		}
		select {
		case <-done:
			t.Fatal("Serve returned before ctx ended")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	waitDone(t, done)
	// Serve has returned, so every timer it ever asked for is in asked.
	if n := len(clk.asked); n != 0 {
		t.Fatalf("Serve kept checking (%d more waits) after ErrUnsupported", n)
	}
}

func TestServeIsOffForAnInstallItDoesNotOwn(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	c := &fakeCore{decision: selfupdate.Decision{Action: selfupdate.Apply}}
	u := testUpdater(t, c, clk)
	u.detect = func() (selfupdate.Install, error) {
		return selfupdate.Install{Path: "/opt/homebrew/bin/app", Kind: selfupdate.PackageManager, Owner: "homebrew", Hint: "brew upgrade"}, nil
	}
	cancel, done := serve(u, nil, nil)
	cancel()
	waitDone(t, done)
	if checks, _, _ := c.counts(); checks != 0 {
		t.Fatalf("checked %d times on a package-manager install", checks)
	}
}

func TestServeConfirmsASwapOnlyOnceReady(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	c := &fakeCore{decision: selfupdate.Decision{Action: selfupdate.Stage}}
	u := testUpdater(t, c, clk)
	u.swapped = true
	confirmed := make(chan struct{})
	u.confirm = func() error { close(confirmed); return nil }

	var mu sync.Mutex
	readyErr := errors.New("database unreachable")
	ready := func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		return readyErr
	}
	cancel, done := serve(u, ready, nil)
	defer cancel()

	// Two timers are live: the first check and the confirm poll. Fire the
	// check while unconfirmed: it must not touch the release.
	var fireCheck, firePoll func()
	for range 2 {
		d, fire := clk.next(t)
		if d == firstCheck {
			fireCheck = fire
		} else if d == confirmPoll {
			firePoll = fire
		}
	}
	if fireCheck == nil || firePoll == nil {
		t.Fatal("want one check timer and one confirm poll")
	}
	fireCheck()
	if d, _ := clk.next(t); d != time.Hour {
		t.Fatalf("after an unconfirmed tick, wait = %s, want the interval", d)
	}
	if checks, _, _ := c.counts(); checks != 0 {
		t.Fatal("Serve checked for a release while the running binary was unconfirmed")
	}
	select {
	case <-confirmed:
		t.Fatal("confirmed while not ready")
	default:
	}

	mu.Lock()
	readyErr = nil
	mu.Unlock()
	firePoll()
	select {
	case <-confirmed:
	case <-time.After(10 * time.Second):
		t.Fatal("never confirmed once ready")
	}
	cancel()
	waitDone(t, done)
}

func TestConfirmWhenReadyDoesNotConfirmAfterDeadline(t *testing.T) {
	t.Parallel()
	for name, withReady := range map[string]bool{"nil ready": false, "late ready": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			confirmed := false
			u := &Updater{confirm: func() error { confirmed = true; return nil }, after: newFakeClock().After}
			var ready func(context.Context) error
			if withReady {
				ready = func(ctx context.Context) error {
					// Simulate a check that ignores its deadline and reports success late.
					<-ctx.Done()
					return nil
				}
			} else {
				<-ctx.Done()
			}
			u.confirmWhenReady(ctx, ready)
			if confirmed || u.confirmed.Load() {
				t.Fatal("confirmed a swap after the readiness deadline")
			}
		})
	}
}

func TestServeConfirmsEvenWhenDisabled(t *testing.T) {
	t.Parallel()
	confirmed := make(chan struct{})
	u := &Updater{log: testkit.DiscardLogger(), binary: "app", swapped: true,
		confirm: func() error { close(confirmed); return nil }, after: newFakeClock().After}
	cancel, done := serve(u, func(context.Context) error { return nil }, nil)
	select {
	case <-confirmed:
	case <-time.After(10 * time.Second):
		t.Fatal("a disabled updater must still confirm a pending swap")
	}
	cancel()
	waitDone(t, done)
	if u.Enabled() || u.ExecPending() != nil {
		t.Fatal("a disabled updater has nothing to exec")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	base := func(s Settings) Options {
		return Options{Settings: s, Binary: "app", Current: selfupdate.Build{Version: "1.0.0"},
			StateDir: filepath.Join(t.TempDir(), "state"), Logger: testkit.DiscardLogger()}
	}

	t.Run("off needs nothing else", func(t *testing.T) {
		t.Parallel()
		u, err := New(base(Settings{Source: "not a source"}))
		if err != nil || u.Enabled() {
			t.Fatalf("u=%v err=%v", u, err)
		}
	})

	refused := map[string]Settings{
		"no source":            {Enabled: true, SignerWorkflow: "release.yml"},
		"http manifest":        {Enabled: true, Source: "http://x/m.json", SignerRepository: "o/r", SignerWorkflow: "release.yml"},
		"bad source":           {Enabled: true, Source: "o/r/x", SignerWorkflow: "release.yml"},
		"manifest no repo":     {Enabled: true, Source: "https://x/m.json", SignerWorkflow: "release.yml"},
		"no signer":            {Enabled: true, Source: "o/r"},
		"signer and insecure":  {Enabled: true, Source: "o/r", SignerWorkflow: "release.yml", InsecureAllowUnsigned: true},
		"bad workflow":         {Enabled: true, Source: "o/r", SignerWorkflow: "../release.yml"},
		"unanchored identity":  {Enabled: true, Source: "o/r", SignerIdentity: "release.yml"},
		"interval below floor": {Enabled: true, Source: "o/r", SignerWorkflow: "release.yml", Interval: time.Second},
	}
	for name, s := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(base(s)); err == nil {
				t.Fatal("New must refuse")
			}
		})
	}

	t.Run("github defaults", func(t *testing.T) {
		t.Parallel()
		o := base(Settings{Enabled: true, Source: "acme/app", SignerWorkflow: ".github/workflows/release.yml"})
		cfg, err := o.config()
		if err != nil {
			t.Fatal(err)
		}
		gh, ok := cfg.Source.(selfupdate.GitHubSource)
		if !ok || gh.Repo != "acme/app" || gh.Bundle != "app-release.sigstore.json" {
			t.Fatalf("source = %#v", cfg.Source)
		}
		if cfg.Channel != DefaultChannel || cfg.interval != DefaultInterval || cfg.Verifier == nil || cfg.InsecureAllowUnsigned {
			t.Fatalf("cfg = %+v", cfg)
		}
		if _, ok := cfg.Policy.(applyNewer); !ok {
			t.Fatalf("policy = %T, want applyNewer", cfg.Policy)
		}
		if _, err := New(o); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("manifest", func(t *testing.T) {
		t.Parallel()
		cfg, err := base(Settings{Enabled: true, Source: "https://dl.example.com/channels.json", Channel: "alpha",
			SignerRepository: "acme/app", SignerWorkflow: "release.yml"}).config()
		if err != nil {
			t.Fatal(err)
		}
		m, ok := cfg.Source.(selfupdate.ManifestSource)
		if !ok || m.Repository != "acme/app" || cfg.Channel != "alpha" {
			t.Fatalf("source = %#v channel=%q", cfg.Source, cfg.Channel)
		}
	})

	t.Run("insecure is explicit", func(t *testing.T) {
		t.Parallel()
		cfg, err := base(Settings{Enabled: true, Source: "acme/app", InsecureAllowUnsigned: true}).config()
		if err != nil || cfg.Verifier != nil || !cfg.InsecureAllowUnsigned {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})

	t.Run("tune sees the built config", func(t *testing.T) {
		t.Parallel()
		o := base(Settings{Enabled: true, Source: "acme/app", InsecureAllowUnsigned: true})
		o.Tune = func(c *selfupdate.Config) { c.SwapMode = selfupdate.ExitForSupervisor }
		cfg, err := o.config()
		if err != nil || cfg.SwapMode != selfupdate.ExitForSupervisor {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})
}

func TestSignerIdentity(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(SignerIdentity("reliant-labs/reliant", "release.yml"))
	for subject, want := range map[string]bool{
		"https://github.com/reliant-labs/reliant/.github/workflows/release.yml@refs/tags/v1.21.3":       true,
		"https://github.com/reliant-labs/reliant/.github/workflows/release.yml@refs/tags/v1.22.0-rc.1":  true,
		"https://github.com/reliant-labs/reliant/.github/workflows/release.yml@refs/heads/main":         false,
		"https://github.com/reliant-labs/reliant/.github/workflows/releaseXyml@refs/tags/v1.0.0":        false,
		"https://github.com/reliant-labs/reliant-fork/.github/workflows/release.yml@refs/tags/v1.0.0":   false,
		"https://github.com/evil/x/.github/workflows/release.yml@refs/tags/v1.0.0#reliant-labs/reliant": false,
	} {
		if got := re.MatchString(subject); got != want {
			t.Errorf("%s: match=%v, want %v", subject, got, want)
		}
	}
	if !strings.HasPrefix(SignerIdentity("o/r", "r.yml"), "^") {
		t.Error("identity must be anchored")
	}
}

func TestJitterStaysWithinATenth(t *testing.T) {
	t.Parallel()
	for range 200 {
		if d := jitterTenth(time.Hour); d < 54*time.Minute || d > 66*time.Minute {
			t.Fatalf("jitter(1h) = %s", d)
		}
	}
}
