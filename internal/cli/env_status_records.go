package cli

// The RECORDS half of `forge env status <env>` (doc §6.3, §7.4, §13): where
// what is bound came FROM, what the reconciler did with it, and which local
// stacks are running it.
//
// WHY THIS BELONGS IN env status AND NOT A NEW VERB. The command already
// answers "is this env running what it declares" by comparing a binding
// against a cluster. These three are the facts a reader needs the moment that
// answer is interesting: a DRIFT whose cause is a convergence that failed is a
// different problem from one whose cause is a bad release, and a reader who
// has to run a second command to tell them apart will not.
//
// FORGE NEVER APPLIES, SO IT NEVER REPORTS AN APPLY. A reconciler converges
// each env to its promoted bundle, and what succeeded is a SECONDARY
// OBSERVATION written by the control plane's observer. So the convergence
// shown here is read from the control plane and labelled as its observation;
// forge contributes no apply record of its own, and this file deliberately
// ignores the fields that carry forge-written ones. An env with no control
// plane has no reconciler, and says so in one line rather than showing an
// empty apply that would read as "nothing has deployed".
//
// IT NEVER CHANGES THE VERDICT, AND THAT IS LOAD-BEARING. Every exit code
// this command returns is computed by envStatusReleaseVerdict from the
// binding and the cluster, exactly as before. Records are DESCRIPTION: a
// failed convergence is not an error here (the cluster comparison already has
// an opinion about whether the bytes landed), an unreachable records backend
// is not an error (it says so and the verdict stands), and an env with no
// records at all prints an empty state and exits 0. A records read that could
// fail the command would make `forge env status` depend on a control plane
// being reachable to answer a question about a local cluster.
//
// EMPTY IS NOT UNKNOWN, and the two are rendered differently throughout. "no
// apply recorded" is an answer; "could not read the records" is not. The
// §7.4 rule that a surface must distinguish "no sessions" from "cannot see
// sessions" is the same rule — a view that collapses them tells a user their
// stack is down when really their ledger could not be opened.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/pkg/release"
)

// envStatusRecords is the additive section of the status document.
//
// Nil when the records were not read at all. Every field inside is
// independently absent-able, because the four facts come from four reads and
// one failing says nothing about the others.
type envStatusRecords struct {
	// Source is which backend answered: "machine ledger" or "control
	// plane". A reader comparing two machines' views needs it, and "the
	// records were empty" is a different diagnosis from "I read a
	// different store than I meant to".
	Source string `json:"source"`
	// Location is the store's address — a directory or an endpoint.
	Location string `json:"location,omitempty"`
	// Provenance is where the BOUND release's content came from, and how
	// the binding was made. Nil when the env is unbound or the release
	// record predates provenance.
	Provenance *envStatusProvenance `json:"provenance,omitempty"`
	// Convergence is what THE RECONCILER did, as the control plane's
	// observer recorded it: converged to a bundle at a time, or failed at
	// a time with a reason.
	//
	// IT ONLY EVER COMES FROM A CONTROL PLANE, and that is an
	// architectural fact rather than a current limitation. forge never
	// applies to a cluster — one reconciler converges every env to its
	// promoted bundle — so forge is not a witness to convergence and has
	// nothing truthful to say about it. A record forge wrote would be a
	// claim about work it did not do.
	//
	// So it is a SECONDARY OBSERVATION, labelled as one wherever it is
	// rendered. A machine-ledger env has no reconciler and therefore no
	// convergence record, which is a different fact from "nothing has
	// converged yet" and is rendered as such (ConvergenceDetail).
	//
	// Nil means none is known. Never a failure: see the file header.
	Convergence *envStatusConvergence `json:"convergence,omitempty"`
	// Trees is the BOUND-vs-LIVE view per tree of the bound bundle: for each
	// cluster x delivery unit x phase, who applies it, the revision the
	// promotion binds it to, the revision the hub's reconciler reports it
	// applied, and what the two add up to. Present only for an env whose
	// bundle carries trees. See tree_status.go.
	//
	// Promotion is INTENT and convergence is OBSERVED: the binding is never
	// moved late to make the two agree, so a tree behind its binding is shown
	// as behind.
	Trees []envStatusTree `json:"trees,omitempty"`
	// TreesDetail says why Trees is empty or imprecise when that is not
	// obvious: no bound bundle, or a control plane that reports per cluster.
	TreesDetail string `json:"trees_detail,omitempty"`
	// ConvergenceDetail says why there is none to show. Two structural
	// cases, which must not render alike: this env has no reconciler, or
	// its control plane has not reported one yet.
	ConvergenceDetail string `json:"convergence_detail,omitempty"`
	// Sessions are the local stacks. Always non-nil when SessionsRead is
	// true, so a consumer sees `[]` rather than `null` for "none
	// running".
	Sessions []envStatusSession `json:"sessions"`
	// SessionsRead distinguishes "no sessions" from "sessions were not
	// read" — the §7.4 rule, as a field rather than as a convention a
	// consumer has to remember. False for a non-local env, which has no
	// local presence to report.
	SessionsRead bool `json:"sessions_read"`
	// SessionsDetail says WHY when SessionsRead is false.
	SessionsDetail string `json:"sessions_detail,omitempty"`
	// Detail carries a read failure. The verdict is unaffected: see the
	// file header.
	Detail string `json:"detail,omitempty"`
}

