// Package cliev records product events ("Events") from a command-line
// program: the forge CLI, and any CLI a forge project builds.
//
// It is the CLI twin of observe.Track. Events share the same contract
// (observe.ProductEventAttrs: event.name product.<name>, the actor's ids,
// app.client "cli", app.version, bounded and PII-guarded product.<key>
// props), so they land in the same product_events table and the same
// dashboards. What a CLI needs and a server does not is here:
//
//   - An anonymous install id: a random UUID minted on first use and kept in
//     <config dir>/telemetry.json. It is recorded as visitor.id and is the
//     only identity an unauthenticated CLI sends.
//   - A one-time notice on stderr before anything is sent, never on a run
//     that asked for machine output (Config.Quiet). Nothing is sent until the
//     notice has been shown once.
//   - Opt-out: DO_NOT_TRACK=1, <APP>_TELEMETRY=off, or `<app> telemetry
//     off` (SetEnabled), which is remembered. It is always off on a CI
//     runner (InCI: CI=true or a CI service's own variable); nothing turns
//     it back on there.
//   - A batch sent at exit (Flush), capped at 300 ms and dropped on any
//     failure, so telemetry never slows or fails a command.
//   - Volume bounds: one event per name per run, and at most MaxEventsPerDay
//     per install.
//   - Install attribution: where the install came from (an install page's
//     rid and utm_*), recorded once by SetInstallAttribution and carried on
//     the first-run event and, by the program, on its own server calls.
//
// <APP>_TELEMETRY_QUIET=1 makes a run quiet (no notice, nothing sent) without
// opting out: an installer that runs the program to print its version sets
// it, so the notice is not swallowed by a redirect.
//
// The transport is OTLP/HTTP JSON logs to Config.Endpoint, normally the
// platform's keyed ingest (https://ingest.reliantlabs.dev/k/<key>/v1/logs)
// with a native ingest key: the key says which org and environment the events
// belong to. An empty Endpoint turns sending off; a development build sets
// none.
package cliev

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

const (
	// FlushTimeout caps the batch sent at exit.
	FlushTimeout = 300 * time.Millisecond
	// MaxEventsPerDay bounds what one install sends in a UTC day.
	MaxEventsPerDay = 50
	// StateFile is the file, in the config dir, holding the install id and
	// the opt-out.
	StateFile = "telemetry.json"
)

// Config configures a Client.
type Config struct {
	// App is the program's name, lowercase: it names the config dir, the
	// <APP>_TELEMETRY variable, the notice and the telemetry command.
	App string
	// Version is recorded as app.version and service.version.
	Version string
	// Endpoint is the OTLP/HTTP logs URL events are posted to. Empty turns
	// sending off (the install id and opt-out still work).
	Endpoint string
	// ConfigDir holds StateFile. Default: os.UserConfigDir()/<App>.
	ConfigDir string
	// Quiet suppresses the first-run notice for this run (--json, --quiet).
	// Events are not sent until the notice has been shown once.
	Quiet bool
	// NoticeURL, when set, is printed in the notice as where to read more.
	NoticeURL string
	// Stderr receives the notice. Default os.Stderr.
	Stderr io.Writer
	// Getenv reads the environment. Default os.Getenv.
	Getenv func(string) string
	// HTTPClient posts the batch. Default: a client with no timeout of its
	// own (Flush bounds it).
	HTTPClient *http.Client
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// SeedInstallID, when set, supplies an install id the program already
	// minted elsewhere (forge's bug reporter), so one machine has one id.
	SeedInstallID func() string
}

// Status says whether events are sent and why.
type Status struct {
	Enabled bool
	// Reason is why: "on", "do_not_track", "env_off", "ci", "opted_out",
	// "no_endpoint".
	Reason    string
	InstallID string
	// EnvVar is the program's <APP>_TELEMETRY variable.
	EnvVar string
}

// Client records one run's events.
type Client struct {
	cfg   Config
	state *state

	mu        sync.Mutex
	pending   []record
	seen      map[string]bool
	firstRun  bool
	noticeOK  bool
	installID string
	status    Status
}

type record struct {
	at    time.Time
	name  string
	attrs []slog.Attr
}

