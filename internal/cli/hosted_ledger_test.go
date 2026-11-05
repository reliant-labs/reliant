package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeDeployService is an httptest control plane speaking Connect's JSON
// binding for the six ledger RPCs plus ListEnvironments, with the SERVER's
// semantics: CutRelease is idempotent on version and refuses a different
// artifact set (AlreadyExists); Promote freezes the pin set from the
// release the server holds and apply release.Decide; responses use proto3 JSON
// names and enum value names.
//
// It is a FAKE of control-plane's handlers, not a mirror of forge's client:
// the request fields it reads are the proto's (environmentId, version,
// artifacts[].variant …), spelled literally here so a typo in the client's
// wire structs fails the test rather than moving with it.
type fakeDeployService struct {
	mu         sync.Mutex
	envs       map[string]string // name → id
	releases   map[string]wireRelease
	promotions map[string][]wirePromotion // env id → oldest first
	calls      []string
	auth       []string

	// The hosted deploy half (EnsureEnvironment / RecordBundle / GetStatus).
	// bodies records every request body in order so a test can assert the
	// wire document.
	// bundles is env id → the manifests layer's hosted records, decoded from
	// the bytes RecordBundle carried. The control plane's hub Flux applies
	// exactly these, so a test asserts on THEM — what the platform would run.
	bundles map[string]map[string]*fakeDeployment
	// layerFetch reads a pushed bundle's manifests layer, as the control
	// plane reads it from its registry. Set by a test that pushes bundles to
	// an in-memory registry (see stubHostedBundleRegistry).
	layerFetch func(repository, manifestDigest string) ([]byte, error)
	bodies     []fakeBody
	// onSecret, when set, sees every SecretStoreService/SetSecret body.
	onSecret func(body map[string]any)
	// Promote's refusals, in the SERVER's order (control-plane C3/C3b):
	// pinned refuses before anything else; then the idempotent no-op; then
	// the compare-and-set (always modelled); then, when inFlightPhase is
	// set, rollout_in_flight unless the request supersedes.
	pinned        bool
	inFlightPhase string
	// refusalWithoutDetail sends a refusal as the reason header alone, the
	// way a control plane without the detail descriptor would.
	refusalWithoutDetail bool

	// gates is the append-only child record RecordGate writes:
	// promotion id → gates, in the order they were recorded (F4, §3.3).
	gates map[string][]wireGate

	// convergences is env id → the hub's reconcile observations, newest
	// first, as ListConvergences returns them. Nil answers Unimplemented,
	// like a control plane that predates the observer.
	convergences map[string][]map[string]any

	// queueOn, when set, makes every promotion this fake appends QUEUED:
	// accepted and recorded, with these holds in the Promote response —
	// what a control plane does for a deploy that needs billing the org
	// has not set up.
	queueOn []deploytarget.HostedHold
}

// hasPromotion reports whether any env holds a promotion of this id. Called
// under f.mu. Gate evidence hangs off a promotion, so an id nobody holds is
// NotFound — the same answer the server's FK gives.
func (f *fakeDeployService) hasPromotion(id string) bool {
	if id == "" {
		return false
	}
	for _, list := range f.promotions {
		for _, p := range list {
			if p.ID == id {
				return true
			}
		}
	}
	return false
}

// refusePromote answers a Promote with the wire form control-plane's
// promoteError produces: FailedPrecondition, the reason under
// x-forge-error-reason, and a DeployPromoteRefusal detail whose `debug`
// member is the protojson payload. Called under f.mu.
func (f *fakeDeployService) refusePromote(w http.ResponseWriter, reason, detail string, body map[string]any, list []wirePromotion, phase string) {
	refusal := map[string]any{"reason": reason, "detail": detail}
	if id, _ := body["expectedCurrentPromotionId"].(string); id != "" {
		refusal["expectedCurrentPromotionId"] = id
	}
	if unbound, _ := body["expectUnbound"].(bool); unbound {
		refusal["expectedUnbound"] = true
	}
	if len(list) > 0 {
		refusal["actualCurrent"] = list[len(list)-1]
	}
	if phase != "" {
		refusal["actualPhase"] = phase
	}
	envelope := map[string]any{"code": "failed_precondition", "message": detail}
	if !f.refusalWithoutDetail {
		debug, _ := json.Marshal(refusal)
		envelope["details"] = []map[string]any{{
			"type": promoteRefusalType, "value": "cHJvdG8", "debug": json.RawMessage(debug),
		}}
	}
	w.Header().Set(cloud.ReasonHeader, reason)
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(envelope)
}

type fakeDeployment struct {
	ID        string
	Tier      string
	Spec      map[string]any
	Published bool
}

type fakeBody struct {
	Path string
	Body map[string]any
}

// NO pushBase HERE, deliberately. The real EnsureEnvironment / ListEnvironments
// advertise no image push base — forge composes it from the env's own
// declaration — so a fake that served one would let a test pass against a
// field the server does not send.

func newFakeDeployService(envs map[string]string) *fakeDeployService {
	return &fakeDeployService{envs: envs, releases: map[string]wireRelease{}, promotions: map[string][]wirePromotion{}}
}

func connectErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