// envStatusProvenance is the bound release's source, plus the binding's own
// story.
//
// THE TWO HALVES ARE DIFFERENT KINDS OF CLAIM and the field names keep them
// apart. Commit and Tree are checkable against the repository (a clean tree's
// hash IS the commit's tree). Branch and Dirty are what forge REPORTED at
// build time — evidence, never authority — which is why the renderer labels
// them as reported rather than stating them flatly.
type envStatusProvenance struct {
	Release string `json:"release"`
	Repo    string `json:"repo,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Tree    string `json:"tree,omitempty"`
	Dirty   bool   `json:"dirty"`
	// ForgeVersion is the binary that rendered it. A render is a function
	// of (forge, KCL, config), so the same commit rendered by two forges
	// is two different artifacts.
	ForgeVersion string `json:"forge_version,omitempty"`
	// BoundBy is HOW the current binding was made: the promotion kind, the
	// env it came from, who made it, and the plan that was approved. This
	// is the half a reader asking "why is this version here" wants, and it
	// is a property of the PROMOTION rather than of the release.
	BoundBy envStatusBinding `json:"bound_by"`
}

// envStatusBinding is the promotion that bound the env.
type envStatusBinding struct {
	PromotionID string `json:"promotion_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	// FromEnv is the env this release was promoted FROM, empty for a
	// first deploy or a direct promote. It makes the path auditable.
	FromEnv string `json:"from_env,omitempty"`
	// PromotedBy is who, as the backend recorded it. A hosted backend
	// sets it from the credential; a file ledger records what it was told.
	PromotedBy string `json:"promoted_by,omitempty"`
	PromotedAt string `json:"promoted_at,omitempty"`
	// PlanDigest and ApprovedBy name the review this binding was written
	// under (O-13). Empty on a binding made before plans, or by a path
	// that needs no approval.
	PlanDigest string `json:"plan_digest,omitempty"`
	ApprovedBy string `json:"approved_by,omitempty"`
	// SupersededInFlight records that this promotion replaced one whose
	// rollout had not finished — a deliberate override, and the audit
	// trail for "who decided to interrupt a rollout".
	SupersededInFlight bool   `json:"superseded_in_flight,omitempty"`
	Note               string `json:"note,omitempty"`
}

// convergenceObserver names who saw a convergence, defaulting to the control
// plane — which is what every record written before the field existed is.
func convergenceObserver(c envStatusConvergence) string {
	if strings.TrimSpace(c.ObservedBy) != "" {
		return c.ObservedBy
	}
	return "the control plane"
}

