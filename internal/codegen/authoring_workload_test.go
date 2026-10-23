package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// TestAuthoringWorkloadCoversSpec pins the authoring schema to the wire type.
//
// fw.Workload (kcl/workload.k) restates WorkloadSpec's fields instead of
// inheriting the generated tiers.Workload, because three of them differ in
// type (env is a map, image is optional, probes are defaulted) and KCL
// refuses a type change in a subschema. The cost of restating is drift: a
// field added to WorkloadSpec (and so to tiers.Workload) that the authoring
// schema does not carry is a field no author can set. This test is what
// makes the restatement safe. Every WorkloadSpec JSON field must be a field
// of `schema Workload` in kcl/workload.k, and the authoring-only fields must
// not collide with a spec field.
func TestAuthoringWorkloadCoversSpec(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(forgeRepoRoot(t), "kcl", "workload.k"))
	if err != nil {
		t.Fatal(err)
	}
	authoring := map[string]bool{}
	for _, f := range parseKCLSchemaFields(string(src))["Workload"] {
		authoring[f] = true
	}
	if len(authoring) == 0 {
		t.Fatal("no `schema Workload` fields parsed from kcl/workload.k")
	}

	var missing []string
	spec := map[string]bool{}
	typ := reflect.TypeOf(v1alpha1.WorkloadSpec{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		spec[name] = true
		if !authoring[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("fw.Workload (kcl/workload.k) lacks WorkloadSpec field(s) %v: add each with the generated tiers type, and lower it in kcl/render.k _spec", missing)
	}

	for _, own := range []string{"name", "build", "runtime", "config_secrets"} {
		if !authoring[own] {
			t.Errorf("authoring field %q is missing from fw.Workload", own)
		}
		if spec[own] {
			t.Errorf("authoring-only field %q collides with a WorkloadSpec field", own)
		}
	}
}
