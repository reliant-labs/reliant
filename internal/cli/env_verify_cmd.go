package cli

// `forge env verify <env>` — prove, with NO side effects, that what forge
// would deploy is what is already running.
//
// It is the shadow-mode step of adopting forge on a project something else
// already deploys: declare the workloads, bind them to forge.OnExternal with a
// `status` and a `plan` hook, and run this until it says MATCH everywhere.
// Only then does `forge env deploy` take over — and its first deploy changes
// nothing.
//
// SIDE-EFFECT FREE, BY CONSTRUCTION. It builds nothing, pushes nothing,
// records nothing and deploys nothing: it holds the promotion ledger only as
// envPromotionReader (Current, no Append), runs only the read-only hooks
// (status, plan), and reads clusters with kubectl get / client-side diff. The
// tests pin that (env_verify_cmd_test.go).
//
// TWO CHECKS PER WORKLOAD, reported separately:
//
//   - IMAGE parity: the digest the env's current promotion pins (or the one
//     `--image` names, before any forge release exists) against the digest
//     running — the `status` hook for an external workload, the cluster for
//     a cluster one.
//   - CONFIG parity: the `plan` hook's exit code for an external workload (0
//     no change, 2 changes — its own diff is shown, never parsed); for a
//     cluster workload, the client-side live diff of the bound release's
//     recorded bundle. With no recorded bundle a cluster's config cannot be
//     checked without building one, and the report says exactly that.
//
// A MISSING HOOK IS "UNVERIFIED", NEVER "NO DIFF" — the same rule as `forge
// env diff`: a check that did not run must not read as a check that passed.
//
// EXIT CODES (the shared table in exitcodes.go): 0 every checked workload
// matches; 1 drift — we looked and it differs; 2 something could not be
// verified (a missing hook, no expected digest, a hook or cluster read that
// failed) and nothing drifted.

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// verifyState is one check's (and one row's) verdict.
type verifyState string

const (
	verifyMatch      verifyState = "match"
	verifyDrift      verifyState = "drift"
	verifyUnverified verifyState = "unverified"
	verifyError      verifyState = "error"
	// verifySkipped: a runtime `env verify` does not judge (a host process,
	// a compose service, a build-only artifact). Shown, never counted as a
	// match.
	verifySkipped verifyState = "skipped"
)

// rank orders states worst-first for a row's verdict.
func (s verifyState) rank() int {
	switch s {
	case verifyDrift:
		return 4
	case verifyError:
		return 3
	case verifyUnverified:
		return 2
	case verifyMatch:
		return 1
	}
	return 0
}

// verifyCheck is one parity check of one workload.
type verifyCheck struct {
	State verifyState `json:"state"`
	// Expected and Running are digests (image parity).
	Expected string `json:"expected,omitempty"`
	Running  string `json:"running,omitempty"`
	// Detail says why, in one line. Always set when State is not match.
	Detail string `json:"detail,omitempty"`
	// Output is the plan hook's / live diff's own text, verbatim.
	Output string `json:"output,omitempty"`
}

// envVerifyRow is one workload.
type envVerifyRow struct {
	Workload string      `json:"workload"`
	Runtime  string      `json:"runtime"`
	State    verifyState `json:"state"`
	Image    verifyCheck `json:"image"`
	Config   verifyCheck `json:"config"`
}

type verifySummary struct {
	Match      int `json:"match"`
	Drift      int `json:"drift"`
	Unverified int `json:"unverified"`
	Error      int `json:"error"`
	Skipped    int `json:"skipped"`
}

// envVerifyDocument is `forge env verify --json`.
type envVerifyDocument struct {
	jsonEnvelope
	Env string `json:"env"`
	// Release is the release the env is promoted to; "" when none is.
	Release   string         `json:"release,omitempty"`
	Workloads []envVerifyRow `json:"workloads"`
	Summary   verifySummary  `json:"summary"`
}

// envPromotionReader is everything verify may do to the promotion ledger:
// read. Declared here, without Append, so a write is not expressible.
type envPromotionReader interface {
	Current(ctx context.Context, env string) (release.Promotion, bool, error)
	Location() string
}

