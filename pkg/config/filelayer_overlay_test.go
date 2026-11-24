package config

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// loadFromFile loads m with a config file holding body (YAML) as its only
// non-default layer.
func loadFromFile(t *testing.T, m protoreflect.Message, body string) {
	t.Helper()
	cmd := cmdWithConfigFlag(t, m.Interface())
	if err := cmd.Flags().Set(ConfigFlag, writeFile(t, "config.yaml", body)); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
}

// Every layer replaces a whole list (parseList), and the file is a layer. It
// used to proto.Merge onto the defaults, which APPENDS a repeated field, so
// a file narrowing allowed_registries to one registry loaded all three.
func TestConfigFile_ListReplacesTheDefault(t *testing.T) {
	m := buildListTestMessage(t) // allowed_registries defaults to docker.io,ghcr.io
	loadFromFile(t, m, "allowed_registries: [quay.io]\nrequired_hosts: [h1]\n")
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, []string{"quay.io"}) {
		t.Errorf("allowed_registries = %v, want [quay.io]: the file's list replaces the default", got)
	}
}

// An empty list in the file is a list the file sets: it clears the default.
// An empty env var is unset, so a deployment clears a defaulted list here or
// with an empty flag (`--allowed-registries=`).
func TestConfigFile_EmptyListClearsTheDefault(t *testing.T) {
	m := buildListTestMessage(t)
	loadFromFile(t, m, "allowed_registries: []\nrequired_hosts: [h1]\n")
	if got := strListOf(t, m, "allowed_registries"); len(got) != 0 {
		t.Errorf("allowed_registries = %v, want [] (the file named an empty list)", got)
	}
}

// A map the file sets replaces whatever the message held. No layer before
// the file can populate a map today (a map takes no default, env or flag),
// so this drives the overlay directly with a populated destination.
func TestOverlayNamed_MapReplaces(t *testing.T) {
	dst := buildListTestMessage(t)
	labels := dst.Descriptor().Fields().ByName("labels")
	mp := dst.Mutable(labels).Map()
	mp.Set(protoreflect.ValueOfString("stale").MapKey(), protoreflect.ValueOfString("x"))

	src := dst.New() // same type: the overlay hands the file's decoded copy over
	sm := src.Mutable(labels).Map()
	sm.Set(protoreflect.ValueOfString("team").MapKey(), protoreflect.ValueOfString("core"))

	if err := overlayNamed(dst, src, []byte(`{"labels": {"team": "core"}}`)); err != nil {
		t.Fatal(err)
	}
	got := dst.Get(labels).Map()
	if got.Len() != 1 || got.Get(protoreflect.ValueOfString("team").MapKey()).String() != "core" {
		t.Errorf("labels has %d entries, want exactly {team: core}: the file's map replaces, it does not union", got.Len())
	}

	// And `{}` clears it.
	if err := overlayNamed(dst, dst.New(), []byte(`{"labels": {}}`)); err != nil {
		t.Fatal(err)
	}
	if n := dst.Get(labels).Map().Len(); n != 0 {
		t.Errorf("labels has %d entries after the file named {}, want 0", n)
	}
}

// A field the file names takes the file's value even when that value is
// proto3's zero — Merge copied only populated fields, so `port: 0` kept 8080
// — and a Duration is replaced whole: Merge recursed into it, so "0.5s" over
// the 30s default loaded 30.5s.
func TestConfigFile_NamedZeroValuesAndDurationsReplace(t *testing.T) {
	m := buildTestMessage(t) // port 8080, ratio 1.5, timeout 30s
	loadFromFile(t, m, "api_key: k\nport: 0\nratio: 0\ntimeout: 0.5s\n")
	if got := intOf(m, "port"); got != 0 {
		t.Errorf("port = %d, want 0 (named in the file)", got)
	}
	if got := floatOf(m, "ratio"); got != 0 {
		t.Errorf("ratio = %v, want 0 (named in the file)", got)
	}
	if got := durOf(t, m, "timeout").AsDuration().String(); got != "500ms" {
		t.Errorf("timeout = %s, want 500ms (a Duration is replaced whole)", got)
	}
	// Unnamed fields keep their defaults.
	if got := strOf(m, "log_level"); got != "info" {
		t.Errorf("log_level = %q, want the default info", got)
	}
}

// A block the file names as an object is overlaid one level down: the leaves
// it names change, the rest keep their defaults. A null value names nothing.
func TestConfigFile_BlockLeavesOverlayAndNullIsUnset(t *testing.T) {
	m := buildFileLayerMessage(t) // log_level info; db.host localhost, db.port 5432
	loadFromFile(t, m, "log_level: null\ndb:\n  port: 6543\n")
	if got := blockIntOf2(t, m, "db", "port"); got != 6543 {
		t.Errorf("db.port = %d, want 6543", got)
	}
	if got := blockStrOf(t, m, "db", "host"); got != "localhost" {
		t.Errorf("db.host = %q, want the default localhost (not named)", got)
	}
	if got := strOf(m, "log_level"); got != "info" {
		t.Errorf("log_level = %q, want the default info (null names nothing)", got)
	}
}
