package cli

// GetLiveView (doc §6.3) and GetDrift: the Live view of a project's
// environments in ONE round trip.
//
// One RPC rather than a read per env: the view is always read whole, and N+1
// reads would make "what is running" cost a round trip per environment while
// giving a reader no way to see a consistent moment.
//
// WHAT EACH POINTER MEANS, because they are easy to conflate:
//
//   - CurrentPromotion / CurrentRelease — where the IMAGES come from.
//   - CurrentBundle — where the CONFIG comes from: the newest bundle with a
//     succeeded apply, or on a hosted env the row's applied_bundle_id. Two
//     pointers because images and config move independently.
//   - LatestApply may be NEWER than CurrentBundle and still running or
//     failed, which is exactly the state worth seeing: it is the deploy
//     somebody is currently watching.
//   - Environment.DeclaredShape is the DECLARATION — what the checked-in
//     config says — and is for display only. A plan never diffs against it:
//     `forge env build` refreshes it from the very render being deployed, so
//     diffing against it would hide every change (§7).
//
// EMPTY IS NOT UNKNOWN. A missing env list is an empty list, never an error,
// and `drift.state = unknown` is kept distinct from `in_sync`: a self-managed
// cluster has no server-side observer, so "we cannot see it" is the normal
// answer there and must never be reported as agreement.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

const (
	procGetLiveView = "controlplane.v1.DeployService/GetLiveView"
	procGetDrift    = "controlplane.v1.DeployService/GetDrift"
)

// Drift states, as controlplane.v1.DeployDrift.state spells them.
const (
	driftInSync  = "in_sync"
	driftDrifted = "drifted"
	driftUnknown = "unknown"
)

// Per-object drift states (DeployDriftObject.state). `missing` and
// `unobservable` are separate on purpose: an object that is GONE is a
// finding, and an object nobody could look at is an absence of information.
const (
	driftObjectInSync       = "in_sync"
	driftObjectDrifted      = "drifted"
	driftObjectMissing      = "missing"
	driftObjectUnobservable = "unobservable"
)

// ─── Wire shapes ─────────────────────────────────────────────────────────────

// wireLiveEnvironment is the DeployEnvironment fields the Live view reads,
// including F-DECL's declaration half (tags 20–22).
//
// This is a WIDER read than deploytarget.wireEnvironment, which is the
// deploy target's view. Kept separate rather than widening that one: the two
// are consumed by different packages for different questions, and a single
// struct serving both would make every Live field look like something the
// deploy path depends on.
type wireLiveEnvironment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Project   string `json:"project,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// DeclaredShape is a google.protobuf.Struct holding release.Shape's
	// canonical JSON (snake_case keys), kept raw for the reason
	// shapeFromWire explains.
	DeclaredShape json.RawMessage `json:"declaredShape,omitempty"`
	DeclaredBy    *wireProvenance `json:"declaredBy,omitempty"`
	// DeclaredAt is the staleness signal: a shape from six weeks ago is
	// still a shape, but a plan computed against it is worth less than one
	// computed against this morning's.
	DeclaredAt          *time.Time `json:"declaredAt,omitempty"`
	ConvergesPromotions bool       `json:"convergesPromotions,omitempty"`
	ExpiresAt           *time.Time `json:"expiresAt,omitempty"`
	SourceRef           string     `json:"sourceRef,omitempty"`
}

// wireLocalSession is controlplane.v1.DeployLocalSession: PRESENCE, not
// ledger. It names no promotion and no release, is mutable (heartbeats),
// is garbage-collected, and nothing authorizes or bills on it.
type wireLocalSession struct {
	ID            string          `json:"id"`
	EnvironmentID string          `json:"environmentId,omitempty"`
	Worktree      *wireWorktree   `json:"worktree,omitempty"`
	Provenance    *wireProvenance `json:"provenance,omitempty"`
	// BundleDigest names the LOCAL bundle by digest rather than id: a local
	// render is never recorded as a bundle row, so there is no id to name.
	BundleDigest string    `json:"bundleDigest,omitempty"`
	StartedAt    time.Time `json:"startedAt"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	// StoppedAt is set when the client reported `state = stopped`: a clean
	// exit, as distinct from a session that simply went quiet.
	StoppedAt *time.Time `json:"stoppedAt,omitempty"`
}