// envVerifyDeps are verify's seams. Production fills every nil one.
type envVerifyDeps struct {
	Render   func(ctx context.Context, projectDir, env string) (*KCLEntities, error)
	Ledger   func(ctx context.Context, projectDir, env string, e *KCLEntities) (envPromotionReader, envLedger, error)
	Hooks    deploytarget.ExternalHooks
	Lister   clusterImageLister
	LiveDiff func(ctx context.Context, projectDir, env, version string, ledger envLedger) ([]clusterLiveDiff, error)
	Secrets  func(e *KCLEntities) hookSecretResolver
	Stderr   io.Writer
}

type envVerifyOptions struct {
	Env       string
	Images    map[string]string // workload -> ref@sha256:…
	Workloads []string
	JSON      bool
}

func newEnvVerifyCmd() *cobra.Command {
	var (
		images    []string
		workloads []string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "verify <environment>",
		Short: "Prove, without deploying anything, that what forge would ship is what is running",
		Args:  cobra.ExactArgs(1),
		Long: `Compare what forge would deploy to <environment> against what is running there,
per workload, with NO side effects: nothing is built, pushed, recorded or
deployed.

It is how a project that something else already deploys adopts forge: bind each
workload to forge.OnExternal with a ` + "`status`" + ` and a ` + "`plan`" + ` hook, run this until
every row says match, then let ` + "`forge env deploy`" + ` drive — its first deploy
changes nothing.

TWO CHECKS PER WORKLOAD:

  image   the digest the env's current promotion pins (or the one --image
          names, before any forge release exists) against the digest running:
          the OnExternal ` + "`status`" + ` hook's {"digest": …}, or the cluster.
  config  forge.OnExternal: the ` + "`plan`" + ` hook (exit 0 no change, 2 changes;
          its own diff is shown, never parsed). forge.OnCluster: the
          client-side live diff of the bound release's recorded bundle — or
          "not verifiable without a build" when none is recorded.

Each check is match, drift, unverified (a missing hook, no expected digest,
nothing recorded to diff) or error (a hook or read that failed). A missing hook
is NEVER reported as "no diff". Host, compose and build-only workloads are
skipped; a hosted one is read by ` + "`forge env status`" + `.

EXIT CODES:
  0  every checked workload matches
  1  drift: forge looked and something differs
  2  nothing drifted, but something could not be verified

Examples:
  ` + Name() + ` env verify prod
  ` + Name() + ` env verify prod --image api=ghcr.io/acme/api@sha256:…   # before a forge release exists
  ` + Name() + ` env verify prod --workload api --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pins, err := parseVerifyImages(images)
			if err != nil {
				return exitCodeError{code: exitWrong, msg: err.Error()}
			}
			return runEnvVerify(cmd.Context(), cmd.OutOrStdout(), envVerifyOptions{
				Env: args[0], Images: pins, Workloads: workloads, JSON: asJSON,
			}, envVerifyDeps{})
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&images, "image", nil, "Expected image of a workload, as name=repo@sha256:<digest> (repeatable); wins over the env's promotion")
	f.StringArrayVar(&workloads, "workload", nil, "Verify only the named workload(s) (repeatable)")
	f.BoolVar(&asJSON, "json", false, "Print the report as one JSON document")
	return cmd
}

var verifyImageFlag = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)=(\S+@sha256:[a-f0-9]{64})$`)

// parseVerifyImages parses --image name=ref@sha256:… flags.
func parseVerifyImages(flags []string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range flags {
		m := verifyImageFlag.FindStringSubmatch(f)
		if m == nil {
			return nil, fmt.Errorf("--image %q: want <workload>=<repository>@sha256:<64 hex> (a digest, not a tag: a tag can move, so it proves nothing)", f)
		}
		if _, dup := out[m[1]]; dup {
			return nil, fmt.Errorf("--image names workload %q twice", m[1])
		}
		out[m[1]] = m[2]
	}
	return out, nil
}

func (d envVerifyDeps) withDefaults() envVerifyDeps {
	if d.Render == nil {
		d.Render = RenderKCL
	}
	if d.Ledger == nil {
		d.Ledger = func(ctx context.Context, projectDir, env string, e *KCLEntities) (envPromotionReader, envLedger, error) {
			l, err := ledgerForRendered(ctx, projectDir, env, e)
			if err != nil {
				return nil, envLedger{}, err
			}
			return l.Bindings, l, nil
		}
	}
	if d.Lister == nil {
		d.Lister = kubectlImageLister{}
	}
	if d.LiveDiff == nil {
		d.LiveDiff = verifyBundleLiveDiff
	}
	if d.Secrets == nil {
		d.Secrets = func(e *KCLEntities) hookSecretResolver {
			if p := externalHookSecretProvider(e); p != nil {
				return p
			}
			return nil
		}
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.Hooks.Stderr == nil {
		d.Hooks.Stderr = d.Stderr
	}
	return d
}

// runEnvVerify is the command. The returned error carries the exit code.
func runEnvVerify(ctx context.Context, out io.Writer, opts envVerifyOptions, deps envVerifyDeps) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deps = deps.withDefaults()
	projectDir := projectDirForKCL()
	doc := envVerifyDocument{Env: opts.Env, Workloads: []envVerifyRow{}}

	entities, err := deps.Render(ctx, projectDir, opts.Env)
	if err != nil {
		return finishEnvVerify(out, opts, &doc, fmt.Errorf("render env %s: %w", opts.Env, err))
	}
	selected, err := selectVerifyWorkloads(entities, opts)
	if err != nil {
		return finishEnvVerify(out, opts, &doc, exitCodeError{code: exitWrong, msg: err.Error()})
	}

	var pins map[string]string
	var ledger envLedger
	reader, ledger, err := deps.Ledger(ctx, projectDir, opts.Env, entities)
	if err != nil {
		return finishEnvVerify(out, opts, &doc, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("read the promotion ledger for %s: %v", opts.Env, err)})
	}
	binding, bound, err := reader.Current(ctx, opts.Env)
	if err != nil {
		return finishEnvVerify(out, opts, &doc, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("read the promotion ledger for %s (%s): %v", opts.Env, reader.Location(), err)})
	}
	if bound {
		doc.Release = binding.Release
		pins = expandLegacyLedgerKeys(binding.Resolved, releaseArtifactURIs(ctx, ledger.Releases, binding.Release))
	}

	v := envVerifier{ctx: ctx, deps: deps, opts: opts, projectDir: projectDir, entities: entities,
		pins: pins, release: doc.Release, ledger: ledger, liveDiffs: map[string]clusterLiveDiff{}}
	for _, w := range selected {
		doc.Workloads = append(doc.Workloads, v.row(w))
	}
	return finishEnvVerify(out, opts, &doc, verifyVerdict(doc.Workloads))
}

