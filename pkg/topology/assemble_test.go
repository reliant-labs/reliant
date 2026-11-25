package topology

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Assemble's table tests state the gathered facts directly — the point of the
// package is that a control plane can do exactly this with its own records.
// The CLI's byte-for-byte contract is pinned separately, by the goldens in
// internal/cli/testdata/topology.

func fixtureReleases(t *testing.T) []release.Release {
	t.Helper()
	// Deliberately NOT newest-first: Assemble orders its own copy.
	return []release.Release{
		{Version: "v1.3.0", Git: release.Git{Commit: "c130", Dirty: true}, CreatedAt: mustTime(t, "2026-07-01T00:25:07Z")},
		{Version: "v1.5.15", Git: release.Git{Commit: "c1515"}, CreatedAt: mustTime(t, "2026-09-10T13:53:22Z")},
		{Version: "v1.4.0", Git: release.Git{Commit: "c140"}, CreatedAt: mustTime(t, "2026-07-20T00:00:00Z")},
	}
}

func envByName(t *testing.T, r Report, name string) Env {
	t.Helper()
	for _, e := range r.Environments {
		if e.Env == name {
			return e
		}
	}
	t.Fatalf("no env %q in %+v", name, r.Environments)
	return Env{}
}

func boolPtr(b bool) *bool { return &b }