func (f *fakeDeployService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.bodies = append(f.bodies, fakeBody{Path: r.URL.Path, Body: body})
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		connectErr(w, http.StatusUnsupportedMediaType, "invalid_argument", "connect json requires POST + application/json")
		return
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	envName := func(id string) string {
		for n, i := range f.envs {
			if i == id {
				return n
			}
		}
		return ""
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/controlplane.v1.DeployService/ListEnvironments":
		var out []map[string]string
		for n, id := range f.envs {
			if strings.Contains(n, str("search")) {
				out = append(out, map[string]string{"id": id, "name": n})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"environments": out})

	case "/controlplane.v1.SecretStoreService/SetSecret":
		if envName(str("environmentId")) == "" {
			connectErr(w, http.StatusNotFound, "not_found", "no such environment")
			return
		}
		if f.onSecret != nil {
			f.onSecret(body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1})

	case "/controlplane.v1.DeployService/EnsureEnvironment":
		spec, _ := body["spec"].(map[string]any)
		name, _ := spec["name"].(string)
		id, ok := f.envs[name]
		if !ok {
			id = "env-" + name + "-id"
			f.envs[name] = id
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"environment": map[string]string{"id": id, "name": name, "namespace": "env-" + id},
			"created":     !ok,
		})

	case "/controlplane.v1.DeployService/RecordBundle":
		envID := str("environmentId")
		if envName(envID) == "" {
			connectErr(w, http.StatusNotFound, "not_found", "no such environment")
			return
		}
		manifest, _ := base64.StdEncoding.DecodeString(str("manifest"))
		sum := sha256.Sum256(manifest)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if f.bundles == nil {
			f.bundles = map[string]map[string]*fakeDeployment{}
		}
		recs, err := hostedRecordsFromBundleBlobs(digest, str("repository"), f.layerFetch)
		if err != nil {
			connectErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		f.bundles[envID] = recs
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bundle": map[string]any{
				"id": "bundle-" + digest[7:15], "environmentId": envID, "digest": digest,
				"reference": str("repository") + "@" + digest,
				"createdAt": "2026-09-23T00:00:00Z",
			},
			"created": true,
		})

	case "/controlplane.v1.DeployService/GetStatus":
		// A published backend is READY on the digest its spec pins — the
		// control plane's observer confirmed what was published.
		envID := str("environmentId")
		var out []map[string]any
		for name, d := range f.bundles[envID] {
			img, _ := d.Spec["image"].(string)
			digest := ""
			if i := strings.LastIndex(img, "@"); i >= 0 {
				digest = img[i+1:]
			}
			// A static site's release digest is its spec.liveDigest.
			if live, ok := d.Spec["liveDigest"].(string); ok {
				digest = live
			}
			// The platform's Flux applied the recorded bundle: ready.
			state := "DEPLOY_OBSERVED_STATE_READY"
			out = append(out, map[string]any{
				"deployment": map[string]any{"id": d.ID, "name": name, "tier": d.Tier, "observed": map[string]any{
					"state": state, "imageDigest": digest, "url": "https://" + name + "-acme.reliantapps.dev"}},
				"verdict": "DEPLOY_VERDICT_CONVERGING", "verdictReason": "within the stability window", "desiredDigest": digest,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployments": out, "environmentVerdict": "DEPLOY_VERDICT_CONVERGING", "reconcilePolicy": "DEPLOY_RECONCILE_POLICY_OBSERVE",
		})

	case "/controlplane.v1.DeployService/CutRelease":
		var req struct {
			Version   string         `json:"version"`
			Artifacts []wireArtifact `json:"artifacts"`
			GitCommit string         `json:"gitCommit"`
			// The real server stores the cut's provenance and returns it
			// on every read of the release (DeployRelease.provenance);
			// a fake that dropped it would hide a client that never
			// reads it back.
			Provenance *wireProvenance `json:"provenance"`
			// Stored and returned the same way (DeployRelease.commits).
			Commits *wireCommitRange `json:"commits"`
			// And DeployRelease.migrations: sets[] of source/commit/files.
			Migrations *struct {
				Sets []struct {
					Source string   `json:"source"`
					Commit string   `json:"commit"`
					Files  []string `json:"files"`
				} `json:"sets"`
			} `json:"migrations"`
		}
		_ = json.Unmarshal(raw, &req)
		want := wireRelease{ID: "rel-" + req.Version, Version: req.Version, GitCommit: req.GitCommit,
			Artifacts: req.Artifacts, Provenance: req.Provenance, Commits: req.Commits,
			CreatedAt: time.Date(2026, 9, 23, 0, len(f.releases), 0, 0, time.UTC)}
		if req.Migrations != nil {
			m := &release.MigrationManifest{Sets: []release.MigrationSet{}}
			for _, s := range req.Migrations.Sets {
				m.Sets = append(m.Sets, release.MigrationSet{Source: s.Source, Commit: s.Commit, Files: s.Files})
			}
			want.Migrations = m
		}
		wantRel, err := releaseFromWire(want)
		if err != nil {
			connectErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if old, ok := f.releases[req.Version]; ok {
			oldRel, _ := releaseFromWire(old)
			if release.CheckRecut(oldRel, wantRel) != nil {
				connectErr(w, http.StatusConflict, "already_exists", "that version has already been cut")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"release": old, "created": false})
			return
		}
		f.releases[req.Version] = want
		_ = json.NewEncoder(w).Encode(map[string]any{"release": want, "created": true})

	case "/controlplane.v1.DeployService/GetRelease":
		rel, ok := f.releases[str("version")]
		if !ok {
			connectErr(w, http.StatusNotFound, "not_found", "release or environment")
			return
		}
		// current_environment_ids: every env whose NEWEST promotion binds
		// this release.
		var current []string
		for envID, list := range f.promotions {
			if len(list) > 0 && list[len(list)-1].ReleaseVersion == rel.Version {
				current = append(current, envID)
			}
		}
		sort.Strings(current)
		_ = json.NewEncoder(w).Encode(map[string]any{"release": rel, "currentEnvironmentIds": current})

	case "/controlplane.v1.DeployService/RecordReleaseMigrations":
		// The server's rule: fill a release's manifest when it has none,
		// accept an equal one as a no-op, refuse a different one.
		var req struct {
			Version    string                     `json:"version"`
			Migrations *release.MigrationManifest `json:"migrations"`
		}
		_ = json.Unmarshal(raw, &req)
		rel, ok := f.releases[req.Version]
		switch {
		case !ok:
			connectErr(w, http.StatusNotFound, "not_found", "release")
			return
		case req.Migrations == nil || req.Migrations.Validate() != nil:
			connectErr(w, http.StatusBadRequest, "invalid_argument", "migrations")
			return
		case rel.Migrations != nil && !rel.Migrations.Equal(*req.Migrations):
			connectErr(w, http.StatusConflict, "already_exists", "the release already records a different migration manifest")
			return
		case rel.Migrations != nil:
			_ = json.NewEncoder(w).Encode(map[string]any{"release": rel, "recorded": false})
			return
		}
		rel.Migrations = req.Migrations
		f.releases[req.Version] = rel
		_ = json.NewEncoder(w).Encode(map[string]any{"release": rel, "recorded": true})

	case "/controlplane.v1.DeployService/ListReleases":
		var out []wireRelease
		for _, rel := range f.releases {
			out = append(out, rel)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"releases": out})

	case "/controlplane.v1.DeployService/Promote":
		envID, version := str("environmentId"), str("version")
		// C6 (§3.4): a from_promotion_id is resolved HERE, under the
		// target's lock, before anything else looks at the version —
		// so the version the write uses is the source promotion's, not
		// one the client resolved and may have seen go stale.
		if sourceID := str("fromPromotionId"); sourceID != "" {
			resolved, refusal := f.resolveFromPromotion(sourceID, str("fromEnvironmentId"), version)
			if refusal != "" {
				f.refusePromote(w, reasonSourceMoved, refusal, body, f.promotions[envID], "")
				return
			}
			version = resolved
		} else if version == "" {
			// The server's own precondition: with no source promotion
			// to resolve from, a version is the only thing that says
			// what to promote.
			connectErr(w, http.StatusBadRequest, "invalid_argument", "version is required without from_promotion_id")
			return
		}
		rel, ok := f.releases[version]
		if !ok {
			connectErr(w, http.StatusBadRequest, "failed_precondition", "that version has not been built")
			return
		}
		kind := release.KindPromote
		relDomain, _ := releaseFromWire(rel)
		list := f.promotions[envID]
		if f.pinned {
			f.refusePromote(w, reasonEnvironmentPinned, "the environment is pinned", body, list, "")
			return
		}
		var scope struct {
			Targets []string `json:"targets"`
		}
		_ = json.Unmarshal(raw, &scope)
		var history []release.Promotion
		for _, p := range list {
			k, _ := promotionKindFromWire(p.Kind)
			history = append(history, release.Promotion{Env: envID, Release: p.ReleaseVersion, Kind: k, Targets: p.Targets})
		}
		existing, err := release.Decide(history, release.Promotion{Env: envID, Release: version, Kind: kind, Targets: scope.Targets})
		if err != nil {
			connectErr(w, http.StatusBadRequest, "failed_precondition", err.Error())
			return
		}
		if existing != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"promotion": list[len(list)-1]})
			return
		}
		expectedID := str("expectedCurrentPromotionId")
		expectUnbound, _ := body["expectUnbound"].(bool)
		switch {
		case expectUnbound && len(list) > 0,
			expectedID != "" && (len(list) == 0 || list[len(list)-1].ID != expectedID):
			f.refusePromote(w, reasonPromotionConflict, "the environment's current promotion is not the one you expected", body, list, "")
			return
		}
		supersede, _ := body["supersedeInFlight"].(bool)
		if f.inFlightPhase != "" && len(list) > 0 && !supersede {
			f.refusePromote(w, reasonRolloutInFlight, "the current promotion is still rolling out", body, list, f.inFlightPhase)
			return
		}
		wk := wireKindPromote
		p := wirePromotion{
			// GLOBALLY unique, like the server's uuid — not per-env.
			// A counter scoped to one environment collides across two
			// of them, and a `promote --from` compares ids from both.
			ID: fmt.Sprintf("promo-%d", f.nextPromotionSeq()), EnvironmentID: envID, ReleaseID: rel.ID,
			ReleaseVersion: version, Kind: wk, ResolvedArtifacts: relDomain.SharedDigests(),
			Targets:         scope.Targets,
			PromotedByActor: str("promotedByActor"), Note: str("note"),
			CreatedAt:          time.Date(2026, 9, 23, 1, len(f.promotions[envID]), 0, 0, time.UTC),
			SupersededInFlight: supersede && f.inFlightPhase != "",
		}
		// Pre-promote evidence is stored ON the entry (F4, §3.3): the
		// promotion row's existing `gates` JSONB. Kept so ListGates can
		// return it BEFORE the recorded gates, which is the order that
		// carries "was this known before the environment moved".
		var gateReq struct {
			Gates []wireGate `json:"gates"`
		}
		_ = json.Unmarshal(raw, &gateReq)
		p.Gates = gateReq.Gates
		if fromID := str("fromEnvironmentId"); fromID != "" {
			p.FromEnvironmentID = fromID
			// The server sets the NAME from the join it already makes
			// (§3.4), which is what lets a ledger reader render
			// "staging → prod" without a lookup per row.
			p.FromEnvironmentName = envName(fromID)
		}
		p.FromPromotionID = str("fromPromotionId")
		f.promotions[envID] = append(f.promotions[envID], p)
		// QUEUE, DON'T REFUSE (control-plane deploy holds): a control plane
		// that accepts the promotion but holds it on billing says so on
		// the write, in the same response, with the hold's own sentence,
		// remedy and link.
		resp := map[string]any{"promotion": p}
		if len(f.queueOn) > 0 {
			holds := make([]deploytarget.HostedHold, 0, len(f.queueOn))
			for _, h := range f.queueOn {
				h.PromotionID = p.ID
				holds = append(holds, h)
			}
			resp["holds"] = holds
		}
		_ = json.NewEncoder(w).Encode(resp)

	case "/controlplane.v1.DeployService/RecordGate":
		// Models control-plane C5. The rules asserted here are the
		// SERVER's, so forge's client is tested against them rather
		// than against a stub that accepts anything:
		//   - the status set is closed → InvalidArgument
		//   - a foreign promotion id → NotFound
		//   - idempotent on (promotion, name, run id)
		//   - recorded_by comes from the PRINCIPAL, never the request
		var req struct {
			PromotionID string   `json:"promotionId"`
			Gate        wireGate `json:"gate"`
		}
		_ = json.Unmarshal(raw, &req)
		if _, err := release.ParseGateStatus(req.Gate.Status); err != nil {
			connectErr(w, http.StatusBadRequest, "invalid_argument",
				"gate status must be one of passed, failed, skipped, error")
			return
		}
		if !f.hasPromotion(req.PromotionID) {
			connectErr(w, http.StatusNotFound, "not_found", "promotion "+req.PromotionID)
			return
		}
		if f.gates == nil {
			f.gates = map[string][]wireGate{}
		}
		for _, existing := range f.gates[req.PromotionID] {
			if existing.Name == req.Gate.Name && existing.RunID == req.Gate.RunID {
				_ = json.NewEncoder(w).Encode(map[string]any{"gate": existing, "created": false})
				return
			}
		}
		stored := req.Gate
		// The server stamps attribution from the authenticated
		// principal and IGNORES whatever the request carried.
		stored.RecordedBy = "token:test-token"
		at := time.Date(2026, 9, 24, 0, len(f.gates[req.PromotionID]), 0, 0, time.UTC)
		stored.RecordedAt = &at
		f.gates[req.PromotionID] = append(f.gates[req.PromotionID], stored)
		_ = json.NewEncoder(w).Encode(map[string]any{"gate": stored, "created": true})

	case "/controlplane.v1.DeployService/ListGates":
		id := str("promotionId")
		if !f.hasPromotion(id) {
			connectErr(w, http.StatusNotFound, "not_found", "promotion "+id)
			return
		}
		// Promote-time gates FIRST, then the recorded ones — §3.3's
		// documented order, which carries "was this known before the
		// environment moved".
		out := make([]wireGate, 0, len(f.gates[id]))
		for _, list := range f.promotions {
			for _, p := range list {
				if p.ID == id {
					out = append(out, p.Gates...)
				}
			}
		}
		out = append(out, f.gates[id]...)
		_ = json.NewEncoder(w).Encode(map[string]any{"gates": out})

	case "/controlplane.v1.DeployService/ListPromotions":
		// C7's semantics: newest first; `beforePromotionId` is a keyset
		// cursor (strictly older; an id foreign to this env is an EMPTY
		// page, not an error); `releaseVersion` filters; the response's
		// nextBeforePromotionId is empty on the last page.
		list := f.promotions[str("environmentId")]
		newest := make([]wirePromotion, 0, len(list))
		for i := len(list) - 1; i >= 0; i-- {
			newest = append(newest, list[i])
		}
		start := 0
		if before := str("beforePromotionId"); before != "" {
			start = len(newest) // foreign cursor ⇒ empty page
			for i, p := range newest {
				if p.ID == before {
					start = i + 1
					break
				}
			}
		}
		var out []wirePromotion
		for _, p := range newest[start:] {
			if v := str("releaseVersion"); v != "" && p.ReleaseVersion != v {
				continue
			}
			out = append(out, p)
		}
		next := ""
		if lim, ok := body["limit"].(float64); ok && int(lim) < len(out) {
			out = out[:int(lim)]
			next = out[len(out)-1].ID
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"promotions": out, "nextBeforePromotionId": next})

	case "/controlplane.v1.DeployService/ListConvergences":
		if f.convergences == nil {
			// A control plane without the observer: the readers degrade
			// (the rollout wait judges workloads directly; the hub check
			// says it could not look).
			connectErr(w, http.StatusNotFound, "unimplemented", "no such procedure "+r.URL.Path)
			return
		}
		rows := f.convergences[str("environmentId")]
		if rows == nil {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"convergences": rows})

	default:
		connectErr(w, http.StatusNotFound, "unimplemented", "no such procedure "+r.URL.Path)
	}
}

