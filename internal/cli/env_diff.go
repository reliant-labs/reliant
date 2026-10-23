package cli

// `forge env diff <env>|--all` — the What-if primitive (doc §8), which the
// UI calls Preview.
//
// It renders a checkout and diffs each env's shape against Live. The whole
// comparison is a SET DIFF over per-object hashes, which is why a shape
// carries one hash per object: no manifest is fetched or parsed, and the
// diff itself is under 10ms. The cost is the render.
//
// WHY IT RENDERS IN PLACE, in the selected checkout, rather than into a
// snapshot (§8.3). A `git archive <tree>` into a temp dir would be far
// easier to reason about and it RENDERS WRONG: KCL resolves sibling
// checkouts by relative path — control-plane's dev render runs host
// processes from `../reliant` — and from a temp dir `../reliant` does not
// exist. A diff computed against a render that silently lost its siblings is
// worse than no diff.
//
// So it renders in place, under three guards, and each one has a per-env
// status rather than a global failure (F-8). A partial diff that hid a
// failure would read as "no changes", which is the single most dangerous
// thing this command could say:
//
//  1. NO WRITES. The render runs with the existing write-watch armed. forge
//     cannot promise purity — KCL evaluates `file.write` during evaluation
//     and there is no hook to suppress a project's own writes — so a render
//     that wrote anything is reported `impure` WITH THE PATHS and its result
//     is discarded. What-if must never modify a user's worktree.
//  2. A TREE-HASH BRACKET. The tree hash is taken before and after. If it
//     moved, the user was editing while we rendered, the result describes
//     neither state, and the env is `stale` — retried once, because the
//     common case is a single save landing mid-render.
//
// THE LIVE SIDE IS THE APPLIED BUNDLE, NOT declared_shape, and the order
// matters: `current_bundle.shape`, else `declared_shape`, else nothing
// recorded. `forge env build` REFRESHES declared_shape from the very render
// being deployed, so an env that has been built since its last deploy would
// diff against its own candidate and report no changes — hiding exactly what
// the user opened Preview to see. declared_shape is the fallback for
// pre-bundle history only, and an env with neither is reported as "no
// recorded config", never as "everything is added".

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/pkg/release"
)

// envDiffStatus is how one env's diff turned out. Closed, and every value
// other than "ok" means the DIFF IS NOT USABLE — a reader must not present a
// non-ok env's diff as a comparison, and must disable deploying it (F-8).
type envDiffStatus string

const (
	// diffOK: the render succeeded, was pure, and the tree held still.
	diffOK envDiffStatus = "ok"
	// diffImpure: the render wrote files. The paths are in the report.
	diffImpure envDiffStatus = "impure"
	// diffStale: the tree hash moved across the render, twice.
	diffStale envDiffStatus = "stale"
	// diffError: the render failed. forge's own message is in the report.
	diffError envDiffStatus = "error"
)

// envDiffDoc is the §8.2 contract: one document, one entry per env.
type envDiffDoc struct {
	// Project and Source say WHAT was compared. A reader showing a diff
	// has to be able to say which checkout produced it.
	Project string `json:"project"`
	Source  string `json:"source"`
	// Against is "live" or a bundle reference.
	Against string `json:"against"`
	// Charts records whether Helm charts were templated. Off by default,
	// and the flag is reported because a diff that skipped charts has a
	// different meaning from one that compared them.
	Charts bool `json:"charts"`
	// Environments is one entry per env, sorted by name.
	Environments []envDiffEntry `json:"environments"`
}