// selectVerifyWorkloads is the render's workloads, narrowed by --workload,
// with every --image and --workload name checked against them.
func selectVerifyWorkloads(e *KCLEntities, opts envVerifyOptions) ([]WorkloadEntity, error) {
	known := map[string]bool{}
	var names []string
	for _, w := range e.Workloads {
		known[w.Name] = true
		names = append(names, w.Name)
	}
	sort.Strings(names)
	for _, n := range append(append([]string{}, opts.Workloads...), verifyImageNames(opts.Images)...) {
		if !known[n] {
			return nil, fmt.Errorf("env %s declares no workload %q (it declares: %s)", opts.Env, n, strings.Join(names, ", "))
		}
	}
	var out []WorkloadEntity
	for _, w := range e.Workloads {
		if len(opts.Workloads) == 0 || slices.Contains(opts.Workloads, w.Name) {
			out = append(out, w)
		}
	}
	return out, nil
}

func verifyImageNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type envVerifier struct {
	ctx        context.Context
	deps       envVerifyDeps
	opts       envVerifyOptions
	projectDir string
	entities   *KCLEntities
	pins       map[string]string
	release    string
	ledger     envLedger
	// liveDiffs caches one live diff per kube context; liveDiffErr is the
	// error the whole bundle diff returned (once).
	liveDiffs   map[string]clusterLiveDiff
	liveDiffRun bool
	liveDiffErr error
}

