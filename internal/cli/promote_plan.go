package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The promote CHANGE SET — computed once, rendered twice, applied optionally.
//
// WHY THIS FILE EXISTS. `forge env deploy <env> <version>` is the one write verb in the
// release model with genuinely reviewable semantics: forge builds once and
// then binds digests, so promote moves a POINTER and rebuilds nothing. That
// makes it cheap and, in principle, fully previewable — you can know the
// entire effect before anything is written. Until this file you could not:
// promote read the ledger and called SetBinding immediately, and the only way
// to find out what it would do was to let it do it.
//
// ONE FUNCTION, BOTH PATHS, AND THAT IS THE LOAD-BEARING CONSTRAINT.
// computePromotePlan produces the whole change set; `--plan` renders it and
// stops, and a real promote renders the SAME value and applies it. Two code
// paths — a preview that describes the write and a write that performs it —
// would be free to disagree, and a preview that disagrees with the write is
// worse than no preview at all: it converts "I did not look" into "I looked
// and was told the wrong thing". TestPromotePlan_PlanAndApplyAgree pins this
// so a later refactor cannot quietly split them.
//
// WHAT A REVIEWER NEEDS, AND WHY EACH PIECE IS MODELLED EXPLICITLY.
//
//   - The image SET can change, not just the digests. control-plane's prod
//     line carries `internal-console` and its staging/preprod line does not,
//     so promoting between those two release lines ADDS or REMOVES an image.
//     A digest map diff leaves that for the caller to notice; an explicit
//     added/removed classification cannot be missed.
//   - DIRECTION is the single most consequential thing a reader can misread.
//     A backwards promote is possible (it is an ordinary promote, and it
//     undoes nothing the newer release did), but "staging moves forward 20 releases" and "staging moves
//     back 20 releases" look identical in a digest diff.
//   - The COMMIT RANGE is what makes the digests mean something. Every way it
//     can be unavailable is a first-class state rather than an error, because
//     a plan that refuses to render because a release was cut on another
//     branch is a plan nobody can use on the day they need it.
//   - Promote SHIPS NOTHING. `forge env status` exists because that gap was
//     invisible; the plan states it rather than assuming the reader knows.

// promoteCommitRangeLimit caps how many commit subjects the range carries.
// A 20-release gap can be hundreds of commits, and a JSON document that grows
// without bound is one a UI cannot render — the count is always exact, and
// Truncated says the list is not.
const promoteCommitRangeLimit = 50

// promoteGitTimeout bounds each read-only git invocation. Generous for a cold
// object store on a large repository, short enough that a wedged git cannot
// hang a plan whose whole promise is that it is cheap.
const promoteGitTimeout = 20 * time.Second

// ─── Enums. Lowercase strings out, strict decode in ──────────────────────────

// promoteImageChangeKind classifies what a promote does to ONE image.
//
// THE ZERO VALUE IS "unknown", DELIBERATELY. The tempting zero value is
// "unchanged", and it is exactly wrong: an unpopulated struct, or a value
// decoded by a binary that does not recognise a newer classification, would
// then read as "nothing happens to this image" — the one answer that makes a
// real change invisible. Unknown is the conservative default, and
// UnmarshalJSON refuses a string it does not recognise for the same reason.
type promoteImageChangeKind int

const (
	// promoteImageUnknown: not classified. Never produced by
	// computePromotePlan; it exists so an uninitialised or
	// forward-incompatible value cannot masquerade as "unchanged".
	promoteImageUnknown promoteImageChangeKind = iota
	// promoteImageUnchanged: present in both bindings, same digest. The
	// env already runs these bytes for this image.
	promoteImageUnchanged
	// promoteImageChanged: present in both, DIFFERENT digest. Both digests
	// are carried, because the reviewer's question is "from what, to what".
	promoteImageChanged
	// promoteImageAdded: in the target release, absent from the current
	// binding. The env gains an image it was not running.
	promoteImageAdded
	// promoteImageRemoved: in the current binding, absent from the target
	// release. The env's binding will stop declaring this image — nothing
	// is deleted from any cluster by promote, but the next deploy will no
	// longer pin it.
	promoteImageRemoved
)

// String renders the fixed-width label used in the text report.
func (k promoteImageChangeKind) String() string {
	switch k {
	case promoteImageUnchanged:
		return "UNCHANGED"
	case promoteImageChanged:
		return "CHANGED"
	case promoteImageAdded:
		return "ADDED"
	case promoteImageRemoved:
		return "REMOVED"
	default:
		return "UNKNOWN"
	}
}

// MarshalJSON emits the lowercase form, derived from String() so the text
// column and the JSON value cannot disagree about a cell.
func (k promoteImageChangeKind) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ToLower(k.String()) + `"`), nil
}

// UnmarshalJSON reads the string form back, REJECTING anything it does not
// know. A lenient decoder that fell back to "unchanged" would under-report a
// real change, which is the single failure this whole file exists to prevent.
func (k *promoteImageChangeKind) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("promote image change must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*k = promoteImageUnknown
	case "unchanged":
		*k = promoteImageUnchanged
	case "changed":
		*k = promoteImageChanged
	case "added":
		*k = promoteImageAdded
	case "removed":
		*k = promoteImageRemoved
	default:
		return fmt.Errorf("unknown promote image change %q (expected unchanged, changed, added or removed) — "+
			"refusing to decode it as a default, which would report a real change as unchanged", name)
	}
	return nil
}

// promoteDirection is where the target release sits relative to what the env
// runs now, in the project's release ordering.
//
// Zero value is "unknown": a direction nobody computed must not read as
// "same", which would tell a reviewer a backwards move was a no-op.
type promoteDirection int

const (
	// promoteDirectionUnknown: the ordering could not place both releases.
	promoteDirectionUnknown promoteDirection = iota
	// promoteDirectionInitial: the env has never been promoted, so there
	// is no "from" to have a direction relative to.
	promoteDirectionInitial
	// promoteDirectionAhead: the target was cut AFTER the release the env
	// currently runs — moving forward.
	promoteDirectionAhead
	// promoteDirectionBehind: the target was cut BEFORE the current
	// release. Possible, never a rollback (the newer release's migrations
	// and data stay), and the thing a reviewer most needs told explicitly.
	promoteDirectionBehind
	// promoteDirectionSame: the env is already bound to this release.
	promoteDirectionSame
)

func (d promoteDirection) String() string {
	switch d {
	case promoteDirectionInitial:
		return "INITIAL"
	case promoteDirectionAhead:
		return "AHEAD"
	case promoteDirectionBehind:
		return "BEHIND"
	case promoteDirectionSame:
		return "SAME"
	default:
		return "UNKNOWN"
	}
}

func (d promoteDirection) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ToLower(d.String()) + `"`), nil
}

func (d *promoteDirection) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("promote direction must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*d = promoteDirectionUnknown
	case "initial":
		*d = promoteDirectionInitial
	case "ahead":
		*d = promoteDirectionAhead
	case "behind":
		*d = promoteDirectionBehind
	case "same":
		*d = promoteDirectionSame
	default:
		return fmt.Errorf("unknown promote direction %q (expected initial, ahead, behind or same) — "+
			"refusing to decode it as a default, which would render a backwards move as a forward promote", name)
	}
	return nil
}

// promoteRangeState says whether the commit range was computed, and when it
// was not, WHY — as a value, not an error.
//
// Every one of these is a state a real project reaches, and none of them is a
// reason to refuse to print a plan. Zero value is "unknown" so an uncomputed
// range never reads as an empty-but-computed one: "0 commits" and "we could
// not tell" are completely different claims to put in front of a reviewer.
type promoteRangeState int

