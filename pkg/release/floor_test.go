package release

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestFloorCasesAgreeWithAdmits(t *testing.T) {
	for _, c := range FloorCases() {
		t.Run(c.Name, func(t *testing.T) {
			if c.Floor.Lift == nil {
				if err := c.Floor.Validate(); err != nil {
					t.Fatalf("fixture floor invalid: %v", err)
				}
			}
			got, why := c.Floor.Admits(c.Target)
			if got != c.Admit {
				t.Fatalf("Admits(%s) = %v (%q), want %v", c.Target.Version, got, why, c.Admit)
			}
			if !got && why == "" {
				t.Fatal("a refusal must say why")
			}
		})
	}
}

// A manifest recorded before declarations existed is judged on what it CAN
// prove: its files. An api declaration it cannot prove is refused with a
// clause that says why, distinct from "does not carry".
func TestLegacyManifestRefusalSaysItCannotBeShown(t *testing.T) {
	for _, c := range FloorCases() {
		if c.Name != "declared: a legacy manifest cannot prove an api declaration and is refused" {
			continue
		}
		_, why := c.Floor.Admits(c.Target)
		if !strings.Contains(why, "cannot be shown to carry") || !strings.Contains(why, "predates rollback-floor declarations") || strings.Contains(why, "does not carry") {
			t.Fatalf("clause = %q", why)
		}
		return
	}
	t.Fatal("case not found")
}

func TestAdmitOverFloorsNamesEveryFailedFloor(t *testing.T) {
	cases := FloorCases()
	var order, declared Floor
	var v1 Release
	for _, c := range cases {
		switch c.Name {
		case "order: a release cut before the floor is refused":
			order, v1 = c.Floor, c.Target
		case "declared: a release before the migration is refused":
			declared = c.Floor
		}
	}
	r := AdmitOverFloors([]Floor{order, declared}, v1)
	if r == nil || len(r.Floors) != 2 || len(r.Clauses) != 2 {
		t.Fatalf("want both floors named, got %+v", r)
	}
	if !strings.Contains(r.Error(), "cut before") || !strings.Contains(r.Error(), "does not carry") {
		t.Fatalf("refusal should explain both floors: %s", r.Error())
	}
	if AdmitOverFloors(nil, v1) != nil {
		t.Fatal("no floors admit everything")
	}
}

func TestDeclaredFloorRefusalNamesTheMigrationAndSaysRollForward(t *testing.T) {
	for _, c := range FloorCases() {
		if c.Name != "declared: the hotfix missing the migration is refused though cut later" {
			continue
		}
		_, why := c.Floor.Admits(c.Target)
		for _, want := range []string{"20260102000001_drop_legacy.up.sql", "breaks-rollback", "stopped reading legacy", "roll forward"} {
			if !strings.Contains(why, want) {
				t.Errorf("refusal %q does not mention %q", why, want)
			}
		}
		return
	}
	t.Fatal("case not found")
}

func TestDeclarationsRaisedByIsIdempotentAndOnlyRaisesWhatIsNew(t *testing.T) {
	m := func(ds ...FloorDeclaration) *MigrationManifest {
		return &MigrationManifest{Declarations: ds, DeclarationsRecorded: true}
	}
	a := FloorDeclaration{Source: "s", ID: "a", Kind: DeclarationAPI, Reason: "x"}
	b := FloorDeclaration{Source: "s", ID: "b", Kind: DeclarationData, Reason: "y"}
	v2 := Release{Version: "v2", CreatedAt: time.Unix(2, 0), Migrations: m(a)}
	v3 := Release{Version: "v3", CreatedAt: time.Unix(3, 0), Migrations: m(a, b)}

	raised := DeclarationsRaisedBy(nil, v2)
	if len(raised) != 1 || raised[0].ID != "a" {
		t.Fatalf("first promote raises a: %+v", raised)
	}
	f := NewDeclaredFloor("prod", v2, raised, Actor{User: "u"}, time.Unix(10, 0), "p1")
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := DeclarationsRaisedBy([]Floor{f}, v2); len(got) != 0 {
		t.Fatalf("a re-promote raises nothing: %+v", got)
	}
	got := DeclarationsRaisedBy([]Floor{f}, v3)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("v3 raises only b: %+v", got)
	}
	lifted := f
	lifted.Lift = &FloorLift{Reason: "r"}
	if got := DeclarationsRaisedBy([]Floor{lifted}, v2); len(got) != 1 {
		t.Fatalf("a lifted floor no longer covers its declarations: %+v", got)
	}
	if DeclarationsRaisedBy(nil, Release{Version: "old"}) != nil {
		t.Fatal("a release with no manifest raises nothing")
	}
}