// envStatusConvergence is what the reconciler did, as the control plane's
// observer reported it.
//
// EVERY FIELD IS AN OBSERVATION, not a thing forge established, and the
// renderer says so once rather than hedging each line. There is no identity
// field for "who applied": the reconciler applied it, that is the only
// answer, and a field inviting a reader to look for a human would misdescribe
// the model.
type envStatusConvergence struct {
	// State is the reconciler's verdict as the control plane spells it.
	// Whatever vocabulary it uses is passed through rather than remapped:
	// forge is relaying another system's observation, and a translation
	// layer here would eventually disagree with what the control plane's
	// own surfaces show for the same env.
	State string `json:"state"`
	// BundleID and BundleDigest name WHAT it converged to. The bundle IS
	// its content, so the digest is the field that actually says which
	// config is running.
	BundleID     string `json:"bundle_id,omitempty"`
	BundleDigest string `json:"bundle_digest,omitempty"`
	// PromotionID is the binding the reconciler was converging toward.
	PromotionID string `json:"promotion_id,omitempty"`
	// ObservedAt is when the control plane recorded this. NOT when the
	// reconciler acted: it is an observation of convergence, and the gap
	// between the two is why the field is named for the observation.
	ObservedAt string `json:"observed_at,omitempty"`
	// Detail is the reason on a failure, verbatim from the observer.
	Detail string `json:"detail,omitempty"`
	// ObservedBy names WHO saw this, and it is load-bearing rather than
	// decorative: a convergence record is a SECONDARY OBSERVATION, and a
	// reader deciding how much to trust it needs to know whose claim it
	// is. Two observers exist, and they are not equally direct:
	//
	//   "the control plane" — a hosted env's reconcile, as that control
	//     plane's own observer recorded it. Forge is relaying a report.
	//   any other name — a reconciler observed by something other than the
	//     control plane. Nothing writes one today; the field is read so a
	//     record that carries one is attributed correctly rather than
	//     defaulted to the control plane.
	//
	// Empty renders as the control-plane phrasing, which is what every
	// pre-existing record is.
	ObservedBy string `json:"observed_by,omitempty"`
}

// envStatusSession is one presence row with the verdicts a renderer needs.
type envStatusSession struct {
	Worktree string `json:"worktree"`
	Host     string `json:"host,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Dirty    bool   `json:"dirty"`
	// State is running | quiet | stopped. "quiet" is a LIVE session that
	// has not been seen for release.SessionStaleAfter — deliberately not
	// called stale or dead: forge cannot tell a crashed stack from a
	// laptop that closed its lid, and naming it after the observation
	// rather than a guessed cause is the honest rendering of both.
	State      string `json:"state"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
}

// Session states as this view names them.
const (
	sessionStateRunning = "running"
	sessionStateQuiet   = "quiet"
	sessionStateStopped = "stopped"
)

// envRecordsReader reads one env's records.
//
// A seam, for the reason every other seam in this command exists: resolving
// it for real renders KCL, opens a ledger home and may call a control plane,
// and a test of the RENDERING should be able to state the records directly.
// The production command injects the real reader rather than leaving this nil
// and branching on it, so both paths run the same code.
type envRecordsReader func(ctx context.Context, projectDir, env string, now time.Time) (envStatusRecords, error)