const (
	// promoteRangeUnknown: nothing was attempted or the attempt's outcome
	// was not recorded. Never produced deliberately.
	promoteRangeUnknown promoteRangeState = iota
	// promoteRangeComputed: the range was read from this checkout's git
	// history. Count is exact.
	promoteRangeComputed
	// promoteRangeFirstPromote: the env has no current binding, so there
	// is no "from" commit and therefore no range.
	promoteRangeFirstPromote
	// promoteRangeSameRelease: current and target are the same release —
	// the range is empty by definition, not by measurement.
	promoteRangeSameRelease
	// promoteRangeLedgerMissing: the release ledger for the CURRENT
	// binding is not in this checkout (it was cut on another branch), so
	// its commit is unknown. Not an error — the binding is still real and
	// still says what the env runs.
	promoteRangeLedgerMissing
	// promoteRangeNoCommit: a ledger exists but records no git commit
	// (cut from a non-git tree, or by an older forge).
	promoteRangeNoCommit
	// promoteRangeCommitNotFound: a commit is named but is not present in
	// THIS checkout — the release was cut on a branch this working copy
	// does not have. Reported plainly; the plan stands.
	promoteRangeCommitNotFound
	// promoteRangeDirtyRelease: an endpoint release was cut from a DIRTY
	// tree. Those bytes correspond to no reviewable commit, so a range
	// computed against the recorded commit would describe something other
	// than what shipped. Labelled meaningless rather than silently
	// computed — a plausible-looking wrong range is worse than none.
	promoteRangeDirtyRelease
	// promoteRangeGitUnavailable: git could not be read (not a repository,
	// no git binary, a failed invocation).
	promoteRangeGitUnavailable
)

func (s promoteRangeState) String() string {
	switch s {
	case promoteRangeComputed:
		return "COMPUTED"
	case promoteRangeFirstPromote:
		return "FIRST-PROMOTE"
	case promoteRangeSameRelease:
		return "SAME-RELEASE"
	case promoteRangeLedgerMissing:
		return "LEDGER-MISSING"
	case promoteRangeNoCommit:
		return "NO-COMMIT"
	case promoteRangeCommitNotFound:
		return "COMMIT-NOT-FOUND"
	case promoteRangeDirtyRelease:
		return "DIRTY-RELEASE"
	case promoteRangeGitUnavailable:
		return "GIT-UNAVAILABLE"
	default:
		return "UNKNOWN"
	}
}

func (s promoteRangeState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ReplaceAll(strings.ToLower(s.String()), "-", "_") + `"`), nil
}

func (s *promoteRangeState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("promote commit range state must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "unknown":
		*s = promoteRangeUnknown
	case "computed":
		*s = promoteRangeComputed
	case "first_promote":
		*s = promoteRangeFirstPromote
	case "same_release":
		*s = promoteRangeSameRelease
	case "ledger_missing":
		*s = promoteRangeLedgerMissing
	case "no_commit":
		*s = promoteRangeNoCommit
	case "commit_not_found":
		*s = promoteRangeCommitNotFound
	case "dirty_release":
		*s = promoteRangeDirtyRelease
	case "git_unavailable":
		*s = promoteRangeGitUnavailable
	default:
		return fmt.Errorf("unknown promote commit range state %q — "+
			"refusing to decode it as a default, which would present an uncomputed range as a measured one", name)
	}
	return nil
}

// ─── The plan document ───────────────────────────────────────────────────────

// promoteImageChange is one image's row in the change set.
type promoteImageChange struct {
	// Image is the bare image name, as it keys both the binding's Resolved
	// map and the release's Artifacts map.
	Image string `json:"image"`
	// Change is the classification. Read this rather than diffing the two
	// digest fields: `added` and `removed` are the cases a digest compare
	// silently turns into "one side is empty".
	Change promoteImageChangeKind `json:"change"`
	// CurrentDigest is what the env's existing binding declares. Empty for
	// an added image.
	CurrentDigest string `json:"current_digest,omitempty"`
	// TargetDigest is what the target release declares. Empty for a
	// removed image.
	TargetDigest string `json:"target_digest,omitempty"`
}

// promoteImageTally counts the classifications, so a consumer can badge the
// change set without walking it.
type promoteImageTally struct {
	Unchanged int `json:"unchanged"`
	Changed   int `json:"changed"`
	Added     int `json:"added"`
	Removed   int `json:"removed"`
}

// promoteCommitRange is the source change between the current and target
// releases: what a reviewer is actually approving.
type promoteCommitRange struct {
	// State says whether Count and Commits mean anything. Check it FIRST —
	// a zero Count with a non-computed state is "unknown", not "nothing".
	State promoteRangeState `json:"state"`
	// Detail explains a non-computed state in one line, naming the release
	// and the commit involved.
	Detail string `json:"detail,omitempty"`
	// FromCommit is the CURRENT release's recorded commit, ToCommit the
	// TARGET's. They are the ledgers' values verbatim, regardless of
	// direction, so a consumer can always tell which release each belongs
	// to. For a BEHIND move, history runs from ToCommit to FromCommit — see
	// Reverts.
	FromCommit string `json:"from_commit,omitempty"`
	ToCommit   string `json:"to_commit,omitempty"`
	// Reverts is true when the target is BEHIND the current release. The
	// listed commits are then the ones being taken AWAY, not added, and a
	// consumer rendering them as "incoming changes" has inverted the most
	// consequential fact on the screen.
	Reverts bool `json:"reverts,omitempty"`
	// Count is the exact number of commits in the range. Exact even when
	// Commits is truncated.
	Count int `json:"count"`
	// Commits is "<short-sha> <subject>", newest first, capped at
	// promoteCommitRangeLimit. Always non-nil so consumers see [].
	Commits []string `json:"commits"`
	// Truncated is true when Commits holds fewer entries than Count.
	Truncated bool `json:"truncated,omitempty"`
}

// promotePlanBinding describes the env's CURRENT state — what it is bound to
// before this promote.
type promotePlanBinding struct {
	// Bound is false for an env that has never been promoted. A separate
	// field from an empty Release so "never promoted" is distinguishable
	// from a binding carrying a blank version.
	Bound bool `json:"bound"`
	// PromotionID is the ledger entry the env is bound to — the value a
	// real promote sends as its compare-and-set expectation. Capture it
	// from `--plan --json` (e.g. when an approval is requested) and pass it
	// back as --expect-current, so a hotfix that lands while the approval
	// waits turns the pipeline red instead of being overwritten. Empty when
	// unbound; --expect-current unbound captures that state.
	PromotionID string `json:"promotion_id,omitempty"`
	// Release is the version the env runs now. Empty when unbound.
	Release string `json:"release,omitempty"`
	// Targets is the current entry's scope: the components a scoped deploy
	// moved to Release. Empty when the whole release moved.
	Targets []string `json:"targets,omitempty"`
	// PromotedAt is RFC3339 for when this binding was WRITTEN — not when
	// it was deployed. Promote moves a pointer; deploy moves bytes.
	PromotedAt string `json:"promoted_at,omitempty"`
	// ReleaseKnown is true when the ledger for Release exists in this
	// checkout. FALSE IS NOT AN ERROR: a release cut on another branch
	// leaves a perfectly real binding pointing at a file this checkout has
	// never seen, so provenance is omitted rather than guessed.
	ReleaseKnown bool `json:"release_known"`
	// Git is the current release's provenance. Nil when the ledger is
	// absent.
	Git *release.Git `json:"git,omitempty"`
	// ReleaseCreatedAt is RFC3339 for when the current release was CUT.
	ReleaseCreatedAt string `json:"release_created_at,omitempty"`
	// Note explains a state that would otherwise look like missing data.
	Note string `json:"note,omitempty"`
	// NotApplied is true when the ledger records that this binding's apply
	// FAILED: the env is bound to Release, but the target was never moved
	// to it. A plan that diffed against the binding alone would call a
	// re-deploy of the same release a no-op while it rolls every workload.
	NotApplied bool `json:"not_applied,omitempty"`
	// ApplyError is what the failed apply said.
	ApplyError string `json:"apply_error,omitempty"`
	// LiveRelease is, for a NotApplied binding, the newest earlier
	// promotion whose apply did not fail — the ledger's answer to "what is
	// actually running". The image diff is computed against it. Empty when
	// the ledger cannot say.
	LiveRelease string `json:"live_release,omitempty"`
}

// promotePlanTarget describes the release being promoted TO.
type promotePlanTarget struct {
	// Release is the version label.
	Release string `json:"release"`
	// CreatedAt is RFC3339 for when it was cut.
	CreatedAt string `json:"created_at,omitempty"`
	// Git is its provenance. Read `dirty`: a release cut from a tree with
	// uncommitted changes ships bytes matching no reviewable commit, and
	// promoting one means nobody can say what is in it.
	Git *release.Git `json:"git,omitempty"`
	// Images is how many images the release resolves digests for.
	Images int `json:"images"`
}