// wireDriftObject is controlplane.v1.DeployDriftObject.
type wireDriftObject struct {
	Cluster   string `json:"cluster,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// ExpectedHash comes from the applied bundle's shape; ObservedHash from
	// the live object, empty when it could not be read.
	ExpectedHash string `json:"expectedHash,omitempty"`
	ObservedHash string `json:"observedHash,omitempty"`
	State        string `json:"state,omitempty"`
}

// wireDrift is controlplane.v1.DeployDrift.
type wireDrift struct {
	State      string            `json:"state,omitempty"`
	BundleID   string            `json:"bundleId,omitempty"`
	ApplyID    string            `json:"applyId,omitempty"`
	ObservedAt *time.Time        `json:"observedAt,omitempty"`
	Objects    []wireDriftObject `json:"objects,omitempty"`
	Detail     string            `json:"detail,omitempty"`
}

// wireLiveEnvRow is controlplane.v1.DeployLiveEnvironment.
//
// `findings` (tag 8) is reserved for #516 and absent here.
type wireLiveEnvRow struct {
	Environment      *wireLiveEnvironment `json:"environment,omitempty"`
	CurrentPromotion *wirePromotion       `json:"currentPromotion,omitempty"`
	CurrentRelease   *wireRelease         `json:"currentRelease,omitempty"`
	CurrentBundle    *wireBundle          `json:"currentBundle,omitempty"`
	LatestApply      *wireApply           `json:"latestApply,omitempty"`
	Phase            string               `json:"phase,omitempty"`
	Sessions         []wireLocalSession   `json:"sessions,omitempty"`
	Drift            *wireDrift           `json:"drift,omitempty"`
}

// ─── Domain shapes ───────────────────────────────────────────────────────────

// LiveEnvironment is one env as Live shows it, in forge's own vocabulary.
//
// Every pointer is nilable and every nil is MEANINGFUL: no promotion means
// never deployed, no bundle means no config has been applied, no drift means
// drift is not observable here. A reader must render those three differently
// from their empty equivalents, which is why none of them is flattened into a
// zero value.
type LiveEnvironment struct {
	// Env is the environment's record: name, kind, and the declaration.
	Env release.EnvRecord
	// EnvironmentID is the control plane's id, for the per-env RPCs a
	// reader may follow up with.
	EnvironmentID string
	Namespace     string
	// ConvergesPromotions: a promotion here is applied server-side. False
	// means nothing moves without a client-side deploy, so a caller should
	// refuse fast rather than wait out a timeout.
	ConvergesPromotions bool

	// CurrentPromotion and CurrentRelease are where the IMAGES come from.
	CurrentPromotion *release.Promotion
	CurrentRelease   *release.Release
	// CurrentBundle is where the CONFIG comes from.
	CurrentBundle *release.BundleRecord
	// LatestApply may be newer than CurrentBundle and still running.
	LatestApply *ApplyRecord
	// Phase is the rollout phase, as the wire enum names it. Rendered with
	// rolloutPhaseName; UNSPECIFIED for a self-managed env until applies
	// exist.
	Phase string
	// Sessions are the live `forge env up` stacks on a LOCAL env: one row
	// per worktree per machine.
	Sessions []release.LocalSession
	// Drift is nil when drift is NOT OBSERVABLE for this env — never
	// confused with "no drift found", which is a Drift whose Objects are
	// empty and whose State is in_sync.
	Drift *LiveDrift
}

// LiveDrift is what an observer saw of the live cluster, against the applied
// bundle (F-17). A REPORT: it never refuses a deploy, never rolls anything
// back and never feeds policy. It does appear in the deploy plan, so an
// operator reviewing a plan sees that the deploy will overwrite an
// out-of-band change.
type LiveDrift struct {
	// State is in_sync | drifted | unknown. UNKNOWN IS NOT in_sync.
	State      string
	BundleID   string
	ApplyID    string
	ObservedAt *time.Time
	// Objects holds only the objects with something to say; an in-sync env
	// reports an empty list rather than every object it owns.
	Objects []LiveDriftObject
	Detail  string
}

// Drifted reports whether any object diverged. It reads STATE rather than
// counting objects, so "unknown" can never be answered as "no".
func (d LiveDrift) Drifted() bool { return d.State == driftDrifted }

// LiveDriftObject is one object's drift verdict.
type LiveDriftObject struct {
	Key          release.ObjectKey
	ExpectedHash string
	ObservedHash string
	// State is in_sync | drifted | missing | unobservable.
	State string
}

// ─── The client ──────────────────────────────────────────────────────────────

// hostedLiveClient reads the Live view from one control plane.
type hostedLiveClient struct {
	client cloudCaller
}

// GetLiveView reads a project's environments. now is the clock the apply
// states are derived against, passed in rather than read here so a caller
// (and a test) controls the moment the whole view is judged at.
//
// A project the control plane has never heard of is an EMPTY list, not an
// error: a project with no hosted envs has nothing Live to show, which is a
// fact rather than a failure.
func (c hostedLiveClient) GetLiveView(ctx context.Context, project string, includeDeleted bool, now time.Time) ([]LiveEnvironment, error) {
	req := map[string]any{"project": project}
	if includeDeleted {
		req["includeDeleted"] = true
	}
	var resp struct {
		Environments []wireLiveEnvRow `json:"environments"`
	}
	if err := c.client.Call(ctx, procGetLiveView, req, &resp); err != nil {
		return nil, bundlesUnsupported(err)
	}
	out := make([]LiveEnvironment, 0, len(resp.Environments))
	for _, row := range resp.Environments {
		live, err := liveEnvFromWire(row, now)
		if err != nil {
			return nil, err
		}
		out = append(out, live)
	}
	return out, nil
}

func liveEnvFromWire(row wireLiveEnvRow, now time.Time) (LiveEnvironment, error) {
	if row.Environment == nil {
		return LiveEnvironment{}, fmt.Errorf("%w: the control plane returned a Live row with no environment", release.ErrInvalid)
	}
	we := row.Environment
	kind, err := envKindFromWire(we.Kind)
	if err != nil {
		return LiveEnvironment{}, fmt.Errorf("environment %q: %w", we.Name, err)
	}
	out := LiveEnvironment{
		EnvironmentID:       we.ID,
		Namespace:           we.Namespace,
		ConvergesPromotions: we.ConvergesPromotions,
		Phase:               row.Phase,
		Env:                 release.EnvRecord{Name: we.Name, Kind: kind},
	}
	shape, err := shapeFromWire(we.DeclaredShape)
	if err != nil {
		return LiveEnvironment{}, fmt.Errorf("environment %q: %w", we.Name, err)
	}
	out.Env.DeclaredShape = shape
	out.Env.DeclaredAt = we.DeclaredAt
	if out.Env.DeclaredShape != nil && out.Env.DeclaredAt == nil {
		// EnvRecord.Validate requires the two together: a declaration with
		// no time cannot be judged stale, and staleness is most of what a
		// declaration is read for. Rather than refuse the whole row — the
		// shape is still useful — the time is taken as unknown and the
		// shape dropped, so no reader can present an undateable shape as
		// current.
		out.Env.DeclaredShape = nil
	}
	declaredBy, err := provenanceFromWire(we.DeclaredBy)
	if err != nil {
		return LiveEnvironment{}, fmt.Errorf("environment %q: %w", we.Name, err)
	}
	out.Env.DeclaredBy = declaredBy
	if err := out.Env.Validate(); err != nil {
		return LiveEnvironment{}, err
	}

	if w := row.CurrentPromotion; w != nil {
		p, perr := livePromotionFromWire(we.Name, *w)
		if perr != nil {
			return LiveEnvironment{}, perr
		}
		out.CurrentPromotion = &p
	}
	if w := row.CurrentRelease; w != nil {
		r, rerr := releaseFromWire(*w)
		if rerr != nil {
			return LiveEnvironment{}, rerr
		}
		out.CurrentRelease = &r
	}
	if w := row.CurrentBundle; w != nil {
		b, berr := bundleFromWire(we.Name, *w)
		if berr != nil {
			return LiveEnvironment{}, berr
		}
		out.CurrentBundle = &b
	}
	if w := row.LatestApply; w != nil {
		a, outcome, aerr := applyFromWire(we.Name, *w)
		if aerr != nil {
			return LiveEnvironment{}, aerr
		}
		out.LatestApply = &ApplyRecord{Apply: a, Outcome: outcome, State: release.DeriveApplyState(a, outcome, now)}
	}
	for _, ws := range row.Sessions {
		s, serr := localSessionFromWire(we.Name, ws)
		if serr != nil {
			return LiveEnvironment{}, serr
		}
		out.Sessions = append(out.Sessions, s)
	}
	if w := row.Drift; w != nil {
		out.Drift = driftFromWire(*w)
	}
	return out, nil
}

// livePromotionFromWire converts a promotion carried on a Live row.
//
// hostedStore.promotionFromWire is a METHOD on the store, because it resolves
// env names against the store's cache; Live already holds the env name on the
// row beside the promotion, so it needs none of that. Hence a plain function
// here rather than reaching for a store that may not exist.
func livePromotionFromWire(env string, w wirePromotion) (release.Promotion, error) {
	kind, err := promotionKindFromWire(w.Kind)
	if err != nil {
		return release.Promotion{}, err
	}
	p := release.Promotion{
		ID: w.ID, Env: env, Release: w.ReleaseVersion, Kind: kind,
		Resolved:   w.ResolvedArtifacts,
		PromotedBy: release.Actor{User: w.PromotedByUserID, Actor: w.PromotedByActor},
		Note:       w.Note, PromotedAt: w.CreatedAt,
		FromPromotionID:    w.FromPromotionID,
		SupersededInFlight: w.SupersededInFlight,
		Run:                runFromWire(w.Run),
	}
	switch {
	case w.FromEnvironmentName != "":
		p.FromEnv = w.FromEnvironmentName
	case w.FromEnvironmentID != "":
		p.FromEnv = w.FromEnvironmentID
	}
	if p.Resolved == nil {
		p.Resolved = map[string]string{}
	}
	if len(w.ResolvedSources) > 0 {
		p.Sources = map[string]release.Source{}
		for name, src := range w.ResolvedSources {
			p.Sources[name] = release.Source(src)
		}
	}
	p.Gates = gatesFromWire(w.Gates)
	p.RecordedGates = gatesFromWire(w.RecordedGates)
	p.PlanDigest = w.PlanDigest
	p.ApprovedBy = w.ApprovedBy
	p.AcknowledgedFindings = w.AcknowledgedFindings
	if err := p.Validate(); err != nil {
		return release.Promotion{}, fmt.Errorf("control plane returned promotion %s: %w", w.ID, err)
	}
	return p, nil
}

func localSessionFromWire(env string, w wireLocalSession) (release.LocalSession, error) {
	s := release.LocalSession{
		ID: w.ID, Env: env, BundleDigest: w.BundleDigest,
		StartedAt: w.StartedAt, LastSeenAt: w.LastSeenAt, StoppedAt: w.StoppedAt,
	}
	if wt := w.Worktree; wt != nil {
		s.Worktree = release.Worktree{Key: wt.Key, Label: wt.Label, Host: wt.HostID}
	}
	prov, err := provenanceFromWire(w.Provenance)
	if err != nil {
		return release.LocalSession{}, fmt.Errorf("control plane returned session %s: %w", w.ID, err)
	}
	if prov != nil {
		s.Provenance = *prov
	}
	if err := s.Validate(); err != nil {
		return release.LocalSession{}, fmt.Errorf("control plane returned session %s: %w", w.ID, err)
	}
	return s, nil
}

func driftFromWire(w wireDrift) *LiveDrift {
	d := &LiveDrift{
		BundleID: w.BundleID, ApplyID: w.ApplyID,
		ObservedAt: w.ObservedAt, Detail: w.Detail,
		State: w.State,
	}
	if d.State == "" {
		// A state the server did not set is UNKNOWN, never in_sync. The
		// dangerous default here is agreement.
		d.State = driftUnknown
	}
	for _, o := range w.Objects {
		state := o.State
		if state == "" {
			state = driftObjectUnobservable
		}
		d.Objects = append(d.Objects, LiveDriftObject{
			Key:          release.ObjectKey{Cluster: o.Cluster, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name},
			ExpectedHash: o.ExpectedHash, ObservedHash: o.ObservedHash, State: state,
		})
	}
	return d
}

// envKindFromWire reads a DeployEnvironmentKind value name into
// release.EnvKind. Unknown and unspecified are REFUSED rather than defaulted:
// a kind forge cannot read decides whether an env's secrets are pullable and
// whether it is a deploy target, so guessing it is the one thing that must not
// happen.
func envKindFromWire(wire string) (release.EnvKind, error) {
	switch wire {
	case string(deployEnvKindPersistent):
		return release.EnvPersistent, nil
	case string(deployEnvKindPreview):
		return release.EnvPreview, nil
	case string(deployEnvKindSelfManaged):
		return release.EnvSelfManaged, nil
	case string(deployEnvKindLocal):
		return release.EnvLocal, nil
	default:
		return "", fmt.Errorf("%w: the control plane returned environment kind %q, which this forge does not recognise", release.ErrInvalid, wire)
	}
}

// The DeployEnvironmentKind enum's value names, as protojson writes them.
//
// deploytarget.HostedEnvKind holds the same four strings for the WRITE path
// (what an ensure creates an env with). They are spelled again here rather
// than imported because this is the READ direction and it needs PREVIEW,
// which no forge code ever writes: forge never creates a preview env, the
// platform does. Importing the write type would mean either adding a constant
// nothing writes to it, or leaving preview unhandled and refusing a perfectly
// valid env.
type deployEnvKind string

const (
	deployEnvKindPersistent  deployEnvKind = "DEPLOY_ENVIRONMENT_KIND_PERSISTENT"
	deployEnvKindPreview     deployEnvKind = "DEPLOY_ENVIRONMENT_KIND_PREVIEW"
	deployEnvKindSelfManaged deployEnvKind = "DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED"
	deployEnvKindLocal       deployEnvKind = "DEPLOY_ENVIRONMENT_KIND_LOCAL"
)

// GetDrift reads one env's drift. nil means drift is NOT OBSERVABLE here,
// which a reader must render differently from "no drift found".
func (c hostedLiveClient) GetDrift(ctx context.Context, environmentID string) (*LiveDrift, error) {
	if environmentID == "" {
		return nil, fmt.Errorf("%w: reading drift needs an environment", release.ErrInvalid)
	}
	var resp struct {
		Drift *wireDrift `json:"drift"`
	}
	if err := c.client.Call(ctx, procGetDrift, map[string]any{"environmentId": environmentID}, &resp); err != nil {
		return nil, bundlesUnsupported(err)
	}
	if resp.Drift == nil {
		return nil, nil
	}
	return driftFromWire(*resp.Drift), nil
}
