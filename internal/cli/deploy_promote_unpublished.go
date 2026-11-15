package cli

// What a hosted-ledger env's CONTROL PLANE will converge of
// `forge env deploy <env> <version>` — decided from the declaration and one
// read of the control plane BEFORE anything is written — and the check, after
// the publish, that the control plane agrees with what it was just sent.
//
// ── The incident this is shaped by (2026-10-09) ──────────────────────────────
//
// `forge env deploy prod 20261009.205925-d29da50b` against control-plane's
// prod: a forge.ControlPlane ledger over sixteen cluster workloads and one
// Firebase frontend, with NO hosted tier. The deploy recorded the promotion,
// applied every cluster workload from this machine (their new ReplicaSets
// were created at 21:30:41Z), built and shipped the frontend to Firebase
// Hosting — ten minutes of work, all of it live — and only THEN exited 2:
// "prod has no published workloads, so its control plane has nothing to
// converge and this release would never roll out … nothing shipped".
//
// Every clause was false for prod. Its control plane converges none of it BY
// DECLARATION, so an empty server-side rollout was the correct answer, not a
// missing publish; and this command's own apply was the whole deploy, already
// finished. The control plane's converger was never involved.
//
// Two defects, fixed separately:
//
//  1. THE PREDICATE. "No workload in the rollout" means "unpublished" only for
//     an env that binds tiers to its control plane. An env with none
//     (envLedger.ledgerOnly) has nothing there by construction: its follow-
//     through neither waits on a server-side rollout nor checks one.
//  2. THE ORDER. Whether the control plane will have anything to converge is
//     knowable before the deploy starts — from the declared hosted tiers, the
//     flags, and the control plane's list of published workloads — so it is
//     decided here, as part of the plan, before the promotion is recorded and
//     before any apply or frontend dispatch. A refused deploy writes nothing
//     and ships nothing, and `--plan` / `--plan-only` show the same verdict.
//
// ── When "nothing to converge" is genuine ────────────────────────────────────
//
// The converger only moves the digest on deployments that already exist; it
// creates none (control-plane internal/deployconverge/doc.go). A version
// deploy PUBLISHES the env's hosted tiers itself (O-15: the pure arm through
// applyHostedPublish, the mixed arm inside its local apply), so after O-15 the
// refusal is reserved for the deploy that will NOT publish them — its flags
// keep it from doing so (--dry-run applies nothing; --frontends-only publishes
// only frontends) — against an env whose control plane holds none of them
// yet. The refusal names every such tier and the flag responsible.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedTier is one tier an env binds to its control plane.
type hostedTier struct {
	Name string `json:"name"`
	// Kind is hostedTierWorkload, hostedTierDatabase or hostedTierFrontend.
	Kind string `json:"kind"`
}

const (
	hostedTierWorkload = "workload"
	hostedTierDatabase = "database"
	hostedTierFrontend = "frontend"
)

func (t hostedTier) String() string { return t.Name + " (" + t.Kind + ")" }

// hostedTiersOf lists every tier the env binds to its control plane — the
// forge.OnHosted workloads, hosted databases and OnHosted frontends
// KCLEntities.HasHosted asks about — sorted by name. Empty means the control
// plane, if the env declares one, converges none of it.
func hostedTiersOf(e *KCLEntities) []hostedTier {
	if e == nil {
		return nil
	}
	var tiers []hostedTier
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeHosted) {
			tiers = append(tiers, hostedTier{Name: w.Name, Kind: hostedTierWorkload})
		}
	}
	for _, d := range e.Databases {
		if d.Hosted() {
			tiers = append(tiers, hostedTier{Name: d.Name, Kind: hostedTierDatabase})
		}
	}
	for _, f := range e.Frontends {
		if frontendIsHosted(f) {
			tiers = append(tiers, hostedTier{Name: f.Name, Kind: hostedTierFrontend})
		}
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Name < tiers[j].Name })
	return tiers
}