// expected is the reference forge expects w to run and where that came from:
// --image, else the promotion's pin for its repository, else a digest the
// declaration itself pins. "" when there is none.
func (v *envVerifier) expected(w WorkloadEntity) (ref string, source string) {
	if ref := v.opts.Images[w.Name]; ref != "" {
		return ref, "--image"
	}
	declared := parseImageRef(w.Spec.Image)
	if declared.Repository == "" {
		declared = parseImageRef(w.Image)
	}
	if declared.Repository == "" {
		return "", ""
	}
	if declared.Tag != "" {
		if d := v.pins[declared.Repository+":"+declared.Tag]; d != "" {
			return declared.Repository + "@" + d, "promotion " + v.release
		}
	}
	if d := v.pins[declared.Repository]; d != "" {
		return declared.Repository + "@" + d, "promotion " + v.release
	}
	if declared.Digest != "" {
		return declared.Repository + "@" + declared.Digest, "declared image"
	}
	return "", ""
}

func (v *envVerifier) row(w WorkloadEntity) envVerifyRow {
	r := envVerifyRow{Workload: w.Name, Runtime: w.Runtime.Type}
	switch w.Runtime.Type {
	case RuntimeExternal:
		r.Image, r.Config = v.external(w)
	case RuntimeCluster:
		r.Image, r.Config = v.cluster(w)
	case RuntimeHosted:
		why := fmt.Sprintf("forge.OnHosted: the control plane runs it; `forge env status %s` reads it through its observer", v.opts.Env)
		r.Image = verifyCheck{State: verifyUnverified, Detail: why}
		r.Config = verifyCheck{State: verifyUnverified, Detail: why}
	default:
		why := fmt.Sprintf("the %s runtime is not a deploy target verify judges", w.Runtime.Type)
		r.Image = verifyCheck{State: verifySkipped, Detail: why}
		r.Config = verifyCheck{State: verifySkipped, Detail: why}
	}
	r.State = r.Image.State
	if r.Config.State.rank() > r.State.rank() {
		r.State = r.Config.State
	}
	return r
}

// external checks a forge.OnExternal workload through its read-only hooks.
func (v *envVerifier) external(w WorkloadEntity) (img, cfg verifyCheck) {
	ref, source := v.expected(w)
	spec := externalSpecFor(v.opts.Env, w, v.entities, v.deps.Secrets(v.entities), v.deps.Stderr)
	parsed := parseImageRef(ref)
	if ref != "" {
		spec.Image = ref
		img.Expected = parsed.Digest
	}
	spec.Release = v.release
	spec.Version = v.release
	if spec.Version == "" {
		spec.Version = parseImageRef(w.Spec.Image).Tag
	}

	switch {
	case w.Runtime.External.Status == nil:
		img.State, img.Detail = verifyUnverified, "no status hook (forge.OnExternal.status): forge cannot read what is running"
	default:
		st, err := v.deps.Hooks.Status(v.ctx, *spec)
		switch {
		case err != nil:
			img.State, img.Detail = verifyError, err.Error()
		case st.Digest == "":
			img.State, img.Detail = verifyUnverified, "the status hook reported no digest"
		case ref == "":
			img.Running = st.Digest
			img.State, img.Detail = verifyUnverified, "no expected digest: the env has no promotion pinning this image; pass --image "+w.Name+"=<repo>@sha256:…"
		case st.Digest == parsed.Digest:
			img.Running, img.State, img.Detail = st.Digest, verifyMatch, "expected from "+source
		default:
			img.Running, img.State = st.Digest, verifyDrift
			img.Detail = fmt.Sprintf("running %s, expected %s (from %s)", shortDigest(st.Digest), shortDigest(parsed.Digest), source)
		}
	}

	if w.Runtime.External.Plan == nil {
		cfg.State, cfg.Detail = verifyUnverified, "no plan hook (forge.OnExternal.plan): config parity not checked — image identity only"
		return img, cfg
	}
	var out strings.Builder
	outcome, err := v.deps.Hooks.Plan(v.ctx, *spec, &out)
	cfg.Output = strings.TrimRight(out.String(), "\n")
	switch {
	case err != nil:
		cfg.State, cfg.Detail = verifyError, err.Error()
	case outcome == deploytarget.PlanChanges:
		cfg.State, cfg.Detail = verifyDrift, "the plan hook reports changes (exit 2)"
	default:
		cfg.State, cfg.Detail = verifyMatch, "the plan hook reports no change (exit 0)"
	}
	return img, cfg
}