// readEnvRecords is the production reader: the records of whichever store the
// env's declaration selects.
//
// It goes through sessionTargetFor, which goes through ledgerForEntities — so
// the store these records are READ from is the store a session is WRITTEN to
// and the store a promotion is recorded in. One declaration, one answer.
func readEnvRecords(ctx context.Context, projectDir, env string, now time.Time) (envStatusRecords, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		// Not fatal, and not silent. An env named on the command line
		// but absent from this checkout is a real case (a release cut
		// on a branch that declares it, read from one that does not),
		// and the records that exist for it are still worth naming as
		// unreadable rather than as empty.
		return envStatusRecords{}, fmt.Errorf("render deploy/kcl/%s to find this env's records: %w", env, err)
	}
	target, err := sessionTargetForEntities(env, entities, projectDir)
	if err != nil {
		return envStatusRecords{}, err
	}
	if target.Hosted {
		return readHostedEnvRecords(ctx, env, entities, now)
	}
	out, err := readMachineEnvRecords(env, projectDir, target, now)
	if target.Report {
		facts := newOSProcFacts()
		reconcileLocalPresence(&out, projectDir, env, facts.pidList(), facts)
		if running, composeErr := ownedComposeRunning(ctx, projectDir, env); composeErr == nil && running {
			markLocalStackRunning(&out, projectDir)
		}
	}
	return out, err
}

// readMachineEnvRecords reads the file ledger: provenance and sessions.
//
// NO CONVERGENCE FROM HERE. This function reads RECORDS, and a machine ledger
// holds none about convergence: nothing watches a jsonl file, so forge never
// wrote one, and a record forge wrote would be a claim about work it did not do. The default detail says "no reconciler for this env", which
// is the truth for an env that declares a lifecycle — forge's own apply is the
// whole story there.
//
// It reuses F7's ledgerShow rather than walking the store again, so
// `forge ledger show` and `forge env status` cannot come to disagree about
// the same env's sessions.
func readMachineEnvRecords(env, projectDir string, target sessionTarget, now time.Time) (envStatusRecords, error) {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return envStatusRecords{}, err
	}
	out := envStatusRecords{
		Source:            "machine ledger",
		Location:          store.Dir(),
		Sessions:          []envStatusSession{},
		SessionsRead:      target.Report,
		SessionsDetail:    target.Skip,
		ConvergenceDetail: "no reconciler for this env",
	}
	doc, err := ledgerShow(store, hostedProjectName(), env, now)
	if err != nil {
		return out, err
	}
	for _, shown := range doc.Environments {
		if shown.Name != env {
			continue
		}
		if shown.Current != nil {
			// The release record is read separately because the
			// promotion names a VERSION and the provenance lives on
			// the release. A missing release record is not an
			// error: a promotion can name a version imported from
			// elsewhere, and "bound to v1.2.0, provenance unknown"
			// is a better answer than no binding information at
			// all.
			rel, _ := store.Release(shown.Current.Release)
			out.Provenance = provenanceOf(*shown.Current, rel)
		}
	}
	if target.Report {
		for _, s := range doc.Sessions {
			// The ledger is project-wide, but this command describes THIS
			// checkout/env. A sibling worktree's heartbeat is not ours.
			if s.Session.Env != env || s.Session.Worktree.Key != devstack.Worktree(projectDir) || s.Session.Worktree.Host != release.HostID() {
				continue
			}
			out.Sessions = append(out.Sessions, sessionOf(s.Session, now))
		}
	}
	return out, nil
}

