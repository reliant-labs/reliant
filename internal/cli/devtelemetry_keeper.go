package cli

// THE TELEMETRY KEEPER: what keeps a hosted dev stack's telemetry delivering
// for as long as the stack runs, whether `forge env up` holds the foreground
// or has returned.
//
// Two things must happen every minute for the life of the stack: the session
// must be reported live (the control plane mints a token only for a session
// that heartbeated within ~2 minutes), and the tokens must be renewed at
// half-life (they last 1h). A holding `env up` could do both in-process, but
// a DETACHED stack — `--background`, and the non-TTY default every agent
// gets — has no forge process left once `env up` returns: its host processes
// are children in their own process groups, tracked in the stack's ledger and
// stopped by `forge env down`. Nothing else outlives the command.
//
// So the owner is one more of those tracked children: `forge env
// telemetry-keeper <spec>`, started through the same procRegistry.start as
// every host service. That makes its lifetime the stack's by construction,
// with no second lifecycle to supervise:
//
//   - it is in the ledger, so `forge env down` (and the next `env up`'s
//     reclaim, and `forge env down --all`) signal it like any other child,
//     and `forge env ps` lists it;
//   - it carries forge's ownership markers, so an orphan is reclaimed;
//   - a holding run tears it down with the rest on Ctrl-C.
//
// It runs in BOTH modes, so there is one mechanism rather than an in-process
// path and a detached one that drift apart; a holding run then skips its own
// heartbeat. It never reports the session stopped: whoever stops the stack
// reports that AFTER the keeper is gone (`forge env down`, and a holding
// run's teardown), so a beat in flight cannot resurrect the row — and a
// reclaim by the next `env up`, which reuses the same session id, must not
// have its fresh row marked stopped by the old keeper on its way out.
//
// Its input is a spec file under .forge/obs/ (no secret in it: the credential
// is re-resolved from the endpoint on every call, as any forge command
// resolves it, so a refreshed login is picked up).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/pkg/release"
)

// devTelemetryKeeperName is the keeper's name in the stack's ledger and its
// log file (.forge/logs/<env>/telemetry-keeper.log).
const devTelemetryKeeperName = "telemetry-keeper"

// devTelemetryKeeperSpec is everything the keeper needs, as `env up` decided
// it. .forge/obs/keeper-<env>.json.
type devTelemetryKeeperSpec struct {
	ProjectDir    string                 `json:"project_dir"`
	Env           string                 `json:"env"`
	Project       string                 `json:"project"`
	Endpoint      cloud.Endpoint         `json:"endpoint"`
	Session       release.LocalSession   `json:"session"`
	Retention     *devTelemetryRetention `json:"retention,omitempty"`
	SessionReplay bool                   `json:"session_replay,omitempty"`
	Frontends     []FrontendEntity       `json:"frontends,omitempty"`
	// The grant the keeper starts from: when the current tokens were
	// minted and expire, and the retention the control plane granted.
	MintedAt      time.Time              `json:"minted_at"`
	ExpiresAt     time.Time              `json:"expires_at"`
	Granted       devtelemetry.Retention `json:"granted"`
	GrantedReplay bool                   `json:"granted_replay,omitempty"`
	// InstanceID is the control plane's instance id of the grant. The keeper
	// runs only while backend-<env>.json still records it (cloud).
	InstanceID string `json:"instance_id"`
}

func devTelemetryKeeperSpecPath(projectDir, env string) string {
	return filepath.Join(projectDir, ".forge", "obs", "keeper-"+env+".json")
}

// keeperSpec is the spec for this run's keeper.
func (r *devTelemetryRun) keeperSpec() devTelemetryKeeperSpec {
	r.mu.Lock()
	g := r.grant
	r.mu.Unlock()
	return devTelemetryKeeperSpec{
		ProjectDir: r.projectDir, Env: r.env, Project: r.project,
		Endpoint: r.endpoint, Session: r.session, Retention: r.retention,
		SessionReplay: r.sessionReplay, Frontends: r.frontends,
		MintedAt: g.mintedAt, ExpiresAt: g.expires, Granted: g.Retention, GrantedReplay: g.SessionReplay,
		InstanceID: g.Instance.ID,
	}
}

