package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge env status` — the whole release/environment picture in ONE read.
//
// WHY A WHOLE-PROJECT COMMAND EXISTS AT ALL. Every other release verb is
// scoped to one env or one version, because every other release verb ACTS on
// one env or one version. This one answers a question no single-env command
// can: "how do the environments relate to each other" — prod is 15 releases
// and ten weeks ahead of staging, and it carries an image staging does not
// have at all. That is a comparison ACROSS envs, and a comparison across envs
// assembled by a client from N single-env calls is a client that has
// re-implemented forge's data model. The rule this project runs on is that the
// UI never computes anything the CLI cannot, so the CLI computes it: one call,
// one document, the entire screen.
//
// VERIFICATION IS OPT-IN, AND THAT IS THE LOAD-BEARING DESIGN DECISION.
// Reading the live cluster for every env needs credentials for every env,
// which almost nobody has locally, and costs a round-trip per workload even
// when they do. So the default reads the LEDGER only: fast, offline, and
// correct about what was DECLARED. `--verify` additionally reads each env's
// cluster through the same path `forge env status` uses.
//
// The consequence for the output contract is the part that must not be gotten
// wrong: in the default mode every image's state is "not_verified", which is
// UNKNOWN — not OK. A consumer painting the ledger-only response must render
// those cells as unknown and fill them in when a --verify pass returns. A
// state model where "not checked" and "checked and matching" share a value
// produces a green screen over an environment nobody looked at, which is the
// exact failure the five-state model in env_verify.go exists to prevent.

// topologyImageState is one image's reconciliation state in a topology report.
//
// It is imageState's five states plus a SIXTH that only this command can be
// in: not_verified. It is a separate type rather than a reuse of imageState
// because the sixth state is not a verification verdict at all — it is the
// absence of one — and widening imageState to hold it would let "nobody
// looked" leak into `forge env status`, whose entire contract is that every
// value it emits is something it actually checked.
//
// THE ZERO VALUE IS not_verified, DELIBERATELY. In imageState the zero value
// is match, which is why its UnmarshalJSON refuses to default. Here the zero
// value is the unknown state, so a struct that was never populated reads as
// "not checked" rather than as a clean bill of health. Unmarshal still refuses
// unknown strings for the same reason its sibling does: a newer forge with a
// seventh state must fail loudly here instead of decoding into whatever this
// binary's zero value happens to mean.
type topologyImageState int

const (
	// topologyNotVerified: the ledger declares this digest and nothing has
	// been compared against a cluster. Says NOTHING about what is running.
	topologyNotVerified topologyImageState = iota
	// The five below mirror imageState exactly and are only ever produced
	// under --verify.
	topologyMatch
	topologyDrift
	topologyMissing
	topologyUntagged
	topologyUnreachable
)

// String renders the fixed-width label used in the text report, mirroring
// imageState.String() so the two commands' output reads as one family.
func (s topologyImageState) String() string {
	switch s {
	case topologyNotVerified:
		return "NOT-VERIFIED"
	case topologyMatch:
		return "MATCH"
	case topologyDrift:
		return "DRIFT"
	case topologyMissing:
		return "MISSING"
	case topologyUntagged:
		return "UNTAGGED"
	case topologyUnreachable:
		return "UNREACHABLE"
	default:
		return "UNKNOWN"
	}
}

// MarshalJSON emits the lowercase, underscore-separated string form, derived
// from String() so the text column and the JSON value cannot disagree about
// which state a cell is in.
func (s topologyImageState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ReplaceAll(strings.ToLower(s.String()), "-", "_") + `"`), nil
}

// UnmarshalJSON reads the string form back. An unrecognized value is an ERROR:
// decoding it as a default would turn a state this binary does not understand
// into one it does, and every wrong guess in this type's direction ends as a
// green cell over an unchecked environment.
func (s *topologyImageState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("topology image state must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "not_verified":
		*s = topologyNotVerified
	case "match":
		*s = topologyMatch
	case "drift":
		*s = topologyDrift
	case "missing":
		*s = topologyMissing
	case "untagged":
		*s = topologyUntagged
	case "unreachable":
		*s = topologyUnreachable
	default:
		return fmt.Errorf("unknown topology image state %q (expected not_verified, match, drift, missing, untagged or unreachable) — "+
			"refusing to decode it as a default, which would report an unchecked environment as clean", name)
	}
	return nil
}

// topologyStateFor maps a verification verdict into the topology state space.
// Exhaustive by construction: an imageState this switch does not know falls to
// not_verified rather than to a green value.
func topologyStateFor(s imageState) topologyImageState {
	switch s {
	case imageMatch:
		return topologyMatch
	case imageDrift:
		return topologyDrift
	case imageMissing:
		return topologyMissing
	case imageUntagged:
		return topologyUntagged
	case imageUnreachable:
		return topologyUnreachable
	default:
		return topologyNotVerified
	}
}

