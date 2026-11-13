package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func ledgerDB(name string) ShapeExternalDatabase {
	return ShapeExternalDatabase{
		Name: name, Cluster: "gke-prod", Namespace: "prod", Service: "pgbouncer",
		Database: name + "_prod", Username: name + "_ro", PasswordSecret: name + "-ro",
	}
}

func shapeWithExternal(xs ...ShapeExternalDatabase) Shape {
	return Shape{Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}, ExternalDatabases: xs}
}

// A shape that declares no external database must encode, and hash, exactly as
// it did before the field, and need no feature of its reader: that is what lets
// a control plane that predates the feature keep accepting every bundle of a
// project that declares none.
func TestExternalDatabasesAbsentIsTheShapeBeforeTheField(t *testing.T) {
	for name, s := range map[string]Shape{
		"nil":   shapeWithExternal(),
		"empty": {Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}, ExternalDatabases: []ShapeExternalDatabase{}},
	} {
		raw, err := s.Encode()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Contains(raw, []byte("external_databases")) {
			t.Errorf("%s: a shape with no external databases carries the key: %s", name, raw)
		}
		if s.HasOptionalParts() {
			t.Errorf("%s: a shape with no external databases claims an optional part", name)
		}
		// The reader a pre-feature control plane is: it names no feature and
		// strict-decodes; the limited shape is the shape itself.
		limited, _ := json.Marshal((&BundleReader{Features: []string{}}).LimitShape(s).Canonical())
		if !bytes.Equal(limited, raw) {
			t.Errorf("%s: limiting for an old reader changed the shape:\n%s\n%s", name, limited, raw)
		}
	}
	// The same declaration hashes the same whether the producer knew the field.
	before := Shape{Kind: EnvSelfManaged, Objects: []ShapeObject{obj("Deployment", "api", "a")}}
	a, _ := before.Encode()
	b, _ := shapeWithExternal().Encode()
	if !bytes.Equal(a, b) {
		t.Errorf("an empty ExternalDatabases changed the encoding:\n%s\n%s", a, b)
	}
}

func TestExternalDatabasesNeedTheFeature(t *testing.T) {
	s := shapeWithExternal(ledgerDB("ledger"), ledgerDB("billing")).Canonical()
	if !s.HasOptionalParts() {
		t.Fatal("a shape with external databases claims to need no optional feature")
	}
	if old := (&BundleReader{Features: []string{}}).LimitShape(s); old.ExternalDatabases != nil {
		t.Errorf("a reader without %s still gets external databases: %+v", BundleFeatureExternalDatabases, old.ExternalDatabases)
	}
	if got := (&BundleReader{Features: []string{BundleFeatureExternalDatabases}}).LimitShape(s); len(got.ExternalDatabases) != 2 {
		t.Errorf("a reader of the feature lost external databases: %+v", got.ExternalDatabases)
	}
	if got := (*BundleReader)(nil).LimitShape(s); !reflect.DeepEqual(got, s) {
		t.Errorf("the nil reader changed the shape")
	}
	found := false
	for _, f := range BundleDocFeatures() {
		found = found || f == BundleFeatureExternalDatabases
	}
	if !found || BundleFeatureExternalDatabases != "shape-external-databases" {
		t.Errorf("feature %q not advertised by BundleDocFeatures", BundleFeatureExternalDatabases)
	}
}

