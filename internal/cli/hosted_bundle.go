package cli

// The HOSTED bundle half of the deploy source of truth (doc §6.3): RecordBundle
// and GetBundle, declared here in proto3 JSON exactly as hosted_ledger.go
// declares the promotion half. forge does not import control-plane, so these
// structs are the only place the two sides' field names are made to agree, and
// the golden tests beside them are the only thing that can catch a drift —
// the compiler is happy whichever way a json tag is spelled, and the symptom
// at runtime is a field that silently reads as its zero value.
//
// THE REQUEST CARRIES THE BUNDLE'S OWN BYTES, NEVER A DESCRIPTION OF THEM.
// `manifest` and `config` are the blobs; the server checks
// digest = sha256(manifest) and manifest.config.digest = sha256(config), then
// decodes the config blob as a release.BundleDoc and takes the shape,
// provenance, config digest and release FROM THE VERIFIED DOCUMENT. That is
// why there is no `shape` or `provenance` field on the request: a client that
// could state them could record a shape that disagrees with the bytes it
// pushed, and every reader downstream would trust the description over the
// artifact. It is the same "the emptiness is the contract" rule
// PublishDeploymentConfig follows.
//
// This file also holds the PROVENANCE READ path (wireProvenance and friends),
// because a bundle is the first record forge reads one back from. The WRITE
// path already exists as deploytarget.ProvenanceWireFields and is reused
// rather than mirrored: two encoders of one message can only ever prove that
// they differ.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	procRecordBundle = "controlplane.v1.DeployService/RecordBundle"
	procGetBundle    = "controlplane.v1.DeployService/GetBundle"
)

// errControlPlanePredatesBundles is F-15: this forge speaks bundles and the
// control plane it is talking to does not.
//
// It is recognised by the Connect code UNIMPLEMENTED on RecordBundle or
// BeginApply — never by message text — because an old server's prose is not a
// contract and a server that improves its wording would silently stop
// matching. A typed error rather than a bare message so the CALLER decides
// what to do: F6a chooses the pre-bundle fallback, and once protected envs
// exist (#516, L6) a protected env refuses instead. Deciding that here would
// bake the weaker of the two behaviours into the wire layer, where neither
// caller could override it.
var errControlPlanePredatesBundles = errors.New("this control plane predates bundles; upgrade it")

// bundlesUnsupported maps an Unimplemented failure to F-15's typed error and
// leaves every other failure alone. The wire error is wrapped, so a caller
// that wants the detail still has it through errors.As.
func bundlesUnsupported(err error) error {
	if hostedErrorHasCode(err, cloud.CodeUnimplemented) {
		// BOTH are wrapped. errors.Is finds the sentinel so a caller can
		// branch on F-15, and errors.As still finds the *cloud.Error so
		// the procedure, endpoint and server message survive for the
		// message a caller prints. Wrapping only the sentinel (%v for the
		// cause) would discard the one thing that says WHICH server is
		// too old.
		return fmt.Errorf("%w: %w", errControlPlanePredatesBundles, err)
	}
	return err
}

// ─── Provenance (the READ path) ──────────────────────────────────────────────

// wireWorktree is controlplane.v1.DeployWorktree.
//
// Note `hostId`, not `host`: the proto field is `host_id`, and
// release.Worktree calls the same thing Host. The mismatch is why this struct
// exists rather than decoding straight into the domain type.
//
// There is no `path`. A checkout's absolute path names a user's home
// directory, so release.Provenance.ForHosted strips it before anything is
// sent and the proto has nowhere to put it on the way back.
type wireWorktree struct {
	Key    string `json:"key,omitempty"`
	Label  string `json:"label,omitempty"`
	HostID string `json:"hostId,omitempty"`
}

