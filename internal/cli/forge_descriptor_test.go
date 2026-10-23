package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/reliant-labs/forge/internal/codegen"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// TestDescriptorParsesErrors verifies that a (forge.v1.method).errors list
// is plumbed onto codegen.Method.Errors. This is the proto-side half of
// the typed-error-contract feature: the LLM/handler-author reads the
// generated methodErrors map; the data has to actually come through the
// descriptor extraction or that map is always empty.
func TestDescriptorParsesErrors(t *testing.T) {
	method := codegen.Method{Name: "GetWidget"}
	mo := &forgev1.MethodOptions{
		Errors: []string{"NotFound", "PermissionDenied"},
	}

	applyMethodOptions(&method, mo)

	want := []string{"NotFound", "PermissionDenied"}
	if !reflect.DeepEqual(method.Errors, want) {
		t.Errorf("Errors = %v, want %v", method.Errors, want)
	}
}

// Regression: methods with no declared errors must yield a nil/empty
// slice (not panic, not propagate a stale value). The template omits
// methods with no errors from the methodErrors map; that hinges on
// len(method.Errors) == 0 here.
func TestDescriptorParsesErrors_Unset(t *testing.T) {
	method := codegen.Method{Name: "GetWidget"}
	mo := &forgev1.MethodOptions{} // no errors field set

	applyMethodOptions(&method, mo)

	if len(method.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", method.Errors)
	}
}

// Regression: applyMethodOptions must coexist with the existing
// AuthRequired plumbing — adding the new errors field should not break
// the optional-bool auth_required round-trip.
func TestDescriptorParsesErrors_PreservesAuthRequired(t *testing.T) {
	method := codegen.Method{Name: "GetWidget", AuthRequired: true}
	authOff := false
	mo := &forgev1.MethodOptions{
		AuthRequired: &authOff,
		Errors:       []string{"NotFound"},
	}

	applyMethodOptions(&method, mo)

	if method.AuthRequired {
		t.Errorf("AuthRequired = true, want false (explicit opt-out)")
	}
	if len(method.Errors) != 1 || method.Errors[0] != "NotFound" {
		t.Errorf("Errors = %v, want [NotFound]", method.Errors)
	}
}

// (forge.v1.method).timeout reaches codegen.Method.Timeout, which projects
// into pkg/middleware/procedures_gen.go's ProcedureDeadlines. Unset stays
// zero: the chain's Deadline slot default, not "no deadline".
func TestDescriptorParsesTimeout(t *testing.T) {
	method := codegen.Method{Name: "Export"}
	applyMethodOptions(&method, &forgev1.MethodOptions{Timeout: durationpb.New(2 * time.Minute)})
	if method.Timeout != 2*time.Minute {
		t.Errorf("Timeout = %s, want 2m0s", method.Timeout)
	}

	unset := codegen.Method{Name: "Get"}
	applyMethodOptions(&unset, &forgev1.MethodOptions{})
	if unset.Timeout != 0 {
		t.Errorf("unset Timeout = %s, want 0 (the slot's default)", unset.Timeout)
	}
}

// Regression: per-invocation fragments under gen/.descriptor.d/ must be
// merged into a single forge_descriptor.json with all sections preserved
// and a deterministic ordering. This is the load-bearing fix for the
// parallel-buf-plugin race where two plugin processes overwrote each
// other's read-modify-write through the prior in-process flock.
func TestMergeDescriptorFragments_CombinesAllFragments(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, descriptorStageDir)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}

	frags := []ForgeDescriptor{
		{
			// "services" fragment — what the admin_server proto plugin
			// invocation would emit.
			Services: []codegen.ServiceDef{{Name: "AdminServerService"}},
		},
		{
			// A second services fragment from another plugin invocation.
			Services: []codegen.ServiceDef{{Name: "WorkspaceService"}},
		},
		{
			// "configs" fragment — what the proto/config plugin invocation
			// would emit.
			Configs: []codegen.ConfigMessage{{Name: "Config"}},
		},
	}
	for i, f := range frags {
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("marshal fragment %d: %v", i, err)
		}
		// Names chosen so sort.Strings produces a known order; the merge
		// step itself is order-independent for correctness, but
		// deterministic ordering matters for repeatable codegen output.
		name := []string{"a.json", "b.json", "c.json"}[i]
		if err := os.WriteFile(filepath.Join(stage, name), data, 0o644); err != nil {
			t.Fatalf("write fragment %d: %v", i, err)
		}
	}

	if err := MergeDescriptorFragments(dir); err != nil {
		t.Fatalf("MergeDescriptorFragments: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(dir, "forge_descriptor.json"))
	if err != nil {
		t.Fatalf("read merged descriptor: %v", err)
	}
	var got ForgeDescriptor
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse merged descriptor: %v", err)
	}
	// A text file ends in a newline. Without one, every end-of-file fixer
	// (forge's own pre-commit hook included) rewrites the generated file,
	// and the next generate rewrites it back.
	if !strings.HasSuffix(string(out), "}\n") {
		t.Errorf("forge_descriptor.json does not end in a newline: %q", out[max(0, len(out)-20):])
	}

	if len(got.Services) != 2 || got.Services[0].Name != "AdminServerService" || got.Services[1].Name != "WorkspaceService" {
		t.Errorf("services not merged correctly: %+v", got.Services)
	}
	if len(got.Configs) != 1 || got.Configs[0].Name != "Config" {
		t.Errorf("configs not merged correctly: %+v", got.Configs)
	}

	// Stage dir should be cleaned up after a successful merge.
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stage dir should be removed, stat err = %v", err)
	}
}

