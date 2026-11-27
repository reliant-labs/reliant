package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func sbomTestDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func sbomTestRelease(sboms map[string]*SBOMRef) Release {
	r := Release{
		Version:   "v1.0.0",
		CreatedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Artifacts: map[string]Artifact{
			"api": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("a")}},
			"web": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("b")}},
		},
	}
	for name, ref := range sboms {
		a := r.Artifacts[name]
		a.SBOM = ref
		r.Artifacts[name] = a
	}
	return r
}

func okSBOM(c, generator string) *SBOMRef {
	return &SBOMRef{Digest: sbomTestDigest(c), Format: SBOMFormatCycloneDXJSON, Generator: generator, Source: SBOMSourceImage, Status: SBOMStatusOK}
}

// A re-cut of the same digests is the same release whatever the SBOMs say:
// none then one, or one generator's then a newer one's.
func TestSameContentIgnoresSBOM(t *testing.T) {
	t.Parallel()
	none := sbomTestRelease(nil)
	old := sbomTestRelease(map[string]*SBOMRef{"api": okSBOM("c", "syft@v1.57.2")})
	newer := sbomTestRelease(map[string]*SBOMRef{"api": okSBOM("d", "syft@v1.60.0"), "web": okSBOM("e", "syft@v1.60.0")})
	for _, pair := range [][2]Release{{none, old}, {old, none}, {old, newer}} {
		if !pair[0].SameContent(pair[1]) {
			t.Fatalf("SBOM changed the release's identity")
		}
		if err := CheckRecut(pair[0], pair[1]); err != nil {
			t.Fatalf("CheckRecut refused an SBOM-only difference: %v", err)
		}
	}
	// ...while a different digest is still a different release.
	moved := sbomTestRelease(nil)
	moved.Artifacts["api"] = Artifact{Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("f")}}
	if err := CheckRecut(old, moved); !errors.Is(err, ErrReleaseConflict) {
		t.Fatalf("CheckRecut accepted a different digest: %v", err)
	}
}

func TestBackfillSBOMsFillsButNeverReplaces(t *testing.T) {
	t.Parallel()
	unavailable := &SBOMRef{Status: SBOMStatusUnavailable, Reason: "syft missing"}

	tests := []struct {
		name        string
		existing    map[string]*SBOMRef
		requested   map[string]*SBOMRef
		wantChanged bool
		want        map[string]*SBOMRef
	}{
		{
			name:        "record has none: filled",
			requested:   map[string]*SBOMRef{"api": okSBOM("c", "syft@v1")},
			wantChanged: true,
			want:        map[string]*SBOMRef{"api": okSBOM("c", "syft@v1")},
		},
		{
			name:      "record has one: kept, never replaced",
			existing:  map[string]*SBOMRef{"api": okSBOM("c", "syft@v1")},
			requested: map[string]*SBOMRef{"api": okSBOM("d", "syft@v2")},
			want:      map[string]*SBOMRef{"api": okSBOM("c", "syft@v1")},
		},
		{
			name:        "record says unavailable: a real SBOM fills it",
			existing:    map[string]*SBOMRef{"api": unavailable},
			requested:   map[string]*SBOMRef{"api": okSBOM("d", "syft@v2")},
			wantChanged: true,
			want:        map[string]*SBOMRef{"api": okSBOM("d", "syft@v2")},
		},
		{
			name:      "both unavailable: the first reason stands",
			existing:  map[string]*SBOMRef{"api": unavailable},
			requested: map[string]*SBOMRef{"api": {Status: SBOMStatusUnavailable, Reason: "other"}},
			want:      map[string]*SBOMRef{"api": unavailable},
		},
		{
			name:        "record has none, retry says unavailable: recorded",
			requested:   map[string]*SBOMRef{"api": unavailable},
			wantChanged: true,
			want:        map[string]*SBOMRef{"api": unavailable},
		},
		{
			name:        "only the missing artifact is filled",
			existing:    map[string]*SBOMRef{"api": okSBOM("c", "syft@v1")},
			requested:   map[string]*SBOMRef{"api": okSBOM("d", "syft@v2"), "web": okSBOM("e", "syft@v2")},
			wantChanged: true,
			want:        map[string]*SBOMRef{"api": okSBOM("c", "syft@v1"), "web": okSBOM("e", "syft@v2")},
		},
		{
			name: "retry carries none: nothing to do",
			existing: map[string]*SBOMRef{
				"api": okSBOM("c", "syft@v1"),
			},
			want: map[string]*SBOMRef{"api": okSBOM("c", "syft@v1")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			existing := sbomTestRelease(tt.existing)
			before, _ := json.Marshal(existing)
			got, changed := BackfillSBOMs(existing, sbomTestRelease(tt.requested))
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if after, _ := json.Marshal(existing); !bytes.Equal(before, after) {
				t.Fatalf("BackfillSBOMs modified the existing record")
			}
			for _, name := range []string{"api", "web"} {
				g, w := got.Artifacts[name].SBOM, tt.want[name]
				if (g == nil) != (w == nil) || (g != nil && *g != *w) {
					t.Fatalf("%s: SBOM = %+v, want %+v", name, g, w)
				}
			}
			if !got.SameContent(existing) {
				t.Fatalf("backfill changed the release's identity")
			}
		})
	}
}

