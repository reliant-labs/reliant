package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestShape_Validate(t *testing.T) {
	ok := Shape{Kind: EnvPersistent, Objects: []ShapeObject{obj("Deployment", "api", "a")}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid shape refused: %v", err)
	}
	cases := map[string]Shape{
		"unknown kind":     {Kind: "cluster"},
		"empty kind":       {},
		"tag not digest":   {Kind: EnvLocal, Objects: []ShapeObject{{Cluster: "c", Kind: "Deployment", Name: "a", Hash: "latest"}}},
		"duplicate object": {Kind: EnvLocal, Objects: []ShapeObject{obj("Deployment", "a", "a"), obj("Deployment", "a", "b")}},
		"duplicate secret": {Kind: EnvLocal, Secrets: []ShapeSecret{{Name: "A"}, {Name: "A"}}},
		// Identity keys are closed so the map can never become a place a
		// secret value hides.
		"open identity key": {Kind: EnvLocal, Objects: []ShapeObject{func() ShapeObject {
			o := obj("Service", "a", "a")
			o.Identity = map[string]string{"password": "hunter2"}
			return o
		}()}},
		"image tag": {Kind: EnvLocal, Objects: []ShapeObject{func() ShapeObject {
			o := obj("Deployment", "a", "a")
			o.Images = map[string]string{"api": "v1"}
			return o
		}()}},
	}
	for name, s := range cases {
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

// Two projections of one render encode identically whatever order the render
// produced, and empty lists encode as [] (not recorded ≠ none).
func TestShape_EncodeCanonical(t *testing.T) {
	a := Shape{Kind: EnvLocal,
		Objects: []ShapeObject{obj("Service", "b", "b"), obj("Deployment", "a", "a")},
		Secrets: []ShapeSecret{{Name: "Z", DeclaredBy: []string{"y", "x"}}, {Name: "A"}},
		Domains: []string{"b.example", "a.example"}}
	b := Shape{Kind: EnvLocal,
		Objects: []ShapeObject{obj("Deployment", "a", "a"), obj("Service", "b", "b")},
		Secrets: []ShapeSecret{{Name: "A"}, {Name: "Z", DeclaredBy: []string{"x", "y"}}},
		Domains: []string{"a.example", "b.example"}}
	ea, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	eb, _ := b.Encode()
	if string(ea) != string(eb) {
		t.Fatalf("canonical encodings differ:\n%s\n%s", ea, eb)
	}
	empty, _ := Shape{Kind: EnvLocal}.Encode()
	if !strings.Contains(string(empty), `"objects":[]`) || !strings.Contains(string(empty), `"workloads":[]`) {
		t.Fatalf("empty lists must encode as []: %s", empty)
	}
}

func TestShape_EncodeBounded(t *testing.T) {
	s := Shape{Kind: EnvLocal}
	for i := 0; len(s.Objects)*120 < MaxShapeBytes+1024; i++ {
		s.Objects = append(s.Objects, ShapeObject{Cluster: "c", Kind: "ConfigMap", Name: strings.Repeat("n", 40) + string(rune('a'+i%26)) + strings.Repeat("x", i%7) + "-" + itoa(i), Hash: digest("a")})
	}
	if _, err := s.Encode(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an oversized shape must be refused, got %v", err)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestDiffShapes(t *testing.T) {
	live := Shape{Kind: EnvPersistent,
		Workloads: []ShapeWorkload{{Name: "api", Runtime: "hosted"}, {Name: "web", Runtime: "hosted"}, {Name: "old", Runtime: "hosted"}},
		Secrets:   []ShapeSecret{{Name: "A"}, {Name: "GONE"}},
		Domains:   []string{"a.example"},
		Objects:   []ShapeObject{obj("Deployment", "api", "a"), obj("Deployment", "old", "b")},
	}
	cand := Shape{Kind: EnvPersistent,
		Workloads: []ShapeWorkload{{Name: "api", Runtime: "hosted"}, {Name: "web", Runtime: "bucket"}, {Name: "new", Runtime: "hosted"}},
		Secrets:   []ShapeSecret{{Name: "A"}, {Name: "NEW"}},
		Domains:   []string{"a.example", "b.example"},
		Objects:   []ShapeObject{obj("Deployment", "api", "c"), obj("Deployment", "new", "d")},
	}
	d := DiffShapes(&live, cand)
	if d.LiveUnknown || d.KindChanged != nil || d.Empty() {
		t.Fatalf("unexpected: %+v", d)
	}
	if len(d.Added) != 1 || d.Added[0].Name != "new" || len(d.Removed) != 1 || d.Removed[0].Name != "old" || len(d.Changed) != 1 {
		t.Fatalf("objects: %+v", d)
	}
	if !reflect.DeepEqual(d.WorkloadsAdded, []string{"new"}) || !reflect.DeepEqual(d.WorkloadsRemoved, []string{"old"}) {
		t.Fatalf("workloads: %+v", d)
	}
	if len(d.RuntimeChanges) != 1 || d.RuntimeChanges[0] != (RuntimeChange{Workload: "web", From: "hosted", To: "bucket"}) {
		t.Fatalf("runtime: %+v", d.RuntimeChanges)
	}
	if len(d.SecretsAdded) != 1 || d.SecretsAdded[0].Name != "NEW" || len(d.SecretsRemoved) != 1 {
		t.Fatalf("secrets: %+v", d)
	}
	if !reflect.DeepEqual(d.DomainsAdded, []string{"b.example"}) || d.DomainsRemoved != nil {
		t.Fatalf("domains: %+v", d)
	}
	if !DiffShapes(&live, live).Empty() {
		t.Fatal("a shape diffed against itself must be empty")
	}
	unknown := DiffShapes(nil, cand)
	if !unknown.LiveUnknown || unknown.Empty() || len(unknown.Added) != 2 {
		t.Fatalf("nil live must be unknown, never empty: %+v", unknown)
	}
}

func TestBundleDoc_DecodeStrict(t *testing.T) {
	doc := BundleDoc{
		Schema: BundleSchema, Project: "cp", Env: "prod", Release: "v1",
		ConfigDigest: digest("c"),
		Pins:         BundlePins{Images: map[string]string{"api": digest("a")}},
		Provenance:   Provenance{Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40), ForgeVersion: "v0.1.44"},
		Shape:        Shape{Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}},
		CreatedAt:    time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeBundleDoc(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(back, doc) {
		t.Fatalf("round trip differs:\n%+v\n%+v", back, doc)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["surprise"] = true
	extra, _ := json.Marshal(m)
	if _, err := DecodeBundleDoc(extra); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown field must be refused, got %v", err)
	}
	doc.Schema = "forge.dev/bundle/v2"
	foreign, _ := json.Marshal(doc)
	if _, err := DecodeBundleDoc(foreign); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a foreign schema must be refused, got %v", err)
	}
}

func TestEnvKind(t *testing.T) {
	for _, k := range EnvKinds {
		if !k.Valid() {
			t.Fatalf("%s invalid", k)
		}
	}
	if EnvSelfManaged.Placed() || EnvLocal.Placed() || !EnvPersistent.Placed() || !EnvPreview.Placed() {
		t.Fatal("only persistent and preview are placed")
	}
	if EnvLocal.Promotable() || !EnvSelfManaged.Promotable() {
		t.Fatal("self-managed is promotable; local is not")
	}
	var k EnvKind
	if err := json.Unmarshal([]byte(`"cluster"`), &k); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind must be refused, got %v", err)
	}
	now := time.Now()
	r := EnvRecord{Name: "prod", Kind: EnvSelfManaged, DeclaredShape: &Shape{Kind: EnvLocal}, DeclaredAt: &now}
	if err := r.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a declared shape disagreeing with the kind must be refused, got %v", err)
	}
}