// wireAttestation is controlplane.v1.DeploySourceAttestation.
//
// `token` is deliberately absent. It is the raw OIDC JWT, carried only on a
// WRITE for the server to verify; what comes back is the verified claims. A
// field here would invite a reader to treat a returned token as meaningful
// and a writer to round-trip one.
type wireAttestation struct {
	Provider string `json:"provider,omitempty"`
	Subject  string `json:"subject,omitempty"`
	RunID    string `json:"runId,omitempty"`
}

// wireProvenance is controlplane.v1.DeploySourceProvenance: where a release
// or a bundle came from, as the control plane returns it.
type wireProvenance struct {
	Repo       string     `json:"repo,omitempty"`
	Commit     string     `json:"commit,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Tag        string     `json:"tag,omitempty"`
	Dirty      bool       `json:"dirty,omitempty"`
	Tree       string     `json:"tree,omitempty"`
	CommitTime *time.Time `json:"commitTime,omitempty"`
	// CommitSubject and CommitAuthor are DeploySourceProvenance tags 11-12:
	// the commit's subject line and author name, as the cut reported them.
	CommitSubject string           `json:"commitSubject,omitempty"`
	CommitAuthor  string           `json:"commitAuthor,omitempty"`
	Worktree      *wireWorktree    `json:"worktree,omitempty"`
	ForgeVersion  string           `json:"forgeVersion,omitempty"`
	Attestation   *wireAttestation `json:"attestation,omitempty"`
}

// provenanceFromWire converts a returned provenance, validating the claims'
// SHAPE. It cannot validate their truth — Branch, Dirty and Worktree are
// claims a client made, stored as such — and Validate is what stops a
// malformed commit or tree hash being rendered as if it were checkable.
func provenanceFromWire(w *wireProvenance) (*release.Provenance, error) {
	if w == nil {
		return nil, nil
	}
	p := release.Provenance{
		Repo: w.Repo, Commit: w.Commit, Branch: w.Branch, Tag: w.Tag,
		Dirty: w.Dirty, Tree: w.Tree, ForgeVersion: w.ForgeVersion,
		CommitTime: w.CommitTime, CommitSubject: w.CommitSubject, CommitAuthor: w.CommitAuthor,
	}
	if wt := w.Worktree; wt != nil {
		p.Worktree = release.Worktree{Key: wt.Key, Label: wt.Label, Host: wt.HostID}
	}
	if a := w.Attestation; a != nil {
		p.Attestation = &release.Attest{Provider: a.Provider, Subject: a.Subject, RunID: a.RunID}
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("control plane returned provenance: %w", err)
	}
	return &p, nil
}

// worktreeWireFields renders a release.Worktree as DeployWorktree, path
// dropped. The write twin of wireWorktree.
func worktreeWireFields(w release.Worktree) map[string]any {
	fields := map[string]any{}
	if w.Key != "" {
		fields["key"] = w.Key
	}
	if w.Label != "" {
		fields["label"] = w.Label
	}
	if w.Host != "" {
		fields["hostId"] = w.Host
	}
	return fields
}

// ─── Shape (a google.protobuf.Struct) ────────────────────────────────────────

// wireShapeStruct decodes a shape carried as google.protobuf.Struct.
//
// protojson writes a Struct as the plain JSON object it holds, and the object
// a control plane holds here is exactly release.Shape's canonical JSON —
// SNAKE_CASE keys, from Shape.Encode (briefing §4.4). So the Struct is kept as
// raw bytes and decoded with release's own tags, rather than being modelled
// field by field in camelCase: a Struct has no proto field names to mirror,
// and inventing some would put a second spelling of Shape in the codebase.
func shapeFromWire(raw json.RawMessage) (*release.Shape, error) {
	if len(raw) == 0 || string(raw) == "null" {
		// Absent, which is not "empty". An env with no recorded shape and
		// an env whose render declares nothing are different facts, and a
		// plan reads the first as unknown. Returning a zero Shape here
		// would collapse them.
		return nil, nil
	}
	var s release.Shape
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("control plane returned shape: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("control plane returned shape: %w", err)
	}
	return &s, nil
}

// ─── Bundle ──────────────────────────────────────────────────────────────────

// wireBundle is controlplane.v1.DeployBundle.
//
// `sourceStatus` (tag 9) is reserved for #516 and has no field here: a
// verdict nothing computes would read as a guarantee.
type wireBundle struct {
	Snapshot       *release.Snapshot `json:"snapshot,omitempty"`
	SnapshotDigest string            `json:"snapshotDigest,omitempty"`
	ID             string            `json:"id"`
	EnvironmentID  string            `json:"environmentId,omitempty"`
	ReleaseVersion string            `json:"releaseVersion,omitempty"`
	Digest         string            `json:"digest"`
	Reference      string            `json:"reference,omitempty"`
	ConfigDigest   string            `json:"configDigest,omitempty"`
	Shape          json.RawMessage   `json:"shape,omitempty"`
	Provenance     *wireProvenance   `json:"provenance,omitempty"`
	Run            *wireRun          `json:"run,omitempty"`
	CreatedBy      string            `json:"createdBy,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
}

