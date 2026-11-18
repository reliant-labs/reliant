package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHasExcludeContractDirective(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name: "package doc directive, spaced",
			files: map[string]string{
				"contract.go": "// forge:exclude-contract\npackage foo\n",
			},
			want: true,
		},
		{
			name: "package doc directive, unspaced",
			files: map[string]string{
				"contract.go": "//forge:exclude-contract\npackage foo\n",
			},
			want: true,
		},
		{
			name: "free-standing directive in doc.go",
			files: map[string]string{
				"contract.go": "package foo\n",
				"doc.go":      "package foo\n\n//forge:exclude-contract\n",
			},
			want: true,
		},
		{
			name: "no directive",
			files: map[string]string{
				"contract.go": "package foo\n\ntype Service interface{}\n",
			},
			want: false,
		},
		{
			name: "prose mentioning directive is not a match",
			files: map[string]string{
				"contract.go": "// This package could use forge:exclude-contract but does not.\npackage foo\n",
			},
			want: false,
		},
		{
			name: "directive only in _test.go is ignored",
			files: map[string]string{
				"contract.go":      "package foo\n",
				"contract_test.go": "//forge:exclude-contract\npackage foo\n",
			},
			want: false,
		},
		{
			name: "directive only in _gen.go is ignored",
			files: map[string]string{
				"contract.go": "package foo\n",
				"mock_gen.go": "//forge:exclude-contract\npackage foo\n",
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				writeGoFile(t, dir, name, content)
			}
			if got := HasExcludeContractDirective(dir); got != tc.want {
				t.Fatalf("HasExcludeContractDirective = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParseExcludeContractComment pins the one spelling forge:lint's reason
// requirement is keyed on: `forge:exclude-contract: <why>`. A bare marker is
// still the directive (it excludes; lint refuses it), prose is not.
func TestParseExcludeContractComment(t *testing.T) {
	cases := []struct {
		raw        string
		wantOK     bool
		wantReason string
	}{
		{"//forge:exclude-contract", true, ""},
		{"// forge:exclude-contract", true, ""},
		{"//forge:exclude-contract: pure string helpers", true, "pure string helpers"},
		{"// forge:exclude-contract:   CRD API types  ", true, "CRD API types"},
		{"//forge:exclude-contract:", true, ""},
		{"// forge:exclude-contract, forge:outbound-io, …", false, ""},
		{"// This package could use forge:exclude-contract but does not.", false, ""},
		{"//forge:exclude-contracts: plural is a typo, not the directive", false, ""},
	}
	for _, tc := range cases {
		reason, ok := ParseExcludeContractComment(tc.raw)
		if ok != tc.wantOK || reason != tc.wantReason {
			t.Errorf("ParseExcludeContractComment(%q) = (%q, %v), want (%q, %v)", tc.raw, reason, ok, tc.wantReason, tc.wantOK)
		}
	}
}

// TestFindExcludeContractMarker_PrefersReasoned: a package that states its
// reason anywhere is not refused over a second, bare copy of the marker.
func TestFindExcludeContractMarker_PrefersReasoned(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "a.go", "//forge:exclude-contract\npackage foo\n")
	writeGoFile(t, dir, "b.go", "package foo\n\n//forge:exclude-contract: embed-only data package\n")
	m, ok := FindExcludeContractMarker(dir)
	if !ok || m.Reason != "embed-only data package" || filepath.Base(m.File) != "b.go" || m.Line != 3 {
		t.Fatalf("FindExcludeContractMarker = %+v, %v", m, ok)
	}
}

func TestHasExternalComponentDirective(t *testing.T) {
	cases := []struct {
		name string
		file string
		want bool
	}{
		{"external-component spaced", "// forge:external-component\npackage foo\n", true},
		{"external-component unspaced", "//forge:external-component\npackage foo\n", true},
		{"provided alias", "//forge:provided\npackage foo\n", true},
		{"no directive", "package foo\n", false},
		{"exclude-contract is NOT external-component", "//forge:exclude-contract\npackage foo\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGoFile(t, dir, "contract.go", tc.file)
			if got := HasExternalComponentDirective(dir); got != tc.want {
				t.Fatalf("HasExternalComponentDirective = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestComposedComponents proves both markers drop a component from the Build
// graph while leaving unmarked ones in place — and that only not-a-component
// is reported back, because only it makes a claim a consumer can contradict.
func TestComposedComponents(t *testing.T) {
	projectDir := t.TempDir()

	writeGoFile(t, filepath.Join(projectDir, "internal", "billing"),
		"contract.go", "//forge:external-component\npackage billing\n\ntype Service interface{}\n")
	writeGoFile(t, filepath.Join(projectDir, "internal", "user"),
		"contract.go", "package user\n\ntype Service interface{}\n")
	writeGoFile(t, filepath.Join(projectDir, "internal", "cliauth"),
		"contract.go", "//forge:not-a-component: built by the login command\npackage cliauth\n\ntype Service interface{}\n")
	// A worker is a server component by definition; the marker is not
	// honoured there (lint reports it), or lifecycle.go would name a field
	// compose.go never declared.
	writeGoFile(t, filepath.Join(projectDir, "internal", "workers", "sweeper"),
		"worker.go", "//forge:not-a-component: misplaced\npackage sweeper\n")

	comps := []BuildComponent{
		{FieldName: "Billing", compRoleRoot: "internal", compImportLeaf: "billing"},
		{FieldName: "User", compRoleRoot: "internal", compImportLeaf: "user"},
		{FieldName: "Cliauth", compRoleRoot: "internal", compImportLeaf: "cliauth"},
		{FieldName: "Sweeper", compRoleRoot: "internal/workers", compImportLeaf: "sweeper"},
	}

	composed, notComponents := composedComponents(projectDir, comps)
	var names []string
	for _, c := range composed {
		names = append(names, c.FieldName)
	}
	if strings.Join(names, ",") != "User,Sweeper" {
		t.Fatalf("composedComponents kept %v, want [User Sweeper]", names)
	}
	if len(notComponents) != 1 || notComponents[0].FieldName != "Cliauth" {
		t.Fatalf("notComponents = %+v, want only [Cliauth]", notComponents)
	}
}

func TestNotAComponentDirective(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		want       bool
		wantReason string
	}{
		{"reasoned unspaced", "//forge:not-a-component: CLI-only\npackage foo\n", true, "CLI-only"},
		{"reasoned spaced", "// forge:not-a-component: daemon-only\npackage foo\n", true, "daemon-only"},
		{"bare still counts for codegen", "//forge:not-a-component\npackage foo\n", true, ""},
		{"prose mentioning it is not the directive", "// see forge:not-a-component for CLI packages\npackage foo\n", false, ""},
		{"external-component is NOT not-a-component", "//forge:external-component\npackage foo\n", false, ""},
		{"no directive", "package foo\n", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGoFile(t, dir, "contract.go", tc.file)
			if got := HasNotAComponentDirective(dir); got != tc.want {
				t.Fatalf("HasNotAComponentDirective = %v, want %v", got, tc.want)
			}
			if m, ok := FindNotAComponentMarker(dir); ok && m.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", m.Reason, tc.wantReason)
			}
		})
	}
}

// TestIsWiredComponentDir_NotAComponent: a not-a-component package has no
// forge-built instance, so it takes no slot in the test-factory namespace —
// the same answer the helpers generator and ComputeTestHelperName must share.
func TestIsWiredComponentDir_NotAComponent(t *testing.T) {
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "billing")
	writeGoFile(t, dir, "contract.go", "//forge:not-a-component: CLI-only\npackage billing\n\ntype Service interface{}\n")
	if IsWiredComponentDir(dir) {
		t.Fatal("a not-a-component package must not count as a wired component")
	}
	if got := ComputeTestHelperName("billing", projectDir); got != "Billing" {
		t.Fatalf("handler billing beside a not-a-component internal/billing must keep its plain factory name, got %q", got)
	}
}