// promotePlan is the full change set, and the `--json` output contract for
// both `--plan` and a real promote.
//
// Extensions are ADDITIVE: new fields may be added; no field is renamed or
// repurposed, so a consumer reading `images[].change` keeps working.
type promotePlan struct {
	// Env is the environment being promoted.
	Env string `json:"env"`
	// Kind is what this entry records. Always "promote" — kept in the
	// document so a future entry type cannot be mistaken for one.
	Kind release.PromotionKind `json:"kind"`
	// Ledger names where the binding is recorded, as the binding store
	// reports it — a path today, a URL for a hosted backend. Opaque, for
	// DISPLAY only; do not join it or open it.
	Ledger string `json:"ledger"`
	// GeneratedAt is RFC3339 for when the plan was computed.
	GeneratedAt string `json:"generated_at"`
	// DryRun is true when --plan was passed.
	DryRun bool `json:"dry_run"`
	// Applied says whether the binding was actually WRITTEN. This is the
	// field that keeps `--plan` and a real promote one document: the shape
	// is identical and this is how a consumer tells them apart. Under
	// --plan it is always false, and nothing was written.
	Applied bool `json:"applied"`
	// FollowError is set when the promotion WAS recorded but applying it
	// failed. applied then stays true (the ledger did move) and this field is
	// what keeps that from reading as "shipped": the release is recorded, NOT
	// running.
	FollowError string `json:"follow_error,omitempty"`
	// Current is what the env runs now.
	Current promotePlanBinding `json:"current"`
	// Target is the release being promoted to.
	Target promotePlanTarget `json:"target"`
	// Scope is the components this promote moves when the deploy is scoped
	// (`--target`, `--frontends-only`), recorded on the promotion as its
	// Targets. Empty when the whole release moves.
	Scope []string `json:"scope,omitempty"`
	// Direction is where the target sits relative to the current release.
	// `behind` moves the env BACKWARDS — possible, never an undo, and the
	// single most consequential thing on this screen to misread.
	Direction promoteDirection `json:"direction"`
	// DirectionDetail says the same thing in one human sentence.
	DirectionDetail string `json:"direction_detail,omitempty"`
	// ReleasesBetween is how many releases separate the two in the
	// project's ordering, regardless of direction. Zero when the direction
	// is not known or the releases are the same.
	ReleasesBetween int `json:"releases_between"`
	// Images is one row per image in the union of the current binding and
	// the target release, sorted by name. Always non-nil.
	Images []promoteImageChange `json:"images"`
	// Tally counts the classifications.
	Tally promoteImageTally `json:"tally"`
	// Commits is the source change between the two releases.
	Commits promoteCommitRange `json:"commits"`
	// Changed is false only when this promote would alter nothing at all —
	// same release, same digest set. A caller can use it to skip a no-op.
	Changed bool `json:"changed"`
	// Confirmed reports whether the O-13 approval gate let this deploy
	// write (deploy_confirm.go). It is in the document because consent is
	// the one thing a consumer must never infer: "applied: false" alone
	// cannot distinguish "nobody approved it" from "the write was refused"
	// from "--plan", and those want three different responses from a
	// pipeline. False with ok:true and exit_code 0 is the approval-declined
	// / --plan-only shape; false with exit_code 5 is plan_unconfirmed.
	Confirmed bool `json:"confirmed"`
	// ShipsNothing is TRUE for the promote itself, and it is in the
	// contract on purpose: the ledger write is a pointer, and not one byte
	// reaches any target because of it. `forge env status` exists precisely
	// because that gap used to be invisible, so the plan states it rather
	// than relying on the reader to know it.
	//
	// It turns FALSE only when this command's own follow-through (the
	// deploy verb's apply) shipped something — then Shipped / PartlyShipped
	// say what. A document claiming "ships nothing" after a cluster rolled
	// and a frontend went live is the lie it exists to prevent.
	ShipsNothing bool `json:"ships_nothing"`
	// Shipped is each stage the deploy's follow-through shipped to a live
	// target — a cluster apply, a control-plane publish, a frontend host —
	// in order. PartlyShipped is each stage that STARTED and then failed:
	// some of its change may be live, so it is never reported as nothing.
	// Both absent when nothing reached a target, and on a promote with no
	// follow-through.
	Shipped       []string `json:"shipped,omitempty"`
	PartlyShipped []string `json:"partly_shipped,omitempty"`
	// Converges is what the env's control plane will converge of this
	// deploy, decided before anything is written
	// (hostedConvergencePreflight): nothing, for an env that binds no tier
	// to it; otherwise its hosted tiers, which of them this deploy publishes
	// for the first time, and any it leaves unpublished. Nil for an env
	// with no control-plane ledger.
	Converges *promotePlanConverges `json:"converges,omitempty"`
	// FrontendPreflight is what would REFUSE the frontend half of a real
	// deploy of this plan from the machine that computed it — typically
	// the `firebase` CLI not being installed. Reported only by a plan that
	// ships nothing (--plan, --plan-only, --dry-run), where it is a finding
	// and not a failure; a real deploy is refused instead, and
	// --skip-frontend waives the check. Empty when there is nothing to
	// report.
	FrontendPreflight []frontendPreflightFinding `json:"frontend_preflight,omitempty"`
	// followed is set once the deploy verb's follow-through ran, which is
	// what decides whether the footer reports what shipped or names the
	// deploy that will ship it.
	followed bool
	// NextStep is the command that actually ships these digests.
	NextStep string `json:"next_step"`
	// Note is the human phrasing of ShipsNothing.
	Note string `json:"note,omitempty"`
	// SourceNote warns about the `--from` source: set when
	// --from-promotion names a promotion that is no longer what the source
	// environment runs, which the control plane will refuse with
	// source_moved. It is on the plan so `--plan` surfaces the coming
	// refusal, rather than the operator meeting it on apply.
	//
	// Separate from Note, which is the invariant "promote ships nothing"
	// sentence every plan carries: folding a conditional warning into it
	// would make the one field mean two things, and a reader could not
	// tell a warning from boilerplate.
	SourceNote string `json:"source_note,omitempty"`
	// ok / exit_code / error: F0's one envelope, stamped from the SAME
	// error the command returns, so the document and the process status
	// cannot disagree. A backwards move is not a failure; a refusal is
	// (exit 3 or 4).
	jsonEnvelope
	// Recorded is the ledger entry the env now resolves to, after an
	// apply: the new entry, or — for a retry of the env's current state —
	// the existing one, unchanged. Nil under --plan and on a refusal.
	Recorded *release.Promotion `json:"recorded,omitempty"`
	// Expected is the compare-and-set the write asserts: the promotion id
	// it requires to be current, or "unbound". Present under --plan too —
	// it is what the real promote WOULD send — so a reviewer sees an
	// --expect-current that disagrees with current.promotion_id before the
	// write is refused for it.
	Expected string `json:"expected,omitempty"`
	// Refusal is set when the ledger DECLINED the write — applied is then
	// false and nothing was written. It names what was expected, what is
	// actually there, and (for rollout_in_flight) the phase that made it
	// in flight.
	Refusal *promoteRefusalJSON `json:"refusal,omitempty"`
	// DeployPlan is the §8.6 plan this deploy was judged against (O-13):
	// the control plane's PlanDeploy answer, or the one built locally for a
	// file-ledger env. Carried in --json so a pipeline reads the same
	// evidence a human reading the rendered plan does — in particular the
	// DIGEST, which is what a two-stage pipeline passes to --approve, and
	// the stop-class codes, which are what it passes to
	// --acknowledge-destructive.
	//
	// Nil when no plan could be computed: a never-built env, or a control
	// plane that predates bundles (F-15). A consumer must read nil as
	// "unavailable", never as "no changes" — the two are the distinction
	// F-20 exists to preserve.
	DeployPlan *release.Plan `json:"deploy_plan,omitempty"`
	// Capacity is the hosted capacity pre-flight's answer, nil for an env with
	// nothing hosted.
	Capacity *promotePlanCapacity `json:"capacity,omitempty"`
	// Queued is set when the control plane ACCEPTED and RECORDED the
	// promotion but holds it on a human action (billing): what it waits on,
	// why, and the action URL. The envelope's exit_code is then 7. Nil for
	// every deploy that is not queued.
	Queued *deployQueuedJSON `json:"queued,omitempty"`

	// targetSources is the source-built frontend snapshot the binding will be
	// written with, the non-container half of targetResolved. Carried on the
	// plan for the same reason the digests are: the value APPLIED must be the
	// value PREVIEWED, and resolving it a second time at write would let the
	// two drift. Without it a promotion records only images and a frontend
	// silently stays on whatever ref is in KCL — see release.Promotion.Sources.
	targetSources map[string]release.Source

	// targetResolved is the digest map the binding will be written with.
	// Unexported so it cannot leak into the JSON contract as a second,
	// redundant encoding of Images — and so applyPromotePlan writes
	// exactly the digests the plan reported, which is what makes the
	// preview and the write the same thing.
	targetResolved map[string]string
}

