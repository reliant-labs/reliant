package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func shapeWithDatabases(dbs ...ShapeDatabase) Shape {
	return Shape{Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}, Databases: dbs}
}

func on(rowCap, timeoutMs, conc int) *ShapeDatabaseDashboards {
	return &ShapeDatabaseDashboards{Enabled: true, RowCap: rowCap, TimeoutMs: timeoutMs, MaxConcurrency: conc}
}

// A database that declares nothing, or declares dashboards off, records
// nothing: a shape recorded before the field, and one that spells the off
// state out, encode and hash identically.
func TestDatabaseDashboardsDisabledIsOmittedFromTheCanonicalShape(t *testing.T) {
	bare, _ := json.Marshal(shapeWithDatabases().Canonical())
	for name, dbs := range map[string][]ShapeDatabase{
		"entry without dashboards": {{Name: "orders"}},
		"explicitly off":           {{Name: "orders", Dashboards: &ShapeDatabaseDashboards{Enabled: false, RowCap: 50}}},
		"empty record":             {{Name: "orders", Dashboards: &ShapeDatabaseDashboards{}}},
	} {
		got := shapeWithDatabases(dbs...).Canonical()
		if got.Databases != nil {
			t.Errorf("%s: disabled dashboards survived Canonical: %+v", name, got.Databases)
		}
		raw, _ := json.Marshal(got)
		if string(raw) != string(bare) {
			t.Errorf("%s: spelling the off state changed the encoding:\n%s\n%s", name, raw, bare)
		}
		if strings.Contains(string(raw), "databases") {
			t.Errorf("%s: the key is in the encoding: %s", name, raw)
		}
	}
}

// Enabled dashboards are recorded with every limit spelled out, so a producer
// that leaves the defaults off and one that writes them hash the same, and the
// shape survives a round trip through the document the control plane stores.
func TestDatabaseDashboardsEnabledIsRecordedResolvedAndRoundTrips(t *testing.T) {
	terse := shapeWithDatabases(ShapeDatabase{Name: "orders", Dashboards: &ShapeDatabaseDashboards{Enabled: true}}).Canonical()
	full := shapeWithDatabases(ShapeDatabase{Name: "orders", Dashboards: on(1000, 5000, 2)}).Canonical()
	a, _ := json.Marshal(terse)
	b, _ := json.Marshal(full)
	if string(a) != string(b) {
		t.Errorf("leaving the defaults off changed the encoding:\n%s\n%s", a, b)
	}
	if want := (ShapeDatabase{Name: "orders", Dashboards: on(1000, 5000, 2)}); len(terse.Databases) != 1 || !reflect.DeepEqual(terse.Databases[0], want) {
		t.Fatalf("canonical = %+v, want %+v", terse.Databases, want)
	}
	if bare, _ := json.Marshal(shapeWithDatabases().Canonical()); string(a) == string(bare) {
		t.Error("enabling dashboards does not move the encoding")
	}

	tuned := shapeWithDatabases(
		ShapeDatabase{Name: "zeta", Dashboards: on(10, 100, 1)},
		ShapeDatabase{Name: "alpha", Dashboards: on(10000, 10000, 4)},
	).Canonical()
	if tuned.Databases[0].Name != "alpha" || tuned.Databases[1].Name != "zeta" {
		t.Errorf("databases are not sorted by name: %+v", tuned.Databases)
	}
	raw, err := json.Marshal(tuned)
	if err != nil {
		t.Fatal(err)
	}
	var back Shape
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Databases, tuned.Databases) {
		t.Errorf("round trip changed the declaration:\n%+v\n%+v", back.Databases, tuned.Databases)
	}
	if err := back.Validate(); err != nil {
		t.Errorf("the round-tripped shape is invalid: %v", err)
	}
}