func TestDeriveApplyState(t *testing.T) {
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	a := Apply{ID: "a", Env: "prod", BundleID: "b", CreatedAt: start, DeadlineAt: start.Add(10 * time.Minute)}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		outcome *ApplyOutcome
		now     time.Time
		want    ApplyState
	}{
		{nil, start.Add(time.Minute), ApplyRunning},
		{nil, start.Add(10 * time.Minute), ApplyAbandoned},
		{&ApplyOutcome{Status: ApplySucceeded}, start.Add(time.Hour), ApplyStateOK},
		{&ApplyOutcome{Status: ApplyFailed}, start, ApplyStateFail},
		{&ApplyOutcome{Status: ApplyTimedOut}, start, ApplyStateTO},
	}
	for _, tc := range cases {
		if got := DeriveApplyState(a, tc.outcome, tc.now); got != tc.want {
			t.Errorf("outcome=%v now=%v: %s, want %s", tc.outcome, tc.now, got, tc.want)
		}
	}
	if ApplyAbandoned.InFlight() || !ApplyRunning.InFlight() {
		t.Fatal("an abandoned apply must not block the next one")
	}
	bad := a
	bad.DeadlineAt = start
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deadline at start must be refused, got %v", err)
	}
	o := ApplyOutcome{ApplyID: "a", Status: ApplySucceeded, FinishedAt: start, ReportedBy: "alice"}
	again := o
	again.ReportedBy = "bob"
	if !o.SameReport(again) {
		t.Fatal("the same report from a retry must be idempotent")
	}
	again.Status = ApplyFailed
	if o.SameReport(again) {
		t.Fatal("a different status is a conflict")
	}
}