// devTelemetryKeeperCmd writes the spec and builds the keeper's command: this
// forge binary, so the keeper is the same version as the `env up` that
// decided the spec.
func devTelemetryKeeperCmd(spec devTelemetryKeeperSpec) (*exec.Cmd, error) {
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	path := devTelemetryKeeperSpecPath(spec.ProjectDir, spec.Env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := atomicfile.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	argv := selfForgeCommand([]string{"forge", "env", devTelemetryKeeperName, path})
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is this forge binary and a path it just wrote
	cmd.Dir = spec.ProjectDir
	// The credential may come from the env var the control plane
	// declaration names (TokenEnv), so the keeper inherits this env.
	cmd.Env = os.Environ()
	return cmd, nil
}

// startKeeper starts the keeper as a child of the stack. false (with a note)
// when it could not be started: the stack still runs, and its telemetry
// stops when the current tokens expire.
func (r *devTelemetryRun) startKeeper(procs *procRegistry, background bool, w io.Writer) bool {
	if r == nil {
		return false
	}
	// A `--target` run replaces only the services it names, so the previous
	// run's keeper can still be running: stop it (by its service marker,
	// like any scoped stop) rather than leave two keepers, one of them no
	// longer in the ledger. A no-op after an unscoped run's reclaim.
	if err := stopDevTelemetryKeeper(r.projectDir, r.env); err != nil {
		fmt.Fprintf(w, "[up] note: could not stop the previous telemetry keeper: %v\n", err)
	}
	cmd, err := devTelemetryKeeperCmd(r.keeperSpec())
	if err == nil {
		err = procs.start(devTelemetryKeeperName, cmd, background)
	}
	if err != nil {
		fmt.Fprintf(w, "[up] note: could not start the telemetry keeper (%v): this stack's hosted telemetry stops when its current token expires\n", err)
		return false
	}
	return true
}

// credentialCaller resolves the credential on every call, so a keeper that
// runs for days follows a refreshed login rather than holding a stale token.
type credentialCaller struct{ ep cloud.Endpoint }

func (c credentialCaller) Call(ctx context.Context, procedure string, req, out any) error {
	cred, err := cloud.ResolveCredential("", c.ep)
	if err != nil {
		return err
	}
	return cloud.NewClient(c.ep, cred).Call(ctx, procedure, req, out)
}

// runDevTelemetryKeeper heartbeats the session and renews the tokens until
// ctx is done — which is the stack stopping: `forge env down` (or a holding
// run's teardown) signals the keeper, and its signal context cancels ctx.
//
// The two run on separate goroutines and never wait on each other: renewal
// has its own timer at each half-life (renewUntil), so a presence report that
// is slow or failing cannot delay it past expiry. The keeper returns once
// both have stopped, so a report in flight lands before the process exits —
// and therefore before `forge env down`, which waits for the exit, reports
// the session stopped.
func runDevTelemetryKeeper(ctx context.Context, spec devTelemetryKeeperSpec, caller cloudCaller, reporter sessionReporter, heartbeat time.Duration, clock devTelemetryClock, log io.Writer) {
	if ctx.Err() != nil {
		return
	}
	// The keeper stops itself the moment the env's telemetry is decided
	// again (local, or another instance), even if nothing signalled it.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	owns := func() bool { return devTelemetryKeeperOwns(spec) }
	run := &devTelemetryRun{
		projectDir: spec.ProjectDir, env: spec.Env, project: spec.Project,
		client: caller, sessionID: spec.Session.ID, retention: spec.Retention,
		sessionReplay: spec.SessionReplay, frontends: spec.Frontends, log: log,
		endpoint: spec.Endpoint, session: spec.Session, stillHosted: owns,
		grant: devTelemetryGrant{mintedAt: spec.MintedAt, expires: spec.ExpiresAt, Retention: spec.Granted, SessionReplay: spec.GrantedReplay},
	}
	sess := &upSession{
		reporter:       ownedReporter{inner: reporter, owns: owns, stop: stop},
		session:        spec.Session,
		log:            log,
		hosted:         true,
		heartbeat:      heartbeat,
		loggedFailures: map[string]bool{},
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		run.renewUntil(ctx, clock)
		stop() // superseded: the heartbeat goes too
	}()
	go func() {
		defer wg.Done()
		// One beat at once: a keeper started after a long startup makes
		// the session live again without waiting a minute.
		sess.report(time.Now().UTC(), false)
		sess.heartbeatUntil(ctx)
	}()
	wg.Wait()
}

// devTelemetryKeeperOwns reports whether backend-<env>.json still records the
// keeper's grant: backend cloud, same instance. Anything else — local, another
// instance, no record, an unreadable one — means the keeper must stop.
func devTelemetryKeeperOwns(spec devTelemetryKeeperSpec) bool {
	rec, ok, err := devtelemetry.Read(spec.ProjectDir, spec.Env)
	return err == nil && ok && rec.Backend == "cloud" && rec.InstanceID == spec.InstanceID
}

// ownedReporter reports presence only while the keeper still owns the env's
// telemetry; otherwise it stops the keeper and reports nothing.
type ownedReporter struct {
	inner sessionReporter
	owns  func() bool
	stop  context.CancelFunc
}

func (o ownedReporter) ReportSession(ctx context.Context, s release.LocalSession) error {
	if !o.owns() {
		o.stop()
		return nil
	}
	return o.inner.ReportSession(ctx, s)
}

// stopDevTelemetryKeeper stops env's telemetry keeper — by its service
// marker, through the ledger and the stop path `forge env down` uses — and
// returns once it has exited. A seam: tests state the stop.
var stopDevTelemetryKeeper = func(projectDir, env string) error {
	_, err := stopStackScopedWithFacts(projectIDForDir(projectDir), env, []string{devTelemetryKeeperName}, newOSProcFacts())
	return err
}

// devTelemetryKeeperMayRun reports whether env may have a keeper running, so
// a project that never went hosted pays no process-table scan: the ledger
// names one, its spec is on disk, or the env is recorded hosted.
func devTelemetryKeeperMayRun(projectDir, env string) bool {
	for _, p := range trackedStack(projectIDForDir(projectDir), env) {
		if p.name == devTelemetryKeeperName {
			return true
		}
	}
	if _, err := os.Stat(devTelemetryKeeperSpecPath(projectDir, env)); err == nil {
		return true
	}
	rec, ok, err := devtelemetry.Read(projectDir, env)
	return err == nil && ok && rec.Backend == "cloud"
}

// newEnvTelemetryKeeperCmd is `forge env telemetry-keeper <spec>`. Hidden:
// `forge env up` starts it as part of a hosted dev stack, and `forge env
// down` stops it with the stack; nobody runs it by hand.
func newEnvTelemetryKeeperCmd() *cobra.Command {
	return &cobra.Command{
		Use:    devTelemetryKeeperName + " <spec-file>",
		Short:  "Keep a hosted dev stack's telemetry delivering (started and stopped with the stack)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var spec devTelemetryKeeperSpec
			if err := json.Unmarshal(b, &spec); err != nil {
				return fmt.Errorf("read %s: %w", args[0], err)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, stop := signalContext(ctx)
			defer stop()
			caller := credentialCaller{ep: spec.Endpoint}
			runDevTelemetryKeeper(ctx, spec, caller, hostedRecordStoreFor(caller, spec.Project), sessionHeartbeat, realDevTelemetryClock{}, cmd.OutOrStdout())
			return nil
		},
	}
}

// signalContext cancels on the signals that stop a stack's children.
func signalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, upTeardownSignals...)
}