// envDiffEntry is one env's result.
//
// THE STATUS COMES FIRST, and Diff is nil for every non-ok status. A
// consumer that read Diff without checking Status would render a failed
// render as "no changes", so there is nothing there to read.
type envDiffEntry struct {
	Env    string        `json:"env"`
	Status envDiffStatus `json:"status"`
	// Detail is the human reason for a non-ok status: forge's render
	// error, or what went stale.
	Detail string `json:"detail,omitempty"`
	// Wrote are the paths an impure render touched (§8.3 guard 1).
	Wrote []string `json:"wrote,omitempty"`

	// Diff is the comparison, present only when Status is ok.
	Diff *release.ShapeDiff `json:"diff,omitempty"`
	// WouldBeCreated: declared in the checkout, absent from Live — no env
	// row and no file-ledger record (§8.2).
	WouldBeCreated bool `json:"would_be_created,omitempty"`
	// LiveSource says where the Live side came from: "bundle",
	// "declared_shape", or "none". A reader needs it to label the
	// comparison honestly — a diff against declared_shape is weaker
	// evidence than one against an applied bundle.
	LiveSource string `json:"live_source,omitempty"`
	// ConfigIdentical is the §8.2 short-circuit: the candidate's render
	// normalized to the Live bundle's config_digest, so there is nothing
	// to walk.
	ConfigIdentical bool `json:"config_identical,omitempty"`
	// SecretPresence maps each declared secret NAME to whether it is set.
	// A name ABSENT from the map is unverifiable, never missing — the
	// forge#398 rule. No value is ever carried.
	SecretPresence map[string]bool `json:"secret_presence,omitempty"`
}

// envDiffConcurrency is how many envs render at once (§8.4).
//
// TWO, deliberately, and this is the opposite call from `project
// checkouts`'s unbounded git reads. Rendering is CPU-bound KCL — 2.2–2.5s
// per env — and the daemon shares its machine with the user's dev stack, so
// the bound is about not starving that. It is per daemon rather than per
// request for the same reason.
const envDiffConcurrency = 2