// nextPromotionSeq hands out the next promotion id, across every
// environment. Called under f.mu.
func (f *fakeDeployService) nextPromotionSeq() int {
	n := 0
	for _, list := range f.promotions {
		n += len(list)
	}
	return n + 1
}

// resolveFromPromotion is C6's server-side `promote --from` resolution
// (control-plane internal/deploystore/store.go, §3.4), modelled in the same
// order and with the same two refusals: the version comes from the named
// source promotion, and the promote is refused if that promotion is no longer
// the source environment's current one, or if a version supplied alongside it
// disagrees. Returns the resolved version, or the refusal's detail sentence.
//
// Called under f.mu.
func (f *fakeDeployService) resolveFromPromotion(sourceID, fromEnvID, version string) (string, string) {
	for envID, list := range f.promotions {
		for i, p := range list {
			if p.ID != sourceID {
				continue
			}
			if fromEnvID != "" && envID != fromEnvID {
				// Scoped like the server's `environment_id = $3`: an
				// id that belongs to another environment is not found.
				continue
			}
			if i != len(list)-1 {
				current := list[len(list)-1]
				return "", fmt.Sprintf(
					"environment %q is no longer on promotion %s (it is on %s (promotion %s))",
					envID, sourceID, current.ReleaseVersion, current.ID)
			}
			if version != "" && version != p.ReleaseVersion {
				return "", fmt.Sprintf("promotion %s of %q is on %s, not the requested %s",
					sourceID, envID, p.ReleaseVersion, version)
			}
			return p.ReleaseVersion, ""
		}
	}
	return "", fmt.Sprintf("source promotion %q not found", sourceID)
}

