package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/benefits"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// THE DEPLOY OUTCOME: what a writing `forge env deploy` learned on its way,
// for the lines it ends with — the facts block after a hosted deploy and the
// RESULT line (command_result.go).
//
// It rides ctx, like the deploy attempt (deploy_attempt.go), because the
// facts it needs are learned deep inside paths that share no return value: the
// release is decided in the plan, the hosted control plane is resolved with
// the ledger, the claim link exists only on the claimable path. Unlike the
// attempt it exists for every writing deploy, hosted or not, and it sends
// nothing anywhere.

type deployOutcomeCtxKey struct{}

// deployOutcome is one invocation's outcome so far. Nil-safe throughout.
type deployOutcome struct {
	env string

	mu      sync.Mutex
	phase   string
	release string
	url     string
	claim   string
	// The env's hosted control plane, when its ledger resolved to one: what
	// the live URL is read from once the deploy succeeds.
	endpoint string
	client   cloudCaller
	project  string
}

// withDeployOutcome starts the outcome of one deploy of env.
func withDeployOutcome(ctx context.Context, env string) (context.Context, *deployOutcome) {
	o := &deployOutcome{env: env}
	return context.WithValue(ctx, deployOutcomeCtxKey{}, o), o
}

func deployOutcomeFrom(ctx context.Context) *deployOutcome {
	if ctx == nil {
		return nil
	}
	o, _ := ctx.Value(deployOutcomeCtxKey{}).(*deployOutcome)
	return o
}

func (o *deployOutcome) set(change func(*deployOutcome)) {
	if o == nil {
		return
	}
	o.mu.Lock()
	change(o)
	o.mu.Unlock()
}

// noteDeployHosted records the env's hosted control plane.
func noteDeployHosted(ctx context.Context, env string, l envLedger) {
	o := deployOutcomeFrom(ctx)
	store, ok := l.Bindings.(*hostedStore)
	if o == nil || !l.Hosted || !ok || store.client == nil || env != o.env {
		return
	}
	o.set(func(o *deployOutcome) {
		if o.client == nil {
			o.endpoint, o.client, o.project = store.endpoint, store.client, store.project
		}
	})
}

// noteDeployClaim records a claimable deploy's live URL and claim link.
func noteDeployClaim(ctx context.Context, c *deployClaimJSON) {
	if c == nil {
		return
	}
	deployOutcomeFrom(ctx).set(func(o *deployOutcome) {
		if c.URL != "" {
			o.url = c.URL
		}
		o.claim = c.ClaimURL
	})
}

// hostedReadTimeout bounds the one status read a successful hosted deploy
// makes for its URL. The deploy already succeeded; a slow read only loses
// the URL from the last lines, never the deploy.
var hostedReadTimeout = 5 * time.Second

// deployTiers is what a successful hosted deploy runs, from its status.
type deployTiers struct {
	workload, static bool
}

// readLiveFacts reads the env's live URL and what it runs, best-effort.
func (o *deployOutcome) readLiveFacts(ctx context.Context) deployTiers {
	o.mu.Lock()
	client, project, env := o.client, o.project, o.env
	o.mu.Unlock()
	if client == nil || project == "" {
		return deployTiers{}
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostedReadTimeout)
	defer cancel()
	st, err := deploytarget.ReadHostedStatus(rctx, client, project, env)
	if err != nil {
		return deployTiers{}
	}
	var t deployTiers
	for _, w := range st.Workloads {
		switch deploytarget.HostedTier(w.Tier) {
		case deploytarget.HostedTierStatic:
			t.static = true
		case deploytarget.HostedTierWorkload:
			t.workload = true
		}
	}
	if url := primaryURL(st.Workloads); url != "" {
		o.set(func(o *deployOutcome) {
			if o.url == "" {
				o.url = url
			}
		})
	}
	return t
}

// deployWrites reports whether the invocation can write: the previews
// (--explain, --dry-run, --plan, --plan-only) print a plan or a manifest and
// end there, and are not what an agent backgrounds and waits on.
func deployWrites(f deployCmdFlags) bool {
	return !f.explain && !f.dryRun && !f.promote.plan && !f.promote.planOnly
}

// finishDeployOutcome ends a writing deploy's text output: on success the
// facts block (a signed-in hosted deploy) and the RESULT line, last; on
// failure the RESULT line appended to the error, which the host prints last.
// --json prints neither: its document is the outcome.
func finishDeployOutcome(ctx context.Context, o *deployOutcome, f deployCmdFlags, err error, w io.Writer) error {
	if o == nil || f.jsonOut || !deployWrites(f) {
		return err
	}
	o.mu.Lock()
	r := commandResult{Status: resultStatus(err), Env: o.env, Release: o.release, URL: o.url, Claim: o.claim}
	phase, hosted := o.phase, o.client != nil
	o.mu.Unlock()

	if err != nil {
		r.Reason = reasonOf(err)
		if r.Status == "failed" || r.Status == "undetermined" {
			r.Stage = phase
		}
		if q := queuedOf(err); q != nil {
			r.Release = emptyAs(r.Release, q.Release)
			for _, h := range q.Holds {
				if h.ActionURL != "" {
					r.Action = h.ActionURL
					break
				}
			}
		}
		r.Next = deployNextOnError(o.env, r.Status, err)
		return withResultLine(err, r)
	}

	claimable := r.Claim != ""
	var tiers deployTiers
	if hosted {
		tiers = o.readLiveFacts(ctx)
		o.mu.Lock()
		r.URL = o.url
		o.mu.Unlock()
	}
	if hosted && !claimable {
		printDeployFacts(w, deployFactsInput{
			Env: o.env, Release: r.Release, Endpoint: o.endpoint, Project: o.project,
			Service: tiers.workload && projectHasForgeServices(), Static: tiers.static,
		})
	}
	switch {
	case claimable:
		r.Next = fmt.Sprintf("give the claim link to your user (%s env status %s --claimable shows it again)", Name(), o.env)
	case hosted && tiers.workload:
		r.Next = fmt.Sprintf("%s obs check %s --wait", Name(), o.env)
	default:
		r.Next = fmt.Sprintf("%s env status %s", Name(), o.env)
	}
	if hosted && !claimable {
		printConnectHint(w, o.project, f)
	}
	fmt.Fprintf(w, "\n%s\n", r.Line())
	return nil
}

