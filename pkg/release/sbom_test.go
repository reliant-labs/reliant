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