// MergeDescriptorFragments must be a no-op when no fragments exist
// (clean projects with no services / entities / configs).
func TestMergeDescriptorFragments_NoStageDir_NoError(t *testing.T) {
	dir := t.TempDir()
	if err := MergeDescriptorFragments(dir); err != nil {
		t.Fatalf("MergeDescriptorFragments on empty dir: %v", err)
	}
}

// TestSecretFieldMarkerRE pins the descriptor-side `// forge:secret`
// marker grammar. buf strips the `//` before protogen sees the comment, so
// the token is matched WITHOUT slashes; leading/trailing/prose variants
// and multi-line doc blocks all resolve.
func TestSecretFieldMarkerRE(t *testing.T) {
	match := []string{
		" forge:secret\n",
		"forge:secret",
		"  forge:secret — database credentials\n",
		"line one about the field\n forge:secret\n",
		"forge:secret\n",
	}
	for _, s := range match {
		if !secretFieldMarkerRE.MatchString(s) {
			t.Errorf("expected secret marker to match %q", s)
		}
	}
	noMatch := []string{
		" not a marker\n",
		" forge:secretly\n", // must not match a longer token
		" forge:entity\n",
		"",
	}
	for _, s := range noMatch {
		if secretFieldMarkerRE.MatchString(s) {
			t.Errorf("did not expect secret marker to match %q", s)
		}
	}
}

// TestReadOnlyFieldMarkerRE pins the descriptor-side `// forge:read-only`
// marker grammar — the INPUT-side mirror of `// forge:secret`. buf strips the
// `//` before protogen sees the comment, so the token matches WITHOUT slashes;
// leading/trailing/prose variants and multi-line doc blocks all resolve.
func TestReadOnlyFieldMarkerRE(t *testing.T) {
	match := []string{
		" forge:read-only\n",
		"forge:read-only",
		"  forge:read-only — the server owns this\n",
		"line one about the field\n forge:read-only\n",
		"forge:read-only\n",
	}
	for _, s := range match {
		if !readOnlyFieldMarkerRE.MatchString(s) {
			t.Errorf("expected read-only marker to match %q", s)
		}
	}
	noMatch := []string{
		" not a marker\n",
		" forge:read-onlyish\n", // must not match a longer token
		" forge:secret\n",
		" forge:entity\n",
		"",
	}
	for _, s := range noMatch {
		if readOnlyFieldMarkerRE.MatchString(s) {
			t.Errorf("did not expect read-only marker to match %q", s)
		}
	}
	// forge:generated puts a field off the write surface on this pass too.
	if !readOnlyFieldMarkerRE.MatchString(" forge:generated a - b\n") {
		t.Error("forge:generated must be read-only on the descriptor path")
	}
}

// TestFieldGeneratedExpr pins the descriptor-side expression extractor: the
// same rest-of-line rule as the raw scanner, from either comment position,
// so `forge scaffold entity --from-proto <svc>.<Message>` births the column
// a bare `forge scaffold` would.
func TestFieldGeneratedExpr(t *testing.T) {
	field := func(leading, trailing string) *protogen.Field {
		return &protogen.Field{Comments: protogen.CommentSet{
			Leading:  protogen.Comments(leading),
			Trailing: protogen.Comments(trailing),
		}}
	}
	cases := []struct {
		name string
		f    *protogen.Field
		want string
	}{
		{"trailing", field("", " forge:generated round(quantity * unit_price_cents)::BIGINT\n"), "round(quantity * unit_price_cents)::BIGINT"},
		{"leading under prose", field(" What is left to pay.\n forge:generated amount_cents - amount_paid_cents\n", ""), "amount_cents - amount_paid_cents"},
		{"leading read-only, trailing generated", field(" forge:read-only\n", " forge:generated a * 2\n"), "a * 2"},
		{"no expression", field("", " forge:generated\n"), ""},
		{"unmarked", field(" just prose\n", ""), ""},
	}
	for _, c := range cases {
		if got := fieldGeneratedExpr(c.f); got != c.want {
			t.Errorf("%s: fieldGeneratedExpr = %q, want %q", c.name, got, c.want)
		}
	}
}