// readHostedEnvRecords reads the control plane's Live view.
//
// ONE RPC, not several. GetLiveView already returns the current promotion,
// the current release, the current bundle, the rollout phase, the observer's
// drift record and the sessions for every env of a project — assembled
// server-side against one snapshot. Several per-env calls would be several
// round trips that could each see a different instant, which is how a view
// ends up describing a convergence onto a bundle it also says is not current.
//
// IT DELIBERATELY IGNORES row.LatestApply. That field carries the records
// forge's own BeginApply / FinishApply wrote, and forge no longer applies to
// clusters — a reconciler does. So those records describe work forge did not
// do, the seams that write them are being deleted, and a reader built on them
// would show a "latest apply" that stops updating the moment the unified
// model lands. The convergence shown here comes from the control plane's OWN
// computation (the rollout phase) and its OWN observer (the drift record),
// both of which are secondary observations of what the reconciler actually
// did.
func readHostedEnvRecords(ctx context.Context, env string, entities *KCLEntities, now time.Time) (envStatusRecords, error) {
	ledger, err := ledgerForEntities(env, entities, projectDirForKCL())
	if err != nil {
		return envStatusRecords{}, err
	}
	hosted, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		return envStatusRecords{}, fmt.Errorf("env %q selected a hosted ledger whose store is %T", env, ledger.Bindings)
	}
	out := envStatusRecords{
		Source:   "control plane",
		Location: hosted.endpoint,
		Sessions: []envStatusSession{},
	}
	rows, err := hostedRecordStoreFor(hosted.client, hosted.project).Live().GetLiveView(ctx, hosted.project, false, now)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		if row.Env.Name != env {
			continue
		}
		if row.CurrentPromotion != nil {
			out.Provenance = provenanceOf(*row.CurrentPromotion, row.CurrentRelease)
		}
		out.Convergence, out.ConvergenceDetail = convergenceOf(row)
		split, serr := unitSplitOf(entities)
		if serr != nil {
			out.TreesDetail = "the env's delivery-unit split could not be read: " + serr.Error()
		} else {
			out.Trees, out.TreesDetail = treesOf(ctx, hosted, env, split, row)
		}
		// Sessions come back only for a LOCAL env; the server's trigger
		// confines them. So the presence of the row IS the answer to
		// "were sessions read", and a non-local env reports none
		// without this client having to classify it a second time.
		out.SessionsRead = true
		for _, s := range row.Sessions {
			out.Sessions = append(out.Sessions, sessionOf(s, now))
		}
	}
	return out, nil
}

// provenanceOf projects a promotion and its release onto the view.
func provenanceOf(p release.Promotion, rel *release.Release) *envStatusProvenance {
	out := &envStatusProvenance{
		Release: p.Release,
		BoundBy: envStatusBinding{
			PromotionID:        p.ID,
			Kind:               string(p.Kind),
			FromEnv:            p.FromEnv,
			PromotedBy:         promoterName(p.PromotedBy),
			PlanDigest:         p.PlanDigest,
			ApprovedBy:         p.ApprovedBy,
			SupersededInFlight: p.SupersededInFlight,
			Note:               p.Note,
		},
	}
	if !p.PromotedAt.IsZero() {
		out.BoundBy.PromotedAt = p.PromotedAt.UTC().Format(time.RFC3339)
	}
	if rel != nil && rel.Provenance != nil {
		prov := rel.Provenance
		out.Repo, out.Commit, out.Branch = prov.Repo, prov.Commit, prov.Branch
		out.Tag, out.Tree, out.Dirty = prov.Tag, prov.Tree, prov.Dirty
		out.ForgeVersion = prov.ForgeVersion
	}
	return out
}

// promoterName is actorLabel for a JSON field.
//
// actorLabel (env_history.go) renders "-" for an unknown actor, which is
// right for a fixed-width table column and wrong here: a JSON consumer
// reading `promoted_by: "-"` has to know that one renderer's placeholder, and
// the field is already omitempty for exactly this case. So the placeholder is
// dropped rather than a second actor renderer written.
func promoterName(a release.Actor) string {
	if name := actorLabel(a); name != "-" {
		return name
	}
	return ""
}