func (f *fakeDeployService) callCount(proc string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == "/"+proc {
			n++
		}
	}
	return n
}

func newHostedTestStore(t *testing.T, fake *fakeDeployService) (*hostedStore, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	ep := cloud.Endpoint{Env: "prod", URL: srv.URL, TokenEnv: "T"}
	l := hostedLedger(cloud.NewClient(ep, cloud.Credential{Token: "rlat_test"}), srv.URL, "acme", deploytarget.HostedEnvPersistent)
	return l.Bindings.(*hostedStore), srv
}

// A release's provenance survives the round trip: what Cut sends, List and
// Get read back. Before they did, every release from a control plane came
// back with none, and a no-version deploy could never find the release cut
// for its own checkout (2026-10-09, prod).
func TestHostedStore_ReleaseProvenanceRoundTrips(t *testing.T) {
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	store, _ := newHostedTestStore(t, fake)
	ctx := context.Background()

	cut := ociRelease("20261009.205925-d29da50b", map[string]string{"registry.test/acme/api": sha("1")})
	cut.SetProvenance(release.Provenance{
		Repo: "github.com/reliant-labs/control-plane", Commit: strings.Repeat("d", 40),
		Tree: strings.Repeat("4", 40), ForgeVersion: "v0.1.44-0.20261009203931-bc24a9963fd6",
		Worktree: release.Worktree{Key: "cp-release-20261009", Host: "dcf965e3b8e09c77"},
	})
	if _, err := store.Cut(ctx, cut); err != nil {
		t.Fatalf("cut: %v", err)
	}

	listed, err := store.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %d release(s), err %v; want 1", len(listed), err)
	}
	got, err := store.Get(ctx, cut.Version)
	if err != nil || got == nil {
		t.Fatalf("Get: %v", err)
	}
	for how, rel := range map[string]release.Release{"List": listed[0], "Get": *got} {
		if rel.Provenance == nil {
			t.Fatalf("%s returned no provenance; the cut sent %+v", how, *cut.Provenance)
		}
		if *rel.Provenance != *cut.Provenance {
			t.Errorf("%s provenance = %+v, want what the cut sent %+v", how, *rel.Provenance, *cut.Provenance)
		}
	}
}

