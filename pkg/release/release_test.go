package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func image(d string) Artifact {
	return Artifact{Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: d}}
}

// Closed enums are the point of the package: an unknown or EMPTY kind must
// fail to decode rather than silently meaning "oci", which is how a package
// would end up treated as a pinnable image.
func TestKindAndMode_ClosedOnDecode(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"","mode":"shared"}`,
		`{"kind":"OCI","mode":"shared"}`,
		`{"kind":"docker","mode":"shared"}`,
		`{"kind":"oci","mode":""}`,
		`{"kind":"oci","mode":"sometimes"}`,
	} {
		var a Artifact
		err := json.Unmarshal([]byte(raw), &a)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("decode %s: want ErrInvalid, got %v", raw, err)
		}
	}
	var ok Artifact
	if err := json.Unmarshal([]byte(`{"kind":"oci","mode":"shared","digests":{"*":"`+digest("a")+`"}}`), &ok); err != nil {
		t.Fatalf("a well-formed artifact must decode: %v", err)
	}
}

// A release whose artifacts are missing the kind field entirely (every ledger
// written before kinds were required) is refused too: absent is not "oci".
func TestRelease_MissingKindRefused(t *testing.T) {
	raw := `{"release":"v1","created_at":"2026-01-01T00:00:00Z","artifacts":{"api":{"mode":"shared","digests":{"*":"` + digest("a") + `"}}}}`
	var r Release
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := r.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a kindless artifact must fail validation, got %v", err)
	}
}

func TestArtifact_Validate(t *testing.T) {
	cases := []struct {
		name string
		a    Artifact
		ok   bool
	}{
		{"shared image", image(digest("a")), true},
		{"image with tag", image("latest"), false},
		{"image with no digest", Artifact{Kind: KindOCI, Mode: ModeShared}, false},
		{"variant image", Artifact{Kind: KindOCI, Mode: ModeVariant, Digests: map[string]string{"prod": digest("a"), "staging": digest("b")}}, true},
		{"variant image on shared key", Artifact{Kind: KindOCI, Mode: ModeVariant, Digests: map[string]string{SharedVariant: digest("a")}}, false},
		{"npm package", Artifact{Kind: KindNPM, Mode: ModeShared, Version: "0.3.1", Integrity: "sha512-x"}, true},
		{"npm package with digest", Artifact{Kind: KindNPM, Mode: ModeShared, Version: "0.3.1", Digests: map[string]string{SharedVariant: digest("a")}}, false},
		{"unaddressable file", Artifact{Kind: KindFile, Mode: ModeShared, Integrity: digest("a")}, false},
		{"git source", Artifact{Kind: KindGit, Mode: ModeSource, Source: &Source{Repo: "github.com/x/y", Ref: "v1", Commit: "abc"}}, true},
		{"git without source", Artifact{Kind: KindGit, Mode: ModeSource}, false},
		{"image in source mode", Artifact{Kind: KindOCI, Mode: ModeSource, Source: &Source{Repo: "r", Ref: "v"}}, false},
	}
	for _, tc := range cases {
		err := tc.a.Validate("x")
		if (err == nil) != tc.ok {
			t.Errorf("%s: ok=%v, err=%v", tc.name, tc.ok, err)
		}
	}
}

// Only shared images pin; packages and source pins never leak into the
// digest set a pod spec is built from.
func TestRelease_SharedDigestsAndSources(t *testing.T) {
	src := Source{Repo: "github.com/x/web", Ref: "v1", Commit: "abc"}
	r := Release{Version: "v1", Artifacts: map[string]Artifact{
		"api": image(digest("a")),
		"pkg": {Kind: KindNPM, Mode: ModeShared, Version: "1.0.0"},
		"web": {Kind: KindGit, Mode: ModeSource, Source: &src},
		"var": {Kind: KindOCI, Mode: ModeVariant, Digests: map[string]string{"prod": digest("b")}},
	}}
	if got := r.SharedDigests(); len(got) != 1 || got["api"] != digest("a") {
		t.Errorf("SharedDigests = %v, want only api", got)
	}
	if got := r.Sources(); len(got) != 1 || got["web"] != src {
		t.Errorf("Sources = %v, want only web", got)
	}
}

// Re-cutting a version is a retry when the bytes match and a conflict when
// they do not; platforms are informational and do not count.
func TestCheckRecut(t *testing.T) {
	a := Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}}
	retry := a
	retry.Artifacts = map[string]Artifact{"api": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: digest("a")}, Platforms: []string{"linux/amd64"}}}
	if err := CheckRecut(a, retry); err != nil {
		t.Errorf("same bytes must be a retry: %v", err)
	}
	diff := Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("b"))}}
	if err := CheckRecut(a, diff); !errors.Is(err, ErrReleaseConflict) {
		t.Errorf("different bytes must conflict, got %v", err)
	}
}

func TestDecide(t *testing.T) {
	p := func(rel string, kind PromotionKind) Promotion {
		return Promotion{Env: "prod", Release: rel, Kind: kind}
	}
	history := []Promotion{p("v1", KindPromote), p("v2", KindPromote)}

	if cur, err := Decide(history, p("v2", KindPromote)); err != nil || cur == nil || cur.Release != "v2" {
		t.Errorf("re-promoting the current release must be a no-op returning it, got %v %v", cur, err)
	}
	if cur, err := Decide(history, p("v1", KindPromote)); err != nil || cur != nil {
		t.Errorf("v2→v1 is a real move and must append, got %v %v", cur, err)
	}
	if cur, err := Decide(nil, p("v1", KindPromote)); err != nil || cur != nil {
		t.Errorf("first promote must append, got %v %v", cur, err)
	}
}

// TestPromotionKind_LegacyRollbackReadsAsPromote: ledgers written before
// rollback was removed carry `"kind":"rollback"` entries. They must still
// parse — an unreadable ledger would strand every env that ever rolled back —
// and they read as the promotion of that release they mechanically were.
// Nothing can WRITE the retired kind: it is not Valid.
func TestPromotionKind_LegacyRollbackReadsAsPromote(t *testing.T) {
	line := `{"env":"prod","release":"v1.6.0","kind":"rollback","resolved":{},"promoted_at":"2026-09-01T00:00:00Z"}`
	var p Promotion
	if err := json.Unmarshal([]byte(line), &p); err != nil {
		t.Fatalf("legacy rollback entry no longer parses: %v", err)
	}
	if p.Kind != KindPromote || p.Release != "v1.6.0" {
		t.Fatalf("legacy rollback entry decoded as %+v, want a promote of v1.6.0", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("decoded legacy entry fails Validate: %v", err)
	}
	if PromotionKind("rollback").Valid() {
		t.Fatal(`"rollback" is a writable promotion kind again`)
	}
	var k PromotionKind
	if err := json.Unmarshal([]byte(`"revert"`), &k); err == nil {
		t.Fatal("an unknown kind decoded instead of being refused")
	}
	// The same rule for a database row, which is read without JSON.
	for stored, want := range map[string]PromotionKind{"promote": KindPromote, "rollback": KindPromote} {
		if got, err := ParsePromotionKind(stored); err != nil || got != want {
			t.Errorf("ParsePromotionKind(%q) = %q, %v; want %q", stored, got, err, want)
		}
	}
	for _, bad := range []string{"", "revert", "Promote"} {
		if _, err := ParsePromotionKind(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParsePromotionKind(%q) must be refused as ErrInvalid, got %v", bad, err)
		}
	}
}

// BuiltFrom is where an image was built, not what it is: it must name a real
// commit, a source-pinned artifact cannot carry one, and two releases with the
// same digests are the same release whichever checkout built them.
func TestArtifact_BuiltFrom(t *testing.T) {
	commit := strings.Repeat("ab", 20)
	built := image(digest("a"))
	built.BuiltFrom = &BuildSource{Repo: "github.com/acme/sib", Commit: commit}
	if err := built.Validate("sib"); err != nil {
		t.Fatalf("an image with a full-commit built_from is valid: %v", err)
	}

	short := built
	short.BuiltFrom = &BuildSource{Commit: commit[:12]}
	if err := short.Validate("sib"); !errors.Is(err, ErrInvalid) {
		t.Errorf("an abbreviated built_from commit must be refused, got %v", err)
	}

	git := Artifact{Kind: KindGit, Mode: ModeSource, Source: &Source{Repo: "r", Ref: "v1", Commit: commit},
		BuiltFrom: &BuildSource{Commit: commit}}
	if err := git.Validate("web"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a git artifact's commit is its pin; built_from must be refused, got %v", err)
	}

	a := Release{Version: "v1", Artifacts: map[string]Artifact{"sib": built}}
	b := Release{Version: "v1", Artifacts: map[string]Artifact{"sib": image(digest("a"))}}
	if err := CheckRecut(a, b); err != nil {
		t.Errorf("same digests, different built_from: an idempotent re-cut, got %v", err)
	}

	raw, err := json.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}
	var back Artifact
	if err := json.Unmarshal(raw, &back); err != nil || back.BuiltFrom == nil || *back.BuiltFrom != *built.BuiltFrom {
		t.Errorf("built_from must round-trip through the ledger's JSON: %s → %+v, %v", raw, back.BuiltFrom, err)
	}
}

func TestPromotion_Validate(t *testing.T) {
	good := Promotion{Env: "prod", Release: "v1", Kind: KindPromote, Resolved: map[string]string{"api": digest("a")}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid promotion refused: %v", err)
	}
	self := good
	self.FromEnv = "prod"
	if err := self.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("self-promotion must be refused, got %v", err)
	}
	kindless := good
	kindless.Kind = ""
	if err := kindless.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("kindless promotion must be refused, got %v", err)
	}
	var decoded Promotion
	if err := json.Unmarshal([]byte(`{"env":"prod","release":"v1","kind":"undo"}`), &decoded); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown promotion kind must fail to decode, got %v", err)
	}
}