// cluster checks a forge.OnCluster workload: the running digest off the
// cluster, and the recorded bundle's client-side live diff.
func (v *envVerifier) cluster(w WorkloadEntity) (img, cfg verifyCheck) {
	c := w.Runtime.Cluster
	ref, source := v.expected(w)
	if ref == "" {
		img.State, img.Detail = verifyUnverified, "no expected digest: the env has no promotion pinning this image; pass --image "+w.Name+"=<repo>@sha256:…"
	} else {
		parsed := parseImageRef(ref)
		img.Expected = parsed.Digest
		running, err := v.deps.Lister.ListWorkloadImages(v.ctx, c.Cluster, c.Namespace)
		var results []imageVerification
		if err != nil {
			results = unreachableVerifications(map[string]string{parsed.Repository: parsed.Digest}, err)
		} else {
			results = verifyEnvImages(running, map[string]string{parsed.Repository: parsed.Digest})
		}
		img = clusterImageCheck(results, parsed.Digest, source)
	}

	if v.release == "" {
		cfg.State, cfg.Detail = verifyUnverified, "not verifiable without a build: no release is promoted, so there is no recorded bundle to diff"
		return img, cfg
	}
	d, err := v.clusterDiff(c.Cluster)
	switch {
	case err != nil && d.Cluster == "":
		cfg.State, cfg.Detail = verifyUnverified, "not verifiable without a build: "+err.Error()
	case d.Error != "":
		cfg.State, cfg.Detail = verifyError, "live diff against "+c.Cluster+": "+d.Error
	case len(d.Changed) > 0:
		cfg.State = verifyDrift
		cfg.Detail = fmt.Sprintf("%d object(s) on %s differ from release %s's bundle", len(d.Changed), c.Cluster, v.release)
		cfg.Output = strings.Join(d.Changed, "\n")
	case len(d.Undiffable) > 0:
		cfg.State, cfg.Detail = verifyError, fmt.Sprintf("%d object(s) on %s could not be diffed", len(d.Undiffable), c.Cluster)
	default:
		cfg.State, cfg.Detail = verifyMatch, fmt.Sprintf("%s matches release %s's bundle (cluster-wide client-side diff)", c.Cluster, v.release)
	}
	return img, cfg
}

func clusterImageCheck(results []imageVerification, expected, source string) verifyCheck {
	chk := verifyCheck{Expected: expected}
	if len(results) == 0 {
		chk.State, chk.Detail = verifyError, "the cluster read returned no verdict"
		return chk
	}
	r := results[0]
	chk.Running = r.Running
	switch r.State {
	case imageMatch:
		chk.State, chk.Detail = verifyMatch, "expected from "+source
	case imageDrift:
		chk.State, chk.Detail = verifyDrift, fmt.Sprintf("running %s, expected %s (from %s)", shortDigest(r.Running), shortDigest(expected), source)
	case imageMissing:
		chk.State, chk.Detail = verifyDrift, "not running in the cluster"
	case imageUntagged:
		chk.State, chk.Detail = verifyUnverified, "running by tag, so its digest cannot be compared: "+r.Running
	default:
		chk.State, chk.Detail = verifyError, emptyAs(r.Detail, "the cluster could not be read")
	}
	return chk
}

// clusterDiff is the bound release bundle's live diff for one kube context,
// computed once for the whole env.
func (v *envVerifier) clusterDiff(kubeContext string) (clusterLiveDiff, error) {
	if !v.liveDiffRun {
		v.liveDiffRun = true
		diffs, err := v.deps.LiveDiff(v.ctx, v.projectDir, v.opts.Env, v.release, v.ledger)
		v.liveDiffErr = err
		for _, d := range diffs {
			v.liveDiffs[d.Cluster] = d
		}
	}
	if d, ok := v.liveDiffs[kubeContext]; ok {
		return d, nil
	}
	if v.liveDiffErr != nil {
		return clusterLiveDiff{}, v.liveDiffErr
	}
	return clusterLiveDiff{}, fmt.Errorf("release %s's bundle carries nothing for %s", v.release, kubeContext)
}

