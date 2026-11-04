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

const tierPkgPath = "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"

// TestTierKCLIsCurrent regenerates kcl/tiers/tiers_gen.k from the Go spec types
// and fails if the committed file differs. With FORGE_UPDATE_GENERATED=1 it
// rewrites the file instead.
func TestTierKCLIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("loads Go packages (seconds); full mode only")
	}
	root := forgeRepoRoot(t)
	in, err := LoadTierSchemas(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GenerateTierKCL(in, tierPkgPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, TierKCLPath)
	if os.Getenv("FORGE_UPDATE_GENERATED") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with FORGE_UPDATE_GENERATED=1)", TierKCLPath, err)
	}
	if string(want) != got {
		t.Fatalf("%s is stale; run: FORGE_UPDATE_GENERATED=1 go test ./internal/codegen -run TestTierKCLIsCurrent", TierKCLPath)
	}
}

// TestTierKCLMatchesGoFieldForField parses the GENERATED KCL (not the
// generator's input) and checks that every tier struct's field set is exactly
// the Go struct's JSON field set. The oracle is reflection over the real Go
// types, a different route from the controller-tools schema the generator
// read. So a generator that dropped, renamed or invented a field fails here,
// and so would a Go field the schema derivation missed.
func TestTierKCLMatchesGoFieldForField(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the generated module; full mode only")
	}
	src, err := os.ReadFile(filepath.Join(forgeRepoRoot(t), TierKCLPath))
	if err != nil {
		t.Fatal(err)
	}
	kclFields := parseKCLSchemaFields(string(src))

	for _, typ := range []reflect.Type{
		reflect.TypeOf(v1alpha1.WorkloadSpec{}), reflect.TypeOf(v1alpha1.StaticSiteSpec{}), reflect.TypeOf(v1alpha1.ManagedDatabaseSpec{}),
		reflect.TypeOf(v1alpha1.EnvVar{}), reflect.TypeOf(v1alpha1.SecretKeyRef{}), reflect.TypeOf(v1alpha1.DatabaseRef{}),
		reflect.TypeOf(v1alpha1.ConfigMapKeyRef{}), reflect.TypeOf(v1alpha1.FieldRef{}), reflect.TypeOf(v1alpha1.WorkloadURLRef{}),
		reflect.TypeOf(v1alpha1.Resources{}), reflect.TypeOf(v1alpha1.Probes{}), reflect.TypeOf(v1alpha1.Port{}),
		reflect.TypeOf(v1alpha1.PolicyRule{}), reflect.TypeOf(v1alpha1.StaticSiteCDN{}),
		reflect.TypeOf(v1alpha1.Container{}), reflect.TypeOf(v1alpha1.Volume{}), reflect.TypeOf(v1alpha1.VolumeSource{}),
		reflect.TypeOf(v1alpha1.SecretVolumeSource{}), reflect.TypeOf(v1alpha1.ConfigMapVolumeSource{}), reflect.TypeOf(v1alpha1.KeyToPath{}),
		reflect.TypeOf(v1alpha1.EmptyDirVolumeSource{}), reflect.TypeOf(v1alpha1.PVCVolumeSource{}), reflect.TypeOf(v1alpha1.Toleration{}),
	} {
		name := kclSchemaName(typ.Name())
		var goFields []string
		for i := 0; i < typ.NumField(); i++ {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			goFields = append(goFields, tag)
		}
		sort.Strings(goFields)
		got := kclFields[name]
		sort.Strings(got)
		if !reflect.DeepEqual(goFields, got) {
			t.Errorf("schema %s: KCL fields %v, Go JSON fields %v", name, got, goFields)
		}
	}
	for _, status := range []string{"WorkloadStatus", "TierStatus", "Phase"} {
		if _, ok := kclFields[status]; ok {
			t.Errorf("%s was generated: status is observed, never authored, so it has no KCL schema", status)
		}
	}
}

// parseKCLSchemaFields reads `schema X:` blocks and their `name[?]: type`
// field lines.
func parseKCLSchemaFields(src string) map[string][]string {
	out := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "schema ") {
			cur = strings.TrimSuffix(strings.TrimPrefix(line, "schema "), ":")
			out[cur] = nil
			continue
		}
		if cur == "" || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			if line != "" && !strings.HasPrefix(line, " ") {
				cur = ""
			}
			continue
		}
		field := strings.TrimSpace(line)
		name, _, ok := strings.Cut(field, ":")
		if !ok || strings.ContainsAny(name, " \"(") || name == "check" {
			continue
		}
		out[cur] = append(out[cur], strings.TrimSuffix(name, "?"))
	}
	return out
}