// New returns the run's client. It reads the state file and the environment
// once; it never fails (an unreadable state file is a fresh install).
func New(cfg Config) *Client {
	cfg.App = strings.ToLower(strings.TrimSpace(cfg.App))
	if cfg.App == "" {
		cfg.App = filepath.Base(os.Args[0])
	}
	if cfg.Getenv == nil {
		cfg.Getenv = processEnv
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if truthy(cfg.Getenv(strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(strings.TrimSpace(cfg.App)))) + "_TELEMETRY_QUIET")) {
		cfg.Quiet = true
	}
	if cfg.ConfigDir == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			cfg.ConfigDir = filepath.Join(dir, cfg.App)
		} else {
			cfg.ConfigDir = filepath.Join(os.TempDir(), cfg.App)
		}
	}
	c := &Client{cfg: cfg, state: newState(filepath.Join(cfg.ConfigDir, StateFile)), seen: map[string]bool{}}
	c.status = c.resolve()
	return c
}

// EnvVar is the program's opt-out variable, <APP>_TELEMETRY.
func (c *Client) EnvVar() string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(c.cfg.App)) + "_TELEMETRY"
}

// resolve decides whether this run sends, and mints the install id when it
// does.
func (c *Client) resolve() Status {
	st := Status{EnvVar: c.EnvVar()}
	doc := c.state.load()
	switch {
	case truthy(c.cfg.Getenv("DO_NOT_TRACK")):
		st.Reason = "do_not_track"
	case falsy(c.cfg.Getenv(st.EnvVar)):
		st.Reason = "env_off"
	case InCI(c.cfg.Getenv):
		// Owner decision 2: nothing is sent from CI, whatever else says so.
		st.Reason = "ci"
	case doc.Enabled != nil && !*doc.Enabled:
		st.Reason = "opted_out"
	case c.cfg.Endpoint == "":
		st.Reason = "no_endpoint"
	default:
		st.Enabled, st.Reason = true, "on"
	}
	if !st.Enabled {
		st.InstallID = doc.InstallID
		return st
	}
	if doc.InstallID == "" {
		if c.cfg.SeedInstallID != nil {
			doc.InstallID = c.cfg.SeedInstallID()
		}
		if !observe.ValidActorID(doc.InstallID) || doc.InstallID == "" {
			doc.InstallID = newUUID()
		}
	}
	if !doc.FirstRunSent {
		c.firstRun = true
	}
	if !doc.NoticeShown && !c.cfg.Quiet {
		c.printNotice()
		doc.NoticeShown = true
	}
	// Persist only what this run decided (the id it minted, the notice it
	// printed) onto the file as it is now: another run may have written it
	// since the read above, an opt-out included.
	minted, shown := doc.InstallID, doc.NoticeShown
	if saved, err := c.state.update(func(d *stateDoc) {
		if d.InstallID == "" {
			d.InstallID = minted
		}
		d.NoticeShown = d.NoticeShown || shown
	}); err == nil {
		doc = saved
	}
	if doc.Enabled != nil && !*doc.Enabled {
		return Status{EnvVar: st.EnvVar, Reason: "opted_out", InstallID: doc.InstallID}
	}
	c.noticeOK = doc.NoticeShown
	st.InstallID = doc.InstallID
	c.installID = doc.InstallID
	return st
}

func (c *Client) printNotice() {
	name := c.cfg.App
	msg := name + " sends anonymous usage events (command, version, OS, an install id) to help improve it. " +
		"No code, paths, names or personal data.\nTurn it off with `" + name + " telemetry off`, " +
		c.EnvVar() + "=off or DO_NOT_TRACK=1."
	if c.cfg.NoticeURL != "" {
		msg += " More: " + c.cfg.NoticeURL
	}
	_, _ = io.WriteString(c.cfg.Stderr, msg+"\n")
}

// Status reports whether this run sends events and why.
func (c *Client) Status() Status {
	if c == nil {
		return Status{Reason: "no_endpoint"}
	}
	return c.status
}

// Enabled reports whether this run records events.
func (c *Client) Enabled() bool { return c != nil && c.status.Enabled }

// Transmits reports whether this run may send anything that identifies the
// install: Enabled, the notice has been shown (a quiet first run records
// events but sends them only once a later run has shown it), and no other run
// has turned telemetry off since this one started (the state file is read
// again on every call). Flush sends only when it is true, and so must anything
// else that sends the install id or its attribution (forge: the
// X-Forge-Install-Id and X-Reliant-Attribution headers).
func (c *Client) Transmits() bool {
	return c.Enabled() && c.noticeOK && !optedOut(c.state.load())
}