func TestArtifactValidateSBOM(t *testing.T) {
	t.Parallel()
	base := Artifact{Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("a")}}
	tests := []struct {
		name string
		ref  *SBOMRef
		ok   bool
	}{
		{"none", nil, true},
		{"ok", okSBOM("c", "syft@v1"), true},
		{"unavailable with a reason", &SBOMRef{Status: SBOMStatusUnavailable, Reason: "no syft"}, true},
		// An open vocabulary on read: a value a newer forge writes must not
		// make this forge refuse the ledger line that carries it.
		{"unknown status and source", &SBOMRef{Digest: sbomTestDigest("c"), Status: "partial", Source: "sbom-service"}, true},
		{"tag, not a digest", &SBOMRef{Digest: "latest", Status: SBOMStatusOK}, false},
		{"ok without a digest", &SBOMRef{Status: SBOMStatusOK}, false},
		{"referrer without a digest", &SBOMRef{Referrer: "r@" + sbomTestDigest("d")}, false},
		{"not applicable with a reason", &SBOMRef{Status: SBOMStatusNotApplicable, Reason: SBOMNotApplicableSourcemaps}, true},
		{"not applicable naming an SBOM", &SBOMRef{Status: SBOMStatusNotApplicable, Digest: sbomTestDigest("c")}, false},
	}
	for _, tt := range tests {
		a := base
		a.SBOM = tt.ref
		err := a.Validate("api")
		if (err == nil) != tt.ok {
			t.Fatalf("%s: Validate = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v does not wrap ErrInvalid", tt.name, err)
		}
	}
}

// preSBOMArtifact and preSBOMRelease are the release record exactly as a
// forge without Artifact.SBOM declares it (v1.8.0), field for field and tag
// for tag, trimmed to what the artifact map needs.
type preSBOMArtifact struct {
	Kind         Kind              `json:"kind"`
	Mode         Mode              `json:"mode"`
	Digests      map[string]string `json:"digests,omitempty"`
	Platforms    []string          `json:"platforms,omitempty"`
	Source       *Source           `json:"source,omitempty"`
	Version      string            `json:"version,omitempty"`
	Integrity    string            `json:"integrity,omitempty"`
	URI          string            `json:"uri,omitempty"`
	BuiltFrom    *BuildSource      `json:"built_from,omitempty"`
	InputsDigest string            `json:"inputs_digest,omitempty"`
	CarriedFrom  string            `json:"carried_from,omitempty"`
}

type preSBOMRelease struct {
	Version   string                     `json:"release"`
	Git       Git                        `json:"git"`
	CreatedAt time.Time                  `json:"created_at"`
	Artifacts map[string]preSBOMArtifact `json:"artifacts"`
}

