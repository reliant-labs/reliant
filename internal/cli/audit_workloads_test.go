package cli

import (
	"errors"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/codegen"
)

// The audit's workload inventory is projected from each env's decoded render:
// one row per workload whatever it runs, its runtime per env, and the literal
// that declares it. Tested from decoded entities, so no KCL is evaluated.
func TestProjectWorkloadInventory(t *testing.T) {
	api := func(runtime string) WorkloadEntity {
		return WorkloadEntity{Name: "api", Kind: "service", Image: "shop", Build: BuildConfigEntity{Type: "go"}, Runtime: RuntimeEntity{Type: runtime}}
	}
	db := WorkloadEntity{Name: "postgres", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeCompose}}
	db.Spec.Image = "postgres:16"
	indexer := WorkloadEntity{Name: "indexer", Kind: "pipeline-stage", Build: BuildConfigEntity{Type: "shell"}, Runtime: RuntimeEntity{Type: RuntimeExternal}}

	sites := []codegen.WorkloadSite{
		{Name: "api", Build: "go", File: "deploy/kcl/workloads.k", Line: 4},
		{Name: "api", File: "deploy/kcl/prod/main.k", Line: 9}, // a second literal: the first locates it
		{Name: "reports", Kind: "cron", Build: "docker", File: "deploy/kcl/workloads.k", Line: 30},
		{Name: "seed", File: "deploy/kcl/workloads.k", Line: 40},
	}
	cases := []struct {
		name    string
		renders []envRender
		sites   []codegen.WorkloadSite
		want    []audittype.Workload
	}{
		{
			name: "merged across envs, sorted, located",
			renders: []envRender{
				{env: "prod", entities: &KCLEntities{Workloads: []WorkloadEntity{api(RuntimeHosted), indexer}}},
				{env: "dev", entities: &KCLEntities{Workloads: []WorkloadEntity{api(RuntimeHost), db}}},
				{env: "staging", err: errors.New("render failed")},
			},
			sites: sites,
			want: []audittype.Workload{
				{Name: "api", Kind: "service", Build: "go", Image: "shop", Runtimes: map[string]string{"dev": "host", "prod": "hosted"}, File: "deploy/kcl/workloads.k", Line: 4},
				{Name: "indexer", Kind: "pipeline-stage", Build: "shell", Runtimes: map[string]string{"prod": "external"}},
				{Name: "postgres", Kind: "service", Build: "none", Image: "postgres:16", Runtimes: map[string]string{"dev": "compose"}},
				{Name: "reports", Kind: "cron", Build: "docker", Runtimes: map[string]string{}, File: "deploy/kcl/workloads.k", Line: 30},
				{Name: "seed", Kind: "service", Build: "none", Runtimes: map[string]string{}, File: "deploy/kcl/workloads.k", Line: 40},
			},
		},
		{
			name: "no envs and no literals",
			want: []audittype.Workload{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := projectWorkloadInventory(tc.renders, tc.sites)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("projectWorkloadInventory =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}
