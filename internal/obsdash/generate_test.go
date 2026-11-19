package obsdash

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestOwnedOverviewMigratesEditsWithoutDroppingIndependentOverride(t *testing.T) {
	for _, bothEdited := range []bool{false, true} {
		t.Run(map[bool]string{false: "migrate", true: "preserve-both"}[bothEdited], func(t *testing.T) {
			dir := t.TempDir()
			legacy := DashboardsDir + "/shop-overview_gen.json"
			owned := OverridePath("shop", Dashboard{ID: "overview"})
			if err := os.MkdirAll(filepath.Join(dir, DashboardsDir), 0755); err != nil {
				t.Fatal(err)
			}
			old := []byte(`{"name":"edited legacy","tiles":[]}`)
			if err := os.WriteFile(filepath.Join(dir, legacy), old, 0644); err != nil {
				t.Fatal(err)
			}
			if bothEdited {
				if err := os.WriteFile(filepath.Join(dir, owned), []byte(`{"name":"independent override"}`), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cs := &checksums.FileChecksums{}
			if _, err := Write(dir, fixture(), cs); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, owned))
			if err != nil {
				t.Fatal(err)
			}
			if !bothEdited && string(got) != string(old) {
				t.Fatalf("legacy edit lost: %s", got)
			}
			_, err = os.Stat(filepath.Join(dir, legacy))
			if bothEdited && err != nil {
				t.Fatal("independent legacy edit was removed")
			}
			if !bothEdited && !os.IsNotExist(err) {
				t.Fatal("migrated legacy twin remains")
			}
			if _, generated := cs.Unstampable[owned]; generated {
				t.Fatal("owned output retained generated ownership")
			}
		})
	}
}

// TestOwnedOverviewDoesNotFreezeTheNoDeployedSentinel pins the fix for the
// overview a fresh `forge project new` shipped: its first generate ran before
// any environment's workloads rendered, scaffolded the owned overview with
// every panel on the no-deployed-process sentinel, and no later generate
// refreshed it, so the local overview drew nothing forever. An untouched
// scaffold (and a pristine legacy render) takes the current render; a file the
// user edited is still never touched.
func TestOwnedOverviewDoesNotFreezeTheNoDeployedSentinel(t *testing.T) {
	const sentinel = "__forge_no_deployed_process__"
	undeployed := fixture()
	undeployed.Processes = nil
	deployed := undeployed
	deployed.Deployed = Identities{ServiceNames: []string{"shop-api"}, Workloads: []string{"shop-api"}}
	owned := OverridePath("shop", Dashboard{ID: "overview"})
	read := func(t *testing.T, dir, rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	contains := strings.Contains

	t.Run("untouched scaffold is refreshed", func(t *testing.T) {
		dir := t.TempDir()
		cs := &checksums.FileChecksums{}
		if _, err := Write(dir, undeployed, cs); err != nil {
			t.Fatal(err)
		}
		if !contains(read(t, dir, owned), sentinel) {
			t.Fatal("fixture: an undeployed project's overview should use the sentinel")
		}
		if _, err := Write(dir, deployed, cs); err != nil {
			t.Fatal(err)
		}
		got := read(t, dir, owned)
		if contains(got, sentinel) || !contains(got, "shop-api") {
			t.Fatalf("the scaffolded overview kept the sentinel once workloads were known:\n%s", got)
		}
	})

	t.Run("edited overview is left alone", func(t *testing.T) {
		dir := t.TempDir()
		cs := &checksums.FileChecksums{}
		if _, err := Write(dir, undeployed, cs); err != nil {
			t.Fatal(err)
		}
		edited := read(t, dir, owned) + "\n"
		if err := os.WriteFile(filepath.Join(dir, owned), []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Write(dir, deployed, cs); err != nil {
			t.Fatal(err)
		}
		if got := read(t, dir, owned); got != edited {
			t.Fatal("an edited owned overview was rewritten")
		}
	})

	t.Run("pristine legacy render is not migrated verbatim", func(t *testing.T) {
		dir := t.TempDir()
		legacy := DashboardsDir + "/shop-overview_gen.json"
		if err := os.MkdirAll(filepath.Join(dir, DashboardsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		old := []byte(`{"name":"shop: overview","tiles":[{"config":{"where":"ServiceName = '` + sentinel + `'"}}]}`)
		if err := os.WriteFile(filepath.Join(dir, legacy), old, 0o644); err != nil {
			t.Fatal(err)
		}
		cs := &checksums.FileChecksums{Unstampable: map[string]string{legacy: checksums.BodyHash(old)}}
		if _, err := Write(dir, deployed, cs); err != nil {
			t.Fatal(err)
		}
		if got := read(t, dir, owned); contains(got, sentinel) {
			t.Fatalf("the pristine legacy overview was migrated with its sentinel:\n%s", got)
		}
		if _, err := os.Stat(filepath.Join(dir, legacy)); !os.IsNotExist(err) {
			t.Fatal("the pristine legacy overview remains")
		}
	})
}

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
	overview := OverridePath("shop", Dashboard{ID: "overview"})

	first, err := Write(dir, fixture(), cs)
	if err != nil {
		t.Fatal(err)
	}
	want := read(overview)
	if want == "" {
		t.Fatal("overview not written")
	}
	for _, rel := range []string{ProjectSpecFile, ProcessSpecFile("shop-api"), SavedSearchesFile("shop", "shop-api")} {
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
	if read(DashboardsDir+"/shop-overview_gen.json") != "" {
		t.Error("the owned overview must have no generated twin")
	}
	if b, _ := os.ReadFile(override); string(b) != `{"name":"mine","tags":[],"tiles":[]}` {
		t.Error("the override was modified")
	}
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, fixture(), cs); err != nil || read(overview) != "" {
		t.Fatalf("deleting an owned overview must not resurrect it (err=%v)", err)
	}

	// A service that disappears takes its dashboard with it.
	ordersFile := DashboardsDir + "/shop-process-shop-api_gen.json"
	if read(ordersFile) == "" {
		t.Fatal("orders dashboard missing")
	}
	in := fixture()
	in.Processes = in.Processes[1:]
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