// TestOlderDecoderIgnoresSBOM is the semver proof for Artifact.SBOM: a
// release line this forge writes, read by a forge that predates the field
// through the decoder every lenient reader uses (the machine ledger's
// decodeAll and the hosted-ledger mirror are plain json.Unmarshal), decodes
// to the same artifacts with the field dropped.
//
// The STRICT readers of a Release — a snapshot release's CutRelease document,
// decoded with DisallowUnknownFields by the control plane and by forge's
// snapshotReleaseFromJSON — would refuse it, which is why a snapshot
// release may never carry an SBOM (TestSnapshotReleaseNeverCarriesSBOM).
func TestOlderDecoderIgnoresSBOM(t *testing.T) {
	t.Parallel()
	r := sbomTestRelease(map[string]*SBOMRef{"api": okSBOM("c", "syft@v1.57.2")})
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, []byte(`"sbom":{`)) {
		t.Fatalf("the line under test carries no sbom: %s", line)
	}

	var old preSBOMRelease
	if err := json.Unmarshal(line, &old); err != nil {
		t.Fatalf("an older forge's decoder refused a line with sbom: %v", err)
	}
	if old.Version != r.Version || len(old.Artifacts) != len(r.Artifacts) {
		t.Fatalf("decoded %+v", old)
	}
	for name, a := range r.Artifacts {
		if got := old.Artifacts[name]; got.Digests[SharedVariant] != a.Digests[SharedVariant] || got.Kind != a.Kind || got.Mode != a.Mode {
			t.Fatalf("%s: decoded %+v, want the identity of %+v", name, got, a)
		}
	}

	// And the counterfactual that makes the snapshot guard necessary.
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&preSBOMRelease{}); err == nil {
		t.Fatalf("a strict decoder accepted an unknown field; the snapshot guard's premise changed")
	}
}

func snapshotTestRelease(t *testing.T) Release {
	t.Helper()
	d := sbomTestDigest("a")
	a := SnapshotArtifact{Kind: KindOCI, Location: "registry.example/app", Content: d, Origin: ArtifactOrigin{Release: "v1", Source: &Source{Repo: "example/app", Commit: strings.Repeat("b", 40)}}}
	id, err := ArtifactIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	return Release{Version: "v1", Project: "p", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Catalog: map[ArtifactID]SnapshotArtifact{id: a}, SnapshotIntents: map[string]string{"prod": d}}
}

// A snapshot release is decoded strictly by readers that predate SBOMs, so
// it may not carry one, and what it serializes never names the field.
func TestSnapshotReleaseNeverCarriesSBOM(t *testing.T) {
	t.Parallel()
	r := snapshotTestRelease(t)
	if err := r.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("sbom")) {
		t.Fatalf("a snapshot release's document names sbom: %s", raw)
	}

	r.Artifacts = map[string]Artifact{"app": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("a")}}}
	if err := r.Validate(); err != nil {
		t.Fatalf("a snapshot release without SBOMs: %v", err)
	}
	a := r.Artifacts["app"]
	a.SBOM = okSBOM("c", "syft@v1")
	r.Artifacts["app"] = a
	if err := r.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate accepted a snapshot release carrying an SBOM: %v", err)
	}
	stripped := r.WithoutSBOMs()
	if err := stripped.Validate(); err != nil {
		t.Fatalf("WithoutSBOMs left it invalid: %v", err)
	}
	if r.Artifacts["app"].SBOM == nil {
		t.Fatalf("WithoutSBOMs modified its receiver")
	}
	if raw, _ := json.Marshal(stripped); bytes.Contains(raw, []byte("sbom")) {
		t.Fatalf("WithoutSBOMs left an sbom: %s", raw)
	}
}

