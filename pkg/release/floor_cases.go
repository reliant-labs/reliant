package release

import "time"

// FloorCase is one row of the floor predicate's conformance table: does
// Floor admit Target?
type FloorCase struct {
	Name   string
	Floor  Floor
	Target Release
	Admit  bool
}

// FloorCases is the conformance table every restatement of [Floor.Admits]
// must agree with. The predicate has two implementations by necessity — this
// one, and the control plane's database trigger that backstops writers which
// never pass through its handler — and the two are held together by running
// both over these rows. Change a rule here and the trigger's test fails until
// the SQL says the same.
//
// The fixture is a project with one migrations source and one declarations
// source:
//
//	v1   cut day 1   carries m1
//	v2   cut day 2   carries m1 m2   (m2 declares breaks-rollback)
//	v3   cut day 3   carries m1 m2 m3, declares api "webhooks-v2"
//	v19  cut day 4   a hotfix from v1's branch: carries m1 only
//	v4sq cut day 5   squashed: a baseline adopting m1 m2 m3, and webhooks-v2
func FloorCases() []FloorCase {
	const (
		mig  = "github.com/acme/app/db/migrations"
		decl = "github.com/acme/app/deploy/rollback-floors"
		m1   = "20260101000001_init.up.sql"
		m2   = "20260102000001_drop_legacy.up.sql"
		m3   = "20260103000001_more.up.sql"
		base = "20260105000001_baseline.up.sql"
	)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	schemaM2 := FloorDeclaration{Source: mig, ID: m2, Kind: DeclarationSchema, Reason: "stopped reading legacy"}
	apiHooks := FloorDeclaration{Source: decl, ID: "webhooks-v2", Kind: DeclarationAPI, Reason: "v1 receivers removed"}
	rel := func(v string, d int, sets []MigrationSet, decls ...FloorDeclaration) Release {
		r := Release{Version: v, CreatedAt: day(d)}
		if sets != nil {
			r.Migrations = &MigrationManifest{Sets: sets, Declarations: decls, DeclarationsRecorded: true}
		}
		return r
	}
	v1 := rel("v1", 1, []MigrationSet{{Source: mig, Files: []string{m1}}})
	v2 := rel("v2", 2, []MigrationSet{{Source: mig, Files: []string{m1, m2}}}, schemaM2)
	v3 := rel("v3", 3, []MigrationSet{{Source: mig, Files: []string{m1, m2, m3}}}, schemaM2, apiHooks)
	v19 := rel("v1.9.1", 4, []MigrationSet{{Source: mig, Files: []string{m1}}})
	v4sq := rel("v4", 5, []MigrationSet{{Source: mig, Files: []string{base}, Adopts: []string{"20260101000001", "20260102000001", "20260103000001"}}}, apiHooks)
	unrecorded := Release{Version: "v0.9", CreatedAt: day(6)}
	// A release cut by a forge that predates declarations: its manifest
	// records files (so a schema floor is judged exactly) and no
	// declarations (so an api floor cannot be proved, and refuses).
	legacy := Release{Version: "v3-legacy", CreatedAt: day(7), Migrations: &MigrationManifest{
		Sets: []MigrationSet{{Source: mig, Files: []string{m1, m2, m3}}},
	}}

	orderAt := func(r Release) Floor {
		return Floor{ID: "flr_order", Env: "prod", Kind: FloorOrder, Source: FloorSourceManual, Release: r.Version, ReleaseCutAt: r.CreatedAt, Reason: "incident 4411"}
	}
	declared := func(from Release, reqs ...FloorDeclaration) Floor {
		return Floor{ID: "flr_decl", Env: "prod", Kind: FloorDeclared, Source: FloorSourceDeclared, Release: from.Version, ReleaseCutAt: from.CreatedAt, Requires: reqs, Reason: "declared"}
	}
	lifted := orderAt(v3)
	lifted.Lift = &FloorLift{Reason: "incident over", At: day(7)}
	sameMicro := Release{Version: "v3a", CreatedAt: day(3).Add(500 * time.Nanosecond)}

	return []FloorCase{
		{"order: the floor's own release is admitted", orderAt(v2), v2, true},
		{"order: a release cut after the floor is admitted", orderAt(v2), v3, true},
		{"order: a release cut before the floor is refused", orderAt(v2), v1, false},
		{"order: the hotfix cut after the floor is admitted by cut order", orderAt(v3), v19, true},
		{"order: a same-microsecond cut orders by version", orderAt(v3), sameMicro, true},
		{"order: a lifted floor admits anything", lifted, v1, true},
		{"declared: skip-ahead — v2 carries the migration prod applied via v3", declared(v3, schemaM2), v2, true},
		{"declared: a later release that carries it is admitted", declared(v2, schemaM2), v3, true},
		{"declared: a release before the migration is refused", declared(v2, schemaM2), v1, false},
		{"declared: the hotfix missing the migration is refused though cut later", declared(v2, schemaM2), v19, false},
		{"declared: a squashed baseline that adopted the migration is admitted", declared(v2, schemaM2), v4sq, true},
		{"declared: an api declaration is carried by the release that declared it", declared(v3, apiHooks), v3, true},
		{"declared: an api declaration is carried after a squash", declared(v3, apiHooks), v4sq, true},
		{"declared: a release without the api declaration is refused", declared(v3, apiHooks), v2, false},
		{"declared: every requirement must be carried", declared(v3, schemaM2, apiHooks), v2, false},
		{"declared: a release with no manifest is refused", declared(v2, schemaM2), unrecorded, false},
		{"declared: a legacy manifest carrying the migration file is admitted", declared(v2, schemaM2), legacy, true},
		{"declared: a legacy manifest cannot prove an api declaration and is refused", declared(v3, apiHooks), legacy, false},
	}
}