// newEnvDiffCmd is `forge env diff`.
func newEnvDiffCmd() *cobra.Command {
	var (
		all     bool
		against string
		charts  bool
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "diff <environment>|--all",
		Short: "Render this checkout and diff each environment against what is deployed",
		Args:  cobra.MaximumNArgs(1),
		Long: `Render the environments declared in this checkout and compare each one against
what is actually deployed. READ-ONLY: no cluster is contacted, no image is
built, nothing is pushed.

WHAT IS COMPARED. Shapes, not manifests — the diff is a set comparison over
one hash per rendered object, so it needs no YAML. You get: objects added,
removed and changed (with image changes split out from config changes),
workloads added and removed, runtime and cluster moves, secrets newly needed,
and domain changes.

THE LIVE SIDE is the last recorded BUNDLE's shape — what was actually
deployed. An environment with no bundle yet falls back to its declared shape,
and one with neither is reported as "no recorded config" rather than as
"everything is new". The report says which, because a diff against a declared
shape is weaker evidence than one against an applied bundle.

THREE GUARDS, each with a PER-ENVIRONMENT status. A failure never silently
reads as "no changes":

  ok           the render succeeded, wrote nothing, and the tree held still
  impure       the render wrote files; the paths are listed and the result is
               discarded
  stale        the tree changed while rendering (you were editing); retried once
  error        the render failed; forge's message is included

Charts are NOT templated by default: that needs ` + "`helm`" + ` and the network, and a
chart's objects belong to the platform dependency rather than this project.
` + "`--charts`" + ` opts in.

Examples:
  ` + Name() + ` env diff prod                 # one environment
  ` + Name() + ` env diff --all                # every declared environment
  ` + Name() + ` env diff --all --json         # the daemon's form`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 0) == !all {
				return fmt.Errorf("name an environment or pass --all (not both, and not neither)")
			}
			env := ""
			if len(args) == 1 {
				env = args[0]
			}
			return runEnvDiff(cmd, envDiffOptions{
				Env:     env,
				All:     all,
				Against: against,
				Charts:  charts,
				JSON:    asJSON,
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Diff every environment declared in this checkout")
	cmd.Flags().StringVar(&against, "against", diffAgainstLive, "What to compare against. Only \"live\" (what is currently deployed) is implemented; a bundle reference is refused rather than silently compared against live")
	cmd.Flags().BoolVar(&charts, "charts", false, "Also template Helm charts (needs helm and the network; off by default)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the §8.2 document as JSON (the form the daemon's hook returns)")
	return cmd
}

// envDiffOptions is one invocation.
type envDiffOptions struct {
	Env     string
	All     bool
	Against string
	Charts  bool
	JSON    bool
}

// runEnvDiff renders the selected envs and diffs them.
func runEnvDiff(cmd *cobra.Command, opts envDiffOptions) error {
	// STDOUT CARRIES ONLY THE DOCUMENT in --json mode. The render path
	// prints progress with fmt.Printf in dozens of places, and one prose
	// line makes the JSON unparseable — so the diversion is structural
	// here rather than a property every callee has to remember. Same
	// guard `forge env shape` uses.
	out := cmd.OutOrStdout()
	if opts.JSON {
		realStdout := os.Stdout
		os.Stdout = os.Stderr
		defer func() { os.Stdout = realStdout }()
	}

	projectDir := projectDirForKCL()
	if err := checkDiffAgainst(opts.Against); err != nil {
		return err
	}
	envs, err := envDiffTargets(projectDir, opts)
	if err != nil {
		return err
	}
	doc := envDiffDoc{
		Project:      hostedProjectName(),
		Source:       projectDir,
		Against:      opts.Against,
		Charts:       opts.Charts,
		Environments: diffEnvironments(cmd.Context(), projectDir, envs, opts, cmd.ErrOrStderr()),
	}
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	writeEnvDiff(out, doc)
	return nil
}

// diffAgainstLive is the only comparison target implemented.
const diffAgainstLive = "live"

// checkDiffAgainst refuses a target this build cannot actually compare
// against.
//
// IT REFUSES RATHER THAN FALLING BACK TO LIVE, and that is the whole reason
// this function exists. `--against <bundle-ref>` is in the §7.2 flag set,
// but fetching a named bundle by reference and diffing against its shape is
// not built — so accepting the flag and comparing against Live anyway would
// print "diff of <checkout> against <some-bundle>" above a comparison that
// was made against something else entirely. A wrong answer labelled as the
// right one is the one outcome worse than a missing feature.
func checkDiffAgainst(against string) error {
	if strings.TrimSpace(against) == "" || against == diffAgainstLive {
		return nil
	}
	return fmt.Errorf("--against %q is not implemented: this build can only diff against %q (what is currently deployed).\n"+
		"  Diffing against a named bundle needs that bundle fetched by reference, which is a separate change.\n"+
		"  Refusing rather than silently comparing against live, which would label the wrong answer as the right one",
		against, diffAgainstLive)
}

// envDiffTargets is which envs to diff.
func envDiffTargets(projectDir string, opts envDiffOptions) ([]string, error) {
	if !opts.All {
		return []string{opts.Env}, nil
	}
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("list the environments declared in %s: %w", projectDir, err)
	}
	if len(envs) == 0 {
		return nil, fmt.Errorf("%s declares no environments under deploy/kcl/", projectDir)
	}
	sort.Strings(envs)
	return envs, nil
}

// diffEnvironments renders and diffs each env, at most envDiffConcurrency at
// a time, and returns the entries in the order the envs were given.
func diffEnvironments(ctx context.Context, projectDir string, envs []string, opts envDiffOptions, errOut io.Writer) []envDiffEntry {
	// The Live side is fetched ONCE for the whole project, not per env.
	// GetLiveView is one round trip returning every env's bundle shape
	// (which is why §8.0 made it one call), so a per-env fetch would be N
	// round trips for data the first one already held.
	live := fetchLiveShapes(ctx, projectDir, envs, errOut)

	entries := make([]envDiffEntry, len(envs))
	sem := make(chan struct{}, envDiffConcurrency)
	var wg sync.WaitGroup
	for i, env := range envs {
		wg.Add(1)
		go func(i int, env string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entries[i] = diffOneEnv(ctx, projectDir, env, live[env], opts, errOut)
		}(i, env)
	}
	wg.Wait()
	return entries
}

// liveSide is what the control plane knows about one env.
type liveSide struct {
	// Shape is the comparison's live side, or nil when nothing is
	// recorded.
	Shape *release.Shape
	// Source is "bundle", "declared_shape" or "none".
	Source string
	// ConfigDigest is the applied bundle's, for the §8.2 short-circuit.
	ConfigDigest string
	// Known: the control plane has a row for this env. False means the
	// env would be CREATED, which is different from an env that exists
	// with nothing recorded.
	Known bool
	// Secrets maps a declared secret name to whether it is set. A name
	// absent from the map is UNVERIFIABLE, never missing (forge#398).
	Secrets map[string]bool
}

// fetchLiveShapes reads every env's Live side in one call.
//
// A control plane that cannot be reached is NOT an error: it yields an empty
// map, every env reports LiveSource "none", and the diff says "no recorded
// config". That is the §8.0 empty-versus-unknown rule — but it is also where
// this command is weakest, so the reason is printed to stderr rather than
// swallowed. A silent fall-through to "everything is new" would be
// indistinguishable from a genuinely never-deployed project.
func fetchLiveShapes(ctx context.Context, projectDir string, envs []string, errOut io.Writer) map[string]liveSide {
	out := map[string]liveSide{}
	// The declaration comes from any env that has one: a project's envs
	// share a control plane, and asking the first that declares one
	// avoids a render per env purely to find the endpoint.
	client, project, ok := liveViewClient(ctx, projectDir, envs, errOut)
	if !ok {
		return out
	}
	environments, err := hostedLiveClient{client: client}.GetLiveView(ctx, project, false, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not read Live (%v); every environment will report \"no recorded config\"\n", err)
		return out
	}
	for _, env := range environments {
		side := liveSide{Known: true, Source: "none"}
		switch {
		case env.CurrentBundle != nil:
			// THE APPLIED BUNDLE, which is the only honest live
			// side: declared_shape is refreshed by every build and
			// would hide the change the user is looking for.
			shape := env.CurrentBundle.Shape
			side.Shape, side.Source = &shape, "bundle"
			side.ConfigDigest = env.CurrentBundle.ConfigDigest
		case env.Env.DeclaredShape != nil:
			// Pre-bundle history only.
			side.Shape, side.Source = env.Env.DeclaredShape, "declared_shape"
		}
		side.Secrets = fetchSecretPresence(ctx, client, env, errOut)
		out[env.Env.Name] = side
	}
	return out
}

