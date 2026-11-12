package release

import (
	"bytes"
	"encoding/json"
	"testing"
)

func verifiedExternalDB() ShapeExternalDatabase {
	x := ledgerDB("ledger")
	x.SSLMode, x.CASecret, x.CAKey = ExternalDatabaseSSLVerifyFull, "pooler-ca", "ca.crt"
	return x
}

func TestExternalDatabaseTLSReaderCompatibility(t *testing.T) {
	t.Parallel()
	legacy := ledgerDB("old")
	secure := verifiedExternalDB()
	full := shapeWithExternal(legacy, secure).Canonical()
	old := (&BundleReader{Features: []string{BundleFeatureExternalDatabases}}).LimitShape(full)
	if len(old.ExternalDatabases) != 1 || old.ExternalDatabases[0] != legacy.resolved() {
		t.Fatal("old reader lost legacy route or retained verified route")
	}
	if len(full.ExternalDatabases) != 2 || full.ExternalDatabases[0].CASecret == "" {
		t.Fatal("limiting mutated source")
	}
	current := (&BundleReader{Features: []string{BundleFeatureExternalDatabases, BundleFeatureExternalDatabaseTLS}}).LimitShape(full)
	if len(current.ExternalDatabases) != 2 {
		t.Fatal("current reader lost verified route")
	}
	raw, _ := old.Encode()
	before, _ := shapeWithExternal(legacy).Encode()
	if !bytes.Equal(raw, before) {
		t.Fatal("limiting changed legacy digest input")
	}
	// An external-route reader predating trust fields strict-decodes this type.
	type oldRoute struct {
		Name           string `json:"name"`
		Cluster        string `json:"cluster"`
		Namespace      string `json:"namespace"`
		Service        string `json:"service"`
		Database       string `json:"database"`
		Username       string `json:"username"`
		PasswordSecret string `json:"password_secret"`
		PasswordKey    string `json:"password_key"`
		SSLMode        string `json:"sslmode"`
		Reach          string `json:"reach"`
		Port           int    `json:"port"`
	}
	routes, _ := json.Marshal(old.ExternalDatabases)
	dec := json.NewDecoder(bytes.NewReader(routes))
	dec.DisallowUnknownFields()
	var decoded []oldRoute
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
}

func TestExternalDatabaseTLSValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*ShapeExternalDatabase)
		valid  bool
	}{
		{"secret", func(*ShapeExternalDatabase) {}, true},
		{"configmap", func(x *ShapeExternalDatabase) { x.CASecret = ""; x.CAConfigMap = "pooler-ca" }, true},
		{"explicit DNS", func(x *ShapeExternalDatabase) { x.TLSServerName = "pooler.prod.svc.cluster.local" }, true},
		{"both sources", func(x *ShapeExternalDatabase) { x.CAConfigMap = "other" }, false},
		{"no source", func(x *ShapeExternalDatabase) { x.CASecret = "" }, false},
		{"no key", func(x *ShapeExternalDatabase) { x.CAKey = "" }, false},
		{"no namespace", func(x *ShapeExternalDatabase) { x.Namespace = "" }, false},
		{"path source", func(x *ShapeExternalDatabase) { x.CASecret = "/etc/ca.pem" }, false},
		{"path key", func(x *ShapeExternalDatabase) { x.CAKey = "../ca.pem" }, false},
		{"IP", func(x *ShapeExternalDatabase) { x.TLSServerName = "127.0.0.1" }, false},
		{"port", func(x *ShapeExternalDatabase) { x.TLSServerName = "pooler:443" }, false},
		{"wildcard", func(x *ShapeExternalDatabase) { x.TLSServerName = "*.prod.svc" }, false},
		{"empty label", func(x *ShapeExternalDatabase) { x.TLSServerName = "a..svc" }, false},
		{"require with trust", func(x *ShapeExternalDatabase) { x.SSLMode = "require" }, false},
		{"disable with trust", func(x *ShapeExternalDatabase) { x.SSLMode = "disable" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := verifiedExternalDB()
			tc.change(&x)
			if err := x.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestExternalDatabaseTLSCanonicalIdentity(t *testing.T) {
	t.Parallel()
	x := verifiedExternalDB()
	resolved, _ := shapeWithExternal(x).Canonical().ExternalDatabase(x.Name)
	if resolved.TLSServerName != "pgbouncer.prod.svc" {
		t.Fatal(resolved.TLSServerName)
	}
	// Compile-time proof: exact route keys remain value-comparable.
	routes := map[ShapeExternalDatabase]bool{resolved: true}
	a, _ := shapeWithExternal(x).Encode()
	b, _ := shapeWithExternal(resolved).Encode()
	if !bytes.Equal(a, b) {
		t.Fatal("derived vs explicit name changed canonical bytes")
	}
	for _, change := range []func(*ShapeExternalDatabase){
		func(y *ShapeExternalDatabase) { y.CASecret = "other-ca" },
		func(y *ShapeExternalDatabase) { y.CASecret = ""; y.CAConfigMap = "pooler-ca" },
		func(y *ShapeExternalDatabase) { y.CAKey = "other.crt" },
		func(y *ShapeExternalDatabase) { y.TLSServerName = "other.prod.svc" },
	} {
		y := resolved
		change(&y)
		c, err := shapeWithExternal(y).Encode()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(a, c) || routes[y] {
			t.Fatal("changed trust identity retained canonical bytes or route equality")
		}
	}
	var back Shape
	if err := json.Unmarshal(a, &back); err != nil {
		t.Fatal(err)
	}
	if back.ExternalDatabases[0] != resolved {
		t.Fatal("trust fields lost on roundtrip")
	}
	// Pin the old route encoding, including ordering and every old default.
	legacy, _ := json.Marshal(ledgerDB("ledger").resolved())
	const before = `{"name":"ledger","cluster":"gke-prod","namespace":"prod","service":"pgbouncer","database":"ledger_prod","username":"ledger_ro","password_secret":"ledger-ro","password_key":"password","sslmode":"require","reach":"port_forward"}`
	if string(legacy) != before {
		t.Fatalf("legacy encoding changed: %s", legacy)
	}
}
