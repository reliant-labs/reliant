// Copyright (c) 2025 Reliant Labs

package pgstore

import (
	"strings"
	"testing"
)

func TestSchemaSQLQualifiesEveryTable(t *testing.T) {
	ddl, err := SchemaSQL("monitoring")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ddl, "{{schema}}") {
		t.Fatal("placeholder left in DDL")
	}
	for _, want := range []string{"monitoring.alert_instances", "monitoring.alert_events"} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL does not mention %s", want)
		}
	}
	bare, _ := SchemaSQL("")
	if strings.Contains(bare, " .alert") || strings.Contains(bare, "..") {
		t.Fatal("an empty schema must produce unqualified names")
	}
}

func TestSchemaNameIsValidated(t *testing.T) {
	for _, bad := range []string{"a;drop table x", "A", "a b", "a-b", "1a", `a"b`} {
		if _, err := SchemaSQL(bad); err == nil {
			t.Fatalf("accepted schema %q", bad)
		}
		if _, err := New(nil, bad); err == nil {
			t.Fatalf("New accepted schema %q", bad)
		}
	}
}