// topologyImage is one image's row in one environment.
type topologyImage struct {
	// Image is the bare image name, as it keys the binding's Resolved map
	// and the release's Artifacts map ("control-plane", "reliant").
	Image string `json:"image"`
	// Digest is the digest the binding FROZE at promote time. This is the
	// declaration, not an observation.
	Digest string `json:"digest"`
	// State is the reconciliation verdict, "not_verified" unless --verify
	// ran. See topologyImageState: not_verified is unknown, never OK.
	State topologyImageState `json:"state"`
	// Running is the digest (or tag reference) actually observed in the
	// cluster. Only ever populated under --verify.
	Running string `json:"running,omitempty"`
	// Detail is the human-readable reason for a non-match verdict.
	Detail string `json:"detail,omitempty"`
}

// promotionLag is how far behind the newest cut release an environment is.
//
// TWO UNITS, BECAUSE THEY ANSWER DIFFERENT QUESTIONS AND DISAGREE. "15
// releases behind" measures how much CHANGE has accumulated; "10 weeks
// behind" measures how STALE the environment is. An env can be one release
// behind and six months stale (nobody has cut anything), or fifteen releases
// behind and a day stale (a burst of releases this morning). Reporting only
// one of them makes the other invisible, and both are things an operator
// looking at this screen needs to see.
type promotionLag struct {
	// LatestRelease is the newest release cut in this project, which is
	// what the lag is measured against. Note this is the newest release
	// KNOWN LOCALLY — a release cut on another branch and never merged is
	// not in this checkout's .forge/releases and cannot be counted.
	LatestRelease string `json:"latest_release"`
	// Current is true when the env is bound to the newest release. When
	// true both lag figures are zero.
	Current bool `json:"current"`
	// ReleasesBehind counts how many releases were cut AFTER the one this
	// env runs. Zero when current; omitted-as-zero is safe because Current
	// carries the distinction.
	ReleasesBehind int `json:"releases_behind"`
	// BehindSeconds is the wall-clock gap between the newest release's
	// created_at and this env's release's created_at. Zero when either
	// timestamp is unparseable, which Behind then reports as empty rather
	// than as "0s" — an unknown gap and a zero gap are different claims.
	BehindSeconds int64 `json:"behind_seconds,omitempty"`
	// Behind is BehindSeconds rendered for humans ("71d13h"). Empty when
	// the gap could not be computed.
	Behind string `json:"behind,omitempty"`
}