// promotePlanConverges is the plan's answer to "what will this env's control
// plane converge of this deploy". Nil for an env with no control-plane ledger,
// and for one the hub converges (deploy_hub_ready.go owns that question).
type promotePlanConverges struct {
	// ControlPlane is where the env's promotions are recorded.
	ControlPlane string `json:"control_plane"`
	// LedgerOnly is true when the env binds NO tier to its control plane:
	// the control plane records the promotion and converges nothing, and
	// this command's own apply is the whole deploy.
	LedgerOnly bool `json:"ledger_only"`
	// Tiers are the env's hosted tiers.
	Tiers []hostedTier `json:"tiers"`
	// Published names the deployments the control plane already holds. Nil
	// when it could not be read (PublishedUnread says why).
	Published       []string `json:"published,omitempty"`
	PublishedUnread string   `json:"published_unread,omitempty"`
	// FirstPublish are hosted tiers the control plane has no deployment for
	// yet, which THIS deploy publishes.
	FirstPublish []hostedTier `json:"first_publish,omitempty"`
	// Unpublished are hosted tiers the control plane has no deployment for
	// that this deploy will NOT publish: they do not run this release.
	Unpublished []hostedTier `json:"unpublished,omitempty"`
	// Units is who applies each delivery unit of the env, when it declares a
	// split (Bundle.hub_converges, or applier = "forge"): the units the hub
	// converges from the recorded bundle, and the units this deploy applies
	// from here. Nil when the env declares no delivery unit and no split.
	Units []release.ShapeUnit `json:"units,omitempty"`
	// Trees is the bound-vs-live view of the CURRENT binding's trees, so a
	// reviewer sees what the hub has and has not converged before approving a
	// move on top of it. Promotion is intent and convergence is observed: a
	// tree behind its binding is shown behind. Empty when the env has no bound
	// bundle, no hub unit, or a control plane that reports nothing per tree
	// (TreesDetail says which).
	Trees       []envStatusTree `json:"trees,omitempty"`
	TreesDetail string          `json:"trees_detail,omitempty"`
	// Detail is the one-sentence reading.
	Detail string `json:"detail"`
}

// publishedWorkloadsReader is the one read the preflight needs from a hosted
// ledger: the names of the deployments its control plane holds for env. An env
// the control plane has never heard of has published nothing. hostedStore
// implements it; declared here, where it is consumed.
type publishedWorkloadsReader interface {
	publishedWorkloads(ctx context.Context, env string) ([]string, error)
}