// ─── The git seam ────────────────────────────────────────────────────────────

// promoteGitReader reads commit facts out of a checkout. READ-ONLY: the two
// methods run `git cat-file -e` and `git log`, neither of which mutates a
// repository. That matters beyond hygiene — this code runs in shared
// checkouts, and a plan that touched the index would be a plan nobody could
// run.
//
// A seam rather than a direct call because every interesting case is a
// property of a repository's history: a commit absent from this checkout, a
// git that cannot run at all. Staging real repositories to imply those states
// makes the tests slow and the premises implicit; a fake states them.
// Declared at the consumer, per the package-boundary rule the rest of this
// package follows for clusterImageLister and bindingStore.
type promoteGitReader interface {
	// HasCommit reports whether commit resolves to a commit object in dir.
	// False means "not in this checkout", which is a reportable state, not
	// an error.
	HasCommit(ctx context.Context, dir, commit string) bool
	// CommitsBetween lists commits reachable from `to` but not `from`,
	// newest first, as "<short-sha> <subject>".
	CommitsBetween(ctx context.Context, dir, from, to string) ([]string, error)
	// IsAncestor reports whether commit a is an ancestor of (or equal to)
	// commit b. The error is "could not tell" — git unavailable, a commit
	// absent — which is distinct from a clean "no".
	IsAncestor(ctx context.Context, dir, a, b string) (bool, error)
}

// gitCommitReader is the production reader: plain read-only git invocations,
// scoped to the project directory rather than the process CWD so the answer
// does not depend on where forge was launched from.
type gitCommitReader struct{}

func (gitCommitReader) HasCommit(ctx context.Context, dir, commit string) bool {
	if commit == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, promoteGitTimeout)
	defer cancel()
	// `^{commit}` makes this reject a blob or tree that happens to share
	// the prefix: the question is whether this is a commit we can log from.
	cmd := exec.CommandContext(cctx, "git", "cat-file", "-e", commit+"^{commit}")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func (gitCommitReader) CommitsBetween(ctx context.Context, dir, from, to string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, promoteGitTimeout)
	defer cancel()
	// Merges are INCLUDED. A release range that hid merge commits would
	// under-count exactly the history that arrives by pull request, which
	// is most of it.
	cmd := exec.CommandContext(cctx, "git", "log", "--pretty=format:%h %s", from+".."+to)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

func (gitCommitReader) IsAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, promoteGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "merge-base", "--is-ancestor", a, b)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit 1 is the definite "not an ancestor"; anything else (128: bad
	// object, a git that cannot run) is "could not tell".
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// ─── Computation ─────────────────────────────────────────────────────────────

// promotePlanOptions carries the inputs and the injected seams.
type promotePlanOptions struct {
	// Env is the environment to bind; Version the release to bind it to.
	Env     string
	Version string
	// ProjectDir is the checkout the ledgers and the git history are read
	// from. Empty falls back to projectDirForKCL().
	ProjectDir string
	// Bindings is the promotion ledger. Nil falls back to the env's
	// store. Injected so a test can STATE the env's current binding
	// instead of staging a file to imply it.
	Bindings bindingStore
	// Releases is the release ledger the target is read from. Nil falls
	// back to the env's.
	Releases releaseLedger
	// Git reads the commit range. Nil uses real git.
	Git promoteGitReader
	// Scope is the deploy's scope (deployScopeTargets): put on the plan and
	// recorded on the promotion as its Targets. Nil for an unscoped deploy.
	Scope []string
	// Ledger is the env's resolved ledger, read only to refuse a Scope its
	// control plane would widen (refuseConvergedScope). The zero value
	// refuses nothing.
	Ledger envLedger
}

// computePromotePlan builds the whole change set WITHOUT writing anything.
//
// It returns an error only for conditions that make a promote impossible at
// all: an unreadable binding ledger, a target release that does not exist, a
// release carrying no resolvable digests. Everything else — a missing current
// ledger, a commit this checkout does not have, a dirty release, a backwards move —
// is a VALUE in the returned plan. That split is deliberate: the states a
// reviewer most needs described are exactly the ones an error would refuse to
// describe.
//
// Both --plan and a real promote call this. The real promote then calls
// applyPromotePlan with the result, so the write is defined by the same value
// that was rendered.
func computePromotePlan(ctx context.Context, opts promotePlanOptions) (promotePlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	projectDir := opts.ProjectDir
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}
	bindings, releaseStore := opts.Bindings, opts.Releases
	if bindings == nil || releaseStore == nil {
		l, err := ledgerFor(ctx, projectDir, opts.Env)
		if err != nil {
			return promotePlan{}, err
		}
		if bindings == nil {
			bindings = l.Bindings
		}
		if releaseStore == nil {
			releaseStore = l.Releases
		}
	}
	git := opts.Git
	if git == nil {
		git = gitCommitReader{}
	}
	// Newest-first, with the ordering env topology already established
	// (semver first, created_at as the tie-break so a skewed CI clock
	// cannot reorder history). Both backends return this ordering, so a
	// promote's DIRECTION has one answer whichever holds the ledger.
	releases, err := releaseStore.List(ctx)
	if err != nil {
		return promotePlan{}, fmt.Errorf("list releases from %s: %w", releaseStore.Location(), err)
	}
	byVersion := map[string]release.Release{}
	for _, rel := range releases {
		byVersion[rel.Version] = rel
	}

	plan := promotePlan{
		Env:          opts.Env,
		Kind:         release.KindPromote,
		Ledger:       bindings.Location(),
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Images:       []promoteImageChange{},
		Commits:      promoteCommitRange{Commits: []string{}},
		ShipsNothing: true,
		NextStep:     fmt.Sprintf("forge env deploy %s", opts.Env),
		Note: fmt.Sprintf("promote writes a POINTER and ships nothing — no image reaches %s until `forge env deploy %s` runs, "+
			"and `forge env status %s` proves it arrived", opts.Env, opts.Env, opts.Env),
	}
	plan.stamp(nil)

	// THE TARGET RELEASE IS REQUIRED, and its absence stays an error in
	// BOTH modes rather than becoming a plan state. Without the ledger
	// there are no digests to resolve, so there is no change set to
	// preview — and reporting it as a renderable plan under --plan while a
	// real promote errored would make the two modes' exit codes disagree,
	// which is the one property this design is holding onto.
	target, ok := byVersion[opts.Version]
	if !ok {
		// Fall back to the direct read: a list is bounded, and a release
		// older than the page is still a legitimate promote target.
		rel, err := releaseStore.Get(ctx, opts.Version)
		if err != nil {
			return promotePlan{}, fmt.Errorf("read release %q: %w", opts.Version, err)
		}
		if rel == nil {
			// The fix is the verb that BUILDS it (O-15), never
			// `--release <v> --no-build`: a version nobody cut has no
			// pushed digests in .forge/state either, so a cut-only
			// re-run would record a release with no images in it.
			return promotePlan{}, errUnknownReleaseForDeploy(opts.Env, opts.Version, releases)
		}
		target = *rel
	}

	resolved, err := resolveReleaseDigests(target)
	if err != nil {
		return promotePlan{}, err
	}
	plan.targetResolved = resolved
	plan.targetSources = target.Sources()
	plan.Target = promotePlanTarget{
		Release:   opts.Version,
		CreatedAt: formatLedgerTime(target.CreatedAt),
		Images:    len(resolved),
	}
	targetGit := target.Git
	plan.Target.Git = &targetGit

	prev, hadPrev, err := bindings.Current(ctx, opts.Env)
	if err != nil {
		return promotePlan{}, fmt.Errorf("read the promotion ledger for %s: %w", opts.Env, err)
	}
	plan.Current = promotePlanBinding{Bound: hadPrev}
	var currentRel release.Release
	var currentKnown bool
	if hadPrev {
		plan.Current.PromotionID = prev.ID
		plan.Current.Release = prev.Release
		plan.Current.Targets = prev.Targets
		plan.Current.PromotedAt = formatLedgerTime(prev.PromotedAt)
		if rel, found := byVersion[prev.Release]; found {
			currentRel, currentKnown = rel, true
			plan.Current.ReleaseKnown = true
			g := rel.Git
			plan.Current.Git = &g
			plan.Current.ReleaseCreatedAt = formatLedgerTime(rel.CreatedAt)
		} else {
			plan.Current.Note = fmt.Sprintf(
				"release %s is not in %s — its provenance and the commit range are unknown",
				prev.Release, releaseStore.Location())
		}
	} else {
		plan.Current.Note = fmt.Sprintf("never promoted — no release is bound to %s, so this is its first promote", opts.Env)
	}

	// The image baseline is what is RUNNING, as far as the ledger can say:
	// the binding, unless the binding's own apply is recorded as failed —
	// then the newest earlier promotion that did apply.
	baseline := prev.Resolved
	if hadPrev {
		if applied := applyOf(projectDir, opts.Env, prev); applied.Failed {
			plan.Current.NotApplied = true
			plan.Current.ApplyError = applied.Summary
			if live, ok := lastAppliedPromotion(ctx, bindings, projectDir, opts.Env, prev.ID); ok {
				plan.Current.LiveRelease = live.Release
				baseline = live.Resolved
			} else {
				baseline = nil
			}
		}
	}
	plan.Images = classifyPromoteImages(baseline, resolved)
	plan.Tally = tallyPromoteImages(plan.Images)
	plan.Changed = plan.Tally.Changed > 0 || plan.Tally.Added > 0 || plan.Tally.Removed > 0 ||
		!hadPrev || prev.Release != opts.Version || plan.Current.NotApplied

	ancestry := promoteAncestryUnknown
	if hadPrev && currentKnown {
		ancestry = promoteAncestryOf(ctx, git, projectDir, currentRel, target)
	}
	plan.Direction, plan.ReleasesBetween, plan.DirectionDetail =
		promoteDirectionFor(releases, ancestry, hadPrev, prev.Release, opts.Version)
	if plan.Current.NotApplied {
		live := "an earlier release the ledger cannot name"
		if plan.Current.LiveRelease != "" {
			live = plan.Current.LiveRelease
		}
		plan.DirectionDetail += fmt.Sprintf(" — but %s's apply FAILED, so %s still runs %s; the images below are live → target",
			prev.Release, opts.Env, live)
	}

	plan.Commits = computePromoteCommitRange(ctx, git, projectDir, promoteRangeInput{
		HadPrev:      hadPrev,
		CurrentRel:   currentRel,
		CurrentKnown: currentKnown,
		CurrentName:  prev.Release,
		Target:       target,
		TargetName:   opts.Version,
		Reverts:      plan.Direction == promoteDirectionBehind,
	})

	return scopePromotePlan(plan, opts.Scope, opts.Ledger)
}