// A provenance the control plane returns that fails forge's own validation is
// DROPPED from that release, not fatal to the read: List feeds every
// promote's direction logic, and one bad historical row must not make the
// ledger unreadable. A release without provenance is simply never reused.
func TestReleaseFromWire_DropsAMalformedProvenance(t *testing.T) {
	r, err := releaseFromWire(wireRelease{
		Version: "v1",
		Artifacts: []wireArtifact{{Name: "registry.test/acme/api", Kind: "oci", Mode: "shared",
			Variant: release.SharedVariant, Digest: sha("1")}},
		Provenance: &wireProvenance{Commit: strings.Repeat("c", 40), Tree: "not-a-tree"},
	})
	if err != nil {
		t.Fatalf("a malformed provenance failed the read: %v", err)
	}
	if r.Provenance != nil {
		t.Errorf("a provenance that does not validate was kept: %+v", *r.Provenance)
	}
}

// The hosted store against an httptest Connect server: cut, promote, retry,
// a backwards promote, and Current — with the pin set coming
// from the SERVER, not from what the client sent.
func TestHostedStore_LedgerRoundTrip(t *testing.T) {
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid", "prod-eu": "env-prod-eu"})
	store, _ := newHostedTestStore(t, fake)
	ctx := context.Background()

	if _, bound, err := store.Current(ctx, "prod"); err != nil || bound {
		t.Fatalf("a fresh env must be unbound: bound=%v err=%v", bound, err)
	}

	v1 := ociRelease("v1", map[string]string{"api": sha("1")})
	v1.Artifacts["web"] = release.Artifact{Kind: release.KindGit, Mode: release.ModeSource,
		Source: &release.Source{Repo: "github.com/acme/web", Ref: "v1", Subdir: "web", Commit: "abc"}}
	if created, err := store.Cut(ctx, v1); err != nil || !created {
		t.Fatalf("cut v1: created=%v err=%v", created, err)
	}
	if created, err := store.Cut(ctx, v1); err != nil || created {
		t.Fatalf("an identical re-cut is a retry: created=%v err=%v", created, err)
	}
	if _, err := store.Cut(ctx, ociRelease("v1", map[string]string{"api": sha("9")})); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("a different set under v1 must be ErrReleaseConflict, got %v", err)
	}
	for _, v := range []string{"v2", "v3"} {
		if _, err := store.Cut(ctx, ociRelease(v, map[string]string{"api": sha(v[1:])})); err != nil {
			t.Fatal(err)
		}
	}

	// Round trip through GetRelease: the source pin and kinds survive the
	// one-row-per-(name, variant) wire shape.
	got, err := store.Get(ctx, "v1")
	if err != nil || got == nil {
		t.Fatalf("get v1: %v", err)
	}
	if !got.SameContent(v1) {
		t.Errorf("release did not round-trip:\n got %+v\nwant %+v", got.Artifacts, v1.Artifacts)
	}
	if missing, err := store.Get(ctx, "v404"); err != nil || missing != nil {
		t.Errorf("a never-cut version is (nil, nil), got %v %v", missing, err)
	}

	// The client sends a BOGUS Resolved map; the server must ignore it and
	// freeze from the release it holds.
	p, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote,
		Resolved: map[string]string{"api": sha("f")}}, appendGuard{})
	if err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	if p.Resolved["api"] != sha("1") || p.Env != "prod" || p.Kind != release.KindPromote || p.Release != "v1" {
		t.Errorf("promotion must carry the server-frozen pin and forge names, got %+v", p)
	}
	again, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{})
	if err != nil || again.ID != p.ID {
		t.Fatalf("a retry must return the existing entry %s, got %+v %v", p.ID, again, err)
	}
	if _, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote}, appendGuard{}); err != nil {
		t.Fatal(err)
	}
	// Moving prod back to v1 is an ordinary promote through the Promote RPC.
	back, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote, Note: "5xx"}, appendGuard{})
	if err != nil || back.Kind != release.KindPromote {
		t.Fatalf("backwards promote to v1: %+v %v", back, err)
	}
	if n := fake.callCount("controlplane.v1.DeployService/Rollback"); n != 0 {
		t.Errorf("the retired Rollback RPC was called %d times", n)
	}

	cur, bound, err := store.Current(ctx, "prod")
	if err != nil || !bound || cur.Release != "v1" || cur.Kind != release.KindPromote || cur.Resolved["api"] != sha("1") {
		t.Fatalf("current must be the promote back to v1, got bound=%v %+v err=%v", bound, cur, err)
	}
	// Exactly one env lookup per env name: the id is cached per process.
	if n := fake.callCount("controlplane.v1.DeployService/ListEnvironments"); n != 1 {
		t.Errorf("ListEnvironments called %d times, want 1 (cached)", n)
	}
	for _, a := range fake.auth {
		if a != "Bearer rlat_test" {
			t.Fatalf("every call must carry the bearer token, saw %q", a)
		}
	}
	if len(fake.promotions["env-prod-eu"]) != 0 {
		t.Error("prod-eu (a search near-miss) must never be written")
	}
}