func TestAssemble_Rows(t *testing.T) {
	rels := fixtureReleases(t)
	in := Input{
		Project:     "acme",
		Ledger:      "https://cp.example.test",
		GeneratedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.FixedZone("x", 3600)),
		Releases:    rels,
		Source:      SourceControlPlane,
		Envs: []EnvInput{
			{
				Name: "staging", Declared: true, Ledger: "https://cp.example.test", Hosted: true, Releases: rels,
				Binding: &release.Promotion{
					ID: "p1", Release: "v1.3.0", Kind: release.KindPromote, PromotedAt: mustTime(t, "2026-07-01T00:25:15Z"),
					Resolved: map[string]string{"reliant": "sha256:b", "control-plane": "sha256:a"},
				},
				RolloutPhase: "succeeded",
				Tracked:      boolPtr(true),
			},
			// Unbound: the "never promoted" note.
			{Name: "preview", Declared: true, Releases: rels, Tracked: boolPtr(true)},
			// File-ledger env known only from the catalog.
			{Name: "dev", Declared: true, Destination: DestinationCompose, Tracked: boolPtr(false),
				Note: "declared in code; outside the control plane"},
			// A duplicate name: the first entry wins.
			{Name: "preview", Declared: false, Note: "duplicate"},
		},
	}
	r := Assemble(in)

	if r.GeneratedAt != "2026-11-05T11:00:00Z" {
		t.Errorf("generated_at = %q, want UTC RFC3339", r.GeneratedAt)
	}
	if got := strings.Join(r.Releases, ","); got != "v1.5.15,v1.4.0,v1.3.0" {
		t.Errorf("releases = %s, want newest first", got)
	}
	if r.LatestRelease != "v1.5.15" || r.LatestReleaseCreatedAt != "2026-09-10T13:53:22Z" {
		t.Errorf("latest = %q @ %q", r.LatestRelease, r.LatestReleaseCreatedAt)
	}
	if len(r.Environments) != 3 || r.Environments[0].Env != "dev" || r.Environments[2].Env != "staging" {
		t.Fatalf("environments must be sorted and deduplicated: %+v", r.Environments)
	}
	if got := strings.Join(r.Images, ","); got != "control-plane,reliant" {
		t.Errorf("images = %s, want the sorted union", got)
	}

	staging := envByName(t, r, "staging")
	if !staging.Bound || !staging.ReleaseKnown || staging.Git == nil || !staging.Git.Dirty {
		t.Errorf("staging must be bound, known and carry the dirty bit: %+v", staging)
	}
	if staging.Lag == nil || staging.Lag.ReleasesBehind != 2 || staging.Lag.Behind != "71d13h" || staging.Lag.Current {
		t.Errorf("staging lag = %+v, want 2 behind, 71d13h", staging.Lag)
	}
	if staging.RolloutPhase != "succeeded" || !staging.Hosted {
		t.Errorf("rollout phase applies to a hosted ledger: %+v", staging)
	}
	if staging.Images[0].Image != "control-plane" || staging.Images[0].State != NotVerified {
		t.Errorf("images must be sorted and not_verified: %+v", staging.Images)
	}

	preview := envByName(t, r, "preview")
	if preview.Bound || preview.Note != "never promoted — no release is bound to this environment" || !preview.Declared {
		t.Errorf("unbound preview = %+v", preview)
	}
	if preview.Images == nil {
		t.Error("images must be [] rather than null")
	}

	dev := envByName(t, r, "dev")
	if dev.Tracked == nil || *dev.Tracked || dev.Destination != DestinationCompose {
		t.Errorf("a catalog-only env must say tracked:false and keep its destination: %+v", dev)
	}
	// The gatherer's note wins over "never promoted".
	if dev.Note != "declared in code; outside the control plane" {
		t.Errorf("dev note = %q", dev.Note)
	}

	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"source":"control_plane"`, `"tracked":false`, `"tracked":true`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("report JSON lacks %s", want)
		}
	}
	if strings.Contains(string(raw), `"catalog"`) {
		t.Error("an absent catalog must be omitted, not null")
	}
}

// TestAssemble_AdditiveFieldsAreAbsentWhenUnset is what keeps the CLI's
// document byte-identical: a gatherer that sets none of the additive fields
// emits none of their keys.
func TestAssemble_AdditiveFieldsAreAbsentWhenUnset(t *testing.T) {
	r := Assemble(Input{Envs: []EnvInput{{
		Name: "prod", Declared: true,
		Binding: &release.Promotion{Release: "v1", Resolved: map[string]string{"api": "sha256:a"}},
	}}})
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "catalog", "tracked", "bundle_digest", "bundle_git", "observed_by"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("unset additive field %q must be omitted:\n%s", key, raw)
		}
	}
}

func TestAssemble_ReleaseOutsideTheWindow(t *testing.T) {
	window := fixtureReleases(t)
	old := release.Release{Version: "v0.9.0", Git: release.Git{Commit: "c090", Dirty: true}, CreatedAt: mustTime(t, "2026-01-01T00:00:00Z")}
	binding := &release.Promotion{Release: "v0.9.0", Resolved: map[string]string{"api": "sha256:a"}}

	t.Run("with the bound record and an exact count", func(t *testing.T) {
		r := Assemble(Input{Envs: []EnvInput{{Name: "prod", Binding: binding, Releases: window, BoundRelease: &old, ReleasesBehind: 212}}})
		prod := r.Environments[0]
		if !prod.ReleaseKnown || prod.Git == nil || prod.Git.Commit != "c090" || !prod.Git.Dirty {
			t.Fatalf("provenance must come from the bound record: %+v", prod)
		}
		if prod.Lag == nil || prod.Lag.ReleasesBehind != 212 || prod.Lag.LatestRelease != "v1.5.15" || prod.Lag.Current {
			t.Errorf("lag = %+v, want the exact count against the window's newest", prod.Lag)
		}
		if prod.Note != "" {
			t.Errorf("a known release needs no note, got %q", prod.Note)
		}
	})

	t.Run("without it the release is unknown, not guessed", func(t *testing.T) {
		r := Assemble(Input{Envs: []EnvInput{{Name: "prod", Binding: binding, Releases: window, ReleasesLocation: "the control plane's release list"}}})
		prod := r.Environments[0]
		if prod.ReleaseKnown || prod.Git != nil || prod.Lag != nil {
			t.Fatalf("provenance and lag must be omitted: %+v", prod)
		}
		want := "release v0.9.0 is not in the control plane's release list — it was cut elsewhere, so its provenance and promotion lag are unknown"
		if prod.Note != want {
			t.Errorf("note = %q, want %q", prod.Note, want)
		}
	})
}

func TestAssemble_NotePrecedence(t *testing.T) {
	binding := &release.Promotion{Release: "v1.5.15"}
	cases := []struct {
		name string
		in   EnvInput
		want string
	}{
		{"ledger error replaces the declaration note",
			EnvInput{Name: "e", Note: "decl", LedgerErr: errors.New("401")},
			"could not resolve this environment's release ledger: 401"},
		{"binding error replaces the declaration note",
			EnvInput{Name: "e", Note: "decl", BindingErr: errors.New("timeout")},
			"could not read the promotion ledger: timeout"},
		{"rollout failure defers to the declaration note",
			EnvInput{Name: "e", Note: "decl", Hosted: true, Binding: binding, RolloutErr: errors.New("slow")},
			"decl"},
		{"rollout failure explains itself when nothing else does",
			EnvInput{Name: "e", Hosted: true, Binding: binding, RolloutErr: errors.New("slow"), Releases: fixtureReleases(t)},
			"rollout phase unavailable: slow"},
		{"rollout is read only for a hosted ledger",
			EnvInput{Name: "e", Binding: binding, RolloutErr: errors.New("slow"), Releases: fixtureReleases(t)},
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assemble(Input{Envs: []EnvInput{tc.in}}).Environments[0]
			if got.Note != tc.want {
				t.Errorf("note = %q, want %q", got.Note, tc.want)
			}
			if tc.in.LedgerErr != nil && (got.Ledger != "" || got.Bound) {
				t.Errorf("a failed ledger stops the row: %+v", got)
			}
		})
	}
}

func TestAssemble_ObservationsAndVerdict(t *testing.T) {
	binding := &release.Promotion{Release: "v1", Resolved: map[string]string{"api": "sha256:a", "web": "sha256:w", "db": "sha256:d"}}
	t.Run("a platform observation is labelled and unobserved images stay unknown", func(t *testing.T) {
		r := Assemble(Input{Envs: []EnvInput{{Name: "prod", Binding: binding, Observed: map[string]Observation{
			"api": {State: Match, Running: "sha256:a", ObservedBy: ObservedByPlatform},
		}}}})
		imgs := r.Environments[0].Images
		if imgs[0].Image != "api" || imgs[0].State != Match || imgs[0].ObservedBy != ObservedByPlatform {
			t.Errorf("api = %+v", imgs[0])
		}
		if imgs[1].State != NotVerified || imgs[2].State != NotVerified {
			t.Errorf("unobserved images must stay not_verified: %+v", imgs)
		}
		// Not a verification pass: an observation alone never fails it.
		if !r.OK || r.ExitCode() != 0 || r.Tally.Match != 1 || r.Tally.NotVerified != 2 {
			t.Errorf("ok=%v exit=%d tally=%+v", r.OK, r.ExitCode(), r.Tally)
		}
	})
	t.Run("verified drift exits 1", func(t *testing.T) {
		r := Assemble(Input{Verified: true, Envs: []EnvInput{{Name: "prod", Binding: binding, Observed: map[string]Observation{
			"api": {State: Match}, "web": {State: Drift}, "db": {State: Unreachable},
		}}}})
		if r.OK || r.ExitCode() != 1 || !strings.Contains(r.Detail, "1 image(s) drifted and 0 missing across 1 environment(s)") {
			t.Errorf("ok=%v exit=%d detail=%q", r.OK, r.ExitCode(), r.Detail)
		}
	})
	t.Run("verified unreachable without drift exits 2", func(t *testing.T) {
		r := Assemble(Input{Verified: true, Envs: []EnvInput{{Name: "prod", Binding: binding, Observed: map[string]Observation{
			"api": {State: Match}, "web": {State: Unreachable}, "db": {State: Unreachable},
		}}}})
		if r.OK || r.ExitCode() != 2 || r.Detail != "2 image(s) could not be checked (cluster, context or credentials), 1 matched, 0 drifted" {
			t.Errorf("ok=%v exit=%d detail=%q", r.OK, r.ExitCode(), r.Detail)
		}
	})
}

func TestSummarizeGates(t *testing.T) {
	if SummarizeGates() != nil || SummarizeGates(nil, nil) != nil {
		t.Error("no gates is no summary")
	}
	s := SummarizeGates(
		[]release.Gate{{Name: "lint", Status: release.GateStatusPassed}},
		[]release.Gate{{Name: "smoke", Status: release.GateStatusFailed}},
	)
	if s == nil || s.Passed != 1 || s.Failed != 1 {
		t.Errorf("summary = %+v", s)
	}
}

func TestHumanizeLag(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                           "",
		-time.Hour:                  "",
		5 * time.Minute:             "5m",
		2*time.Hour + 3*time.Minute: "2h3m",
		(71*24 + 13) * time.Hour:    "71d13h",
	} {
		if got := HumanizeLag(d); got != want {
			t.Errorf("HumanizeLag(%v) = %q, want %q", d, got, want)
		}
	}
}