// optedOut reports whether doc records the user's `telemetry off`.
func optedOut(doc stateDoc) bool { return doc.Enabled != nil && !*doc.Enabled }

// InstallID is this machine's anonymous install id, "" when telemetry is
// off and none was ever minted. A program may send it to its own server
// (forge: the X-Forge-Install-Id header) so server events stitch to it.
func (c *Client) InstallID() string {
	if c == nil {
		return ""
	}
	return c.status.InstallID
}

// FirstRun reports whether this install has not yet sent its first-run
// event. MarkFirstRun records that it has.
func (c *Client) FirstRun() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstRun
}

// SetEnabled records the user's choice (`<app> telemetry on|off`). Off also
// drops this run's pending events.
func (c *Client) SetEnabled(on bool) error {
	if _, err := c.state.update(func(d *stateDoc) {
		d.Enabled = &on
		if on {
			d.NoticeShown = true // the user just read about it
		}
	}); err != nil {
		return err
	}
	c.status = c.resolve()
	if !on {
		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
	}
	return nil
}

// Track records one event for the batch sent at Flush. The actor is the
// install id plus whatever observe.WithActor put on ctx (a signed-in CLI
// can name its user and org). A name already recorded this run is ignored.
// Like observe.Track it never fails the caller; an invalid name is dropped.
func (c *Client) Track(ctx context.Context, name string, attrs ...slog.Attr) {
	if !c.Enabled() || !observe.ValidEventName(name) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[name] {
		return
	}
	c.seen[name] = true
	if ctx == nil {
		ctx = context.Background()
	}
	actor := observe.ActorFrom(ctx)
	actor.AnonymousID = c.installID
	out := observe.ProductEventAttrs(ctx, name, actor, observe.AppClientCLI, attrs)
	if !hasKey(out, observe.AttrAppVersion) && c.cfg.Version != "" {
		out = append(out, slog.String(observe.AttrAppVersion, c.cfg.Version))
	}
	c.pending = append(c.pending, record{at: c.cfg.Now(), name: name, attrs: out})
}

// Flush sends the run's events in one batch, waiting at most FlushTimeout for
// the state lock and the post together, and records the first-run event as
// sent when it was in the batch (one more locked write, bounded by its own
// short wait). Call it
// once, as the program exits. Failures are dropped silently, and still count
// toward the day's MaxEventsPerDay, which bounds what every run on the machine
// together attempts.
func (c *Client) Flush(ctx context.Context) {
	if !c.Transmits() {
		return
	}
	c.mu.Lock()
	batch := c.pending
	c.pending = nil
	c.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	// Reserve the batch's share of the day's allowance before posting, under
	// the state lock, so concurrent runs cannot all spend the same room. A
	// reservation is spent whether or not the post succeeds: the cap bounds
	// what is attempted. A run turned off by another since it started
	// reserves, and sends, nothing.
	//
	// One deadline for the whole send: waiting for another run's state lock
	// spends the same FlushTimeout as the post.
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, FlushTimeout)
	defer cancel()
	day := c.cfg.Now().UTC().Format("2006-01-02")
	reserved := 0
	if _, err := c.state.updateCtx(ctx, func(d *stateDoc) {
		if optedOut(*d) {
			return
		}
		if d.Day != day {
			d.Day, d.SentToday = day, 0
		}
		reserved = min(len(batch), max(MaxEventsPerDay-d.SentToday, 0))
		d.SentToday += reserved
	}); err != nil || reserved == 0 {
		return
	}
	batch = batch[:reserved]
	if !c.post(ctx, batch) {
		return
	}
	for _, r := range batch {
		if r.name == FirstRunEvent(c.cfg.App) {
			// Only this flag: the file is re-read under its lock, so a choice
			// another run made during the post (`telemetry off`) is kept. Past
			// the deadline on purpose (bounded by the lock's own wait): an
			// event that was delivered must be recorded, or the next run
			// sends it again.
			_, _ = c.state.update(func(d *stateDoc) { d.FirstRunSent = true })
			break
		}
	}
}

// FirstRunEvent is the name of a program's first-run event:
// <app>_cli_first_run ("forge_cli_first_run").
func FirstRunEvent(app string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(app)) + "_cli_first_run"
}