// hostedConvergencePreflight decides, BEFORE the promotion is recorded, what
// the env's control plane will converge of this deploy, and refuses the deploy
// when the answer is "nothing it could ever converge" or when a flag would be
// refused by the hosted publish halfway through the follow-through.
//
// It runs for --plan / --plan-only too, because it only reads: a reviewer sees
// the refusal instead of approving a plan that cannot converge.
//
// The --target and pure-hosted --frontends-only refusals are the hosted
// publish's own (runHostedDeploy, dispatchHostedDeploy), stated here as well
// because there they fire AFTER the promotion is recorded — and on a mixed env
// after its cluster half has already been applied. They stay where they were
// too: `forge env up` reaches that path with no plan in front of it.
func hostedConvergencePreflight(ctx context.Context, env string, ledger envLedger, o promoteFollowOptions) (*promotePlanConverges, error) {
	if !ledger.Hosted {
		return nil, nil
	}
	if ledger.hubUnits() {
		return hubConvergencePlan(ctx, env, ledger), nil
	}
	conv := &promotePlanConverges{ControlPlane: ledger.Bindings.Location(), Tiers: ledger.HostedTiers}
	conv.Units = ledger.Units.ShapeUnits()
	if ledger.ledgerOnly() {
		conv.LedgerOnly = true
		conv.Tiers = []hostedTier{}
		conv.Detail = fmt.Sprintf("nothing — %s binds no tier to its control plane (no forge.OnHosted workload, "+
			"hosted database or hosted frontend), so %s only records this promotion. This command's own apply "+
			"is the whole deploy; there is no server-side rollout to wait on", env, conv.ControlPlane)
		return conv, nil
	}
	if len(ledger.HostedTiers) == 0 {
		// A pure hosted env that binds nothing is refused by the deploy
		// path itself (refuseLocalEnvDeploy); there is no tier to report.
		return nil, nil
	}
	d := o.clientDeploy
	if len(d.targets) > 0 {
		return conv, fmt.Errorf("--target is not supported for the hosted tiers of env %q (%s): a hosted deploy "+
			"publishes them all at the bound release, and a partial publish would leave the platform running two "+
			"releases under one binding.\n\nNOTHING WAS RECORDED: env %q's binding did not move. Re-run without --target",
			env, joinTiers(ledger.HostedTiers), env)
	}
	if d.frontendsOnly && !ledger.Mixed {
		return conv, fmt.Errorf("--frontends-only is not supported on hosted env %q: its frontends ship with its "+
			"hosted tiers (%s) through the control plane.\n\nNOTHING WAS RECORDED: env %q's binding did not move. "+
			"Re-run without --frontends-only", env, joinTiers(ledger.HostedTiers), env)
	}
	publishing, why := tiersThisDeployPublishes(ledger.HostedTiers, d)
	var published []string
	var readErr error
	if r, ok := ledger.Bindings.(publishedWorkloadsReader); ok {
		published, readErr = r.publishedWorkloads(ctx, env)
	} else {
		readErr = errors.New("this ledger cannot list the control plane's published workloads")
	}
	return convergenceVerdict(env, conv, publishing, why, published, readErr)
}

// tiersThisDeployPublishes is which of the env's hosted tiers the
// follow-through's apply will publish, and — when that is not all of them —
// the flag that keeps the rest back.
func tiersThisDeployPublishes(tiers []hostedTier, d deployOptions) (publishing []hostedTier, why string) {
	switch {
	case d.dryRun:
		return nil, "--dry-run applies nothing, so it publishes nothing"
	case d.frontendsOnly:
		for _, t := range tiers {
			if t.Kind == hostedTierFrontend {
				publishing = append(publishing, t)
			}
		}
		return publishing, "--frontends-only publishes only the env's frontends"
	}
	return tiers, ""
}

// convergenceVerdict is the DECISION, split from the reads so it is testable
// over plain values: given the tiers, what this deploy publishes, and what the
// control plane already holds, fill in the plan's answer and refuse when the
// control plane would have NOTHING to converge.
//
// A read failure is never a refusal. This check exists to stop a deploy that
// provably cannot converge; one whose evidence is missing is not that, and the
// plan says what could not be read instead.
func convergenceVerdict(env string, conv *promotePlanConverges, publishing []hostedTier, why string, published []string, readErr error) (*promotePlanConverges, error) {
	isPublishing := map[string]bool{}
	for _, t := range publishing {
		isPublishing[t.Name] = true
	}
	if readErr != nil {
		conv.PublishedUnread = readErr.Error()
		conv.Detail = fmt.Sprintf("%d hosted tier(s) — %s; which of them %s already holds could not be read",
			len(conv.Tiers), publishingPhrase(len(publishing), len(conv.Tiers), why), conv.ControlPlane)
		return conv, nil
	}
	conv.Published = append([]string{}, published...)
	sort.Strings(conv.Published)
	isPublished := map[string]bool{}
	for _, name := range published {
		isPublished[name] = true
	}
	for _, t := range conv.Tiers {
		switch {
		case isPublished[t.Name]:
		case isPublishing[t.Name]:
			conv.FirstPublish = append(conv.FirstPublish, t)
		default:
			conv.Unpublished = append(conv.Unpublished, t)
		}
	}
	conv.Detail = fmt.Sprintf("%d hosted tier(s) on %s — %s", len(conv.Tiers), conv.ControlPlane,
		publishingPhrase(len(publishing), len(conv.Tiers), why))
	if len(conv.Unpublished) < len(conv.Tiers) {
		return conv, nil
	}
	return conv, fmt.Errorf("%s's control plane would have nothing to converge: none of its hosted tiers is "+
		"published there, and this deploy will not publish them (%s).\n"+
		"  Unpublished: %s\n"+
		"  The control plane's converger only moves the digest on deployments that already exist, so recording "+
		"this release would roll out nothing at all.\n\n"+
		"NOTHING WAS RECORDED: env %q's binding did not move, and nothing was applied. "+
		"Re-run without that flag — the deploy publishes the tiers itself",
		env, why, joinTiers(conv.Unpublished), env)
}