// bundleFromWire converts one bundle. env is the NAME the caller asked about;
// the wire carries ids, and release.BundleRecord speaks names like every other
// domain record.
func bundleFromWire(env string, w wireBundle) (release.BundleRecord, error) {
	b := release.BundleRecord{
		ID: w.ID, Env: env, Release: w.ReleaseVersion,
		Digest: w.Digest, Reference: w.Reference, ConfigDigest: w.ConfigDigest,
		Run: runFromWire(w.Run), CreatedBy: w.CreatedBy, CreatedAt: w.CreatedAt,
	}
	b.Snapshot = w.Snapshot
	b.SnapshotDigest = w.SnapshotDigest
	if b.Snapshot != nil {
		digest, err := b.Snapshot.Digest()
		if err != nil || digest != b.SnapshotDigest || b.Snapshot.Environment != env {
			return release.BundleRecord{}, fmt.Errorf("invalid frozen bundle snapshot")
		}
	}
	shape, err := shapeFromWire(w.Shape)
	if err != nil {
		return release.BundleRecord{}, fmt.Errorf("control plane returned bundle %s: %w", w.Digest, err)
	}
	if shape != nil {
		b.Shape = *shape
	}
	prov, err := provenanceFromWire(w.Provenance)
	if err != nil {
		return release.BundleRecord{}, fmt.Errorf("control plane returned bundle %s: %w", w.Digest, err)
	}
	if prov != nil {
		b.Provenance = *prov
	}
	return b, nil
}

// ─── The client ──────────────────────────────────────────────────────────────

// hostedBundleClient records and reads bundles on one control plane.
type hostedBundleClient struct {
	client cloudCaller
}