// FirstRunAttrs are the standard first-run tags: version, os, arch, harness
// (the coding agent running the command, "" when none) and, when the install
// recorded one, its attribution (source, medium, campaign, visitor_id: the
// install page's utm_* and rid). There is no ci tag: a CI runner sends
// nothing (InCI).
func (c *Client) FirstRunAttrs(harness string) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("version", c.cfg.Version),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("harness", harness),
	}
	q := c.InstallAttribution()
	for _, kv := range [][2]string{{"utm_source", "source"}, {"utm_medium", "medium"}, {"utm_campaign", "campaign"}, {"rid", "visitor_id"}} {
		if v := q.Get(kv[0]); v != "" {
			attrs = append(attrs, slog.String(kv[1], v))
		}
	}
	return attrs
}

var (
	attrRIDRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	attrValueRE = regexp.MustCompile(`^[A-Za-z0-9 _.+:/-]{1,128}$`)
)

// attributionKeys are what an install page may pass.
var attributionKeys = []string{"rid", "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"}

// CleanAttribution keeps the rid and utm_* values of a query string that are
// well formed (an opaque rid, campaign words with no '@'), and drops the rest.
func CleanAttribution(raw string) url.Values {
	out := url.Values{}
	in, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(raw), "?"))
	if err != nil || len(raw) > 1024 {
		return out
	}
	for _, k := range attributionKeys {
		v := strings.TrimSpace(in.Get(k))
		switch {
		case v == "":
		case k == "rid" && attrRIDRE.MatchString(v):
			out.Set(k, v)
		case k != "rid" && attrValueRE.MatchString(v) && !observe.LooksLikePII(v):
			out.Set(k, v)
		}
	}
	return out
}

// SetInstallAttribution records where this install came from (the install
// page's rid and utm_*, as a query string; CleanAttribution keeps what is well
// formed). First touch: an install that already recorded one keeps it. A
// no-op while telemetry is off.
func (c *Client) SetInstallAttribution(raw string) error {
	if !c.Enabled() {
		return nil
	}
	q := CleanAttribution(raw)
	if len(q) == 0 {
		return nil
	}
	_, err := c.state.update(func(d *stateDoc) {
		if d.Attribution == "" {
			d.Attribution = q.Encode()
		}
	})
	return err
}

// InstallAttribution is the recorded install attribution; empty while
// telemetry is off or none was recorded.
func (c *Client) InstallAttribution() url.Values {
	if !c.Enabled() {
		return url.Values{}
	}
	return CleanAttribution(c.state.load().Attribution)
}

// ciFlagVars are set to "true" (or "1") by CI services generally.
var ciFlagVars = []string{"CI", "CONTINUOUS_INTEGRATION"}

// ciServiceVars are set, to any value, by one CI service each.
var ciServiceVars = []string{
	"GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TRAVIS", "TF_BUILD",
	"JENKINS_URL", "TEAMCITY_VERSION", "BITBUCKET_BUILD_NUMBER", "CODEBUILD_BUILD_ID",
	"DRONE", "APPVEYOR", "SEMAPHORE", "BUDDY", "CIRRUS_CI", "WOODPECKER", "HARNESS_BUILD_ID",
}

// InCI reports whether the process runs on a CI runner: CI or
// CONTINUOUS_INTEGRATION set to anything but a false value, or a CI
// service's own variable set at all. Telemetry is always off there.
func InCI(getenv func(string) string) bool {
	for _, v := range ciFlagVars {
		if truthy(getenv(v)) {
			return true
		}
	}
	for _, v := range ciServiceVars {
		if getenv(v) != "" {
			return true
		}
	}
	return false
}

func hasKey(attrs []slog.Attr, key string) bool {
	for _, a := range attrs {
		if a.Key == key {
			return true
		}
	}
	return false
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

func falsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// The package-level client, for a program that sets it up once in main.
var (
	defaultMu     sync.Mutex
	defaultClient *Client
)

// Init builds the process's client (New) and makes it the one the
// package-level Track and Flush use.
func Init(cfg Config) *Client {
	c := New(cfg)
	defaultMu.Lock()
	defaultClient = c
	defaultMu.Unlock()
	return c
}

// Default returns the client Init installed, nil before Init.
func Default() *Client {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultClient
}

// Track records an event on the Init client; a no-op before Init.
func Track(ctx context.Context, name string, attrs ...slog.Attr) {
	Default().Track(ctx, name, attrs...)
}

// Flush sends the Init client's batch; a no-op before Init.
func Flush(ctx context.Context) { Default().Flush(ctx) }