func TestCheckNewFloorRefusesAFloorAboveCurrent(t *testing.T) {
	v1 := Release{Version: "v1", CreatedAt: time.Unix(1, 0)}
	v2 := Release{Version: "v2", CreatedAt: time.Unix(2, 0)}
	at := NewOrderFloor("prod", v2, FloorSourceManual, "why", Actor{}, time.Unix(9, 0), "")
	if err := CheckNewFloor(at, &v1); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("a floor above current must be refused, got %v", err)
	}
	if err := CheckNewFloor(at, &v2); err != nil {
		t.Fatalf("a floor at current is fine: %v", err)
	}
	if err := CheckNewFloor(at, nil); err != nil {
		t.Fatalf("an unbound env admits any floor: %v", err)
	}
	noReason := NewOrderFloor("prod", v1, FloorSourceManual, "  ", Actor{}, time.Unix(9, 0), "")
	if err := CheckNewFloor(noReason, &v2); err == nil {
		t.Fatal("a floor needs a reason")
	}
}

func TestCheckLiftOfADeclaredFloorMustNameEveryDeclaration(t *testing.T) {
	a := FloorDeclaration{Source: "s", ID: "a", Kind: DeclarationSchema}
	b := FloorDeclaration{Source: "t", ID: "b", Kind: DeclarationFeature, Reason: "r"}
	f := Floor{ID: "flr_1", Env: "prod", Kind: FloorDeclared, Source: FloorSourceDeclared, Release: "v3", Requires: []FloorDeclaration{a, b}, Reason: "x"}
	if err := CheckLift(f, "rollout never ran", []DeclarationRef{a.Ref()}); err == nil || !strings.Contains(err.Error(), "t:b") {
		t.Fatalf("a lift missing b must be refused naming it, got %v", err)
	}
	if err := CheckLift(f, "rollout never ran", []DeclarationRef{a.Ref(), b.Ref()}); err != nil {
		t.Fatalf("a lift naming both is admitted: %v", err)
	}
	if err := CheckLift(f, "", []DeclarationRef{a.Ref(), b.Ref()}); err == nil {
		t.Fatal("a lift needs a reason")
	}
	order := Floor{ID: "flr_2", Env: "prod", Kind: FloorOrder, Source: FloorSourceManual, Release: "v3", ReleaseCutAt: time.Unix(3, 0), Reason: "x"}
	if err := CheckLift(order, "incident over", nil); err != nil {
		t.Fatalf("an order floor lifts with a reason alone: %v", err)
	}
	order.Lift = &FloorLift{Reason: "done"}
	if err := CheckLift(order, "again", nil); err == nil {
		t.Fatal("a floor is lifted at most once")
	}
}

func TestOrderFloorsAboveNeverIncludesDeclaredFloors(t *testing.T) {
	var floors []Floor
	var target Release
	for _, c := range FloorCases() {
		if !c.Admit && c.Floor.Lift == nil {
			floors = append(floors, c.Floor)
		}
		if c.Target.Version == "v1" {
			target = c.Target
		}
	}
	for _, f := range OrderFloorsAbove(floors, target) {
		if f.Kind != FloorOrder {
			t.Fatalf("lower must never lift a declared floor: %+v", f)
		}
	}
}