// convergenceOf projects the control plane's own observations of one env onto
// the view, or says why there is nothing to project.
//
// It reads the rollout PHASE (the control plane's server-side computation)
// and the observer's DRIFT record. Neither is written by forge, which is the
// property that makes them usable under the unified-apply model: forge is
// relaying what another system saw, not reporting work it claims to have
// done.
//
// WHAT IT CONVERGED TO comes from the CURRENT BUNDLE, which is the right
// source here and was not under the old model: the reconciler's whole job is
// to converge the env to its promoted bundle, so the bundle the control plane
// calls current IS the target, and the phase says how far that got. There is
// no second bundle id to reconcile against — which is why dropping
// LatestApply loses no information a reader had.
func convergenceOf(row LiveEnvironment) (*envStatusConvergence, string) {
	phase := rolloutPhaseName(row.Phase)
	// "unspecified" is the control plane saying it has nothing to report —
	// an env it knows about but whose reconciler has not acted or has not
	// been observed. That is the "not yet reported" case, which must not
	// render as a state.
	if phase == "" || phase == "unspecified" {
		return nil, "not yet reported by the control plane"
	}
	out := &envStatusConvergence{State: phase}
	if row.CurrentBundle != nil {
		out.BundleID, out.BundleDigest = row.CurrentBundle.ID, row.CurrentBundle.Digest
	}
	if row.CurrentPromotion != nil {
		out.PromotionID = row.CurrentPromotion.ID
	}
	// The drift record carries the observer's own timestamp and reason.
	// Used for WHEN and WHY only — its in_sync/drifted verdict is a
	// different question from the phase and is already reported by the
	// verify half of this command, so restating it here would give a
	// reader two places to look for one answer.
	if row.Drift != nil {
		if row.Drift.ObservedAt != nil {
			out.ObservedAt = row.Drift.ObservedAt.UTC().Format(time.RFC3339)
		}
		out.Detail = row.Drift.Detail
	}
	return out, ""
}

