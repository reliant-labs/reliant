// Copyright (c) 2025 Reliant Labs

package dashspeccontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// The fixture is written by the web test editorPanelsContract.test.ts from the
// builder itself, which fails when the builder's output and the fixture part.
// It is read here with os.ReadFile, in this process, so Go's test cache sees
// it and an edit to it re-runs this test even under -short.
var fixturePath = filepath.Join("..", "..", "frontends", "web", "src", "components", "Forge", "Observability", "Dashboards", "__fixtures__", "editor-panels.json")

type fixture struct {
	Valid   json.RawMessage `json:"valid"`
	Invalid []struct {
		Why   string          `json:"why"`
		Panel json.RawMessage `json:"panel"`
	} `json:"invalid"`
}

func readFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read the builder's fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode %s: %v", fixturePath, err)
	}
	if len(f.Valid) == 0 || len(f.Invalid) == 0 {
		t.Fatalf("%s has no valid dashboard or no invalid panels", fixturePath)
	}
	return f
}

// TestForgeAcceptsEveryBuilderPanel: every kind and display style the builder
// makes is a dashboard forge's validator accepts, so a save of it is accepted.
func TestForgeAcceptsEveryBuilderPanel(t *testing.T) {
	f := readFixture(t)
	d, err := dashspec.ParseDashboard("editor-panels.json", f.Valid)
	if err != nil {
		for _, p := range dashspec.ProblemsOf(err) {
			t.Errorf("%s: %s", p.Path, p.Message)
		}
		if len(dashspec.ProblemsOf(err)) == 0 {
			t.Error(err)
		}
		return
	}
	kinds := map[dashspec.PanelKind]bool{}
	for _, p := range d.Panels {
		kinds[p.Kind] = true
	}
	for _, k := range []dashspec.PanelKind{
		dashspec.KindNumber, dashspec.KindLine, dashspec.KindTable, dashspec.KindSearch, dashspec.KindBar, dashspec.KindHeatmap,
		dashspec.KindSQL, dashspec.KindIssues, dashspec.KindSessions, dashspec.KindFunnel, dashspec.KindPostgres, dashspec.KindText,
	} {
		if !kinds[k] {
			t.Errorf("the builder's fixture has no %s panel: every kind forge has is one the builder must make", k)
		}
	}
}

// TestForgeRefusesWhatTheBuilderRefuses: a panel the builder's own checks
// refuse is one forge refuses too, so the builder never blocks a save forge
// would take.
func TestForgeRefusesWhatTheBuilderRefuses(t *testing.T) {
	for _, c := range readFixture(t).Invalid {
		t.Run(c.Why, func(t *testing.T) {
			doc := []byte(`{"id":"invalid","title":"Invalid","panels":[` + string(c.Panel) + `]}`)
			if _, err := dashspec.ParseDashboard("invalid.json", doc); err == nil {
				t.Errorf("forge accepts a panel the builder refuses (%s): %s", c.Why, c.Panel)
			}
		})
	}
}