// A release's source SBOM is evidence like an artifact's: it never changes
// the release's identity, a re-cut fills it but never replaces it, and a
// snapshot release refuses it (and WithoutSBOMs strips it).
func TestSourceSBOMIsEvidenceNotIdentity(t *testing.T) {
	t.Parallel()
	recorded := Release{Version: "v1.4.0", RecordOnly: true, Git: Git{Commit: "0123456789abcdef0123456789abcdef01234567"}}
	withSource := recorded
	withSource.SourceSBOM = &SBOMRef{Digest: sbomTestDigest("c"), Format: SBOMFormatCycloneDXJSON, Source: SBOMSourceLockfile, Status: SBOMStatusOK}
	if err := withSource.Validate(); err != nil {
		t.Fatalf("a version-only record with a source SBOM: %v", err)
	}
	for _, pair := range [][2]Release{{recorded, withSource}, {withSource, recorded}} {
		if err := CheckRecut(pair[0], pair[1]); err != nil {
			t.Fatalf("CheckRecut refused a source-SBOM-only difference: %v", err)
		}
	}

	filled, changed := BackfillSBOMs(recorded, withSource)
	if !changed || filled.SourceSBOM == nil || filled.SourceSBOM.Digest != sbomTestDigest("c") || recorded.SourceSBOM != nil {
		t.Fatalf("backfill: %+v changed=%v (existing %+v)", filled.SourceSBOM, changed, recorded.SourceSBOM)
	}
	other := withSource
	other.SourceSBOM = &SBOMRef{Digest: sbomTestDigest("d"), Status: SBOMStatusOK}
	if kept, changed := BackfillSBOMs(withSource, other); changed || kept.SourceSBOM.Digest != sbomTestDigest("c") {
		t.Fatalf("backfill replaced a recorded source SBOM: %+v", kept.SourceSBOM)
	}
	none := recorded
	none.SourceSBOM = &SBOMRef{Status: SBOMStatusUnavailable, Reason: "skipped"}
	if got, changed := BackfillSBOMs(none, withSource); !changed || !got.SourceSBOM.HasSBOM() {
		t.Fatalf("an unavailable source SBOM was not filled: %+v", got.SourceSBOM)
	}
	if _, changed := BackfillSBOMs(none, Release{SourceSBOM: &SBOMRef{Status: SBOMStatusUnavailable, Reason: "other"}}); changed {
		t.Fatal("a second 'none' replaced the first reason")
	}

	bad := withSource
	bad.SourceSBOM = &SBOMRef{Status: SBOMStatusOK}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate accepted an ok source SBOM with no digest: %v", err)
	}

	snap := snapshotTestRelease(t)
	snap.SourceSBOM = withSource.SourceSBOM
	if err := snap.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "source SBOM") {
		t.Fatalf("Validate accepted a snapshot release with a source SBOM: %v", err)
	}
	stripped := snap.WithoutSBOMs()
	if stripped.SourceSBOM != nil || snap.SourceSBOM == nil {
		t.Fatalf("WithoutSBOMs: stripped %+v, receiver %+v", stripped.SourceSBOM, snap.SourceSBOM)
	}
	if raw, _ := json.Marshal(stripped); bytes.Contains(raw, []byte("sbom")) {
		t.Fatalf("WithoutSBOMs left an sbom: %s", raw)
	}
}

// Where an SBOM was attached beside its artifact is filled in for the SAME
// SBOM, never replaced, and never taken from another SBOM.
func TestBackfillSBOMsFillsTheReferrerOfTheSameSBOM(t *testing.T) {
	t.Parallel()
	plain := okSBOM("c", "syft@v1.57.2")
	attached := okSBOM("c", "syft@v1.57.2")
	attached.Referrer = "ghcr.io/acme/api@" + sbomTestDigest("f")
	existing := sbomTestRelease(map[string]*SBOMRef{"api": plain})

	got, changed := BackfillSBOMs(existing, sbomTestRelease(map[string]*SBOMRef{"api": attached}))
	if !changed || got.Artifacts["api"].SBOM.Referrer != attached.Referrer || got.Artifacts["api"].SBOM.Digest != plain.Digest {
		t.Fatalf("referrer not filled: %+v changed=%v", got.Artifacts["api"].SBOM, changed)
	}
	if existing.Artifacts["api"].SBOM.Referrer != "" {
		t.Fatal("BackfillSBOMs modified the existing record")
	}
	other := okSBOM("c", "syft@v1.57.2")
	other.Referrer = "ghcr.io/acme/api@" + sbomTestDigest("e")
	if _, changed := BackfillSBOMs(got, sbomTestRelease(map[string]*SBOMRef{"api": other})); changed {
		t.Fatal("a recorded referrer was replaced")
	}
	newer := okSBOM("d", "syft@v1.60.0")
	newer.Referrer = "ghcr.io/acme/api@" + sbomTestDigest("e")
	if _, changed := BackfillSBOMs(existing, sbomTestRelease(map[string]*SBOMRef{"api": newer})); changed {
		t.Fatal("the referrer of another SBOM was taken")
	}
}