// An env the control plane has never heard of has never been promoted — a
// read answers unbound and creates nothing. Every WRITE ensures the env by
// name first (ensureHostedEnv), so the first PROMOTE of a fresh hosted env
// works. Mutation: dropping the ensure in Append fails the promote half.
func TestHostedStore_UnknownEnvironment(t *testing.T) {
	fake := newFakeDeployService(map[string]string{})
	fake.releases["v1"] = wireRelease{ID: "rel-1", Version: "v1", Artifacts: []wireArtifact{
		{Name: "api", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "oci", Mode: "shared", Variant: release.SharedVariant},
	}}
	store, _ := newHostedTestStore(t, fake)
	if _, bound, err := store.Current(context.Background(), "prod"); err != nil || bound {
		t.Fatalf("read of an unknown env must be unbound, got bound=%v err=%v", bound, err)
	}
	p, err := store.Append(context.Background(), release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{})
	if err != nil {
		t.Fatalf("first promote of a fresh hosted env: %v", err)
	}
	if p.Release != "v1" || fake.envs["prod"] == "" {
		t.Fatalf("promote did not ensure the env: promotion=%+v envs=%v", p, fake.envs)
	}
	if _, bound, err := store.Current(context.Background(), "prod"); err != nil || !bound {
		t.Fatalf("after the first promote the env must be bound, got bound=%v err=%v", bound, err)
	}
}

// An unknown promotion kind from the server is refused, never read as a
// forward promote.
func TestPromotionKindFromWire_Closed(t *testing.T) {
	if _, err := promotionKindFromWire("DEPLOY_PROMOTION_KIND_UNSPECIFIED"); err == nil {
		t.Fatal("UNSPECIFIED must be refused")
	}
}