// applyPromotePlan writes the binding the plan describes.
//
// It takes the plan rather than a version and a digest map so the write cannot
// drift from the preview: the digests applied here are the same map
// computePromotePlan classified the Images from. PromotedAt is stamped now,
// which is the one field a plan cannot predict — it is the time of the WRITE,
// and a plan that pre-stamped it would be claiming a promote happened at the
// moment it was previewed.
//
// It APPENDS. The ledger decides (release.Decide) whether the entry is a real
// move, a retry of the state the env is already in (nothing is written, and
// the existing entry comes back). Applied reports whether a NEW entry was written.
//
// It ALWAYS compare-and-sets: w.Guard is the promotion the plan read (or the
// caller's --expect-current), and the ledger refuses the write if the env has
// moved since. A refusal is left on the plan (Refusal, applied false) AND
// returned, so the document and the exit code describe the same outcome.
func applyPromotePlan(ctx context.Context, bindings bindingStore, plan *promotePlan, w promoteWrite) error {
	p := release.Promotion{
		Env:        plan.Env,
		Release:    plan.Target.Release,
		Kind:       plan.Kind,
		Resolved:   plan.targetResolved,
		Sources:    plan.targetSources,
		Targets:    plan.Scope,
		PromotedBy: w.By,
		Note:       w.Note,
		Gates:      w.Gates,
		Run:        w.Run,
		// The source of a `promote --from`. The hosted store turns
		// FromEnv into an environment id and sends the promotion id
		// beside it; the server resolves and re-checks both.
		FromEnv:         w.FromEnv,
		FromPromotionID: w.FromPromotionID,
		// O-13: what this promote was approved against.
		PlanDigest:           w.PlanDigest,
		AcknowledgedFindings: w.AcknowledgedFindings,
	}
	guard := w.Guard
	guard.ResolveVersionFromSource = w.VersionFromSource
	got, err := bindings.Append(ctx, p, guard)
	if err != nil {
		if plan.applyRefusal(err) {
			// Not wrapped: the refusal's own message names the env, what
			// was expected and what is there, and the location adds
			// nothing a reader acts on.
			return err
		}
		return fmt.Errorf("record %s of %s → %s in %s: %w", plan.Kind, plan.Env, plan.Target.Release, bindings.Location(), err)
	}
	plan.Recorded = &got
	plan.Applied = true
	return nil
}

// promoteWrite is everything a promote WRITE carries beyond the plan: who,
// why, the compare-and-set, and the provenance the optional flags add.
type promoteWrite struct {
	By    release.Actor
	Note  string
	Guard appendGuard
	// Gates is the pre-promote evidence (--gate, F4).
	Gates []release.Gate
	// Run is the CI run performing the promote (--run-id / CI default).
	Run release.Run
	// FromEnv and FromPromotionID are the resolved --from (F5): the source
	// environment, and the promotion this promote's decision was made
	// against. The id is the half with teeth — the server resolves the
	// version from it and refuses source_moved if the source has moved
	// past it — while FromEnv alone is unverified provenance.
	FromEnv         string
	FromPromotionID string
	// PlanDigest is the §8.6 deploy plan this write was approved against,
	// and AcknowledgedFindings the stop-class codes the caller named (O-13).
	// Both are sent to the control plane, which RECOMPUTES the plan under
	// the env row lock and refuses the write unless the digest matches and
	// every stop finding is acknowledged. Empty on a file-ledger env, which
	// has no server to recompute anything.
	PlanDigest           string
	AcknowledgedFindings []string
	// VersionFromSource says the plan's release was PREVIEWED from the
	// source environment, not named by the caller, so the write must send
	// no version and let the server resolve it from FromPromotionID
	// (§3.4). See promoteSource.VersionFromSource.
	VersionFromSource bool
}

// promotedBySuffix renders ", by alice" / ", by ci" for a refusal's actual
// promotion — the "who moved it" a human acts on.
func promotedBySuffix(a release.Actor) string {
	switch {
	case a.User != "" && a.Actor != "":
		return ", by " + a.User + " via " + a.Actor
	case a.User != "":
		return ", by " + a.User
	case a.Actor != "":
		return ", by " + a.Actor
	default:
		return ""
	}
}

