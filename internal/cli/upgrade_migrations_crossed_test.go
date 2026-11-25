package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Forge reports 2d879f92 and cca9cce3: a playbook whose release the pin has
// already crossed, that migrations.json does not record and whose detection
// still matches, was hidden from `upgrade list` ("No migration playbooks
// pending"). It is listed as crossed when its release is within the upgrade
// window below the pin.
func TestCrossedUnappliedMigrations(t *testing.T) {
	root := t.TempDir()
	metas := []migrationMeta{
		{ID: "v1.11.0", Version: "v1.11.0", Detection: "true"},        // ahead of the pin: migrationApplies'
		{ID: "v1.10.0", Version: "v1.10.0", Detection: "true"},        // crossed, in the window
		{ID: "v1.9.0", Version: "v1.9.0", Detection: "true"},          // crossed, recorded
		{ID: "v1.8.0/topic", Version: "v1.8.0", Detection: "false"},   // crossed, old shape gone
		{ID: "v1.8.0", Version: "v1.8.0", Detection: "true"},          // crossed, in the window
		{ID: "v1.7.0", Version: "v1.7.0", Detection: "true"},          // crossed, past the window
		{ID: "v1.10.0/nodetect", Version: "v1.10.0", Detection: "  "}, // cannot show it applies
		{ID: "v1.6.0", Version: "not-a-version", Detection: "true"},   // cannot be ordered
	}
	state := migrationsState{Applied: map[string]string{"v1.9.0": "2026-11-21T08:27:55Z"}}
	const pin = "v1.10.1-0.20261122204638-05bc400d9f0f"

	var ids []string
	for _, p := range crossedUnappliedMigrations(metas, pin, root, state) {
		if !p.Crossed || p.Applied {
			t.Errorf("%s: crossed=%v applied=%v, want crossed and not applied", p.Meta.ID, p.Crossed, p.Applied)
		}
		ids = append(ids, p.Meta.ID)
	}
	if got, want := strings.Join(ids, ","), "v1.8.0,v1.10.0"; got != want {
		t.Errorf("crossed = %s, want %s (release order)", got, want)
	}

	for _, baseline := range []string{"", "dev", "0.0.0"} {
		if got := crossedUnappliedMigrations(metas, baseline, root, state); len(got) != 0 {
			t.Errorf("baseline %q: an unknown pin crossed nothing, got %v", baseline, got)
		}
	}
}

// End to end through `upgrade list` with the shipped v1.11.0 SBOM playbook:
// a project whose pin moved past v1.11.0 while build-images.yml still runs
// Trivy sees it, marked missed, in text and JSON; a recorded one, or one past
// the window, does not.
func TestUpgradeList_ListsAPlaybookTheUpgradeSkipped(t *testing.T) {
	list := func(t *testing.T, dir string) []pendingMigration {
		t.Helper()
		var pending []pendingMigration
		var err error
		withCwd(t, dir, func() { pending, err = computePendingMigrations() })
		if err != nil {
			t.Fatal(err)
		}
		return pending
	}
	find := func(pending []pendingMigration) *pendingMigration {
		for i := range pending {
			if pending[i].Meta.ID == sbomPlaybookID {
				return &pending[i]
			}
		}
		return nil
	}

	t.Run("crossed and unrecorded", func(t *testing.T) {
		dir := newTestProjectWithVersion(t, "v1.12.0")
		writeBuildImages(t, dir, []byte("        uses: aquasecurity/trivy-action@v0.35.0\n"))
		pending := list(t, dir)
		row := find(pending)
		if row == nil || !row.Crossed {
			t.Fatalf("%s not listed as crossed: %+v", sbomPlaybookID, pending)
		}
		var out bytes.Buffer
		withCwd(t, dir, func() {
			if err := writePendingMigrationsHuman(&out, pending, len(pending)); err != nil {
				t.Fatal(err)
			}
		})
		for _, want := range []string{sbomPlaybookID, "Missed:", "an earlier upgrade skipped it", "upgrade apply " + sbomPlaybookID} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("upgrade list lacks %q:\n%s", want, out.String())
			}
		}
		var js bytes.Buffer
		if err := writePendingMigrationsJSON(&js, pending, len(pending)); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Pending []struct {
				Meta    struct{ ID string } `json:"meta"`
				Crossed bool                `json:"crossed"`
			} `json:"pending"`
		}
		if err := json.Unmarshal(js.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		crossed := false
		for _, p := range doc.Pending {
			crossed = crossed || (p.Meta.ID == sbomPlaybookID && p.Crossed)
		}
		if !crossed {
			t.Errorf("--json does not mark %s crossed:\n%s", sbomPlaybookID, js.String())
		}
	})

	t.Run("recorded", func(t *testing.T) {
		dir := newTestProjectWithVersion(t, "v1.12.0")
		writeBuildImages(t, dir, []byte("        uses: aquasecurity/trivy-action@v0.35.0\n"))
		state := []byte(`{"applied":{"` + sbomPlaybookID + `":"2026-11-22T08:34:58Z"}}`)
		if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, migrationsStateFile), state, 0o644); err != nil {
			t.Fatal(err)
		}
		if row := find(list(t, dir)); row != nil {
			t.Errorf("a recorded playbook is listed: %+v", row)
		}
	})

	t.Run("past the window", func(t *testing.T) {
		dir := newTestProjectWithVersion(t, "v1.14.0")
		writeBuildImages(t, dir, []byte("        uses: aquasecurity/trivy-action@v0.35.0\n"))
		if row := find(list(t, dir)); row != nil {
			t.Errorf("a playbook three minors below the pin is listed: %+v", row)
		}
	})
}