// ─── End to end through the command tree ─────────────────────────────────────

// `forge env build prod --release v1 --no-build` → `forge env deploy prod v1` →
// `forge env releases list prod`, for an env whose KCL declares forge.ControlPlane,
// against the fake control plane. Nothing is written to the project's ledger
// files: the env's declaration routed every write to the control plane.
//
// --no-wait because the fake serves no rollout: the subject here is the LEDGER
// write reaching the control plane, and the health gate has its own tests
// (deploy_promote_follow_test.go).
//
// The env is HOSTED-ONLY — its one workload is forge.OnHosted — so the deploy
// records and applies nothing locally. That is deliberate: an env with a
// CLUSTER workload beside its hosted database would be MIXED, and `forge env
// deploy` would (correctly) go on to apply that half from here, which needs a
// kubeconfig and a complete forge.yaml and would make this test about the
// apply rather than about where the promotion was recorded. The mixed shape is
// covered by TestDeployRelease_MixedEnvAppliesItsClusterHalfAndWaits.
func TestHostedLedger_CutPromoteListEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareEnvDir(t, dir, "prod")
	t.Chdir(dir)
	t.Setenv("FORGE_E2E_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fmt.Sprintf(
		`{"output":{"control_plane":{"type":"control_plane","endpoint":%q,"token_env":"FORGE_E2E_CP_TOKEN"},"workloads":[{"name":"api","kind":"service","image":"api","runtime":{"type":"hosted"},"spec":{"kind":"service"}}],"databases":[{"name":"orders","runtime":"hosted"}]}}`, srv.URL)))
	// The build state an earlier `forge env build prod --push` left behind.
	if err := WriteBuildState(dir, "prod", BuildState{
		Image: "api", Tag: "v1", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("1"),
	}); err != nil {
		t.Fatal(err)
	}

	// This test's subject is the LEDGER — cut, promote, list, through the
	// real hosted store. A hosted deploy also publishes (applyHostedPublish),
	// which loads the project config and renders; this fixture is a ledger
	// fixture, not a deployable project, so the apply is stubbed. What the
	// publish SENDS is covered by TestHostedCLIEndToEnd against a real
	// project.
	prevApply := runPromoteClientDeploy
	runPromoteClientDeploy = func(context.Context, string, deployOptions) error { return nil }
	t.Cleanup(func() { runPromoteClientDeploy = prevApply })
	// No registry stands behind this fixture's digests; the platform guard has
	// its own tests (hosted_image_arch_test.go).
	prevPlatforms := hostedPlatformResolver
	hostedPlatformResolver = func(context.Context, string) ([]string, error) { return []string{"linux/amd64"}, nil }
	t.Cleanup(func() { hostedPlatformResolver = prevPlatforms })

	run := func(args ...string) (string, error) {
		root := newTestRootCmd(t)
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(args)
		var err error
		out := captureStdout(t, func() { err = root.Execute() })
		return out + buf.String(), err
	}

	if out, err := run("env", "build", "prod", "--release", "v1", "--no-build"); err != nil {
		t.Fatalf("forge env build --release: %v\n%s", err, out)
	}
	if out, err := run("env", "deploy", "prod", "v1", "--yes", "--actor", "ci", "--no-wait"); err != nil {
		t.Fatalf("forge env deploy: %v\n%s", err, out)
	}
	out, err := run("env", "releases", "list", "prod", "--json", "--since", "2026-01-01")
	if err != nil {
		t.Fatalf("forge env releases list: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"version": "v1"`) || !strings.Contains(out, `"backend": "cloud"`) {
		t.Errorf("env releases list must show the hosted promotion of v1, got:\n%s", out)
	}

	if n := fake.callCount(procCutRelease); n != 1 {
		t.Errorf("CutRelease calls = %d, want 1", n)
	}
	if n := fake.callCount(procPromote); n != 1 {
		t.Errorf("Promote calls = %d, want 1", n)
	}
	list := fake.promotions["env-prod-uuid"]
	if len(list) != 1 || list[0].ReleaseVersion != "v1" || list[0].ResolvedArtifacts["api"] != sha("1") || list[0].PromotedByActor != "ci" {
		t.Fatalf("the control plane must hold prod→v1 pinned to the pushed digest, got %+v", list)
	}
	// Nothing lands in the checkout. The retired locations are named
	// literally rather than through constants, because the constants are
	// gone and this assertion is precisely that those PATHS stay empty.
	for _, p := range []string{".forge/releases", ".forge/promotions"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s must not exist — a hosted env's ledger is the control plane (stat err %v)", p, err)
		}
	}

	// And deploy resolves the digest from the SAME ledger.
	bindings, err := bindingStoreFor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	digests, bound, err := resolveDeployDigests(context.Background(), dir, "prod", false, bindings, testReleases(t, dir))
	if err != nil || bound != "v1" || digests["api"] != sha("1") {
		t.Fatalf("deploy must pin from the hosted ledger: rel=%q digests=%v err=%v", bound, digests, err)
	}
}