// topologyEnv is one environment's full row: what it is bound to, where that
// came from, where it runs, and how far behind it is.
type topologyEnv struct {
	// Env is the environment name.
	Env string `json:"env"`
	// Lifecycle is the env's declared Bundle.lifecycle ("local",
	// "ephemeral", or "" when unset or not declared in this checkout).
	Lifecycle string `json:"lifecycle"`
	// Declared is true when deploy/kcl/<env>/main.k exists in THIS
	// checkout. An env can be bound without being declared here — a
	// release promoted on a branch that has the env, read from one that
	// does not — and that is a real, reportable state rather than an
	// error. When false, KubeContext and Namespace cannot be resolved.
	Declared bool `json:"declared"`
	// Ledger is where THIS env's promotions are recorded: the endpoint URL
	// of the control plane its KCL declares (forge.ControlPlane), or the
	// project's promotion log. Opaque — for display only.
	Ledger string `json:"ledger,omitempty"`
	// Hosted is true when Ledger is a control plane. It drives the text
	// renderer only and is NOT part of the JSON contract: consumers read
	// `destination == "hosted"`, and a second spelling of the same fact is
	// a second place for the two to disagree.
	Hosted bool `json:"-"`
	// Kind is the current entry's kind. Always "promote": a legacy
	// "rollback" entry reads as the promote of its release.
	Kind release.PromotionKind `json:"kind,omitempty"`
	// Bound is false for an env that has never been promoted. Not a
	// failure: it has declared nothing, so there is nothing to be wrong
	// about. A separate field from an empty Release so a consumer can tell
	// "never promoted" from a binding carrying a blank version.
	Bound bool `json:"bound"`
	// Release is the version label the binding names. Empty when unbound.
	Release string `json:"release,omitempty"`
	// PromotedAt is RFC3339 for when the env was PROMOTED — not when it
	// was deployed. Promotion writes a pointer; deployment moves bytes.
	// A consumer rendering this as "shipped at" has reintroduced the bug
	// `forge env status` exists to catch.
	PromotedAt string `json:"promoted_at,omitempty"`
	// ReleaseKnown is true when the release ledger file for Release exists
	// in this checkout. FALSE IS NOT AN ERROR: a release cut on another
	// branch leaves a perfectly real binding pointing at a file this
	// checkout has never seen. The binding still says what the env runs;
	// only the provenance and the lag are unavailable, so Git,
	// ReleaseCreatedAt and Lag are omitted rather than guessed.
	ReleaseKnown bool `json:"release_known"`
	// Git is the source provenance of the bound release. Read `dirty`: a
	// release cut from a tree with uncommitted changes ships bytes that
	// correspond to no reviewable commit, and this is the only place on
	// this screen that fact surfaces. Nil when the ledger is absent.
	Git *release.Git `json:"git,omitempty"`
	// ReleaseCreatedAt is RFC3339 for when the bound release was CUT, as
	// distinct from when this env was promoted to it. The gap between the
	// two is how long the release sat before this env took it.
	ReleaseCreatedAt string `json:"release_created_at,omitempty"`
	// KubeContext and Namespace are where this env runs, resolved
	// declaratively from the env's KCL (forge.K8sCluster.cluster) — never
	// the ambient current-context. Empty for a host-only or compose env,
	// and for an env not declared in this checkout.
	KubeContext string `json:"kube_context,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	// Lag is how far behind the newest release this env is. Nil when it
	// cannot be computed: unbound, or a release ledger this checkout lacks.
	Lag *promotionLag `json:"lag,omitempty"`
	// Images is one row per image the binding declares, sorted by name.
	// Always non-nil so consumers see [] rather than null.
	Images []topologyImage `json:"images"`
	// Note explains a state that would otherwise look like missing data —
	// an unbound env, a binding whose release ledger is absent, an env
	// with no cluster declared.
	Note string `json:"note,omitempty"`

	// Destination / Endpoint / EnvironmentID / Verdict / Workloads are the
	// console's "where does this env run" contract. See env_destination.go
	// for the vocabulary. Destination is set for every DECLARED env; the
	// rest are hosted-only, and EnvironmentID is empty (never fabricated)
	// for a hosted env that has not been deployed.
	Destination string `json:"destination,omitempty"`
	// ControlPlaneKind is "local" | "persistent" for an env that declares
	// control_plane (see env_destination.go); omitted otherwise.
	ControlPlaneKind string                              `json:"control_plane_kind,omitempty"`
	Endpoint         string                              `json:"endpoint,omitempty"`
	EnvironmentID    string                              `json:"environment_id,omitempty"`
	Verdict          string                              `json:"verdict,omitempty"`
	Workloads        []deploytarget.HostedWorkloadStatus `json:"workloads,omitempty"`
	// Placements is every part of the env and where it runs; see
	// env_destination.go.
	Placements []partPlacement `json:"placements,omitempty"`

	// §3.5's additive fields (F7), so `topology --json` is the one-shot
	// whole-screen read a dashboard polls.
	//
	// PromotionID is the current binding's ledger entry — the value
	// `promote --expect-current` and `--from-promotion` take.
	PromotionID string `json:"promotion_id,omitempty"`
	// FromEnv is the environment the current binding was promoted from.
	FromEnv string `json:"from_env,omitempty"`
	// GatesSummary counts the current binding's evidence, both halves
	// (claimed at promote time and recorded afterwards). Absent when it
	// carries none.
	GatesSummary *gatesSummary `json:"gates_summary,omitempty"`
	// RolloutPhase is the current promotion's rollout phase as the control
	// plane computes it (`forge env status --wait --timeout 0`'s answer): "succeeded",
	// "progressing", "degraded", "unknown" … Hosted envs only; absent when
	// the env keeps a file ledger or the read failed (Note says which).
	RolloutPhase string `json:"rollout_phase,omitempty"`
	// NotApplied is true when the ledger records that the current
	// binding's apply FAILED: Release is what the env is bound to, NOT what
	// it runs. ApplyError is what the apply said.
	NotApplied bool   `json:"not_applied,omitempty"`
	ApplyError string `json:"apply_error,omitempty"`
}

// topologyTally counts image cells by state across every environment, so a
// consumer can badge the whole screen without walking the matrix itself.
type topologyTally struct {
	NotVerified int `json:"not_verified"`
	Match       int `json:"match"`
	Drift       int `json:"drift"`
	Missing     int `json:"missing"`
	Untagged    int `json:"untagged"`
	Unreachable int `json:"unreachable"`
}

// envTopologyReport is the `--json` output contract: the whole screen.
//
// Extensions are ADDITIVE. New fields may be added; no field is renamed or
// repurposed, so a consumer reading `environments[].release` keeps working.
type envTopologyReport struct {
	// Project is the forge project name, best-effort from forge.yaml.
	Project string `json:"project,omitempty"`
	// Ledger names where bindings were read from, as the binding store
	// reports it — a path today, a URL for a hosted backend. It is an
	// opaque label for DISPLAY; do not join it or open it.
	Ledger string `json:"ledger"`
	// GeneratedAt is RFC3339 for when this snapshot was taken. A topology
	// view is a point-in-time read of a moving system, and a consumer
	// caching it needs to know how old its copy is.
	GeneratedAt string `json:"generated_at"`
	// LatestRelease is the newest release cut in this checkout — the
	// baseline every env's lag is measured against. Empty when the project
	// has cut none.
	LatestRelease string `json:"latest_release,omitempty"`
	// LatestReleaseCreatedAt is RFC3339 for when that release was cut.
	LatestReleaseCreatedAt string `json:"latest_release_created_at,omitempty"`
	// Releases is every release ledger in this checkout, NEWEST FIRST. It
	// is what makes "prod is 15 releases ahead" checkable by a consumer
	// rather than a number it has to trust, and it is the list a promote
	// UI offers. Always non-nil.
	Releases []string `json:"releases"`
	// Images is the union of every image name across every environment,
	// sorted. THIS IS THE MATRIX'S COLUMN AXIS, and it is why the union is
	// computed here instead of per-env: an image present in prod and
	// absent from staging shows up as a gap in the matrix only if the
	// column exists in the first place. A consumer intersecting per-env
	// lists would make exactly that image disappear — which is the case
	// this screen most needs to surface. Always non-nil.
	Images []string `json:"images"`
	// Environments is one row per environment, sorted by name.
	Environments []topologyEnv `json:"environments"`
	// Verified reports whether --verify ran. When false EVERY image state
	// is "not_verified" and the report makes no claim about any cluster.
	Verified bool `json:"verified"`
	// Tally counts image cells by state across all environments.
	Tally topologyTally `json:"tally"`
	// OK is false exactly when text mode exits non-zero. In ledger-only
	// mode it is always true: reading a ledger cannot prove anything
	// wrong, and a command that went red for an unverified env would be
	// red always.
	OK bool `json:"ok"`
	// Detail carries the one-line reason for a non-OK result.
	Detail string `json:"detail,omitempty"`
}

// envTopologyOptions carries the flags and the injected seams into the run
// function. The three seams are the same ones env verify injects, for the
// same reason: a test of this command's assembly should be able to state the
// ledger, the cluster and the target directly rather than staging a project
// and a live cluster to imply them.
type envTopologyOptions struct {
	// JSON switches the RENDERING only. Every value and the exit code are
	// computed before either renderer runs, so the two modes cannot
	// disagree about what was found.
	JSON bool
	// Verify opts in to reading clusters. Off by default — see the file
	// header for why that is load-bearing rather than a performance tweak.
	Verify  bool
	Timeout time.Duration
	// ProjectDir is the checkout the ledgers and the declared-env scan
	// are read from. The command passes the discovered root; an empty
	// value falls back to projectDirForKCL() so a caller that only cares
	// about the other seams need not resolve it.
	ProjectDir string
	Lister     clusterImageLister
	Resolver   envTargetResolver
	// Ledgers resolves each env's ledger. Nil uses ledgerFor — the env's
	// declared backend. Per ENV, not per report: one project can keep
	// prod's promotions on a control plane and dev's in its own files.
	Ledgers func(ctx context.Context, env string) (envLedger, error)
	// Destinations resolves where each DECLARED env runs (the console
	// contract). Nil renders the env's KCL and, for a hosted env, reads its
	// status from the control plane.
	Destinations func(ctx context.Context, projectDir, env string) envDestination
	// Rollouts reads a hosted env's rollout for a promotion (rollout_phase).
	// Nil uses the env's declared control plane, as `env rollout` does.
	Rollouts func(ctx context.Context, env, promotionID string) (wireRollout, error)
}

// runEnvTopology assembles the report and renders it.
func runEnvTopology(ctx context.Context, envArgs []string, opts envTopologyOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultEnvStatusReleaseTimeout
	}
	if opts.Resolver == nil {
		opts.Resolver = kclTargetResolver{}
	}
	if opts.Lister == nil {
		opts.Lister = kubectlImageLister{}
	}

	projectDir := opts.ProjectDir
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}
	if opts.Ledgers == nil {
		opts.Ledgers = func(ctx context.Context, env string) (envLedger, error) {
			return ledgerFor(ctx, projectDir, env)
		}
	}
	if opts.Destinations == nil {
		opts.Destinations = func(ctx context.Context, projectDir, env string) envDestination {
			e, err := RenderKCL(ctx, projectDir, env)
			if err != nil {
				return envDestination{Note: "could not render this environment to resolve where it runs: " + err.Error()}
			}
			return resolveEnvDestination(ctx, env, e, readHostedStatusFromDeclaration)
		}
	}

	declared := declaredEnvNames(projectDir)
	envs := envArgs
	if len(envs) == 0 {
		envs = declared
	}
	isDeclared := map[string]bool{}
	for _, name := range declared {
		isDeclared[name] = true
	}

	releases := readReleaseLedgers(projectDir)
	report, failure := buildEnvTopology(ctx, projectDir, envs, isDeclared, releases, opts)

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("write topology report: %w", err)
		}
		// The report is already on stdout; returning the same sentinel
		// gives cobra the exit code text mode would have produced.
		return failure
	}

	renderEnvTopologyText(report)
	return failure
}

// buildEnvTopology does all the work and decides the verdict ONCE, before
// either renderer runs. That is what makes "--json exits identically to text
// mode" a structural property rather than two switches someone must keep in
// agreement.
func buildEnvTopology(
	ctx context.Context,
	projectDir string,
	envs []string,
	isDeclared map[string]bool,
	releases []release.Release,
	opts envTopologyOptions,
) (envTopologyReport, error) {
	report := envTopologyReport{
		Ledger:       machineLedgerLocation(projectDir),
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Releases:     []string{},
		Images:       []string{},
		Environments: []topologyEnv{},
		Verified:     opts.Verify,
		OK:           true,
	}
	if cfg, err := config.LoadProjectDir(projectDir); err == nil && cfg != nil {
		report.Project = cfg.Name
	}

	byVersion := map[string]release.Release{}
	for _, rel := range releases {
		report.Releases = append(report.Releases, rel.Version)
		byVersion[rel.Version] = rel
	}
	if len(releases) > 0 {
		report.LatestRelease = releases[0].Version
		report.LatestReleaseCreatedAt = formatLedgerTime(releases[0].CreatedAt)
	}

	names := append([]string(nil), envs...)
	sort.Strings(names)
	names = dedupe(names)

	// Every env is independent, so they are assembled in parallel. That
	// matters even without --verify: resolving where an env runs renders
	// its KCL, and doing four of those in series is four times the wait
	// for a screen whose whole promise is that it paints at once.
	rows := make([]topologyEnv, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			rows[i] = buildTopologyEnvRow(ctx, projectDir, name, isDeclared[name], byVersion, releases, opts)
		}(i, name)
	}
	wg.Wait()

	imageSet := map[string]bool{}
	for _, row := range rows {
		report.Environments = append(report.Environments, row)
		for _, img := range row.Images {
			imageSet[img.Image] = true
			switch img.State {
			case topologyNotVerified:
				report.Tally.NotVerified++
			case topologyMatch:
				report.Tally.Match++
			case topologyDrift:
				report.Tally.Drift++
			case topologyMissing:
				report.Tally.Missing++
			case topologyUntagged:
				report.Tally.Untagged++
			case topologyUnreachable:
				report.Tally.Unreachable++
			}
		}
	}
	for image := range imageSet {
		report.Images = append(report.Images, image)
	}
	sort.Strings(report.Images)

	// THE VERDICT. Ledger-only mode is always OK: it read a file and
	// reported what was in it, which cannot prove an environment wrong. A
	// command that went red because nothing had been verified would be red
	// on every invocation, and a permanently-red command is a deleted one.
	var failure error
	if opts.Verify {
		switch {
		case report.Tally.Drift > 0 || report.Tally.Missing > 0:
			failure = exitCodeError{code: 1, msg: fmt.Sprintf(
				"%d image(s) drifted and %d missing across %d environment(s) — at least one environment is not running the release it is bound to",
				report.Tally.Drift, report.Tally.Missing, len(report.Environments))}
		case report.Tally.Unreachable > 0:
			failure = exitCodeError{code: 2, msg: fmt.Sprintf(
				"%d image(s) could not be checked (cluster, context or credentials), %d matched, 0 drifted",
				report.Tally.Unreachable, report.Tally.Match)}
		}
	}
	report.OK = failure == nil
	if failure != nil {
		report.Detail = failure.Error()
	}
	return report, failure
}

// buildTopologyEnvRow assembles one environment's row.
func buildTopologyEnvRow(
	ctx context.Context,
	projectDir, envName string,
	declared bool,
	byVersion map[string]release.Release,
	releases []release.Release,
	opts envTopologyOptions,
) topologyEnv {
	row := topologyEnv{
		Env:      envName,
		Declared: declared,
		Images:   []topologyImage{},
	}
	if declared {
		row.Lifecycle = declaredLifecycle(ctx, opts.Resolver, projectDir, envName)
	}
	if !declared {
		row.Note = fmt.Sprintf("not declared in this checkout (%s does not exist) — it may be declared on another branch",
			filepath.Join(projectDir, "deploy", "kcl", envName, "main.k"))
	} else if opts.Destinations != nil {
		// Resolved FIRST and independently of the ledger: where an env runs
		// is a fact of its declaration, and a ledger that cannot be read
		// must not also blank the destination badge.
		dest := opts.Destinations(ctx, projectDir, envName)
		row.Destination, row.Endpoint, row.EnvironmentID = dest.Destination, dest.Endpoint, dest.EnvironmentID
		row.ControlPlaneKind = dest.ControlPlaneKind
		row.Verdict, row.Workloads, row.Placements = dest.Verdict, dest.Workloads, dest.Placements
		if dest.Note != "" {
			row.Note = dest.Note
		}
	}

	ledger, err := opts.Ledgers(ctx, envName)
	if err != nil {
		// Reported on the row rather than aborting the whole screen: the
		// other environments' answers are still worth having.
		row.Note = fmt.Sprintf("could not resolve this environment's release ledger: %v", err)
		return row
	}
	row.Hosted = ledger.Hosted
	row.Ledger = ledger.Bindings.Location()
	if ledger.Hosted {
		// A hosted env's releases live on its control plane, not in this
		// checkout, so provenance and lag are measured against THAT set.
		// A failed listing degrades to "provenance unknown", not a blank row.
		if hosted, lerr := ledger.Releases.List(ctx); lerr == nil {
			releases = hosted
			byVersion = map[string]release.Release{}
			for _, rel := range hosted {
				byVersion[rel.Version] = rel
			}
		}
	}

	binding, bound, err := ledger.Bindings.Current(ctx, envName)
	if err != nil {
		// A ledger that cannot be read is reported on the row rather
		// than aborting the whole screen: the other environments'
		// answers are still worth having, and a single failed read
		// should not blank a dashboard.
		row.Note = fmt.Sprintf("could not read the promotion ledger: %v", err)
		return row
	}
	row.Bound = bound
	if !bound {
		if row.Note == "" {
			row.Note = "never promoted — no release is bound to this environment"
		}
		return row
	}
	row.Release = binding.Release
	row.Kind = binding.Kind
	row.PromotedAt = formatLedgerTime(binding.PromotedAt)
	row.PromotionID = binding.ID
	row.FromEnv = binding.FromEnv
	row.GatesSummary = summarizeGates(binding.Gates, binding.RecordedGates)
	if applied := applyOf(projectDir, envName, binding); applied.Failed {
		row.NotApplied, row.ApplyError = true, applied.Summary
	}
	if ledger.Hosted {
		read := opts.Rollouts
		if read == nil {
			read = readDeclaredRollout
		}
		// One read per hosted env, the same GetRollout `env rollout`
		// makes. A failure leaves the phase absent — a dashboard row
		// without a phase is honest; a row that blanked the whole screen
		// because one control plane was slow is not.
		if r, rerr := read(ctx, envName, binding.ID); rerr == nil {
			row.RolloutPhase = rolloutPhaseName(r.Phase)
		} else if row.Note == "" {
			row.Note = fmt.Sprintf("rollout phase unavailable: %v", rerr)
		}
	}

	if rel, ok := byVersion[binding.Release]; ok {
		row.ReleaseKnown = true
		git := rel.Git
		row.Git = &git
		row.ReleaseCreatedAt = formatLedgerTime(rel.CreatedAt)
		row.Lag = computePromotionLag(releases, rel)
	} else if row.Note == "" {
		// A FIRST-CLASS STATE, NOT AN ERROR. The binding is real and
		// says exactly what this env runs; only the ledger describing
		// the release is absent, because it was cut on a branch this
		// checkout does not have. Provenance and lag are therefore
		// omitted rather than guessed at — a lag computed against a
		// release set that does not contain the env's own release
		// would be a number with no meaning.
		row.Note = fmt.Sprintf("release %s is not in %s — it was cut elsewhere, so its provenance and promotion lag are unknown",
			binding.Release, ledger.Releases.Location())
	}

	images := make([]string, 0, len(binding.Resolved))
	for name := range binding.Resolved {
		images = append(images, name)
	}
	sort.Strings(images)
	for _, name := range images {
		row.Images = append(row.Images, topologyImage{
			Image:  name,
			Digest: binding.Resolved[name],
			State:  topologyNotVerified,
		})
	}

	// Where the env runs is resolved declaratively from its KCL. An env
	// not declared in this checkout has no KCL to read, so skip it rather
	// than paying for a render that is certain to fail.
	if declared && row.Destination != destinationHosted {
		target := opts.Resolver.Resolve(ctx, projectDir, envName)
		row.KubeContext = target.KubeContext
		row.Namespace = target.Namespace
	}

	if opts.Verify && len(row.Images) > 0 {
		applyTopologyVerification(ctx, &row, opts)
	}
	return row
}

// applyTopologyVerification reads one env's cluster and overwrites its image
// states with real verdicts.
//
// It delegates to verifyEnvImages rather than re-deriving the comparison: the
// five-state model has subtleties (a digest that matches where pinned while
// some workload still runs a mutable tag is UNTAGGED, not MATCH) and a second
// implementation would eventually disagree with `forge env status` about the
// same environment, which is worse than having no second view at all.
func applyTopologyVerification(ctx context.Context, row *topologyEnv, opts envTopologyOptions) {
	declared := map[string]string{}
	for _, img := range row.Images {
		declared[img.Image] = img.Digest
	}

	var results []imageVerification
	if row.KubeContext == "" || row.Namespace == "" {
		// Cannot even address the cluster. UNREACHABLE, not drift:
		// nothing has been learned about what this env is running.
		reason := fmt.Errorf("no forge.K8sCluster.cluster/namespace resolved for %s (a host-only or compose env has no cluster to verify)", row.Env)
		if !row.Declared {
			reason = fmt.Errorf("environment %s is not declared in this checkout, so there is no KCL to resolve a cluster from", row.Env)
		}
		results = unreachableVerifications(declared, reason)
	} else {
		readCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
		running, lerr := opts.Lister.ListWorkloadImages(readCtx, row.KubeContext, row.Namespace)
		if lerr != nil {
			results = unreachableVerifications(declared, lerr)
		} else {
			results = verifyEnvImages(running, declared)
		}
	}

	byImage := map[string]imageVerification{}
	for _, r := range results {
		byImage[r.Image] = r
	}
	for i := range row.Images {
		r, ok := byImage[row.Images[i].Image]
		if !ok {
			// No verdict came back for a declared image. Leave it
			// not_verified: inventing a state for an image the
			// verifier did not report on is the one move that
			// could make an unchecked cell look checked.
			continue
		}
		row.Images[i].State = topologyStateFor(r.State)
		row.Images[i].Running = r.Running
		row.Images[i].Detail = r.Detail
	}
}

// computePromotionLag measures how far behind the newest release `bound` is.
//
// releases is newest-first, so the index of the bound release IS the number of
// releases cut after it. Counting by INDEX rather than by parsing version
// numbers is what makes this correct for the version schemes semver cannot
// order: the ordering was already decided once, in sortReleasesNewestFirst,
// and re-deriving it here is how two answers about the same project start to
// disagree.
func computePromotionLag(releases []release.Release, bound release.Release) *promotionLag {
	if len(releases) == 0 {
		return nil
	}
	latest := releases[0]
	idx := -1
	for i, rel := range releases {
		if rel.Version == bound.Version {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	lag := &promotionLag{
		LatestRelease:  latest.Version,
		Current:        idx == 0,
		ReleasesBehind: idx,
	}
	// The time gap is best-effort. An unparseable timestamp leaves both
	// the seconds and the rendered string empty, so a consumer sees "not
	// known" rather than "zero" — a release with a broken created_at is
	// not a release that shipped today.
	newest, cut := latest.CreatedAt, bound.CreatedAt
	if !newest.IsZero() && !cut.IsZero() && newest.After(cut) {
		d := newest.Sub(cut)
		lag.BehindSeconds = int64(d.Seconds())
		lag.Behind = humanizeLag(d)
	}
	return lag
}

// humanizeLag renders a duration at the granularity a release cadence is
// actually read at: days and hours, not the 1713h0m0s time.Duration prints.
func humanizeLag(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// declaredEnvNames lists the environments declared under deploy/kcl/ — one
// directory per env, each carrying a main.k. This is the same rule
// `forge env list` applies, so the two commands can never disagree about which
// environments a project has.
//
// A missing or unreadable deploy/kcl/ yields an empty list rather than an
// error: a project that declares no environments has an empty topology, which
// is a legitimate (if uninteresting) answer, and failing here would make the
// command unusable in exactly the project where someone is trying to find out
// why they have no environments.
func declaredEnvNames(projectDir string) []string {
	base := filepath.Join(projectDir, "deploy", "kcl")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var envs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(base, e.Name(), "main.k")); statErr == nil {
			envs = append(envs, e.Name())
		}
	}
	sort.Strings(envs)
	return envs
}

// readReleaseLedgers loads this project's releases from the MACHINE ledger,
// NEWEST FIRST.
//
// It reads the non-hosted store because that is where a project's own cut
// releases are recorded; an env whose ledger is a control plane contributes
// its releases through its own ledger, which the per-env loop below reads
// (buildEnvTopology's ledger.Releases.List).
//
// An unreadable ledger is EMPTY, not fatal. This command's job is to show
// the shape of a project's releases; refusing to show any of them because
// the store could not be opened trades a complete answer for no answer.
func readReleaseLedgers(projectDir string) []release.Release {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return nil
	}
	all, err := store.Releases()
	if err != nil {
		return nil
	}
	sortReleasesNewestFirst(all)
	return all
}

// sortReleasesNewestFirst orders releases newest first, by WHEN THEY WERE CUT.
//
// CUT TIME LEADS, and the version label never decides recency between two
// releases that were cut at different moments. A project's labels are not one
// scheme: control-plane's ledger holds semver names (v1.7.38, v1.7.40) AND
// date-named auto cuts (20261014.042859-cd678a0d6531). Semver cannot parse the
// date names, so a semver-first comparator ranked EVERY v1.x.y above EVERY
// date-named cut — and a forward promote from v1.7.40 to a cut made days later
// was reported as "BEHIND, 67 releases OLDER". Time is the one key every
// scheme shares.
//
// Where the cut times are equal or unknown (a zero created_at sorts as oldest),
// semver orders two parseable labels, a parseable label precedes an
// unparseable one, and the label itself is the final tie-break so the order is
// stable across runs rather than dependent on directory iteration. Each of
// those is a total order on its own, so the composite is one too — a
// comparator that mixed "semver when both parse, time otherwise" would be
// intransitive and sort differently from run to run.
//
// Callers that need the DIRECTION of a move between two releases consult
// commit ancestry first (promoteDirectionFor); this order is the fallback for
// releases whose commits are not comparable, and what "latest" and "releases
// behind" count by.
func sortReleasesNewestFirst(releases []release.Release) {
	sort.SliceStable(releases, func(i, j int) bool {
		ti, tj := releases[i].CreatedAt, releases[j].CreatedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		vi, vj := semverKey(releases[i].Version), semverKey(releases[j].Version)
		if vi != "" && vj != "" {
			if c := semver.Compare(vi, vj); c != 0 {
				return c > 0
			}
		}
		if (vi != "") != (vj != "") {
			return vi != ""
		}
		return releases[i].Version > releases[j].Version
	})
}

// renderEnvTopologyText prints the human report: a header, one block per
// environment, and the cross-environment image matrix.
func renderEnvTopologyText(report envTopologyReport) {
	name := report.Project
	if name == "" {
		name = "project"
	}
	fmt.Printf("Release topology for %s\n", name)
	fmt.Printf("  ledger    %s (per-env override: an env declaring forge.ControlPlane records on its control plane)\n", report.Ledger)
	if report.LatestRelease != "" {
		fmt.Printf("  latest    %s (cut %s, %d release(s) on record)\n",
			report.LatestRelease, report.LatestReleaseCreatedAt, len(report.Releases))
	} else {
		fmt.Printf("  latest    (no releases cut in this checkout)\n")
	}
	if !report.Verified {
		// Stated up front, not as a footnote. Every state below is
		// "not verified", and a reader who skims the table without
		// this line has read a claim about clusters that was never made.
		fmt.Printf("  clusters  NOT READ — every image state below is the LEDGER's declaration, not what is running. Add --verify to reconcile.\n")
	}
	fmt.Println()

	if len(report.Environments) == 0 {
		fmt.Println("No environments found. Declare one under deploy/kcl/<env>/main.k, or name one explicitly:")
		fmt.Println("  forge env status staging")
		return
	}

	for _, env := range report.Environments {
		fmt.Printf("%s\n", env.Env)
		if env.Hosted {
			fmt.Printf("  ledger    %s (hosted)\n", env.Ledger)
		}
		switch {
		case !env.Bound:
			fmt.Printf("  release   (unbound)\n")
		default:
			line := fmt.Sprintf("  release   %s", env.Release)
			if env.Lag != nil {
				if env.Lag.Current {
					line += "  [current]"
				} else {
					behind := fmt.Sprintf("%d release(s) behind %s", env.Lag.ReleasesBehind, env.Lag.LatestRelease)
					if env.Lag.Behind != "" {
						behind += ", " + env.Lag.Behind + " older"
					}
					line += "  [" + behind + "]"
				}
			}
			fmt.Println(line)
			if env.NotApplied {
				// Directly under the release it qualifies: a reader who
				// stops at "release" must not walk away believing it runs.
				fmt.Printf("  NOT APPLIED  bound to %s, but its apply FAILED: %s\n", env.Release, oneLine(env.ApplyError, 200))
			}
			// promoted_at is the single most misread field in the
			// ledger, so the label says what it means instead of
			// leaving the reader to assume it is a deploy time.
			fmt.Printf("  promoted  %s (promote time, NOT deploy time)\n", env.PromotedAt)
			if env.ReleaseKnown && env.Git != nil && env.Git.Commit != "" {
				provenance := fmt.Sprintf("  cut from  %s", env.Git.Commit)
				if env.Git.Tag != "" {
					provenance += fmt.Sprintf(" (tag %s)", env.Git.Tag)
				}
				if env.Git.Dirty {
					provenance += "  [DIRTY TREE — these bytes match no reviewable commit]"
				}
				fmt.Println(provenance)
			}
		}
		if env.KubeContext != "" || env.Namespace != "" {
			fmt.Printf("  cluster   %s (namespace %s)\n", env.KubeContext, env.Namespace)
		}
		if env.Note != "" {
			fmt.Printf("  note      %s\n", env.Note)
		}
		for _, img := range env.Images {
			fmt.Printf("    %-12s %-18s %s\n", img.State, img.Image, img.Digest)
			if img.Running != "" && img.State != topologyMatch {
				fmt.Printf("      running %s\n", img.Running)
			}
		}
		fmt.Println()
	}

	renderTopologyMatrix(report)

	if report.Verified {
		fmt.Printf("\n%d match, %d drifted, %d missing, %d untagged, %d unreachable\n",
			report.Tally.Match, report.Tally.Drift, report.Tally.Missing,
			report.Tally.Untagged, report.Tally.Unreachable)
	}
	if report.Detail != "" {
		fmt.Printf("\n%s\n", report.Detail)
	}
}

// renderTopologyMatrix prints the image × environment grid.
//
// This is the part that cannot be assembled from per-env output: a blank cell
// means the image is in one environment's release and NOT in another's, and
// that is only visible when every environment is laid against the same column
// set.
func renderTopologyMatrix(report envTopologyReport) {
	if len(report.Images) == 0 || len(report.Environments) == 0 {
		return
	}
	fmt.Println("Images by environment (· = not in this environment's release)")

	width := 5
	for _, img := range report.Images {
		if len(img) > width {
			width = len(img)
		}
	}

	fmt.Printf("  %-*s", width, "image")
	for _, env := range report.Environments {
		fmt.Printf("  %-14s", env.Env)
	}
	fmt.Println()

	for _, image := range report.Images {
		fmt.Printf("  %-*s", width, image)
		for _, env := range report.Environments {
			cell := "·"
			for _, img := range env.Images {
				if img.Image == image {
					// The short digest is the identity a
					// reader compares across the row: two
					// envs on the same bytes show the same
					// twelve characters.
					cell = matrixDigest(img.Digest)
					break
				}
			}
			fmt.Printf("  %-14s", cell)
		}
		fmt.Println()
	}
}

// matrixDigest abbreviates a digest for a matrix cell.
//
// Distinct from shortDigest (deploy.go) because the two are read differently:
// shortDigest keeps the "sha256:" prefix for a line a reader copies out of,
// while a matrix cell is scanned ACROSS a row to see which environments share
// bytes, and a prefix repeated in every cell is nine characters of noise in a
// column that has to stay narrow. The full digest is in the per-environment
// blocks above and in the JSON, so nothing is lost.
func matrixDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	if d == "" {
		return "?"
	}
	return d
}