// verifyBundleLiveDiff is the production live diff: pull the bound release's
// recorded bundle into a temp dir and kubectl-diff it client side. It reads;
// the temp dir is removed.
func verifyBundleLiveDiff(ctx context.Context, projectDir, env, version string, ledger envLedger) ([]clusterLiveDiff, error) {
	ref, err := resolveReleaseBundleRef(ctx, projectDir, env, version, ledger)
	if err != nil {
		return nil, err
	}
	fetched, err := fetchBundleRef(ctx, projectDir, env, ref)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}
	dir, err := os.MkdirTemp("", "forge-env-verify-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := unpackBundleInto(fetched, dir); err != nil {
		return nil, err
	}
	return liveDiffBundle(ctx, dir, fetched.Doc.ClusterPaths)
}

// verifyVerdict is the process outcome of a report: drift wins (1), then
// anything unverified or failed (2), else success.
func verifyVerdict(rows []envVerifyRow) error {
	var drift, unsure []string
	for _, r := range rows {
		switch r.State {
		case verifyDrift:
			drift = append(drift, r.Workload)
		case verifyUnverified, verifyError:
			unsure = append(unsure, r.Workload)
		}
	}
	switch {
	case len(drift) > 0:
		return exitCodeError{code: exitWrong, msg: "drift: " + strings.Join(drift, ", ")}
	case len(unsure) > 0:
		return exitCodeError{code: exitUndetermined, msg: "not verified: " + strings.Join(unsure, ", ")}
	}
	return nil
}

func finishEnvVerify(out io.Writer, opts envVerifyOptions, doc *envVerifyDocument, err error) error {
	for _, r := range doc.Workloads {
		switch r.State {
		case verifyMatch:
			doc.Summary.Match++
		case verifyDrift:
			doc.Summary.Drift++
		case verifyUnverified:
			doc.Summary.Unverified++
		case verifyError:
			doc.Summary.Error++
		case verifySkipped:
			doc.Summary.Skipped++
		}
	}
	doc.stamp(err)
	if opts.JSON {
		if jerr := writeJSONDocument(out, doc); jerr != nil {
			return jerr
		}
		return err
	}
	renderEnvVerify(out, doc)
	return err
}

func renderEnvVerify(out io.Writer, doc *envVerifyDocument) {
	release := doc.Release
	if release == "" {
		release = "(no release promoted)"
	}
	fmt.Fprintf(out, "Verifying env %s against %s — read-only: nothing is built, recorded or deployed\n\n", doc.Env, release)
	if len(doc.Workloads) > 0 {
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "WORKLOAD\tRUNTIME\tIMAGE\tCONFIG\tRESULT")
		for _, r := range doc.Workloads {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Workload, r.Runtime, r.Image.State, r.Config.State, strings.ToUpper(string(r.State)))
		}
		_ = tw.Flush()
	}
	for _, r := range doc.Workloads {
		if r.State == verifyMatch {
			continue
		}
		fmt.Fprintf(out, "\n%s:\n", r.Workload)
		for _, c := range []struct {
			label string
			chk   verifyCheck
		}{{"image", r.Image}, {"config", r.Config}} {
			if c.chk.Detail != "" {
				fmt.Fprintf(out, "  %-6s %s: %s\n", c.label, c.chk.State, c.chk.Detail)
			}
			if c.chk.Output != "" {
				for _, line := range strings.Split(c.chk.Output, "\n") {
					fmt.Fprintf(out, "         | %s\n", line)
				}
			}
		}
	}
	s := doc.Summary
	fmt.Fprintf(out, "\n%d match, %d drift, %d unverified, %d error, %d skipped\n", s.Match, s.Drift, s.Unverified, s.Error, s.Skipped)
	if doc.Error != "" {
		fmt.Fprintf(out, "exit %d: %s\n", doc.ExitCode, doc.Error)
	}
}