// publishingPhrase says how much of the env's hosted half this deploy
// publishes, for the plan's one-line reading.
func publishingPhrase(publishing, total int, why string) string {
	switch {
	case publishing == total:
		return "this deploy publishes all of them, then waits on the rollout its control plane computes"
	case publishing == 0:
		return "this deploy publishes NONE of them (" + why + ")"
	}
	return fmt.Sprintf("this deploy publishes %d of them (%s)", publishing, why)
}

func joinTiers(tiers []hostedTier) string {
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, ", ")
}

// renderConvergesSection prints the plan's convergence block. Silent for an env
// with no control-plane ledger.
func renderConvergesSection(out io.Writer, c *promotePlanConverges) {
	if c == nil {
		return
	}
	fmt.Fprintf(out, "  converges %s\n", c.Detail)
	if len(c.Units) > 0 {
		var hub, forge []string
		for _, u := range c.Units {
			if u.Applier == release.UnitApplierHub {
				hub = append(hub, u.Name)
			} else {
				forge = append(forge, u.Name)
			}
		}
		fmt.Fprintf(out, "    units          hub: %s | forge: %s\n", emptyAs(strings.Join(hub, ", "), "(none)"), emptyAs(strings.Join(forge, ", "), "(none)"))
	}
	writeTreeTable(out, c.Trees)
	if c.TreesDetail != "" {
		fmt.Fprintf(out, "    trees: %s\n", c.TreesDetail)
	}
	for _, t := range c.FirstPublish {
		fmt.Fprintf(out, "    first publish  %s — not on the control plane yet; this deploy creates it\n", t)
	}
	for _, t := range c.Unpublished {
		fmt.Fprintf(out, "    UNPUBLISHED    %s — not on the control plane, and this deploy does not publish it: it will not run this release\n", t)
	}
}

// ─── After the publish ───────────────────────────────────────────────────────

// promotionRolloutReader is the read the post-publish check needs from a
// hosted ledger: the control plane's rollout for one promotion. hostedStore
// implements it.
type promotionRolloutReader interface {
	promotionRollout(ctx context.Context, env, promotionID string) (wireRollout, error)
}

// verifyHostedRolloutAfterPublish runs AFTER this command published the env's
// hosted tiers, immediately before the wait: a rollout that still names no
// workload has nothing for the wait to observe, and waiting anyway is a
// 15-minute silence ending in UNKNOWN (measured on the k3d e2e stack).
//
// Unlike hostedConvergencePreflight this one DEPENDS on the apply — it checks
// the publish's result — which is why it is the only convergence check left
// after the write. It is skipped for an env with no hosted tier (nothing to
// publish, nothing to report) and is deliberately not fatal on a read failure:
// it replaces a silent wait with one sentence, and a transport error is not
// that sentence.
func verifyHostedRolloutAfterPublish(ctx context.Context, env string, ledger envLedger, promotionID string) error {
	if len(ledger.HostedTiers) == 0 {
		return nil
	}
	r, ok := ledger.Bindings.(promotionRolloutReader)
	if !ok {
		return nil
	}
	rollout, err := r.promotionRollout(ctx, env, promotionID)
	if err != nil {
		return nil
	}
	return emptyRolloutAfterPublish(env, ledger.HostedTiers, promotionID, rollout)
}