func TestExternalDatabasesCanonical(t *testing.T) {
	terse := shapeWithExternal(ledgerDB("zeta"), ledgerDB("alpha")).Canonical()
	if terse.ExternalDatabases[0].Name != "alpha" || terse.ExternalDatabases[1].Name != "zeta" {
		t.Errorf("external databases are not sorted by name: %+v", terse.ExternalDatabases)
	}
	for _, x := range terse.ExternalDatabases {
		if x.PasswordKey != DefaultExternalDatabasePasswordKey || x.SSLMode != ExternalDatabaseSSLRequire || x.Reach != ExternalDatabaseReachPortForward {
			t.Errorf("canonical leaves a default unset: %+v", x)
		}
	}
	full := ledgerDB("alpha")
	full.PasswordKey, full.SSLMode, full.Reach = DefaultExternalDatabasePasswordKey, ExternalDatabaseSSLRequire, ExternalDatabaseReachPortForward
	a, _ := shapeWithExternal(ledgerDB("alpha")).Encode()
	b, _ := shapeWithExternal(full).Encode()
	if !bytes.Equal(a, b) {
		t.Errorf("spelling the defaults out changed the encoding:\n%s\n%s", a, b)
	}

	direct := ledgerDB("platform")
	direct.Reach, direct.Port = ExternalDatabaseReachDirect, 6432
	declared := shapeWithExternal(ledgerDB("ledger"), direct).Canonical()
	raw, _ := declared.Encode()
	if !bytes.Contains(raw, []byte(`"reach":"direct","port":6432`)) || !bytes.Contains(raw, []byte(`"reach":"port_forward"`)) {
		t.Errorf("reach and port are not recorded: %s", raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var back Shape
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.ExternalDatabases, declared.ExternalDatabases) {
		t.Errorf("round trip changed the declaration:\n%+v\n%+v", back.ExternalDatabases, declared.ExternalDatabases)
	}
	if got, _ := back.ExternalDatabase("platform"); got.Reach != ExternalDatabaseReachDirect || got.Port != 6432 {
		t.Errorf("reach did not round-trip: %+v", got)
	}
	if err := back.Validate(); err != nil {
		t.Errorf("the round-tripped shape no longer validates: %v", err)
	}
}

func TestExternalDatabaseDirectAddress(t *testing.T) {
	x := ledgerDB("platform")
	if got := x.DirectAddress(); got != "" {
		t.Errorf("a port-forwarded database has a direct address %q", got)
	}
	x.Reach = ExternalDatabaseReachDirect
	if got := x.DirectAddress(); got != "pgbouncer.prod.svc:5432" {
		t.Errorf("direct with no port = %q", got)
	}
	x.Port = 6432
	if got := x.DirectAddress(); got != "pgbouncer.prod.svc:6432" {
		t.Errorf("direct with a pooler port = %q", got)
	}
	x.Namespace = ""
	if got := x.DirectAddress(); got != "" {
		t.Errorf("direct with no namespace = %q", got)
	}
}

func TestDatabaseKindOf(t *testing.T) {
	s := shapeWithExternal(ledgerDB("ledger"))
	s.Objects = append(s.Objects, obj("ManagedDatabase", "app", "b"))
	s.Databases = []ShapeDatabase{{Name: "orders", Dashboards: &ShapeDatabaseDashboards{Enabled: true}}}
	for name, want := range map[string]DatabaseKind{
		"ledger": DatabaseExternal,
		"app":    DatabaseManaged,
		"orders": DatabaseManaged,
		"api":    "",
		"":       "",
	} {
		if got := s.DatabaseKindOf(name); got != want {
			t.Errorf("DatabaseKindOf(%q) = %q, want %q", name, got, want)
		}
	}
	x, ok := s.ExternalDatabase("ledger")
	if !ok || x.Service != "pgbouncer" || x.PasswordSecret != "ledger-ro" || x.PasswordKey != DefaultExternalDatabasePasswordKey || x.SSLMode != ExternalDatabaseSSLRequire {
		t.Errorf("ExternalDatabase(ledger) = %+v %v, want it with its defaults filled", x, ok)
	}
	if _, ok := s.ExternalDatabase("app"); ok {
		t.Error("a managed database reads as external")
	}
}

func TestExternalDatabasesValidate(t *testing.T) {
	with := func(f func(*ShapeExternalDatabase)) []ShapeExternalDatabase {
		x := ledgerDB("ledger")
		f(&x)
		return []ShapeExternalDatabase{x}
	}
	for name, tc := range map[string]struct {
		xs      []ShapeExternalDatabase
		managed bool
		wantErr string
	}{
		"minimal":           {xs: with(func(*ShapeExternalDatabase) {}), wantErr: ""},
		"no cluster":        {xs: with(func(x *ShapeExternalDatabase) { x.Cluster, x.Namespace = "", "" }), wantErr: ""},
		"disable":           {xs: with(func(x *ShapeExternalDatabase) { x.SSLMode = ExternalDatabaseSSLDisable }), wantErr: ""},
		"dotted secret":     {xs: with(func(x *ShapeExternalDatabase) { x.PasswordSecret = "ledger.ro" }), wantErr: ""},
		"no name":           {xs: with(func(x *ShapeExternalDatabase) { x.Name = "" }), wantErr: "name"},
		"no service":        {xs: with(func(x *ShapeExternalDatabase) { x.Service = "" }), wantErr: "service"},
		"bad namespace":     {xs: with(func(x *ShapeExternalDatabase) { x.Namespace = "Prod" }), wantErr: "namespace"},
		"no database":       {xs: with(func(x *ShapeExternalDatabase) { x.Database = "" }), wantErr: "needs a database"},
		"no username":       {xs: with(func(x *ShapeExternalDatabase) { x.Username = "" }), wantErr: "needs a database"},
		"no secret":         {xs: with(func(x *ShapeExternalDatabase) { x.PasswordSecret = "" }), wantErr: "password_secret"},
		"a password":        {xs: with(func(x *ShapeExternalDatabase) { x.PasswordSecret = "hunter2!" }), wantErr: "never hold a value"},
		"a DSN":             {xs: with(func(x *ShapeExternalDatabase) { x.PasswordSecret = "postgres://u:p@h/db" }), wantErr: "never hold a value"},
		"a password as key": {xs: with(func(x *ShapeExternalDatabase) { x.PasswordKey = "p@ss word" }), wantErr: "never hold a value"},
		"sslmode":           {xs: with(func(x *ShapeExternalDatabase) { x.SSLMode = "verify-full" }), wantErr: "sslmode"},
		"direct":            {xs: with(func(x *ShapeExternalDatabase) { x.Reach, x.Port = ExternalDatabaseReachDirect, 6432 }), wantErr: ""},
		"bad reach":         {xs: with(func(x *ShapeExternalDatabase) { x.Reach = "tunnel" }), wantErr: `reach "tunnel"`},
		"direct no ns":      {xs: with(func(x *ShapeExternalDatabase) { x.Reach, x.Namespace = ExternalDatabaseReachDirect, "" }), wantErr: "needs a namespace"},
		"bad port":          {xs: with(func(x *ShapeExternalDatabase) { x.Port = 70000 }), wantErr: "port 70000"},
		"duplicate":         {xs: []ShapeExternalDatabase{ledgerDB("ledger"), ledgerDB("ledger")}, wantErr: "twice"},
		"managed too":       {xs: with(func(x *ShapeExternalDatabase) { x.Name = "app" }), managed: true, wantErr: "both managed and external"},
	} {
		s := shapeWithExternal(tc.xs...)
		if tc.managed {
			s.Objects = append(s.Objects, obj("ManagedDatabase", "app", "b"))
		}
		err := s.Validate()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.wantErr != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want ErrInvalid naming %q", name, err, tc.wantErr)
		}
	}
}