// sessionOf projects a presence row onto the view.
func sessionOf(s release.LocalSession, now time.Time) envStatusSession {
	out := envStatusSession{
		Worktree: s.Worktree.Label,
		Host:     s.Worktree.Host,
		Branch:   s.Provenance.Branch,
		Commit:   s.Provenance.Commit,
		Dirty:    s.Provenance.Dirty,
		State:    sessionStateStopped,
	}
	if out.Worktree == "" {
		out.Worktree = "(primary checkout)"
	}
	switch {
	case s.Stale(now):
		out.State = sessionStateQuiet
	case s.Live():
		out.State = sessionStateRunning
	}
	if !s.LastSeenAt.IsZero() {
		out.LastSeenAt = s.LastSeenAt.UTC().Format(time.RFC3339)
	}
	if !s.StartedAt.IsZero() {
		out.StartedAt = s.StartedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// collectEnvRecords runs the reader and folds a failure into the document
// rather than returning it.
//
// This is where "records never change the verdict" is implemented. The error
// becomes Detail and the caller gets a document either way, so there is no
// path by which a records read reaches runEnvStatusRelease's return value.
func collectEnvRecords(ctx context.Context, read envRecordsReader, projectDir, env string, now time.Time) envStatusRecords {
	if read == nil {
		read = readEnvRecords
	}
	records, err := read(ctx, projectDir, env, now)
	if err != nil {
		if records.Source == "" {
			records.Source = "unknown"
		}
		if records.Sessions == nil {
			records.Sessions = []envStatusSession{}
		}
		records.Detail = err.Error()
	}
	return records
}

// writeEnvStatusRecords renders the text form.
//
// Printed as its own block after the per-image report, because it answers a
// different question and a reader follows them in order: what is wrong, then
// where it came from and whether it finished landing.
func writeEnvStatusRecords(w io.Writer, r envStatusRecords) {
	fmt.Fprintf(w, "\nRecords (%s", r.Source)
	if r.Location != "" {
		fmt.Fprintf(w, " %s", r.Location)
	}
	fmt.Fprintf(w, ")\n")
	if r.Detail != "" {
		// Stated as a read failure, never as an empty result — the
		// empty-versus-unknown rule from the file header. The reader
		// must not conclude "nothing has been deployed" from "I could
		// not look".
		fmt.Fprintf(w, "  could not read the records: %s\n", r.Detail)
		return
	}

	switch p := r.Provenance; {
	case p == nil:
		fmt.Fprintf(w, "  provenance    not recorded for this binding\n")
	default:
		fmt.Fprintf(w, "  provenance    %s\n", provenanceLine(*p))
		fmt.Fprintf(w, "  bound by      %s\n", boundByLine(p.BoundBy))
	}

	if r.Convergence == nil {
		// One plain line, and it names WHICH of the two structural
		// reasons applies. "No reconciler for this env" and "the
		// control plane has not reported" are different facts, and a
		// reader who confuses them goes looking for the wrong thing.
		fmt.Fprintf(w, "  convergence   %s\n", emptyAs(r.ConvergenceDetail, "not reported"))
	} else {
		c := *r.Convergence
		fmt.Fprintf(w, "  convergence   %s%s\n", strings.ToUpper(c.State), convergenceWhen(c))
		if c.BundleDigest != "" {
			fmt.Fprintf(w, "                bundle %s (%s)\n", c.BundleID, c.BundleDigest)
		} else if c.BundleID != "" {
			fmt.Fprintf(w, "                bundle %s\n", c.BundleID)
		}
		// Said ONCE, for the whole block, rather than hedged per line.
		// Every field above is something another system saw; forge did
		// not apply this and is not the witness.
		fmt.Fprintf(w, "                observed by %s (forge does not apply; a reconciler converges this env)\n",
			convergenceObserver(c))
		if c.Detail != "" {
			fmt.Fprintf(w, "                %s\n", c.Detail)
		}
	}

	writeTreeTable(w, r.Trees)
	if r.TreesDetail != "" {
		fmt.Fprintf(w, "  trees: %s\n", r.TreesDetail)
	}

	switch {
	case !r.SessionsRead:
		fmt.Fprintf(w, "  sessions      not reported for this environment")
		if r.SessionsDetail != "" {
			fmt.Fprintf(w, " — %s", r.SessionsDetail)
		}
		fmt.Fprintln(w)
	case len(r.Sessions) == 0:
		fmt.Fprintf(w, "  sessions      no local stacks running\n")
	default:
		fmt.Fprintf(w, "  sessions      %d local stack(s)\n", len(r.Sessions))
		for _, s := range r.Sessions {
			fmt.Fprintf(w, "                %-10s %s\n", s.State, sessionLine(s))
		}
	}
}

// provenanceLine is "v1.2.0 from main@abc1234 (dirty)".
func provenanceLine(p envStatusProvenance) string {
	parts := []string{p.Release}
	switch {
	case p.Branch != "" && p.Commit != "":
		parts = append(parts, "from "+p.Branch+"@"+shortID(p.Commit))
	case p.Commit != "":
		parts = append(parts, "from "+shortID(p.Commit))
	}
	if p.Tag != "" {
		parts = append(parts, "tag "+p.Tag)
	}
	if p.Dirty {
		// Flagged as REPORTED because it is a claim, not a checkable
		// fact: a dirty tree's hash cannot be verified against the
		// repository, so the renderer must not state it as established.
		parts = append(parts, "dirty tree (reported by forge at build time)")
	}
	if p.ForgeVersion != "" {
		parts = append(parts, "forge "+p.ForgeVersion)
	}
	return strings.Join(parts, ", ")
}

// boundByLine is how the binding was made.
func boundByLine(b envStatusBinding) string {
	parts := []string{}
	if b.Kind != "" {
		parts = append(parts, b.Kind)
	}
	if b.FromEnv != "" {
		parts = append(parts, "from "+b.FromEnv)
	}
	if b.PromotedBy != "" {
		parts = append(parts, "by "+b.PromotedBy)
	}
	if b.PromotedAt != "" {
		parts = append(parts, "at "+b.PromotedAt)
	}
	if b.ApprovedBy != "" {
		parts = append(parts, "approved by "+b.ApprovedBy)
	}
	if b.SupersededInFlight {
		parts = append(parts, "superseded a rollout in flight")
	}
	if b.Note != "" {
		parts = append(parts, "note: "+b.Note)
	}
	if len(parts) == 0 {
		return "(no promotion detail recorded)"
	}
	return strings.Join(parts, ", ")
}

// convergenceWhen names the OBSERVATION time, not an action time. A
// convergence forge can see is one the control plane recorded seeing, and
// labelling it "at T" would invite a reader to treat T as when the reconciler
// acted.
func convergenceWhen(c envStatusConvergence) string {
	if c.ObservedAt == "" {
		return ""
	}
	return ", observed " + c.ObservedAt
}

func sessionLine(s envStatusSession) string {
	parts := []string{s.Worktree}
	if s.Branch != "" {
		label := s.Branch
		if s.Commit != "" {
			label += "@" + shortID(s.Commit)
		}
		parts = append(parts, label)
	}
	if s.Dirty {
		parts = append(parts, "dirty")
	}
	if s.State != sessionStateStopped && s.LastSeenAt != "" {
		parts = append(parts, "last seen "+s.LastSeenAt)
	}
	return strings.Join(parts, ", ")
}

// shortID abbreviates a git object id for display. The full value is always
// in --json, so the text form may shorten; a reader copying a commit out of a
// terminal wants seven characters, and a machine never reads this path.
func shortID(id string) string {
	if len(id) > 7 {
		return id[:7]
	}
	return id
}

// treesOf is the per-tree bound-vs-live view of one env's Live row.
//
// BOUND is the bundle the env's current promotion resolves to: the control
// plane's recorded bundle for the promoted release, whose shape names every
// object's tree. It is read from the Live row (CurrentBundle) and falls back to
// nothing when the control plane holds none.
//
// LIVE is the control plane's convergence timeline (ListConvergences), read at
// whatever precision it reports (see tree_status.go). A timeline that cannot be
// read, or a control plane without one, leaves every hub tree "unobserved" and
// says so in TreesDetail: this view never fails the command and never invents a
// reading.
func treesOf(ctx context.Context, hosted *hostedStore, env string, split unitSplit, row LiveEnvironment) ([]envStatusTree, string) {
	bundle := row.CurrentBundle
	if bundle == nil {
		if row.CurrentPromotion == nil {
			return nil, ""
		}
		return nil, "no bundle is recorded for the bound release, so its trees cannot be listed"
	}
	trees := treeInventory(bundle.Shape)
	if len(trees) == 0 {
		return nil, ""
	}
	if !split.AnyHub() {
		return judgeTrees(trees, split, bundle.Digest, nil), "forge applies every unit of this env; no reconciler observes its trees"
	}
	readings, found, rerr := readHubReadings(ctx, envLedger{Bindings: hosted, Hosted: true}, env)
	if rerr != nil {
		return judgeTrees(trees, split, bundle.Digest, nil), "the hub's convergence readings could not be read: " + rerr.Error()
	}
	if !found {
		return judgeTrees(trees, split, bundle.Digest, nil), "the control plane has not reported on this env yet"
	}
	var tr []treeReading
	perTree := false
	for _, r := range readings {
		if r.Tree != "" {
			perTree = true
		}
		tr = append(tr, treeReading{
			Cluster: r.Cluster, Tree: r.Tree, State: r.State, Revision: revisionDigestOf(r.Revision),
			Reason: r.Reason, Message: r.Message, ObservedAt: r.ObservedAt,
		})
	}
	detail := ""
	if !perTree && len(tr) > 0 {
		detail = "this control plane reports convergence per cluster, not per tree; each tree shows its cluster's reading"
	}
	return judgeTrees(trees, split, bundle.Digest, tr), detail
}

// revisionDigestOf extracts the sha256 digest from a reconciler revision such
// as "latest@sha256:abc…"; a revision without one is returned verbatim.
func revisionDigestOf(rev string) string {
	if i := strings.Index(rev, "sha256:"); i >= 0 {
		return rev[i:]
	}
	return rev
}