// emptyRolloutAfterPublish is the DECISION, split from the read so it can be
// tested over rollout shapes without a control plane. Returns nil unless the
// rollout names no workload at all.
func emptyRolloutAfterPublish(env string, tiers []hostedTier, promotionID string, rollout wireRollout) error {
	// Unpinned rows count. A database is not release-bound and never gates
	// a release (owner ruling Q4), but its presence proves the control
	// plane holds what was published — which is the only thing asked here.
	if len(rollout.Workloads) > 0 || len(rollout.Unpinned) > 0 {
		return nil
	}
	// A QUEUED promotion's rollout is empty BECAUSE it is queued: the
	// control plane holds it on a human action and applies it when that
	// clears. The wait that follows reports (or waits through) the hold.
	if rollout.Phase == wireRolloutPhaseHeld {
		return nil
	}
	return &exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(
		"%s: this deploy published its hosted tiers (%s), but the control plane's rollout for promotion %s "+
			"names none of them, so there is nothing for the wait to observe.\n"+
			"  The promotion IS recorded and the publish above DID run: this is the control plane disagreeing "+
			"with what it was just sent, not a missing publish, and re-running the deploy will not change it.\n"+
			"  See what it holds: forge env status %s",
		env, joinTiers(tiers), emptyAs(promotionID, "(current)"), env)}
}

// ─── hostedStore reads ───────────────────────────────────────────────────────

// publishedWorkloads names the deployments the control plane holds for env,
// read through the same client the ledger writes with. An environment the
// control plane has never heard of has published nothing.
func (s *hostedStore) publishedWorkloads(ctx context.Context, env string) ([]string, error) {
	st, err := deploytarget.ReadHostedStatus(ctx, s.client, s.project, env)
	if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(st.Workloads))
	for _, w := range st.Workloads {
		names = append(names, w.Name)
	}
	return names, nil
}

// promotionRollout reads the control plane's rollout for one promotion of env.
func (s *hostedStore) promotionRollout(ctx context.Context, env, promotionID string) (wireRollout, error) {
	envID, err := s.envID(ctx, env)
	if err != nil {
		return wireRollout{}, err
	}
	return readRollout(ctx, s.client, envID, promotionID)
}

// hubConvergencePlan is the plan's answer for an env the hub converges at least
// one delivery unit of: the split, so the reviewer sees which units this deploy
// leaves to the hub and which it applies itself, and the bound-vs-live view of
// the current binding's trees.
//
// It never refuses. The refusals that apply to a hub unit are the hub-readiness
// check (refuseUnreadyHub) and the bundle push/record gates; this is description.
func hubConvergencePlan(ctx context.Context, env string, ledger envLedger) *promotePlanConverges {
	conv := &promotePlanConverges{
		ControlPlane: ledger.Bindings.Location(),
		Tiers:        []hostedTier{},
		Units:        ledger.Units.ShapeUnits(),
	}
	switch {
	case ledger.Units.AllHub():
		conv.Detail = fmt.Sprintf("the hub converges every delivery unit (%s) from the recorded bundle; "+
			"nothing is applied from this machine except anything outside the bundle (a frontend)", strings.Join(ledger.Units.Hub, ", "))
	default:
		conv.Detail = fmt.Sprintf("the hub converges %s from the recorded bundle; this deploy applies %s directly",
			strings.Join(ledger.Units.Hub, ", "), strings.Join(ledger.Units.Forge, ", "))
	}
	hosted, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		return conv
	}
	if lr, lerr := (hostedLiveClient{client: hosted.client}).GetLiveView(ctx, hosted.project, false, time.Now().UTC()); lerr == nil {
		for _, row := range lr {
			if row.Env.Name != env {
				continue
			}
			conv.Trees, conv.TreesDetail = treesOf(ctx, hosted, env, ledger.Units, row)
		}
	} else {
		conv.TreesDetail = "the control plane's Live view could not be read: " + lerr.Error()
	}
	return conv
}
