package contractsrc

import (
	"os"
	"path/filepath"
	"testing"
)

func writePkg(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestContractSource(t *testing.T) {
	cases := []struct {
		name      string
		files     map[string]string
		has       bool
		marked    bool
		path      string // base name, "" for none
		handCount int
	}{
		{
			name:  "nothing",
			files: map[string]string{"a.go": "package p\n\ntype x struct{}\n"},
		},
		{
			name:      "hand-written contract.go",
			files:     map[string]string{"contract.go": "package p\n\ntype Service interface{ Do() }\n"},
			has:       true,
			path:      FileName,
			handCount: 1,
		},
		{
			name:   "marked struct, spaced",
			files:  map[string]string{"service.go": "package p\n\n// service does it.\n//\n// forge:contract\ntype service struct{}\n"},
			has:    true,
			marked: true,
			path:   DerivedFileName,
		},
		{
			name:   "marked struct in a group, inline",
			files:  map[string]string{"service.go": "package p\n\ntype (\n\tservice struct{} //forge:contract\n\tother   struct{}\n)\n"},
			has:    true,
			marked: true,
			path:   DerivedFileName,
		},
		{
			// A marked interface keeps its old meaning: it is hand-written.
			name:      "marked interface",
			files:     map[string]string{"gw.go": "package p\n\n//forge:contract\ntype Gateway interface{ Do() }\n"},
			handCount: 1,
		},
		{
			name:  "prose that mentions the marker",
			files: map[string]string{"service.go": "package p\n\n// Put forge:contract on the type to derive it.\ntype service struct{}\n"},
		},
		{
			// Generated and test files never carry the marker.
			name: "marker only in generated and test files",
			files: map[string]string{
				"x_gen.go":  "package p\n\n//forge:contract\ntype g struct{}\n",
				"x_test.go": "package p\n\n//forge:contract\ntype tt struct{}\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writePkg(t, tc.files)
			if got := HasContract(dir); got != tc.has {
				t.Errorf("HasContract = %v, want %v", got, tc.has)
			}
			if got := HasMarkedType(dir); got != tc.marked {
				t.Errorf("HasMarkedType = %v, want %v", got, tc.marked)
			}
			got := Path(dir)
			if tc.path == "" && got != "" || tc.path != "" && got != filepath.Join(dir, tc.path) {
				t.Errorf("Path = %q, want %q", got, tc.path)
			}
			if n := len(HandWrittenContracts(dir)); n != tc.handCount {
				t.Errorf("HandWrittenContracts = %d, want %d", n, tc.handCount)
			}
		})
	}
}

// TestMarkedTypes_Constrained: a marker is found whatever the build target
// (so discovery agrees on every platform), and a file some build leaves
// out is flagged for derivation to refuse.
func TestMarkedTypes_Constrained(t *testing.T) {
	mark := "package p\n\n//forge:contract\ntype service struct{}\n"
	cases := map[string]bool{
		"plain.go":             false,
		"svc_linux.go":         true,
		"svc_arm64.go":         true,
		"svc_windows_amd64.go": true,
		"svc_service.go":       false,
	}
	for name, want := range cases {
		dir := writePkg(t, map[string]string{name: mark})
		m := MarkedTypes(dir)
		if len(m) != 1 || m[0].Constrained != want {
			t.Errorf("%s: MarkedTypes = %+v, want one with Constrained=%v", name, m, want)
		}
	}
	for _, header := range []string{"//go:build !windows\n\n", "// +build linux\n\n"} {
		dir := writePkg(t, map[string]string{"svc.go": header + mark})
		if m := MarkedTypes(dir); len(m) != 1 || !m[0].Constrained || !HasContract(dir) {
			t.Errorf("%q: MarkedTypes = %+v, want one, constrained, and HasContract", header, m)
		}
	}
}

func TestMarkedTypes_GenericAndAlias(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"a.go": "package p\n\n//forge:contract\ntype g[T any] struct{ v T }\n",
		"b.go": "package p\n\ntype impl struct{}\n\n//forge:contract\ntype al = impl\n",
	})
	m := MarkedTypes(dir)
	if len(m) != 2 || !m[0].Generic || m[0].Name != "g" || !m[1].Alias || m[1].File != "b.go" || m[1].Line != 6 {
		t.Fatalf("MarkedTypes = %+v", m)
	}
}
