package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeServiceProto lays down proto/services/<svc>/v1/<svc>.proto declaring
// pkg — the shape `forge scaffold service` and a hand-split project both
// produce.
func writeServiceProto(t *testing.T, root, svc, pkg string) {
	t.Helper()
	dir := filepath.Join(root, "proto", "services", svc, "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("syntax = \"proto3\";\n\npackage %s;\n\nservice %sService {}\n", pkg, svc)
	if err := os.WriteFile(filepath.Join(dir, svc+".proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeProtoPackageForgeYAML(t *testing.T, root, extra string) {
	t.Helper()
	src := "name: myapp\nmodule_path: example.com/myapp\n" + extra
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scaffoldedProto(t *testing.T, root, svc string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "proto", "services", svc, "v1", svc+".proto"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestGenerateServiceFiles_InfersSharedPackage is the control-plane report: a
// project whose services all share ONE proto package must get that package
// on a newly scaffolded service, not `services.<name>.v1`. Hand-fixing it
// after the fact was the only way out before.
func TestGenerateServiceFiles_InfersSharedPackage(t *testing.T) {
	root := t.TempDir()
	for _, svc := range []string{"billing", "daemon", "user"} {
		writeServiceProto(t, root, svc, "controlplane.v1")
	}

	if err := GenerateServiceFiles(root, "example.com/myapp", "git-credential-internal", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles: %v", err)
	}

	got := scaffoldedProto(t, root, "git_credential_internal")
	if !strings.Contains(got, "\npackage controlplane.v1;\n") {
		t.Errorf("scaffolded proto should share the project's package controlplane.v1:\n%s", got)
	}
	// go_package stays per-directory: every service owns its own generated
	// Go package even when the wire package is shared. That is the layout
	// buf's paths=source_relative emits, and the one handlers import.
	want := `option go_package = "example.com/myapp/gen/services/git_credential_internal/v1;git_credential_internalv1";`
	if !strings.Contains(got, want) {
		t.Errorf("go_package must stay per-service directory, want %q in:\n%s", want, got)
	}
}

// A single deliberate outlier must not veto the convention. control-plane
// carries proto/services/project/v1 as `package reliant.v1` — a wire-
// compatible mirror of another product's API — beside twenty
// `controlplane.v1` services. Requiring unanimity would hand that project the
// fallback forever, which is the defect this test exists to prevent.
func TestGenerateServiceFiles_MajorityWinsOverOutlier(t *testing.T) {
	root := t.TempDir()
	for _, svc := range []string{"billing", "daemon", "user"} {
		writeServiceProto(t, root, svc, "controlplane.v1")
	}
	writeServiceProto(t, root, "project", "reliant.v1")

	if err := GenerateServiceFiles(root, "example.com/myapp", "audit", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles: %v", err)
	}
	if got := scaffoldedProto(t, root, "audit"); !strings.Contains(got, "\npackage controlplane.v1;\n") {
		t.Errorf("3 of 4 services share controlplane.v1; the new one should too:\n%s", got)
	}
}

// A project that uses a per-service package under its OWN prefix gets the
// same pattern with the new service's name substituted, not forge's default
// prefix.
func TestGenerateServiceFiles_InfersPerServicePattern(t *testing.T) {
	root := t.TempDir()
	writeServiceProto(t, root, "orders", "acme.orders.v1")
	writeServiceProto(t, root, "admin_server", "acme.admin_server.v1")

	if err := GenerateServiceFiles(root, "example.com/myapp", "billing", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles: %v", err)
	}
	if got := scaffoldedProto(t, root, "billing"); !strings.Contains(got, "\npackage acme.billing.v1;\n") {
		t.Errorf("per-service pattern acme.<svc>.v1 should be followed:\n%s", got)
	}
}

// The fallback: nothing to infer from (a brand-new project) or no majority
// (a tie between conventions) keeps today's default, services.<name>.v1.
func TestGenerateServiceFiles_FallsBackToDefault(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"no existing services": func(*testing.T, string) {},
		"tie between conventions": func(t *testing.T, root string) {
			writeServiceProto(t, root, "billing", "controlplane.v1")
			writeServiceProto(t, root, "project", "reliant.v1")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(t, root)
			if err := GenerateServiceFiles(root, "example.com/myapp", "orders", "myapp"); err != nil {
				t.Fatalf("GenerateServiceFiles: %v", err)
			}
			if got := scaffoldedProto(t, root, "orders"); !strings.Contains(got, "\npackage services.orders.v1;\n") {
				t.Errorf("expected the services.<name>.v1 fallback:\n%s", got)
			}
		})
	}
}

// The default convention votes as itself: a project of forge-default
// services keeps getting forge-default packages, including one scaffolded by
// `forge project new --service a --service b` before any proto compiled.
func TestGenerateServiceFiles_DefaultConventionIsAVote(t *testing.T) {
	root := t.TempDir()
	writeServiceProto(t, root, "api", "services.api.v1")

	if err := GenerateServiceFiles(root, "example.com/myapp", "billing", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles: %v", err)
	}
	if got := scaffoldedProto(t, root, "billing"); !strings.Contains(got, "\npackage services.billing.v1;\n") {
		t.Errorf("expected services.billing.v1:\n%s", got)
	}
}

// The override: forge.yaml `api.proto_package` wins over whatever the
// existing protos say, in both of its forms — a fixed package, and a pattern
// carrying the {service} placeholder.
func TestGenerateServiceFiles_ConfigOverride(t *testing.T) {
	cases := []struct {
		name, setting, want string
	}{
		{"fixed package", "platform.v1", "platform.v1"},
		{"pattern", "acme.{service}.v2", "acme.billing.v2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// Every existing proto disagrees with the setting: the explicit
			// declaration must still win.
			writeServiceProto(t, root, "orders", "controlplane.v1")
			writeServiceProto(t, root, "users", "controlplane.v1")
			writeProtoPackageForgeYAML(t, root, "api:\n  proto_package: \""+tc.setting+"\"\n")

			if err := GenerateServiceFiles(root, "example.com/myapp", "billing", "myapp"); err != nil {
				t.Fatalf("GenerateServiceFiles: %v", err)
			}
			if got := scaffoldedProto(t, root, "billing"); !strings.Contains(got, "\npackage "+tc.want+";\n") {
				t.Errorf("api.proto_package %q should produce package %s:\n%s", tc.setting, tc.want, got)
			}
		})
	}
}

// The plan path (`GeneratePlanProtoFile`) writes the same file the scaffold
// does, so it must agree with it on the package.
func TestGeneratePlanProtoFile_UsesResolvedPackage(t *testing.T) {
	root := t.TempDir()
	writeServiceProto(t, root, "billing", "controlplane.v1")
	writeServiceProto(t, root, "daemon", "controlplane.v1")

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "audit", nil, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile: %v", err)
	}
	if got := scaffoldedProto(t, root, "audit"); !strings.Contains(got, "\npackage controlplane.v1;\n") {
		t.Errorf("plan proto should share the project's package:\n%s", got)
	}
}

// ResolveServiceProtoPackage's provenance is what the CLI prints, so a user
// can see WHY a package was chosen and where to change it.
func TestResolveServiceProtoPackage_ReportsSource(t *testing.T) {
	root := t.TempDir()
	for _, svc := range []string{"billing", "daemon", "user"} {
		writeServiceProto(t, root, svc, "controlplane.v1")
	}
	writeServiceProto(t, root, "project", "reliant.v1")

	res := ResolveServiceProtoPackage(root, "audit")
	if res.Package != "controlplane.v1" || res.Source != ProtoPackageInferred {
		t.Fatalf("got %+v, want inferred controlplane.v1", res)
	}
	if len(res.Dissenters) != 1 || !strings.Contains(res.Dissenters[0], "project") {
		t.Errorf("the outlier should be named so it is not silently overruled: %+v", res.Dissenters)
	}
	// The service being scaffolded never votes on its own package: a
	// --force re-stamp must not let the stub forge wrote last time decide.
	writeServiceProto(t, root, "audit", "services.audit.v1")
	if again := ResolveServiceProtoPackage(root, "audit"); again.Package != "controlplane.v1" {
		t.Errorf("the target service's own proto must not vote: %+v", again)
	}
}
