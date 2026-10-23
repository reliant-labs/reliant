package cli

// `promote --from <env> [--from-promotion <id>]`: promote exactly what another
// environment is running (control-plane docs/design/hosted-deploy-primitives.md
// §3.4).
//
// THE RACE THIS CLOSES, AND WHY THE SERVER RESOLVES THE VERSION. "Promote
// whatever staging runs" done entirely client-side is two calls: read
// staging's current release, then promote that version to prod. If staging is
// promoted between the two, prod gets a release that was NEVER the one
// approved — and may still be rolling out on staging — and the client cannot
// tell that it happened. So forge does not send a version it resolved and
// call that provenance. It sends `from_promotion_id`: the promotion its
// decision was made against. The server re-reads that promotion under the
// TARGET environment's lock, takes the version from it, and refuses
// `source_moved` if it is no longer what the source runs (C6). The read below
// therefore feeds the PLAN — the preview a human reads — while the write's
// authority stays server-side, where a lock can back it.
//
// WHY A PROMOTION ID AND NOT A VERSION. A version label is ambiguous about
// time: staging may have run v1.4.0, moved to v1.5.0, and rolled forward to
// v1.4.1. An id names exactly one entry, so "the build QA signed off on" and
// "what staging happens to run now" cannot be confused for each other.
//
// SAME CONTROL PLANE ONLY. See promoteFromGuardError.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/cloud"
)

// promoteFromOptions names the source of a promote.
type promoteFromOptions struct {
	// Env is the source environment (--from).
	Env string
	// PromotionID pins the source promotion captured earlier
	// (--from-promotion); the server refuses `source_moved` if the source
	// has moved past it.
	PromotionID string
}

func (o promoteFromOptions) requested() bool { return o.Env != "" || o.PromotionID != "" }

// promoteSource is what a resolved --from contributes to the write.
type promoteSource struct {
	// Version is the release to promote: the positional argument, or —
	// when --from supplies it — the one the source environment runs.
	//
	// When VersionFromSource is set this value is FOR THE PLAN ONLY. See
	// that field.
	Version string
	// VersionFromSource says the version above was read from the source
	// environment rather than named by the caller, which makes it a
	// PREVIEW and not an instruction.
	//
	// §3.4: "`--from` without a version sends `from_promotion_id` and no
	// version". The server resolves the release from that promotion under
	// the TARGET's lock, and it must be the only authority on which
	// release the id names — otherwise forge is asserting a version it
	// read earlier, outside any lock, which is the very race --from
	// exists to close. So the plan previews this value and the wire omits
	// it.
	//
	// IT CANNOT BE INFERRED FROM FromPromotionID. A caller who passes an
	// explicit version BESIDE --from gets both fields sent, deliberately:
	// the server's must-agree check is what turns "promote v1.4.0, which
	// I saw on staging" into a refusal when staging was never on v1.4.0.
	// Dropping the version there would silently discard the caller's
	// second assertion. One bit distinguishes "I previewed this" from "I
	// am asserting this".
	VersionFromSource bool
	// FromEnv is the source environment NAME. Recorded as provenance, so
	// a ledger reader can reconstruct the path a release took rather than
	// seeing each environment's promotions as unrelated events.
	FromEnv string
	// FromPromotionID is the source promotion the decision was made
	// against. It is the field with teeth: the server resolves the
	// version from it and refuses source_moved if the source has moved
	// past it. FromEnv alone is provenance nobody checked.
	FromPromotionID string
	// Note is a plan warning about the source, or "". Set when
	// --from-promotion names a promotion that is not what the source runs
	// now, which the control plane will refuse.
	Note string
}

// promoteEnvLedger is what --from needs to know about ONE environment: which
// control plane records its promotions (nil for a file ledger), and the
// ledger itself.
type promoteEnvLedger struct {
	// ControlPlane is the env's declared control plane, or nil when its
	// promotions live in the project's own files.
	ControlPlane *cloud.Declaration
	// Ledger is that backend, already bound to a client.
	Ledger envLedger
}

// promoteEnvLedgerFor resolves one environment's ledger and the control plane
// that owns it. A seam, so a test can state two environments' ledgers instead
// of staging two KCL trees and a control plane to imply them.
var promoteEnvLedgerFor = promoteEnvLedgerOf