// SBOMApplicable is the one rule the cut records by and every coverage count
// judges by: code that ships has an SBOM; source maps and git source pins do
// not, and say why.
func TestSBOMApplicable(t *testing.T) {
	t.Parallel()
	image := Artifact{Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("a")}}
	gitPin := Artifact{Kind: KindGit, Mode: ModeSource, Source: &Source{Repo: "github.com/acme/app", Ref: "main"}}
	for _, tc := range []struct {
		name   string
		art    Artifact
		reason string // "" = applicable
	}{
		{"ghcr.io/acme/api", image, ""},
		{"cli-linux", Artifact{Kind: KindFile, Mode: ModeShared, Version: "linux"}, ""},
		{"@acme/sdk", Artifact{Kind: KindNPM, Mode: ModeShared, Version: "1.0.0"}, ""},
		{"example.com/acme/pkg", Artifact{Kind: KindGoModule, Mode: ModeShared, Version: "v0.1.0"}, ""},
		{SourcemapsArtifactName("web"), image, SBOMNotApplicableSourcemaps},
		{"web", gitPin, SBOMNotApplicableGitSource},
	} {
		reason, na := SBOMNotApplicable(tc.name, tc.art)
		if na != (tc.reason != "") || reason != tc.reason || SBOMApplicable(tc.name, tc.art) == na {
			t.Errorf("%s: SBOMNotApplicable = %q, %v; want %q", tc.name, reason, na, tc.reason)
		}
		ref, ok := NotApplicableSBOMRef(tc.name, tc.art)
		if ok != na || (ok && (ref.Status != SBOMStatusNotApplicable || ref.Reason != tc.reason || ref.HasSBOM() || !ref.NotApplicable())) {
			t.Errorf("%s: NotApplicableSBOMRef = %+v, %v", tc.name, ref, ok)
		}
		if ok {
			a := tc.art
			a.SBOM = &ref
			if err := a.Validate(tc.name); err != nil {
				t.Errorf("%s: a not-applicable ref does not validate: %v", tc.name, err)
			}
		}
	}
}

// The semver proof for SBOMStatusNotApplicable: an older forge (one that
// knows only ok and unavailable) reads a release carrying it, validates it,
// and finds no SBOM on that artifact. Its coverage leaves the same artifacts
// out by kind and name, so the value changes no older verdict.
func TestOlderReaderToleratesNotApplicable(t *testing.T) {
	t.Parallel()
	r := sbomTestRelease(map[string]*SBOMRef{"api": okSBOM("c", "syft@v1.57.2")})
	r.Artifacts["web.sourcemaps"] = Artifact{Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sbomTestDigest("e")},
		SBOM: &SBOMRef{Status: SBOMStatusNotApplicable, Reason: SBOMNotApplicableSourcemaps}}
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, []byte(`"status":"not_applicable"`)) {
		t.Fatalf("the line under test carries no not_applicable ref: %s", line)
	}
	var back Release
	if err := json.Unmarshal(line, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("a release carrying not_applicable does not validate: %v", err)
	}
	// What an older reader's validate checks, with the status it does not know.
	unknown := *back.Artifacts["web.sourcemaps"].SBOM
	unknown.Status = "some-future-status"
	if err := unknown.validate(); err != nil || unknown.HasSBOM() {
		t.Fatalf("an unknown status without a digest: validate %v, HasSBOM %v", err, unknown.HasSBOM())
	}
	var old preSBOMRelease
	if err := json.Unmarshal(line, &old); err != nil || len(old.Artifacts) != len(r.Artifacts) {
		t.Fatalf("a forge without Artifact.SBOM refused the line: %v (%d artifacts)", err, len(old.Artifacts))
	}
}
