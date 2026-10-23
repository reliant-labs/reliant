package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanTestsReadsDeclarationsNotText(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a_e2e_test.go", "//go:build e2e\n\npackage p\n\nimport \"testing\"\n\n"+
		"func TestE2EReal(t *testing.T) {}\n"+
		// A Go file embedded in a string, as forge's e2e tests write into
		// the projects they scaffold: not a test of THIS package.
		"var embedded = `\nfunc TestEmbedded(t *testing.T) {}\n`\n"+
		"func TestMain(m *testing.M) {}\n"+
		"func Testlowercase(t *testing.T) {}\n"+
		"func TestHelper(t *testing.T, extra int) {}\n"+
		"func Test(t *testing.T) {}\n")
	write("b_test.go", "//go:build !e2e && !integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestUnit(t *testing.T) {}\n")
	write("c_e2e_linux_test.go", "//go:build e2e && linux\n\npackage p\n\nimport \"testing\"\n\nfunc TestE2ELinux(t *testing.T) {}\n")
	write("d_test.go", "package p\n\nimport \"testing\"\n\nfunc TestPlain(t *testing.T) {}\n")
	write("e_test.go", "//go:build integration || e2e\n\npackage p\n\nimport \"testing\"\n\nfunc TestE2EEither(t *testing.T) {}\n")

	got, err := scanTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []testFunc{
		{name: "TestE2EReal", file: "a_e2e_test.go", e2eFile: true},
		{name: "Test", file: "a_e2e_test.go", e2eFile: true},
		{name: "TestUnit", file: "b_test.go", e2eFile: false},
		{name: "TestE2ELinux", file: "c_e2e_linux_test.go", e2eFile: true},
		{name: "TestPlain", file: "d_test.go", e2eFile: false},
		{name: "TestE2EEither", file: "e_test.go", e2eFile: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanTests =\n%v\nwant\n%v", got, want)
	}
}