// RecordBundle records the bundle and reports whether this call created it.
// Idempotent on (env, digest): a retry of the same bytes returns
// created=false, which is what makes the push safe to re-run after a lost
// response (F-3).
//
// env is the environment NAME, for the returned record; environmentID is what
// the wire is keyed on. repository is where the bytes were pushed, which the
// control plane checks against the registry subtree it admits for the caller's
// organization (the subtree its realm admits) or the env's declared BYO
// registry.
// run is the CI run that built the bundle, or the zero Run when a human did.
//
// THE BYTES ARE PARAMETERS, and there is deliberately no struct bundling them
// with a description — no shape, no provenance, no config digest. The server
// derives every one of those from the config blob, so a caller has nowhere to
// state them, which is the intent: a client that can describe a bundle can
// describe one that disagrees with the bytes it pushed, and every reader
// downstream then trusts the description over the artifact.
func (c hostedBundleClient) RecordBundle(ctx context.Context, env, environmentID, repository string, manifest, config []byte, run release.Run, layers ...[]byte) (release.BundleRecord, bool, error) {
	if len(manifest) == 0 || len(config) == 0 {
		// Refused here rather than sent. A record whose bytes are absent
		// could only be a description of a bundle, which is the one thing
		// this RPC exists not to accept.
		return release.BundleRecord{}, false, fmt.Errorf("%w: recording a bundle needs its manifest and config bytes", release.ErrInvalid)
	}
	req := map[string]any{
		"environmentId": environmentID,
		"repository":    repository,
		// proto3 JSON encodes `bytes` as base64; encoding/json does the
		// same for []byte, so these two agree without a conversion.
		"manifest": manifest,
		"config":   config,
	}
	if fields := runWireFields(run); fields != nil {
		req["run"] = fields
	}
	if len(layers) > 0 && len(layers[0]) > 0 {
		req["manifestsLayer"] = layers[0]
	}
	var doc struct {
		Snapshot *release.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(config, &doc); err != nil {
		return release.BundleRecord{}, false, err
	}
	if doc.Snapshot != nil {
		if err := requireSnapshotCapabilities(ctx, c.client); err != nil {
			return release.BundleRecord{}, false, err
		}
		if len(layers) == 0 || len(layers[0]) == 0 {
			return release.BundleRecord{}, false, fmt.Errorf("snapshot seal requires complete verified manifest layer")
		}
		if err := release.ValidateSnapshotLayer(*doc.Snapshot, layers[0]); err != nil {
			return release.BundleRecord{}, false, err
		}
	}
	var resp struct {
		Bundle  wireBundle `json:"bundle"`
		Created bool       `json:"created"`
	}
	if err := c.client.Call(ctx, procRecordBundle, req, &resp); err != nil {
		return release.BundleRecord{}, false, bundlesUnsupported(err)
	}
	b, err := bundleFromWire(env, resp.Bundle)
	if err != nil {
		return release.BundleRecord{}, false, err
	}
	if doc.Snapshot != nil {
		d, err := doc.Snapshot.Digest()
		if err != nil || b.Snapshot == nil || b.SnapshotDigest != d {
			return release.BundleRecord{}, false, fmt.Errorf("server did not preserve the sealed snapshot")
		}
	}
	return b, resp.Created, nil
}

// GetBundleByID reads one bundle by its ledger id — what a caller reading
// Live holds.
//
// A bundle the control plane does not hold is (nil, nil): "never recorded" is
// an answer, not a failure, the same rule hostedStore.Get follows for a
// version nobody cut. Collapsing the two would make a missing bundle
// indistinguishable from an unreachable control plane, and F-4's remedy
// (re-build and re-record) is right for only one of them.
func (c hostedBundleClient) GetBundleByID(ctx context.Context, env, bundleID string) (*release.BundleRecord, error) {
	if bundleID == "" {
		return nil, fmt.Errorf("%w: reading a bundle needs an id", release.ErrInvalid)
	}
	return c.getBundle(ctx, env, map[string]any{"bundleId": bundleID})
}

// GetBundleByDigest reads one bundle by (env, digest) — what a caller that
// just pushed holds, since the id is the server's to assign.
func (c hostedBundleClient) GetBundleByDigest(ctx context.Context, env, environmentID, digest string) (*release.BundleRecord, error) {
	if environmentID == "" || digest == "" {
		return nil, fmt.Errorf("%w: reading a bundle by digest needs an environment and a digest", release.ErrInvalid)
	}
	return c.getBundle(ctx, env, map[string]any{"environmentId": environmentID, "digest": digest})
}

func (c hostedBundleClient) getBundle(ctx context.Context, env string, req map[string]any) (*release.BundleRecord, error) {
	var resp struct {
		Bundle wireBundle `json:"bundle"`
	}
	if err := c.client.Call(ctx, procGetBundle, req, &resp); err != nil {
		if hostedErrorHasCode(err, cloud.CodeNotFound) {
			return nil, nil
		}
		return nil, err
	}
	b, err := bundleFromWire(env, resp.Bundle)
	if err != nil {
		return nil, err
	}
	return &b, nil
}
