package obsdash

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestWriteIsIdempotentHonoursOverridesAndRetiresStaleFiles(t *testing.T) {
	dir := t.TempDir()
	cs := &checksums.FileChecksums{}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return ""
		}
		return string(b)
	}
	overview := DashboardsDir + "/shop-overview_gen.json"

	first, err := Write(dir, fixture(), cs)
	if err != nil {
		t.Fatal(err)
	}
	want := read(overview)
	if want == "" {
		t.Fatal("overview not written")
	}
	for _, rel := range []string{ProjectSpecFile, ServiceSpecFile("orders"), SavedSearchesFile("shop", "orders")} {
		if read(rel) == "" {
			t.Errorf("%s not written", rel)
		}
	}

	if _, err := Write(dir, fixture(), cs); err != nil || read(overview) != want {
		t.Fatalf("second Write changed the output (err=%v)", err)
	}

	// An owned override takes the dashboard over; forge removes only its own
	// pristine twin and never touches the override.
	override := filepath.Join(dir, OverridePath("shop", Dashboard{ID: "overview"}))
	if err := os.WriteFile(override, []byte(`{"name":"mine","tags":[],"tiles":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, fixture(), cs); err != nil {
		t.Fatal(err)
	}
	if read(overview) != "" {
		t.Error("the generated overview should be retired while an override exists")
	}
	if b, _ := os.ReadFile(override); string(b) != `{"name":"mine","tags":[],"tiles":[]}` {
		t.Error("the override was modified")
	}
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, fixture(), cs); err != nil || read(overview) != want {
		t.Fatalf("removing the override should bring the generated file back (err=%v)", err)
	}

	// A service that disappears takes its dashboard with it.
	ordersFile := DashboardsDir + "/shop-service-orders_gen.json"
	if read(ordersFile) == "" {
		t.Fatal("orders dashboard missing")
	}
	in := fixture()
	in.Services = in.Services[1:]
	if _, err := Write(dir, in, cs); err != nil {
		t.Fatal(err)
	}
	if read(ordersFile) != "" {
		t.Error("a removed service's dashboard was left behind")
	}

	// A file that is not forge's is never touched.
	mine := filepath.Join(dir, DashboardsDir, "custom.json")
	if err := os.WriteFile(mine, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, in, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error("custom.json was removed")
	}
	_ = first
}

// TestWriteRetiresTheWholeProjectAggregates: the spec and saved searches
// used to be one file each for every service. Write now writes them per
// service, and removes the old aggregates forge wrote — a pristine one only;
// one the user edited stays.
func TestWriteRetiresTheWholeProjectAggregates(t *testing.T) {
	dir := t.TempDir()
	cs := &checksums.FileChecksums{}
	legacySearches := DashboardsDir + "/saved-searches_gen.ndjson"
	for _, rel := range []string{LegacySpecFile, legacySearches} {
		if _, err := checksums.WriteGeneratedFile(dir, rel, []byte("{}\n"), cs, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Write(dir, fixture(), cs); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{LegacySpecFile, legacySearches} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("%s survived Write: %v", rel, err)
		}
	}

	// A hand-edited aggregate is the user's.
	if _, err := checksums.WriteGeneratedFile(dir, LegacySpecFile, []byte("{}\n"), cs, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LegacySpecFile), []byte(`{"mine":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, fixture(), cs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, LegacySpecFile)); err != nil {
		t.Errorf("a hand-edited %s was removed: %v", LegacySpecFile, err)
	}
}

// TestReadSpecIsBuild: the parts Write puts on disk assemble into exactly
// the Spec Build derives — the model the bundle and the console read.
func TestReadSpecIsBuild(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, fixture(), &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadSpec(dir)
	if err != nil || !ok {
		t.Fatalf("ReadSpec = ok %v, err %v", ok, err)
	}
	want := Build(fixture())
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadSpec differs from Build:\ngot  %+v\nwant %+v", got, want)
	}
	if _, ok, err := ReadSpec(t.TempDir()); ok || err != nil {
		t.Errorf("no spec on disk: ok %v, err %v; want none and no error", ok, err)
	}
}