func TestParseFloorDeclarationFile(t *testing.T) {
	good, err := ParseFloorDeclarationFile("webhooks-v2.yaml", []byte("kind: api\nreason: v1 receivers removed\n"))
	if err != nil || good.ID != "webhooks-v2" || good.Kind != DeclarationAPI || good.Reason != "v1 receivers removed" {
		t.Fatalf("got %+v, %v", good, err)
	}
	for name, tc := range map[string]struct{ file, body, want string }{
		"schema is a migration's":  {"x.yaml", "kind: schema\nreason: r\n", "forge:breaks-rollback"},
		"unknown kind":             {"x.yaml", "kind: vibes\nreason: r\n", "not one of"},
		"reason required":          {"x.yaml", "kind: data\n", "reason"},
		"unknown key":              {"x.yaml", "kind: data\nreason: r\nfloor: v1\n", "floor"},
		"not yaml extension":       {"x.yml", "kind: data\nreason: r\n", "<id>.yaml"},
		"upper-case id":            {"Webhooks.yaml", "kind: data\nreason: r\n", "must match"},
		"reason over the byte cap": {"x.yaml", "kind: data\nreason: " + strings.Repeat("a", MaxFloorReasonBytes+1) + "\n", "at most"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFloorDeclarationFile(tc.file, []byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestManifestValidateChecksDeclarations(t *testing.T) {
	base := func() MigrationManifest {
		return MigrationManifest{Sets: []MigrationSet{{Source: "s/db/migrations", Files: []string{"1_a.up.sql"}}}, DeclarationsRecorded: true}
	}
	ok := base()
	ok.Declarations = []FloorDeclaration{
		{Source: "s/db/migrations", ID: "1_a.up.sql", Kind: DeclarationSchema},
		{Source: "s/deploy/rollback-floors", ID: "x", Kind: DeclarationData, Reason: "r"},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid manifest refused: %v", err)
	}
	for name, d := range map[string]FloorDeclaration{
		"schema names a file not carried": {Source: "s/db/migrations", ID: "2_b.up.sql", Kind: DeclarationSchema},
		"schema id is not a file name":    {Source: "s/db/migrations", ID: "../1_a.up.sql", Kind: DeclarationSchema},
		"non-schema without a reason":     {Source: "s/deploy/rollback-floors", ID: "x", Kind: DeclarationAPI},
		"unknown kind":                    {Source: "s/deploy/rollback-floors", ID: "x", Kind: "vibes", Reason: "r"},
		"no source":                       {ID: "x", Kind: DeclarationAPI, Reason: "r"},
	} {
		t.Run(name, func(t *testing.T) {
			m := base()
			m.Declarations = []FloorDeclaration{d}
			if err := m.Validate(); err == nil {
				t.Fatal("want refusal")
			}
		})
	}
	dup := base()
	dup.Declarations = []FloorDeclaration{ok.Declarations[1], ok.Declarations[1]}
	if err := dup.Validate(); err == nil {
		t.Fatal("a declaration recorded twice is refused")
	}
	adopt0 := base()
	adopt0.Sets[0].Adopts = []string{"0"}
	if err := adopt0.Validate(); err == nil {
		t.Fatal("adopted version 0 is refused")
	}
}

func TestManifestEqualIgnoresDeclarations(t *testing.T) {
	a := MigrationManifest{Sets: []MigrationSet{{Source: "s", Files: []string{"1_a.up.sql"}}}}
	b := a
	b.Declarations = []FloorDeclaration{{Source: "s", ID: "1_a.up.sql", Kind: DeclarationSchema}}
	b.DeclarationsRecorded = true
	if !a.Equal(b) {
		t.Fatal("declarations are evidence; a backfill recorded before them must still equal")
	}
}

func TestParseDeclarationRef(t *testing.T) {
	r, err := ParseDeclarationRef("github.com/acme/app/db/migrations:20260102_x.up.sql")
	if err != nil || r.Source != "github.com/acme/app/db/migrations" || r.ID != "20260102_x.up.sql" {
		t.Fatalf("got %+v %v", r, err)
	}
	if r.String() != "github.com/acme/app/db/migrations:20260102_x.up.sql" {
		t.Fatal("String round-trips")
	}
	for _, bad := range []string{"", "nocolon", ":id", "src:"} {
		if _, err := ParseDeclarationRef(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestCutBeforeTruncatesToMicroseconds(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Release{Version: "b", CreatedAt: at.Add(400 * time.Nanosecond)}
	b := Release{Version: "a", CreatedAt: at}
	if !CutBefore(b, a) || CutBefore(a, b) {
		t.Fatal("within one microsecond the version decides, as a database read-back would")
	}
}

// TestFloorInvariantHoldsOverRandomHistories is §2.3's invariant: whatever
// sequence of promotes, floor sets and lifts an environment sees — each
// admitted only as the rules admit it — its current release always satisfies
// every active floor.
func TestFloorInvariantHoldsOverRandomHistories(t *testing.T) {
	const src = "s/db/migrations"
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		// A project's releases: each carries every migration so far, unless
		// it is a hotfix cut from an earlier release's tree.
		var releases []Release
		var files []string
		var decls []FloorDeclaration
		for i := 0; i < 8; i++ {
			r := Release{Version: "v" + string(rune('a'+i)), CreatedAt: time.Unix(int64(i+1), 0)}
			if rng.Intn(4) == 0 && i > 1 {
				// hotfix from an earlier tree
				prev := releases[rng.Intn(i)]
				r.Migrations = prev.Migrations
			} else {
				f := "1000" + string(rune('0'+i)) + "_m.up.sql"
				files = append(files, f)
				if rng.Intn(2) == 0 {
					decls = append(decls, FloorDeclaration{Source: src, ID: f, Kind: DeclarationSchema})
				}
				r.Migrations = &MigrationManifest{
					Sets:                 []MigrationSet{{Source: src, Files: append([]string(nil), files...)}},
					Declarations:         append([]FloorDeclaration(nil), decls...),
					DeclarationsRecorded: true,
				}
			}
			releases = append(releases, r)
		}
		var floors []Floor
		var current *Release
		for step := 0; step < 30; step++ {
			switch rng.Intn(4) {
			case 0, 1: // promote
				target := releases[rng.Intn(len(releases))]
				if AdmitOverFloors(floors, target) != nil {
					continue
				}
				if raised := DeclarationsRaisedBy(floors, target); len(raised) > 0 {
					floors = append(floors, NewDeclaredFloor("e", target, raised, Actor{}, time.Unix(100, 0), ""))
				}
				current = &target
			case 2: // set an order floor
				at := releases[rng.Intn(len(releases))]
				f := NewOrderFloor("e", at, FloorSourceManual, "r", Actor{}, time.Unix(100, 0), "")
				if CheckNewFloor(f, current) == nil {
					floors = append(floors, f)
				}
			case 3: // lift one
				if len(floors) == 0 {
					continue
				}
				i := rng.Intn(len(floors))
				var ack []DeclarationRef
				for _, d := range floors[i].Requires {
					ack = append(ack, d.Ref())
				}
				if CheckLift(floors[i], "r", ack) == nil {
					floors[i].Lift = &FloorLift{Reason: "r"}
				}
			}
			if current != nil {
				if r := AdmitOverFloors(floors, *current); r != nil {
					t.Fatalf("iter %d step %d: current %s fails its own floors: %s", iter, step, current.Version, r.Error())
				}
			}
		}
	}
}
