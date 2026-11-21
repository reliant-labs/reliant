package audit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// The shape category inventories every process, whatever code it runs: each
// //forge:root under details.roots and each declared workload under
// details.workloads. A part that cannot be read is a warning beside its key,
// never an error, so the audit's exit code does not move.
func TestAuditShape_InventoriesRootsAndWorkloads(t *testing.T) {
	const roots = `package app

//forge:root server
type Server struct {
	API *API
}

//forge:root relay command=relay migrate=wait
type Relay struct {
	Router *Router
}

type RouterDeps struct{}

//forge:root-only relay
func NewRouter(RouterDeps) *Router { return nil }
`
	api := audittype.Workload{Name: "api", Kind: "service", Build: "go", Image: "shop",
		Runtimes: map[string]string{"dev": "host", "prod": "hosted"}, File: "deploy/kcl/workloads.k", Line: 3}

	cases := []struct {
		name        string
		rootsSrc    string
		inventory   *audittype.WorkloadInventory // nil: the audit surface provides no workload reader
		wantStatus  audittype.Status
		wantSummary string
		wantKeys    []string
		absentKeys  []string
		wantRoots   []string
		wantLoads   int
	}{
		{
			name:        "roots and workloads",
			rootsSrc:    roots,
			inventory:   &audittype.WorkloadInventory{Workloads: []audittype.Workload{api}},
			wantStatus:  audittype.StatusOK,
			wantSummary: ", 0 frontend(s), 2 root(s), 1 workload(s)",
			wantKeys:    []string{"roots", "workloads"},
			absentKeys:  []string{"roots_integrity", "workloads_integrity"},
			wantRoots:   []string{"relay", "server"},
			wantLoads:   1,
		},
		{
			name:        "an env that does not render is a warning",
			inventory:   &audittype.WorkloadInventory{Workloads: []audittype.Workload{api}, Failed: map[string]string{"staging": "kcl: boom"}},
			wantStatus:  audittype.StatusWarn,
			wantSummary: ", 0 root(s), 1 workload(s) (warn: staging did not render — see workloads_integrity)",
			wantKeys:    []string{"roots", "workloads", "workloads_integrity"},
			wantLoads:   1,
		},
		{
			name:        "an unreadable root directive is a warning",
			rootsSrc:    "package app\n\n//forge:root server migrate=sometimes\ntype Server struct{}\n",
			inventory:   &audittype.WorkloadInventory{},
			wantStatus:  audittype.StatusWarn,
			wantSummary: ", 0 root(s), 0 workload(s) (warn: roots unreadable — see roots_integrity)",
			wantKeys:    []string{"roots", "roots_integrity", "workloads"},
		},
		{
			name:        "no workload reader",
			wantStatus:  audittype.StatusOK,
			wantSummary: ", 0 frontend(s), 0 root(s)",
			wantKeys:    []string{"roots"},
			absentKeys:  []string{"workloads"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.rootsSrc != "" {
				writeFileTest(t, dir, "internal/app/roots.go", tc.rootsSrc)
			}
			f := testFactory(auditAPIConfig{})
			if tc.inventory != nil {
				inv := *tc.inventory
				f.Audit.Workloads = func(string) audittype.WorkloadInventory { return inv }
			}
			cat := auditShape(f, &config.ProjectConfig{Name: "shop"}, dir)
			if cat.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s (summary %q)", cat.Status, tc.wantStatus, cat.Summary)
			}
			if !strings.HasSuffix(cat.Summary, tc.wantSummary) {
				t.Errorf("summary = %q, want it to end %q", cat.Summary, tc.wantSummary)
			}
			if strings.Contains(cat.Summary, "not inventoried") {
				t.Errorf("summary still says processes are not inventoried: %q", cat.Summary)
			}
			for _, k := range tc.wantKeys {
				if _, ok := cat.Details[k]; !ok {
					t.Errorf("details has no %q key", k)
				}
			}
			for _, k := range tc.absentKeys {
				if _, ok := cat.Details[k]; ok {
					t.Errorf("details has %q, want it absent", k)
				}
			}

			data, err := json.Marshal(cat.Details)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Roots []struct {
					Name     string   `json:"name"`
					Type     string   `json:"type"`
					Command  []string `json:"command"`
					Migrate  string   `json:"migrate"`
					File     string   `json:"file"`
					Line     int      `json:"line"`
					RootOnly []struct {
						Provider string `json:"provider"`
					} `json:"root_only"`
				} `json:"roots"`
				Workloads []audittype.Workload `json:"workloads"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, r := range got.Roots {
				names = append(names, r.Name)
				if r.File == "" || r.Line <= 0 {
					t.Errorf("root %s has no file:line", r.Name)
				}
			}
			if strings.Join(names, ",") != strings.Join(tc.wantRoots, ",") {
				t.Errorf("roots = %v, want %v", names, tc.wantRoots)
			}
			if len(got.Roots) == 2 {
				relay := got.Roots[0]
				if strings.Join(relay.Command, " ") != "relay" || relay.Migrate != "wait" || relay.Type != "Relay" ||
					len(relay.RootOnly) != 1 || relay.RootOnly[0].Provider != "NewRouter" {
					t.Errorf("relay root = %+v, want command relay, migrate wait, root-only NewRouter", relay)
				}
			}
			if len(got.Workloads) != tc.wantLoads {
				t.Errorf("workloads = %+v, want %d", got.Workloads, tc.wantLoads)
			}
		})
	}
}