// hostedRecordsFromBundleBlobs is the fake control plane reading a recorded
// bundle the way the real one does: fetch the manifests layer from the
// registry by the manifest digest, and read the forge.dev records under the
// hosted tree. Keyed by metadata.name; Tier and Spec are what a test asserts.
func hostedRecordsFromBundleBlobs(
	digest, repository string, fetch func(repository, digest string) ([]byte, error),
) (map[string]*fakeDeployment, error) {
	if fetch == nil {
		return nil, fmt.Errorf("the fake control plane has no registry to read the bundle from")
	}
	layer, err := fetch(repository, digest)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(os.TempDir(), "fake-cp-unpack-"+digest[7:19])
	_ = os.RemoveAll(dest)
	if _, err := bundle.Unpack(bytes.NewReader(layer), dest); err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dest) }()
	tree := filepath.Join(dest, release.BundleClusterPath(release.BundleHostedCluster))
	entries, err := os.ReadDir(tree)
	if errors.Is(err, os.ErrNotExist) {
		// A bundle with no hosted tree (an env with nothing hosted, or one
		// recorded before its release pins the tiers) records fine; it just
		// gives Flux nothing to apply for hosted.
		return map[string]*fakeDeployment{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*fakeDeployment{}
	tiers := map[string]string{"Workload": "DEPLOY_TIER_BACKEND", "ManagedDatabase": "DEPLOY_TIER_DATABASE", "StaticSite": "DEPLOY_TIER_STATIC"}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(tree, e.Name()))
		if err != nil {
			return nil, err
		}
		var doc struct {
			Kind     string                `json:"kind"`
			Metadata struct{ Name string } `json:"metadata"`
			Spec     map[string]any        `json:"spec"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		tier, ok := tiers[doc.Kind]
		if !ok {
			return nil, fmt.Errorf("the hosted tree carries a %s", doc.Kind)
		}
		out[doc.Metadata.Name] = &fakeDeployment{ID: "dep-" + doc.Metadata.Name, Tier: tier, Spec: doc.Spec, Published: true}
	}
	return out, nil
}

// stubHostedBundleRegistry points the bundle push at an in-memory registry and
// hands the fake control plane the way to read back what was pushed — the one
// place a hosted E2E test's registry exists, so the push and the platform's
// read cannot disagree.
func stubHostedBundleRegistry(t *testing.T, fake *fakeDeployService) {
	t.Helper()
	registry := memory.New()
	prev := bundlePushTarget
	bundlePushTarget = func(context.Context, string, string, string) (bundle.Pusher, error) {
		return tagByDigest{registry}, nil
	}
	t.Cleanup(func() { bundlePushTarget = prev })
	fake.layerFetch = func(_, manifestDigest string) ([]byte, error) {
		fetched, err := bundle.Fetch(context.Background(), registry, manifestDigest)
		if err != nil {
			return nil, err
		}
		return fetched.Manifests, nil
	}
}

// tagByDigest tags every manifest pushed to it with its own digest string, so
// the control plane's fetch-by-digest resolves in an in-memory store — which,
// unlike a real registry, only resolves what it was told is tagged.
type tagByDigest struct{ *memory.Store }

func (t tagByDigest) Push(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error {
	if err := t.Store.Push(ctx, desc, r); err != nil {
		return err
	}
	if desc.MediaType == ocispec.MediaTypeImageManifest {
		return t.Store.Tag(ctx, desc, desc.Digest.String())
	}
	return nil
}

// D4 + D5 over the wire: a cut sends its commit subject, author and range and
// reads them back; a scoped promote sends its targets, reads them back, and
// the server's append rule treats the whole deploy that follows as a move.
func TestHostedStore_CommitsAndTargetsRoundTrip(t *testing.T) {
	fake := newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})
	store, _ := newHostedTestStore(t, fake)
	ctx := context.Background()

	cut := ociRelease("v2", map[string]string{"registry.test/acme/api": sha("1")})
	cut.SetProvenance(release.Provenance{
		Commit: strings.Repeat("d", 40), CommitSubject: "fix(gateway): round per request (#973)", CommitAuthor: "sean",
	})
	cut.Commits = &release.CommitRange{
		BaseRelease: "v1", BaseCommit: strings.Repeat("c", 40), Total: 37, Truncated: true,
		Commits: []release.Commit{{SHA: strings.Repeat("d", 40), Subject: "fix(gateway): round per request (#973)", Author: "sean",
			Time: time.Date(2026, 10, 18, 6, 37, 0, 0, time.UTC)}},
	}
	if _, err := store.Cut(ctx, cut); err != nil {
		t.Fatalf("cut: %v", err)
	}
	got, err := store.Get(ctx, "v2")
	if err != nil || got == nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Provenance == nil || got.Provenance.CommitSubject != cut.Provenance.CommitSubject || got.Provenance.CommitAuthor != "sean" {
		t.Errorf("provenance read back = %+v", got.Provenance)
	}
	if got.Commits == nil || !reflect.DeepEqual(*got.Commits, *cut.Commits) {
		t.Errorf("commits read back = %+v, want %+v", got.Commits, cut.Commits)
	}

	scoped, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote, Targets: []string{"api"}}, appendGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scoped.Targets, []string{"api"}) {
		t.Fatalf("scoped promotion read back with targets %v", scoped.Targets)
	}
	retry, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote, Targets: []string{"api"}}, appendGuard{})
	if err != nil || retry.ID != scoped.ID {
		t.Fatalf("a retried scoped promote must not append: %v, %s vs %s", err, retry.ID, scoped.ID)
	}
	whole, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote}, appendGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if whole.ID == scoped.ID || whole.Scoped() {
		t.Fatalf("the whole deploy after a scoped one is a new entry: %+v", whole)
	}
}