// deployNextOnError is the RESULT line's next step for a deploy that did not
// succeed: the error's own remedy when it names one, else what the status
// asks for.
func deployNextOnError(env, status string, err error) string {
	n := Name()
	switch status {
	case "queued":
		return fmt.Sprintf("hand the action link to a person who can resolve it; it goes live by itself after (%s env status %s --wait blocks until then)", n, env)
	case "pending":
		return fmt.Sprintf("%s env status %s --wait", n, env)
	case "declined":
		return fmt.Sprintf("%s env deploy %s --yes", n, env)
	}
	if fix := fixOf(err); fix != "" {
		return fix
	}
	switch status {
	case "unconfirmed":
		return fmt.Sprintf("read the plan above, then %s env deploy %s --yes", n, env)
	case "undetermined":
		return fmt.Sprintf("%s env status %s, then retry %s env deploy %s", n, env, n, env)
	default:
		return fmt.Sprintf("fix the error above, then %s env deploy %s", n, env)
	}
}

// projectHasForgeServices reports whether this project builds forge Go
// services: a forge.yaml with a module_path. Only those get forge's edge
// telemetry with no code; an adopted app's own process exports what it was
// instrumented to export.
func projectHasForgeServices() bool {
	_, cfg, err := initConfig()
	return err == nil && cfg != nil && cfg.ModulePath != ""
}

// ─── The facts block ────────────────────────────────────────────────────────

// deployFactIDs are the benefits a successful hosted deploy names, by what the
// env runs. The lines are the benefits' own titles from benefits.yaml, so they
// say no more than the registry (and its evidence tests) holds true.
var (
	// A forge service on hosted compute: every one of these is on with no
	// setup, and the dashboards have data the moment it serves a request.
	serviceDeployFactIDs = []string{"edge-telemetry", "issues-symbolication", "default-alerts", "release-regressed-alert"}
	// A static site: hosted, recorded, and promotable as it is.
	staticDeployFactIDs = []string{"static-hosting-cdn", "build-once-promote-by-digest", "release-ledger"}
)

type deployFactsInput struct {
	Env, Release, Endpoint, Project string
	// Service: the env runs a forge service on hosted compute. Static: a
	// hosted static site.
	Service, Static bool
}

// printDeployFacts prints at most four lines of what this release has with no
// setup, each a benefit title, and where to see it. Prints nothing when it has
// nothing true to say: no registry, or an env that runs neither shape.
func printDeployFacts(w io.Writer, in deployFactsInput) {
	for _, line := range deployFactLines(in) {
		fmt.Fprintln(w, line)
	}
}

func deployFactLines(in deployFactsInput) []string {
	ids := staticDeployFactIDs
	if in.Service {
		ids = serviceDeployFactIDs
	} else if !in.Static {
		return nil
	}
	reg, err := benefits.Load()
	if err != nil {
		return nil
	}
	titles := benefitTitles(reg, ids)
	if len(titles) == 0 {
		return nil
	}
	lines := []string{
		"",
		fmt.Sprintf("Included with this release, no setup (%s project benefits lists every one):", Name()),
		"  " + strings.Join(titles, " · "),
	}
	console := deployConsole(in.Endpoint, in.Project)
	switch {
	case !console.OK():
	case in.Service:
		lines = append(lines, "  Dashboards: "+console.Dashboards(in.Env))
	case in.Release != "":
		lines = append(lines, "  Release: "+console.Release(in.Release, in.Env))
	}
	return lines
}

// benefitTitles is the titles of the benefits ids name, in that order, that
// the registry holds as shipped and enforced. An id it does not hold is
// skipped, never invented.
func benefitTitles(reg *benefits.Registry, ids []string) []string {
	byID := map[string]benefits.Benefit{}
	for _, b := range reg.Benefits {
		byID[b.ID] = b
	}
	var out []string
	for _, id := range ids {
		b, ok := byID[id]
		if !ok || b.Status != benefits.StatusEnforced {
			continue
		}
		out = append(out, b.Title)
	}
	return out
}

// deployConsole is the console of project for the control plane at endpoint:
// links only where forge knows that control plane's web app (Reliant's, or
// FORGE_CONSOLE_URL), so a self-hosted control plane is never sent to
// Reliant's console.
func deployConsole(endpoint, project string) cloud.Console {
	base := cloud.WebAppBase(endpoint)
	if base == "" {
		return cloud.Console{}
	}
	return cloud.NewConsole(base, project)
}