// fetchSecretPresence asks the MANAGED secret store which of an env's
// secrets are set (§8.2's "new secrets needed" row).
//
// NAMES ONLY. ListSecrets has no value field, so this cannot carry one even
// by accident — which is the property that makes it safe to call from a diff
// a user pastes into a bug report.
//
// A NIL RESULT MEANS UNVERIFIABLE, NOT MISSING, and this is the forge#398
// rule at its source rather than at the renderer. An env whose secrets live
// with an external provider (ExternalSecrets, a cloud secret manager) cannot
// be queried from here at all, and a failed or unavailable read is the same
// situation. Returning an empty map of falses instead would put "MISSING —
// you must set this" in front of a user whose secrets are perfectly fine,
// which is worse than saying nothing.
func fetchSecretPresence(ctx context.Context, client cloudCaller, env LiveEnvironment, errOut io.Writer) map[string]bool {
	if env.EnvironmentID == "" {
		// No row, so nothing to ask about. A never-registered env's
		// secrets are unverifiable by definition.
		return nil
	}
	// Only the MANAGED store can be read. An env whose declared shape
	// names another provider is left unverifiable rather than queried
	// against a store that does not hold its values — which would report
	// every secret missing.
	if !managedSecretProvider(env) {
		return nil
	}
	summaries, err := cloudSecretWriter{client: client, environmentID: env.EnvironmentID}.List(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not read %s's secret presence (%v); its secrets are reported as unverifiable\n",
			env.Env.Name, err)
		return nil
	}
	presence := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		// present() is the store's own verdict: a current version that
		// is neither deleted nor destroyed. Re-deriving it here could
		// disagree with what `forge env secrets list` shows.
		presence[s.Name] = s.present()
	}
	return presence
}

// managedSecretProvider reports whether this env's secrets are held by the
// control plane's own store, which is the only one presence can be read
// from.
//
// Read from the env's DECLARED shape, because that is the fact the control
// plane holds without a render — and this whole function runs on the Live
// side, which must not need a checkout.
func managedSecretProvider(env LiveEnvironment) bool {
	shape := env.Env.DeclaredShape
	if shape == nil {
		if env.CurrentBundle == nil {
			return false
		}
		shape = &env.CurrentBundle.Shape
	}
	for _, s := range shape.Secrets {
		// One managed secret is enough to make the store worth asking:
		// a mixed env's managed names get a real verdict and the rest
		// stay absent, which is exactly the per-name distinction the
		// forge#398 rule asks for.
		if s.Provider == "hosted" || s.Provider == "managed" {
			return true
		}
	}
	return false
}