// promoteEnvLedgerOf is the production resolver: render the env's KCL once,
// and read BOTH answers out of it.
//
// It repeats ledgerFor's selection rather than calling it because it needs the
// declaration the selection was made FROM, and ledgerFor returns only the
// chosen backend — so calling it would mean rendering the same env's KCL a
// second time to recover what the first render already knew. A --from promote
// resolves two environments, which would be four renders of KCL for two
// questions.
func promoteEnvLedgerOf(ctx context.Context, projectDir, env string) (promoteEnvLedger, error) {
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", env, "main.k")); err != nil {
		// No KCL for this env in this checkout, so nothing can declare a
		// control plane: the answer is this machine's ledger.
		ledger, err := machineLedger(projectDir)
		if err != nil {
			return promoteEnvLedger{}, err
		}
		return promoteEnvLedger{Ledger: ledger}, nil
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return promoteEnvLedger{}, fmt.Errorf("choose the release ledger for env %q: render deploy/kcl/%s: %w", env, env, err)
	}
	ledger, err := ledgerForEntities(env, entities, projectDir)
	if err != nil {
		return promoteEnvLedger{}, err
	}
	out := promoteEnvLedger{Ledger: ledger}
	// The declaration, whatever the env's KIND. A LOCAL env that declares a
	// control plane now records there too (see ledgerForEntities), so the
	// previous exception here — which excluded LOCAL on the reasoning that
	// the platform runs nothing of it — would make this function disagree
	// with the selection it exists to mirror, and a --from promote between
	// two envs on one control plane would read as crossing control planes.
	out.ControlPlane = declarationFromEntities(entities)
	return out, nil
}

// resolvePromoteFrom resolves the source before the plan is computed. version
// is the positional argument ("" when --from supplies it), toEnv the
// environment being promoted TO.
//
// Everything that can refuse the promote happens here, BEFORE the plan and so
// before any write: a malformed flag pair, a source on another control plane,
// a source that has never been promoted. A promote that cannot name its
// source should never move the target's pointer first.
func resolvePromoteFrom(ctx context.Context, version, toEnv, projectDir string, o promoteFromOptions) (promoteSource, error) {
	if !o.requested() {
		return promoteSource{Version: version}, nil
	}
	if o.Env == "" {
		return promoteSource{}, fmt.Errorf(
			"--from-promotion %s needs --from <env>: a promotion id alone does not say which environment it belongs to, "+
				"and the source_moved check is against that environment's current promotion", o.PromotionID)
	}
	if o.Env == toEnv {
		return promoteSource{}, fmt.Errorf(
			"--from %s is also the target: an environment cannot be promoted from itself", o.Env)
	}
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}

	target, err := promoteEnvLedgerFor(ctx, projectDir, toEnv)
	if err != nil {
		return promoteSource{}, err
	}
	source, err := promoteEnvLedgerFor(ctx, projectDir, o.Env)
	if err != nil {
		return promoteSource{}, err
	}
	if err := sameControlPlane(o.Env, toEnv, source.ControlPlane, target.ControlPlane); err != nil {
		return promoteSource{}, err
	}
	if err := sameOrganization(ctx, o.Env, toEnv, source.ControlPlane, target.ControlPlane); err != nil {
		return promoteSource{}, err
	}

	// READ THE SOURCE THROUGH THE SHARED CONTROL PLANE. The guard above
	// has just established that both environments' ledgers are the same
	// control plane — same endpoint, same organization (by credential), and the project is
	// this checkout's for both — so the source's own store IS the target's
	// control plane, reached by the source env's name.
	current, bound, err := source.Ledger.Bindings.Current(ctx, o.Env)
	if err != nil {
		return promoteSource{}, fmt.Errorf("read what %s is running (--from): %w", o.Env, err)
	}
	if !bound {
		return promoteSource{}, fmt.Errorf(
			"--from %s: that environment has never been promoted, so there is nothing to promote from "+
				"(promote a release to %s first, or name the release here)", o.Env, o.Env)
	}

	out := promoteSource{Version: version, FromEnv: o.Env, FromPromotionID: current.ID}
	if version == "" {
		// A PREVIEW, not an instruction: the plan shows what the server
		// will resolve from the promotion id sent beside it, and the
		// wire carries no version at all (see VersionFromSource).
		out.Version = current.Release
		out.VersionFromSource = true
	}
	if o.PromotionID != "" {
		// --from-promotion pins an id captured earlier — at approval
		// time (§3.6) — so it OUTRANKS what the source runs now. When
		// the two differ the server refuses source_moved, which is the
		// entire point of having captured it.
		out.FromPromotionID = o.PromotionID
		if o.PromotionID != current.ID {
			// SAY SO IN THE PLAN. forge cannot read that promotion's
			// release — a promotion is reachable through this ledger
			// only as an environment's CURRENT entry — so the preview
			// above is the source's current release, which is NOT what
			// this request names. Rather than show a target the write
			// would never bind, the plan states the discrepancy and the
			// refusal it leads to, so `--plan` surfaces it instead of
			// the operator discovering it on apply.
			out.Note = fmt.Sprintf(
				"--from-promotion %s is not what %s runs now (it is on %s, promotion %s), so the control plane "+
					"will refuse this promote with source_moved; the target below is %s's CURRENT release, "+
					"not the one %s names",
				o.PromotionID, o.Env, current.Release, current.ID, o.Env, o.PromotionID)
		}
	}
	return out, nil
}