// formatLedgerTime renders a ledger timestamp for the plan and topology
// documents, whose JSON contract is RFC3339 strings. The zero time is "".
func formatLedgerTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// classifyPromoteImages diffs the two digest maps into an explicit
// classification per image, over the UNION of both sides.
//
// The union is the whole point. Iterating only the target's images would make
// a REMOVED image vanish from the report, and iterating only the current
// binding's would hide an ADDED one — and a promote between release lines
// with different image sets (control-plane's prod carries internal-console,
// its staging line does not) is exactly when the set changes rather than the
// digests.
func classifyPromoteImages(current, target map[string]string) []promoteImageChange {
	names := make([]string, 0, len(current)+len(target))
	for name := range current {
		names = append(names, name)
	}
	for name := range target {
		if _, both := current[name]; !both {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := make([]promoteImageChange, 0, len(names))
	for _, name := range names {
		cur, inCur := current[name]
		tgt, inTgt := target[name]
		row := promoteImageChange{Image: name, CurrentDigest: cur, TargetDigest: tgt}
		switch {
		case inCur && inTgt && cur == tgt:
			row.Change = promoteImageUnchanged
		case inCur && inTgt:
			row.Change = promoteImageChanged
		case inTgt:
			row.Change = promoteImageAdded
		default:
			row.Change = promoteImageRemoved
		}
		out = append(out, row)
	}
	return out
}

// tallyPromoteImages counts the classifications.
func tallyPromoteImages(images []promoteImageChange) promoteImageTally {
	var t promoteImageTally
	for _, img := range images {
		switch img.Change {
		case promoteImageUnchanged:
			t.Unchanged++
		case promoteImageChanged:
			t.Changed++
		case promoteImageAdded:
			t.Added++
		case promoteImageRemoved:
			t.Removed++
		}
	}
	return t
}

// promoteAncestry is what the two releases' commits say about which is newer.
type promoteAncestry int

const (
	// promoteAncestryUnknown: the commits cannot order the releases — a
	// dirty or missing commit, a commit absent from this checkout, a git
	// that cannot run, the same commit on both, or diverged histories.
	promoteAncestryUnknown promoteAncestry = iota
	// promoteAncestryForward: the current release's commit is an ancestor
	// of the target's, so the target is the newer source.
	promoteAncestryForward
	// promoteAncestryBackward: the target's commit is an ancestor of the
	// current release's.
	promoteAncestryBackward
)

// promoteAncestryOf asks git which of two releases' commits is the ancestor.
//
// Ancestry is the ground truth for "is this a rollback": it is what the commit
// range printed beneath the direction line is computed from, so the two can
// never disagree. It abstains (Unknown) whenever it would be guessing — a dirty
// endpoint's recorded commit is real but does not describe the bytes that
// shipped, and a commit this checkout does not have cannot be placed.
func promoteAncestryOf(ctx context.Context, git promoteGitReader, dir string, current, target release.Release) promoteAncestry {
	cur, tgt := current.Git.Commit, target.Git.Commit
	if cur == "" || tgt == "" || cur == tgt || current.Git.Dirty || target.Git.Dirty {
		return promoteAncestryUnknown
	}
	if !git.HasCommit(ctx, dir, cur) || !git.HasCommit(ctx, dir, tgt) {
		return promoteAncestryUnknown
	}
	curIsAncestor, err := git.IsAncestor(ctx, dir, cur, tgt)
	if err != nil {
		return promoteAncestryUnknown
	}
	tgtIsAncestor, err := git.IsAncestor(ctx, dir, tgt, cur)
	if err != nil {
		return promoteAncestryUnknown
	}
	switch {
	case curIsAncestor && !tgtIsAncestor:
		return promoteAncestryForward
	case tgtIsAncestor && !curIsAncestor:
		return promoteAncestryBackward
	}
	return promoteAncestryUnknown
}

// promoteDirectionFor places the target relative to the current release.
//
// TWO SOURCES, IN ORDER: commit ancestry, then the ledger's cut-time order.
// Ancestry decides when git can say — it is what the commit range is read
// from, so the direction and the range cannot disagree. When it cannot (a
// dirty cut, a commit this checkout lacks, diverged histories) the release
// list's own newest-first order decides: that order is by cut time
// (sortReleasesNewestFirst), never by comparing version labels, which do not
// share one scheme — v1.7.40 and 20261014.042859-cd678a0d6531 have no
// meaningful string or semver order, and treating them as if they did is what
// reported a forward move as "67 releases OLDER".
//
// releases is the newest-first list; comparing INDEXES supplies the order and
// the "N releases" count. A smaller index is a newer release.
func promoteDirectionFor(releases []release.Release, ancestry promoteAncestry, hadPrev bool, currentVersion, targetVersion string) (promoteDirection, int, string) {
	if !hadPrev {
		return promoteDirectionInitial, 0,
			"FIRST PROMOTE — this environment has never been bound to a release, so there is nothing to move from"
	}
	if currentVersion == targetVersion {
		return promoteDirectionSame, 0, fmt.Sprintf(
			"NO MOVE — already bound to %s; re-promoting re-resolves the digests from the release ledger", targetVersion)
	}
	curIdx, tgtIdx := -1, -1
	for i, rel := range releases {
		if rel.Version == currentVersion {
			curIdx = i
		}
		if rel.Version == targetVersion {
			tgtIdx = i
		}
	}
	known := curIdx >= 0 && tgtIdx >= 0

	var ahead bool
	switch {
	case ancestry == promoteAncestryForward:
		ahead = true
	case ancestry == promoteAncestryBackward:
		ahead = false
	case known:
		ahead = tgtIdx < curIdx
	default:
		// Neither git nor the ledger can place them. UNKNOWN rather than a
		// guess: a direction invented here is the one number on the screen
		// that would be believed without checking.
		missing := currentVersion
		if tgtIdx < 0 {
			missing = targetVersion
		}
		return promoteDirectionUnknown, 0, fmt.Sprintf(
			"DIRECTION UNKNOWN — release %s has no ledger in this checkout, so forge cannot tell whether %s is newer or older than %s",
			missing, targetVersion, currentVersion)
	}

	// The count is the ledger's, so it is only quoted when it points the
	// same way as the verdict. Ancestry and cut time can disagree (a
	// release cut from an older branch after a newer one); then the verdict
	// stands and the count is omitted rather than contradicting it.
	between := 0
	if known && (tgtIdx < curIdx) == ahead {
		between = curIdx - tgtIdx
		if between < 0 {
			between = -between
		}
	}
	basis := "by commit ancestry"
	if ancestry == promoteAncestryUnknown {
		basis = "by cut time"
	}
	if ahead {
		how := "a newer release"
		if between > 0 {
			how = fmt.Sprintf("%d release(s) NEWER", between)
		}
		return promoteDirectionAhead, between, fmt.Sprintf(
			"FORWARD — %s is %s than %s (%s)", targetVersion, how, currentVersion, basis)
	}
	how := "an older release"
	if between > 0 {
		how = fmt.Sprintf("%d release(s) OLDER", between)
	}
	return promoteDirectionBehind, between, fmt.Sprintf(
		"BEHIND — %s is %s than %s (%s). This moves the environment BACKWARDS; it undoes nothing %s did",
		targetVersion, how, currentVersion, basis, currentVersion)
}

// promoteRangeInput is what the commit-range computation needs, gathered so
// the function reads as a classification rather than a parameter list.
type promoteRangeInput struct {
	HadPrev      bool
	CurrentRel   release.Release
	CurrentKnown bool
	CurrentName  string
	Target       release.Release
	TargetName   string
	Reverts      bool
}

// computePromoteCommitRange derives the source change between the two
// releases, classifying every way it can be unavailable instead of failing.
//
// ORDER MATTERS HERE. Dirty is checked BEFORE the commits are looked up,
// because a dirty release's recorded commit is a real commit that does not
// describe the bytes that shipped — so the range would compute successfully
// and be wrong, which is the worst available outcome. A plainly-labelled
// "meaningless" beats a plausible number nobody can check.
func computePromoteCommitRange(ctx context.Context, git promoteGitReader, dir string, in promoteRangeInput) promoteCommitRange {
	out := promoteCommitRange{Commits: []string{}, Reverts: in.Reverts}

	if !in.HadPrev {
		out.State = promoteRangeFirstPromote
		out.ToCommit = in.Target.Git.Commit
		out.Detail = fmt.Sprintf("no commit range — this is the first promote of this environment, so there is no previous release to compare %s against", in.TargetName)
		return out
	}
	if in.CurrentName == in.TargetName {
		out.State = promoteRangeSameRelease
		out.FromCommit = in.Target.Git.Commit
		out.ToCommit = in.Target.Git.Commit
		out.Detail = fmt.Sprintf("no commit range — %s is already bound to %s", in.CurrentName, in.TargetName)
		return out
	}
	if !in.CurrentKnown {
		out.State = promoteRangeLedgerMissing
		out.ToCommit = in.Target.Git.Commit
		out.Detail = fmt.Sprintf("no commit range — release %s has no ledger in this checkout, so its commit is unknown (it was cut on another branch)", in.CurrentName)
		return out
	}

	out.FromCommit = in.CurrentRel.Git.Commit
	out.ToCommit = in.Target.Git.Commit

	// DIRTY FIRST. See the function comment: a dirty endpoint's commit is
	// real but does not describe what shipped, so any range computed from
	// it is confidently wrong.
	switch {
	case in.Target.Git.Dirty && in.CurrentRel.Git.Dirty:
		out.State = promoteRangeDirtyRelease
		out.Detail = fmt.Sprintf("commit range is MEANINGLESS — both %s and %s were cut from DIRTY working trees, so neither endpoint's bytes correspond to a reviewable commit", in.CurrentName, in.TargetName)
		return out
	case in.Target.Git.Dirty:
		out.State = promoteRangeDirtyRelease
		out.Detail = fmt.Sprintf("commit range is MEANINGLESS — target release %s was cut from a DIRTY working tree, so its bytes correspond to no reviewable commit (recorded commit %s does not describe them)", in.TargetName, shortSHA(in.Target.Git.Commit))
		return out
	case in.CurrentRel.Git.Dirty:
		out.State = promoteRangeDirtyRelease
		out.Detail = fmt.Sprintf("commit range is MEANINGLESS — the currently bound release %s was cut from a DIRTY working tree, so there is no reviewable commit to measure from", in.CurrentName)
		return out
	}

	if out.FromCommit == "" || out.ToCommit == "" {
		out.State = promoteRangeNoCommit
		which := in.CurrentName
		if out.ToCommit == "" {
			which = in.TargetName
		}
		out.Detail = fmt.Sprintf("no commit range — release %s's ledger records no git commit (cut from a non-git tree, or by a forge too old to record provenance)", which)
		return out
	}

	// A COMMIT ABSENT FROM THIS CHECKOUT IS NOT A FAILURE. It means the
	// release was cut on a branch this working copy does not have, which is
	// routine when promoting from a machine that only tracks main. Report
	// it and let the rest of the plan stand — the digests, the image set
	// and the direction are all still exactly right.
	missing := make([]string, 0, 2)
	if !git.HasCommit(ctx, dir, out.FromCommit) {
		missing = append(missing, fmt.Sprintf("%s (%s)", shortSHA(out.FromCommit), in.CurrentName))
	}
	if !git.HasCommit(ctx, dir, out.ToCommit) {
		missing = append(missing, fmt.Sprintf("%s (%s)", shortSHA(out.ToCommit), in.TargetName))
	}
	if len(missing) > 0 {
		out.State = promoteRangeCommitNotFound
		out.Detail = fmt.Sprintf("commit range unavailable — %s not present in this checkout; fetch the branch the release was cut from to see the range",
			strings.Join(missing, " and "))
		return out
	}

	// History runs oldest → newest. For a BEHIND move that is target →
	// current, and the commits listed are the ones being taken AWAY.
	from, to := out.FromCommit, out.ToCommit
	if in.Reverts {
		from, to = out.ToCommit, out.FromCommit
	}
	commits, err := git.CommitsBetween(ctx, dir, from, to)
	if err != nil {
		out.State = promoteRangeGitUnavailable
		out.Detail = fmt.Sprintf("commit range unavailable — git could not be read in %s: %v", dir, err)
		return out
	}
	out.State = promoteRangeComputed
	out.Count = len(commits)
	if len(commits) > promoteCommitRangeLimit {
		out.Commits = append(out.Commits, commits[:promoteCommitRangeLimit]...)
		out.Truncated = true
	} else {
		out.Commits = append(out.Commits, commits...)
	}
	return out
}

// ─── Rendering ───────────────────────────────────────────────────────────────

// renderPromotePlanText prints the human report.
//
// The same value drives this and the JSON, and the same value drove (or did
// not drive) the write — so the heading states which of those happened before
// anything else, rather than leaving a reader to infer it from the absence of
// a success line.
// renderPromotePlanHeadline prints the plan's first line: what happened, or
// what would.
func renderPromotePlanHeadline(out io.Writer, plan promotePlan) {
	switch {
	case plan.Refusal != nil:
		// The plan below is what the write WOULD have done. Said first,
		// because a refused promote that read like a success is the stomp
		// this whole path exists to prevent.
		fmt.Fprintf(out, "REFUSED (%s — nothing was written): promote env %q → release %s\n",
			plan.Refusal.Reason, plan.Env, plan.Target.Release)
	case plan.Applied && plan.FollowError != "" && plan.shippedAnything():
		// The deploy failed AFTER part of it went live. Saying "nothing
		// shipped" here is the claim an operator acts on — they re-run, or
		// they roll back a frontend they believe never left — so the stages
		// that did ship are listed before the error.
		fmt.Fprintf(out, "RECORDED AND PARTLY SHIPPED: env %q is now bound to release %s, and the deploy FAILED after part of it reached a live target — %s is running only where listed here.\n",
			plan.Env, plan.Target.Release, plan.Target.Release)
		renderShippedStages(out, plan)
		fmt.Fprintf(out, "  error     %s\n", oneLine(plan.FollowError, 400))
		fmt.Fprintf(out, "  Re-plan and re-approve: `forge env deploy %s %s --plan-only`, then deploy with the --approve digest it prints.\n",
			plan.Env, plan.Target.Release)
	case plan.Applied && plan.FollowError != "":
		fmt.Fprintf(out, "RECORDED BUT NOT APPLIED: env %q is now bound to release %s in the ledger, but the deploy FAILED before this command shipped anything to a live target.\n",
			plan.Env, plan.Target.Release)
		fmt.Fprintf(out, "  apply error: %s\n", oneLine(plan.FollowError, 400))
		// NOT "re-run the same command": the binding moved, so the plan
		// (and its digest) moved with it, and an --approve of the old one
		// is refused as plan_stale. A fresh plan also shows the image diff
		// from what is actually running, which the old one could not.
		fmt.Fprintf(out, "  Re-plan and re-approve: `forge env deploy %s %s --plan-only`, then deploy with the --approve digest it prints.\n",
			plan.Env, plan.Target.Release)
	case plan.Applied:
		if plan.Current.Bound && plan.Current.Release != plan.Target.Release {
			fmt.Fprintf(out, "Promoted env %q: %s → %s\n", plan.Env, plan.Current.Release, plan.Target.Release)
		} else {
			fmt.Fprintf(out, "Promoted env %q → release %s\n", plan.Env, plan.Target.Release)
		}
	default:
		fmt.Fprintf(out, "PLAN (dry run — nothing was written): promote env %q → release %s\n", plan.Env, plan.Target.Release)
	}
}

func renderPromotePlanText(out io.Writer, plan promotePlan) {
	renderPromotePlanHeadline(out, plan)

	if plan.Current.Bound {
		fmt.Fprintf(out, "  current   %s (promoted %s, promotion %s)\n", plan.Current.Release, plan.Current.PromotedAt, plan.Current.PromotionID)
		if len(plan.Current.Targets) > 0 {
			fmt.Fprintf(out, "            scoped: only %s moved to %s\n", strings.Join(plan.Current.Targets, ", "), plan.Current.Release)
		}
		if plan.Current.NotApplied {
			live := "(the ledger cannot say)"
			if plan.Current.LiveRelease != "" {
				live = plan.Current.LiveRelease
			}
			fmt.Fprintf(out, "  NOT APPLIED  %s is bound but its apply FAILED (%s); live runs %s\n",
				plan.Current.Release, oneLine(plan.Current.ApplyError, 200), live)
		}
	} else {
		fmt.Fprintf(out, "  current   (never promoted)\n")
	}
	if plan.Expected != "" {
		fmt.Fprintf(out, "  expected  %s  (compare-and-set: the write is refused if the env moved since)\n", plan.Expected)
	}
	if r := plan.Refusal; r != nil && r.ActualCurrent != nil {
		fmt.Fprintf(out, "  actual    %s (promotion %s, promoted %s%s)\n", r.ActualCurrent.Release, r.ActualCurrent.ID,
			formatLedgerTime(r.ActualCurrent.PromotedAt), promotedBySuffix(r.ActualCurrent.PromotedBy))
	}
	if r := plan.Refusal; r != nil && r.ActualPhase != "" {
		fmt.Fprintf(out, "  rollout   %s\n", r.ActualPhase)
	}
	fmt.Fprintf(out, "  target    %s", plan.Target.Release)
	if plan.Target.CreatedAt != "" {
		fmt.Fprintf(out, " (cut %s)", plan.Target.CreatedAt)
	}
	if plan.Target.Git != nil && plan.Target.Git.Dirty {
		fmt.Fprintf(out, "  [DIRTY TREE — these bytes match no reviewable commit]")
	}
	fmt.Fprintln(out)
	if len(plan.Scope) > 0 {
		fmt.Fprintf(out, "  scope     %s  (a scoped deploy: only these move, and the promotion records it)\n", strings.Join(plan.Scope, ", "))
	}
	// The direction line is printed with the state's own word in it, not
	// as a symbol: a reader skimming for "am I going forwards" must not
	// have to decode an arrow.
	fmt.Fprintf(out, "  direction %s  %s\n", plan.Direction, plan.DirectionDetail)
	if plan.Direction == promoteDirectionBehind {
		// A backwards promote is not a rollback and cannot be one: the
		// newer release's migrations stay applied and the rows it wrote stay
		// written. Said on every BEHIND plan, applied or not, because the
		// operator reaching for this is usually mid-incident.
		fmt.Fprintf(out, "  WARNING   %s moves BACKWARDS. The schema and data %s left behind stay; %s's code will run against them.\n",
			plan.Env, plan.Current.Release, plan.Target.Release)
		fmt.Fprintf(out, "            Recovery is roll forward: prefer cutting a release with the fix and promoting that.\n")
	}
	if plan.Current.Note != "" {
		fmt.Fprintf(out, "  note      %s\n", plan.Current.Note)
	}
	if plan.SourceNote != "" {
		// WARNING, not note: this one says the promote is going to be
		// refused, which a reader skimming "note" lines would skip.
		fmt.Fprintf(out, "  WARNING   %s\n", plan.SourceNote)
	}
	renderConvergesSection(out, plan.Converges)
	renderFrontendPreflightSection(out, plan.FrontendPreflight)

	renderCapacitySection(out, plan.Capacity)

	fmt.Fprintln(out)
	from := ""
	if plan.Current.NotApplied {
		// Said in the header, because the default reading of this block is
		// "binding → target", and here that would understate the change.
		from = fmt.Sprintf("live %s → target ", emptyAs(plan.Current.LiveRelease, "(unknown)"))
	}
	fmt.Fprintf(out, "Images %s(%d unchanged, %d changed, %d added, %d removed)\n", from,
		plan.Tally.Unchanged, plan.Tally.Changed, plan.Tally.Added, plan.Tally.Removed)
	for _, img := range plan.Images {
		fmt.Fprintf(out, "  %-10s %-22s", img.Change, img.Image)
		switch img.Change {
		case promoteImageChanged:
			// Both digests, on their own labelled lines. This is the
			// case the reviewer came for and the values get copied
			// out; a single-line diff makes that harder for no gain.
			fmt.Fprintln(out)
			fmt.Fprintf(out, "      from  %s\n", img.CurrentDigest)
			fmt.Fprintf(out, "      to    %s\n", img.TargetDigest)
		case promoteImageRemoved:
			fmt.Fprintf(out, " %s\n", img.CurrentDigest)
			fmt.Fprintf(out, "      no longer declared by %s — the next deploy will not pin it\n", plan.Target.Release)
		default:
			fmt.Fprintf(out, " %s\n", img.TargetDigest)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Commits  %s\n", plan.Commits.State)
	switch plan.Commits.State {
	case promoteRangeComputed:
		verb := "arriving in"
		if plan.Commits.Reverts {
			verb = "REVERTED OUT OF"
		}
		fmt.Fprintf(out, "  %d commit(s) %s %s (%s..%s)\n",
			plan.Commits.Count, verb, plan.Env, shortSHA(plan.Commits.FromCommit), shortSHA(plan.Commits.ToCommit))
		for _, line := range plan.Commits.Commits {
			fmt.Fprintf(out, "    %s\n", line)
		}
		if plan.Commits.Truncated {
			fmt.Fprintf(out, "    … %d more (showing the newest %d)\n",
				plan.Commits.Count-len(plan.Commits.Commits), len(plan.Commits.Commits))
		}
	default:
		fmt.Fprintf(out, "  %s\n", plan.Commits.Detail)
	}

	fmt.Fprintln(out)
	renderPromotePlanFooter(out, plan)
}

// renderPromotePlanFooter prints where the binding lives and what reached a
// live target — stated on every invocation, applied or not. Promote's own
// effect is a pointer move, and the gap between "promoted" and "running" is
// the thing `forge env status` had to be written to expose.
func renderPromotePlanFooter(out io.Writer, plan promotePlan) {
	fmt.Fprintf(out, "  Binding:  %s\n", plan.Ledger)
	switch {
	case plan.Refusal != nil:
		fmt.Fprintf(out, "  NOTHING WRITTEN: the ledger refused this promote (%s). %s\n", plan.Refusal.Reason, refusalHint(plan.Refusal.Reason))
	case plan.Applied && plan.followed:
		// The deploy verb's follow-through ran, so the footer reports what
		// it shipped — not "ships nothing until the deploy below", which
		// after an apply is false, and whose suggested command would build
		// and cut a NEW release (O-15).
		switch {
		case plan.FollowError != "" && plan.shippedAnything():
			// Listed in the headline, beside the error.
		case plan.shippedAnything():
			renderShippedStages(out, plan)
		case plan.FollowError != "":
			fmt.Fprintf(out, "  Shipped:  nothing — the deploy failed before reaching a live target.\n")
		default:
			fmt.Fprintf(out, "  Shipped:  nothing from this machine — %s's control plane applies it.\n", plan.Env)
		}
	case plan.Applied:
		fmt.Fprintf(out, "  SHIPS NOTHING: the binding moved; no image reaches %s until you run the deploy below.\n", plan.Env)
	default:
		fmt.Fprintf(out, "  NOTHING WRITTEN: re-run without --plan to record this binding. Even then, no image ships until the deploy below.\n")
	}
	if !plan.followed {
		fmt.Fprintf(out, "  Deploy:   forge env deploy %s\n", plan.Env)
	}
	fmt.Fprintf(out, "  Verify:   forge env status %s\n", plan.Env)
}

// renderFrontendPreflightSection names each frontend preflight finding as its
// own block, separate from the release change set above it: the plan is whole
// and correct, and these are the reasons the frontend half of a real deploy
// from THIS machine would be refused.
func renderFrontendPreflightSection(out io.Writer, findings []frontendPreflightFinding) {
	for _, f := range findings {
		fmt.Fprintf(out, "  FRONTEND PREFLIGHT  %s — not fatal to this plan; a real deploy from this machine is refused for it (--skip-frontend ships everything else)\n", f.Check)
		for _, line := range strings.Split(f.Detail, "\n") {
			fmt.Fprintf(out, "      %s\n", strings.TrimSpace(line))
		}
	}
}

// renderShippedStages lists what the deploy's follow-through shipped, one stage
// per line, and the stages that failed part-way.
func renderShippedStages(out io.Writer, plan promotePlan) {
	for _, s := range plan.Shipped {
		fmt.Fprintf(out, "  shipped   %s\n", s)
	}
	for _, s := range plan.PartlyShipped {
		fmt.Fprintf(out, "  PARTLY    %s — failed part-way; some of it may be live\n", s)
	}
}

// shippedAnything reports whether the follow-through reached a live target.
func (p promotePlan) shippedAnything() bool {
	return len(p.Shipped) > 0 || len(p.PartlyShipped) > 0
}

// recordFollow folds the deploy's follow-through into the plan: what it
// shipped and the error it ended on. ShipsNothing turns false the moment a
// stage reached a live target, because from then on it is not true.
func (p *promotePlan) recordFollow(shipped *shipLog, err error) {
	p.followed = true
	p.Shipped, p.PartlyShipped = shipped.Shipped(), shipped.Partial()
	if p.shippedAnything() {
		p.ShipsNothing = false
	}
	if err != nil {
		p.FollowError = err.Error()
	}
}