// Provenance JSON, with and without a commit time. The field is omitted when
// unset, so a ledger written before it existed (or from a dirty tree) is
// byte-identical to what it was.
func TestProvenance_CommitTimeJSON(t *testing.T) {
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ct := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		p    Provenance
		want string
	}{
		{"without", Provenance{Commit: commit, Tree: tree, ForgeVersion: "v1"},
			`{"repo":"","commit":"` + commit + `","dirty":false,"tree":"` + tree + `","worktree":{"key":""},"forge_version":"v1"}`},
		{"with", Provenance{Commit: commit, Tree: tree, ForgeVersion: "v1", CommitTime: &ct},
			`{"repo":"","commit":"` + commit + `","commit_time":"2026-03-04T05:00:00Z","dirty":false,"tree":"` + tree + `","worktree":{"key":""},"forge_version":"v1"}`},
	} {
		got, err := json.Marshal(tc.p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
		var back Provenance
		if err := json.Unmarshal(got, &back); err != nil || !reflect.DeepEqual(back, tc.p) {
			t.Errorf("%s: round trip: %+v %v", tc.name, back, err)
		}
	}
}

func TestProvenance(t *testing.T) {
	ct := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	good := Provenance{Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40), Worktree: Worktree{Path: "/home/x/repo"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if good.ForHosted().Worktree.Path != "" {
		t.Fatal("a path must never leave the machine")
	}
	for name, p := range map[string]Provenance{
		"short commit":      {Commit: "abc"},
		"tree no commit":    {Tree: strings.Repeat("b", 40)},
		"attest no subject": {Commit: strings.Repeat("a", 40), Attestation: &Attest{Provider: "github-oidc"}},
		"time no commit":    {CommitTime: &ct},
	} {
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	var r Release
	r.SetProvenance(Provenance{Commit: strings.Repeat("c", 40), Tag: "v1", Dirty: true})
	if r.Git != (Git{Commit: strings.Repeat("c", 40), Tag: "v1", Dirty: true}) {
		t.Fatalf("legacy Git view not kept in step: %+v", r.Git)
	}
	// A release written before Provenance existed still decodes.
	var old Release
	if err := json.Unmarshal([]byte(`{"release":"v1","git":{"commit":"abc"},"created_at":"2026-01-01T00:00:00Z","artifacts":{}}`), &old); err != nil || old.Provenance != nil {
		t.Fatalf("old release: %v %+v", err, old)
	}
}

func TestCanonicalRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:reliant-labs/forge.git":                "github.com/reliant-labs/forge",
		"https://github.com/reliant-labs/forge.git":            "github.com/reliant-labs/forge",
		"https://x-token:SECRET@github.com/reliant-labs/forge": "github.com/reliant-labs/forge",
		"ssh://git@GitHub.com:22/reliant-labs/forge/":          "github.com/reliant-labs/forge",
	} {
		if got := CanonicalRepo(in); got != want {
			t.Errorf("CanonicalRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