// sameControlPlane refuses a --from across two ledgers.
//
// A RELEASE LABEL DOES NOT IDENTIFY THE SAME BYTES ACROSS TWO LEDGERS. "v1.4.0"
// is a name each ledger assigns independently, so copying it from one to the
// other would promote whatever the target's control plane happens to call
// v1.4.0 — not the artifact the source verified. Worse, the source_moved check
// cannot be made at all: it is a comparison the TARGET's server performs
// against a promotion row in its own database, and a row in a different
// organization, or in a file on disk, is not there to be compared. So the
// refusal is not a limitation being worked around — it is the absence of the
// guarantee the flag exists to provide, stated instead of silently dropped.
//
// A file-ledger env is refused for the second reason even when both
// environments share one project directory: there is no server to re-read the
// source under the target's lock, so `--from` would decay into exactly the
// client-side read-then-promote race §3.4 exists to close.
func sameControlPlane(sourceEnv, targetEnv string, source, target *cloud.Declaration) error {
	switch {
	case target == nil && source == nil:
		return promoteFromGuardError(sourceEnv, targetEnv,
			"neither records promotions on a control plane")
	case target == nil:
		return promoteFromGuardError(sourceEnv, targetEnv,
			fmt.Sprintf("%s records promotions in this project's files, not on a control plane", targetEnv))
	case source == nil:
		return promoteFromGuardError(sourceEnv, targetEnv,
			fmt.Sprintf("%s records promotions in this project's files, not on a control plane", sourceEnv))
	}
	sourceEP, err := cloud.ResolveEndpoint(sourceEnv, source)
	if err != nil {
		return err
	}
	targetEP, err := cloud.ResolveEndpoint(targetEnv, target)
	if err != nil {
		return err
	}
	if sourceEP.URL != targetEP.URL {
		return promoteFromGuardError(sourceEnv, targetEnv,
			fmt.Sprintf("%s is on %s and %s is on %s", sourceEnv, sourceEP.URL, targetEnv, targetEP.URL))
	}
	return nil
}

// sameOrganization is the other half of the shared-ledger guard: both envs'
// credentials must act for ONE organization. Same endpoint is not enough — two
// envs naming different token_envs can be different orgs on one control plane,
// and a release label means nothing across two orgs' ledgers.
//
// The org is the credential's (resolveDeclarationOrg), so this is the one place
// the comparison needs a call; it is cached per (endpoint, token env), so two
// envs sharing a credential cost one.
func sameOrganization(ctx context.Context, sourceEnv, targetEnv string, source, target *cloud.Declaration) error {
	sourceOrg, err := orgResolver(ctx, sourceEnv, source)
	if err != nil {
		return err
	}
	targetOrg, err := orgResolver(ctx, targetEnv, target)
	if err != nil {
		return err
	}
	if sourceOrg != targetOrg {
		return promoteFromGuardError(sourceEnv, targetEnv,
			fmt.Sprintf("their credentials act for different organizations (%s vs %s)", sourceOrg, targetOrg))
	}
	return nil
}

// errPromoteFromCrossLedger marks every same-control-plane refusal, so a
// caller (and a test) can recognise the CLASS without matching message text.
var errPromoteFromCrossLedger = errors.New("--from requires both environments to share one release ledger")

// promoteFromGuardError is the one refusal sentence, with the next step on it.
// The detail names which of the two environments is the problem, because
// "they do not share a ledger" alone leaves a reader to go and diff two KCL
// files to find out which one to change.
func promoteFromGuardError(sourceEnv, targetEnv, detail string) error {
	return fmt.Errorf("%w: %s and %s do not share a ledger — %s\n"+
		"  deploy by version instead: forge env deploy %s <version>\n"+
		"  a release label is assigned per ledger, so it does not name the same bytes in both, "+
		"and the source_moved check needs the source promotion to be a row in the TARGET's control plane",
		errPromoteFromCrossLedger, sourceEnv, targetEnv, detail, targetEnv)
}