// liveViewClient resolves the control plane to ask, from whichever env
// declares one.
func liveViewClient(ctx context.Context, projectDir string, envs []string, errOut io.Writer) (cloudCaller, string, bool) {
	for _, env := range envs {
		entities, err := RenderKCL(ctx, projectDir, env)
		if err != nil {
			continue
		}
		decl := declarationFromEntities(entities)
		if decl == nil {
			continue
		}
		ep, err := cloud.ResolveEndpoint(env, decl)
		if err != nil {
			continue
		}
		cred, err := cloud.ResolveCredential("", ep)
		if err != nil {
			fmt.Fprintf(errOut, "warning: %s declares a control plane at %s but no credential resolved (%v); "+
				"the diff will report \"no recorded config\"\n", env, ep.URL, err)
			return nil, "", false
		}
		return cloud.NewClient(ep, cred), hostedProjectName(), true
	}
	// No env declares a control plane. That is a normal local-only
	// project, not a failure, and it needs no warning: there is nothing
	// to have been reached.
	return nil, "", false
}

// diffOneEnv renders one env under the §8.3 guards and diffs it.
func diffOneEnv(ctx context.Context, projectDir, env string, live liveSide, opts envDiffOptions, errOut io.Writer) envDiffEntry {
	const attempts = 2 // the original, plus the ONE retry guard 2 allows
	var entry envDiffEntry
	for attempt := 0; attempt < attempts; attempt++ {
		entry = renderAndDiffOnce(ctx, projectDir, env, live, opts, errOut)
		if entry.Status != diffStale {
			return entry
		}
	}
	// Still stale after the retry: the user is actively editing, and
	// another attempt would be as likely to lose. Reported rather than
	// retried forever.
	entry.Detail = "the checkout changed while rendering, twice — " +
		"the result would describe neither state. Re-run when your edits have settled."
	return entry
}

// renderAndDiffOnce is one bracketed attempt.
func renderAndDiffOnce(ctx context.Context, projectDir, env string, live liveSide, opts envDiffOptions, errOut io.Writer) envDiffEntry {
	entry := envDiffEntry{Env: env, Status: diffOK, LiveSource: emptyAs(live.Source, "none")}

	// GUARD 2, opening half: the tree hash BEFORE the render. An empty
	// hash means the tree could not be hashed (F-16), in which case the
	// bracket is skipped rather than reported as stale — "I could not
	// measure" must not read as "it moved".
	before := checkoutTreeHash(ctx, projectDir)

	doc, wrote, err := renderEnvShapeForDiff(ctx, projectDir, env, errOut)

	// GUARD 2, closing half — taken BEFORE the error is reported, so a
	// render that failed BECAUSE the tree moved under it is reported as
	// stale (and retried) rather than as a permanent error.
	if before != "" {
		if after := checkoutTreeHash(ctx, projectDir); after != before {
			entry.Status = diffStale
			return entry
		}
	}

	// GUARD 1: an impure render's result is DISCARDED, not reported with a
	// warning. A declaration derived from a render nobody can reproduce
	// is not evidence, and the paths are what makes the report
	// actionable.
	if len(wrote) > 0 {
		entry.Status, entry.Wrote = diffImpure, wrote
		entry.Detail = fmt.Sprintf("the render wrote %d file(s), so this diff is not reproducible and was discarded. "+
			"KCL evaluates file.write during evaluation and forge cannot suppress a project's own writes.", len(wrote))
		return entry
	}
	if err != nil {
		entry.Status, entry.Detail = diffError, err.Error()
		return entry
	}

	entry.WouldBeCreated = !live.Known
	// §8.2's short-circuit: a candidate whose render normalizes to the
	// Live bundle's config_digest reports "config identical" with no
	// per-object walk. It is reported rather than used to skip the diff,
	// because the diff is already computed and under 10ms — the value is
	// in SAYING so, which is a stronger statement than an empty diff.
	//
	// Computed through release.ConfigDigest, F2's shared implementation,
	// so the digest a bundle recorded and the digest this render produces
	// cannot disagree. A second implementation here would show up as a
	// short-circuit that never fires.
	if live.ConfigDigest != "" {
		if candidate, derr := release.ConfigDigest(doc.Shape); derr == nil {
			entry.ConfigIdentical = candidate == live.ConfigDigest
		}
	}
	diff := release.DiffShapes(live.Shape, doc.Shape)
	entry.Diff = &diff
	entry.SecretPresence = secretPresenceFor(doc.Shape, live)
	return entry
}