// FAIL CLOSED. Every way of not saying "enabled" reads as disabled, including
// a record that decoded from JSON missing the field, a database the shape does
// not name, and a shape that predates the field altogether.
func TestDatabaseDashboardsFailClosed(t *testing.T) {
	disabled := ShapeDatabaseDashboards{}
	var nilShape Shape
	if got := nilShape.DashboardsFor("orders"); got != disabled {
		t.Errorf("a shape with no databases: %+v, want disabled", got)
	}
	var none *ShapeDatabaseDashboards
	if got := none.Resolved(); got != disabled {
		t.Errorf("a nil declaration resolves to %+v, want disabled", got)
	}
	// Limits with enabled false (or absent from the JSON) are not an enablement.
	var decoded ShapeDatabaseDashboards
	if err := json.Unmarshal([]byte(`{"row_cap": 500, "timeout_ms": 1000, "max_concurrency": 1}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Enabled || decoded.Resolved() != disabled {
		t.Errorf("limits without enabled read as %+v, want disabled", decoded.Resolved())
	}
	if got := (Shape{Databases: []ShapeDatabase{{Name: "orders", Dashboards: &decoded}}}).DashboardsFor("orders"); got != disabled {
		t.Errorf("DashboardsFor over an unenabled record = %+v, want disabled", got)
	}

	s := shapeWithDatabases(ShapeDatabase{Name: "orders", Dashboards: on(0, 0, 0)}).Canonical()
	if got := s.DashboardsFor("orders"); got != DefaultDatabaseDashboards() {
		t.Errorf("enabled with no limits = %+v, want the defaults %+v", got, DefaultDatabaseDashboards())
	}
	if got := s.DashboardsFor("billing"); got != disabled {
		t.Errorf("a database the shape does not name = %+v, want disabled", got)
	}

	// A reader that predates the feature never sees the field, so a producer
	// writing to it records nothing the reader could misread as enabled.
	old := (&BundleReader{Features: []string{}}).LimitShape(s)
	if old.Databases != nil || old.DashboardsFor("orders") != disabled {
		t.Errorf("a reader without %s still gets databases: %+v", BundleFeatureDatabaseDashboards, old.Databases)
	}
	if !s.HasOptionalParts() {
		t.Error("a shape with enabled dashboards claims to need no optional feature")
	}
	if shapeWithDatabases().HasOptionalParts() {
		t.Error("a shape with no dashboards claims an optional part")
	}
}

func TestDatabaseDashboardsValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		dbs     []ShapeDatabase
		wantErr string
	}{
		"defaults":         {[]ShapeDatabase{{Name: "a", Dashboards: on(0, 0, 0)}}, ""},
		"at the ceilings":  {[]ShapeDatabase{{Name: "a", Dashboards: on(10000, 10000, 4)}}, ""},
		"at the floors":    {[]ShapeDatabase{{Name: "a", Dashboards: on(1, 100, 1)}}, ""},
		"row cap too big":  {[]ShapeDatabase{{Name: "a", Dashboards: on(10001, 0, 0)}}, "row_cap 10001"},
		"row cap negative": {[]ShapeDatabase{{Name: "a", Dashboards: on(-1, 0, 0)}}, "row_cap -1"},
		"timeout too long": {[]ShapeDatabase{{Name: "a", Dashboards: on(0, 10001, 0)}}, "timeout_ms 10001"},
		"timeout too low":  {[]ShapeDatabase{{Name: "a", Dashboards: on(0, 99, 0)}}, "timeout_ms 99"},
		"concurrency":      {[]ShapeDatabase{{Name: "a", Dashboards: on(0, 0, 5)}}, "max_concurrency 5"},
		"no name":          {[]ShapeDatabase{{Dashboards: on(0, 0, 0)}}, "needs a name"},
		"duplicate":        {[]ShapeDatabase{{Name: "a", Dashboards: on(0, 0, 0)}, {Name: "a", Dashboards: on(0, 0, 0)}}, "twice"},
		// Limits on a disabled record are never read, so they are not judged.
		"disabled with junk limits": {[]ShapeDatabase{{Name: "a", Dashboards: &ShapeDatabaseDashboards{RowCap: 99999}}}, ""},
	} {
		err := shapeWithDatabases(tc.dbs...).Validate()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.wantErr != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want ErrInvalid naming %q", name, err, tc.wantErr)
		}
	}
}

// The shape is strict-decoded by the control plane: an unknown key anywhere in
// it is refused, which is why the feature gates the whole field.
func TestDatabaseDashboardsFeatureIsAdvertisedAndOwnsOnlyItsKey(t *testing.T) {
	found := false
	for _, f := range BundleDocFeatures() {
		found = found || f == BundleFeatureDatabaseDashboards
	}
	if !found || BundleFeatureDatabaseDashboards != "shape-database-dashboards" {
		t.Errorf("feature %q not advertised by BundleDocFeatures", BundleFeatureDatabaseDashboards)
	}
	s := shapeWithDatabases(ShapeDatabase{Name: "orders", Dashboards: on(0, 0, 0)}).Canonical()
	s.DataAccess = &ShapeDataAccess{DBQuery: DBQueryDeny}
	limited := (&BundleReader{Features: []string{BundleFeatureDatabaseDashboards}}).LimitShape(s)
	if len(limited.Databases) != 1 || limited.DataAccess != nil {
		t.Errorf("a reader of database dashboards only: %+v", limited)
	}
}