// secretPresenceFor joins the candidate's declared secret names with what is
// known about whether each is set.
//
// A NAME ABSENT FROM THE RESULT IS UNVERIFIABLE, never missing — the
// forge#398 rule, and the reason this returns a partial map rather than
// defaulting to false. An external-provider env (ExternalSecrets, a cloud
// secret manager) cannot be queried from here, so claiming its secrets are
// missing would put a false "you must set this" in front of a user whose
// secrets are fine.
func secretPresenceFor(candidate release.Shape, live liveSide) map[string]bool {
	if len(live.Secrets) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, s := range candidate.Secrets {
		if set, known := live.Secrets[s.Name]; known {
			out[s.Name] = set
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// renderEnvShapeForDiff renders one env read-only and reports what it wrote.
//
// It is a separate path from projectEnvShape (env_shape.go) for ONE reason:
// that function turns an impure render into an ERROR, which is right when a
// caller is about to RECORD a declaration. Here an impure render is a
// per-env status in a document that still reports every other env (F-8), so
// the write verdict has to come back as data rather than as a failure.
//
// A var so a test can supply a fixed shape and a fixed write list without
// evaluating KCL — the projection itself is tested in internal/bundle, and
// the guards are what this file owns.
var renderEnvShapeForDiff = func(ctx context.Context, projectDir, env string, errOut io.Writer) (envShapeDoc, []string, error) {
	unpin := pinPaths(
		filepath.Join(projectDir, ".forge", "ports-"+env+".json"),
		filepath.Join(projectDir, "deploy", "kcl", "kcl.mod.lock"),
	)
	scan := newRenderWriteScan(projectDir, false)
	activateDevStack(ctx, projectDir, env, renderDeclaration, inspectBlocks)
	defer func() {
		unpin()
		reportDeclinedWrites(errOut, kclplugin.SuppressedWrites())
	}()

	doc, err := renderEnvShape(ctx, errOut, projectDir, env)
	unpin()

	var wrote []string
	for _, change := range scan.changes() {
		wrote = append(wrote, change.path)
	}
	sort.Strings(wrote)
	return doc, wrote, err
}

// writeEnvDiff is the human form.
func writeEnvDiff(w io.Writer, doc envDiffDoc) {
	fmt.Fprintf(w, "diff of %s against %s\n", doc.Source, doc.Against)
	if !doc.Charts {
		fmt.Fprintf(w, "  (Helm chart objects are not compared; pass --charts to template them)\n")
	}
	for _, entry := range doc.Environments {
		fmt.Fprintf(w, "\n%s: %s\n", entry.Env, entry.Status)
		if entry.Status != diffOK {
			fmt.Fprintf(w, "  %s\n", entry.Detail)
			for _, path := range entry.Wrote {
				fmt.Fprintf(w, "    wrote %s\n", path)
			}
			continue
		}
		writeOneEnvDiff(w, entry)
	}
}

// writeOneEnvDiff prints one ok env's comparison.
func writeOneEnvDiff(w io.Writer, entry envDiffEntry) {
	switch {
	case entry.WouldBeCreated:
		fmt.Fprintf(w, "  would be created: this environment is declared here and the control plane has no row for it\n")
	case entry.LiveSource == "none":
		// NOT "everything is added", and this RETURNS rather than
		// falling through to the counts. §8.2 is explicit that an env
		// with nothing recorded is reported as such, and printing the
		// object counts underneath would undo the whole point:
		// someone reads "149 added" as 149 changes, when it is one
		// render of an env nothing has ever deployed. The objects are
		// still in --json for a consumer that wants to list them; what
		// the human form must not do is present them as a comparison.
		fmt.Fprintf(w, "  no recorded config: nothing has been built for this environment yet, so there is nothing to compare\n")
		if entry.Diff != nil {
			fmt.Fprintf(w, "  this render declares %d object(s); deploy once and later diffs will be real comparisons\n",
				len(entry.Diff.Added))
		}
		return
	case entry.LiveSource == "declared_shape":
		fmt.Fprintf(w, "  comparing against the DECLARED shape (no bundle recorded yet), which is weaker evidence than an applied bundle\n")
	}
	d := entry.Diff
	if d == nil {
		return
	}
	if d.KindChanged != nil {
		// An ERROR, not a change: a kind is immutable, so this cannot
		// be deployed at all.
		fmt.Fprintf(w, "  ERROR kind is immutable: this environment is %s and cannot become %s\n",
			d.KindChanged.Live, d.KindChanged.Candidate)
	}
	if entry.ConfigIdentical {
		fmt.Fprintf(w, "  config identical: this render normalizes to the deployed bundle's config digest\n")
	}
	if d.Empty() {
		fmt.Fprintf(w, "  no changes\n")
		return
	}
	writeDiffCounts(w, d)
	for _, name := range d.WorkloadsAdded {
		fmt.Fprintf(w, "  + workload %s\n", name)
	}
	for _, name := range d.WorkloadsRemoved {
		fmt.Fprintf(w, "  - workload %s\n", name)
	}
	for _, rc := range d.RuntimeChanges {
		fmt.Fprintf(w, "  ~ %s: %s → %s\n", rc.Workload, describeRuntime(rc.From, rc.FromCluster), describeRuntime(rc.To, rc.ToCluster))
	}
	for _, s := range d.SecretsAdded {
		fmt.Fprintf(w, "  + secret %s\n", describeSecretNeed(s, entry.SecretPresence))
	}
	for _, s := range d.SecretsRemoved {
		fmt.Fprintf(w, "  - secret %s (no longer declared; the value is NOT deleted)\n", s.Name)
	}
	for _, host := range d.DomainsAdded {
		fmt.Fprintf(w, "  + domain %s\n", host)
	}
	for _, host := range d.DomainsRemoved {
		fmt.Fprintf(w, "  - domain %s\n", host)
	}
	for _, name := range d.ClustersAdded {
		fmt.Fprintf(w, "  + cluster %s\n", name)
	}
	for _, name := range d.ClustersRemoved {
		fmt.Fprintf(w, "  - cluster %s\n", name)
	}
}

// writeDiffCounts is the object summary: counts, then the image and config
// changes split out, which is the distinction §8.2 asks for ("config
// changed" versus "would run a new build").
func writeDiffCounts(w io.Writer, d *release.ShapeDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintf(w, "  objects: %d added, %d removed, %d changed\n", len(d.Added), len(d.Removed), len(d.Changed))
	// Grouped by workload, because that is the unit a user thinks in —
	// "admin-server changed", not "seven objects changed".
	byWorkload := map[string][]release.ObjectChange{}
	for _, c := range d.Changed {
		byWorkload[c.Candidate.Workload] = append(byWorkload[c.Candidate.Workload], c)
	}
	names := make([]string, 0, len(byWorkload))
	for name := range byWorkload {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var images, config int
		for _, c := range byWorkload[name] {
			if len(c.Images) > 0 {
				images++
			}
			if c.ConfigChanged {
				config++
			}
		}
		label := emptyAs(name, "(no workload)")
		switch {
		case images > 0 && config > 0:
			fmt.Fprintf(w, "    %s: config changed, and would run a new build\n", label)
		case images > 0:
			fmt.Fprintf(w, "    %s: would run a new build\n", label)
		default:
			fmt.Fprintf(w, "    %s: config changed\n", label)
		}
	}
}

// describeRuntime is "hosted" or "hosted on cluster-a".
func describeRuntime(runtime, cluster string) string {
	if cluster == "" {
		return emptyAs(runtime, "(none)")
	}
	return runtime + " on " + cluster
}

// describeSecretNeed is a newly declared secret, joined with its presence.
//
// A name absent from presence is "presence not verifiable" rather than
// "missing" — the forge#398 rule, stated in the output because the
// difference decides whether a user has something to do.
func describeSecretNeed(s release.ShapeSecret, presence map[string]bool) string {
	line := s.Name
	if s.Provider != "" {
		line += " [" + s.Provider + "]"
	}
	switch set, known := presence[s.Name]; {
	case !known:
		line += " — declared; presence not verifiable from here"
	case set:
		line += " — already set"
	default:
		line += " — MISSING"
	}
	if len(s.DeclaredBy) > 0 {
		line += " (needed by " + strings.Join(s.DeclaredBy, ", ") + ")"
	}
	return line
}
