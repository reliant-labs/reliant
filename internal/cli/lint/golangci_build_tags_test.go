package lint

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// control-plane, 2026-10-22: noctx findings in an `//go:build integration`
// test file were reported by the repo's `task ci` (golangci-lint run twice,
// the second under --build-tags=integration) and not by `forge lint`, whose
// one golangci-lint run built the default tag set and never saw the file.

func writeTagTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscoverBuildTagSets(t *testing.T) {
	dir := writeTagTree(t, map[string]string{
		"go.mod":                   "module example.com/m\n\ngo 1.22\n",
		"a/a.go":                   "package a\n",
		"a/a_integration_test.go":  "//go:build integration\n\npackage a\n",
		"b/b_e2e_test.go":          "// Copyright\n\n//go:build e2e && !windows\n\npackage b\n",
		"c/c_linux.go":             "//go:build linux && amd64\n\npackage c\n",
		"c/c_ignore.go":            "//go:build ignore\n\npackage main\n",
		"d/d.go":                   "//go:build !integration\n\npackage d\n",
		"node_modules/x/x_test.go": "//go:build nodemodtag\n\npackage x\n",
		"testdata/t.go":            "//go:build testdatatag\n\npackage t\n",
		"sub/go.mod":               "module example.com/sub\n",
		"sub/s_test.go":            "//go:build othermodule\n\npackage s\n",
		"e/e.go":                   "package e\n\n// //go:build notaconstraint\n",
		".hidden/h_test.go":        "//go:build hiddentag\n\npackage h\n",
		"f/f_test.go":              "//go:build integration || e2e\n\npackage f\n",
	})
	got := discoverBuildTags(dir)

	want := []buildTagPackages{
		{tag: "e2e", dirs: []string{"./b", "./f"}},
		{tag: "integration", dirs: []string{"./a", "./f"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverBuildTags:\n got: %+v\nwant: %+v", got, want)
	}
}

// A package's own tagged files are linted, in the packages given: ./... under
// the tag would drop the files tagged !integration and re-lint everything.
func TestBuildTagPassesAreScopedToTheRunsPackages(t *testing.T) {
	sets := []buildTagPackages{{tag: "integration", dirs: []string{"./a", "./internal/db", "./internal/db/sub", "./x"}}}
	for _, tc := range []struct {
		paths []string
		want  []string
	}{
		{[]string{"./..."}, []string{"./a", "./internal/db", "./internal/db/sub", "./x"}},
		{[]string{"./internal/db/..."}, []string{"./internal/db", "./internal/db/sub"}},
		{[]string{"./internal/db"}, []string{"./internal/db"}},
		{[]string{"./a", "./x"}, []string{"./a", "./x"}},
		{[]string{"./proto/..."}, nil},
	} {
		var got []string
		for _, set := range scopeBuildTags(sets, tc.paths) {
			got = append(got, set.dirs...)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("paths %v: dirs = %v, want %v", tc.paths, got, tc.want)
		}
	}
}

func TestBuildTagsFromGolangciConfig(t *testing.T) {
	for name, body := range map[string]string{
		"v2": "version: \"2\"\nrun:\n  build-tags:\n    - integration\n    - e2e\n",
		"v1": "run:\n  build-tags: [integration, e2e]\n",
	} {
		dir := writeTagTree(t, map[string]string{".golangci.yml": body})
		if got, want := golangciConfigBuildTags(dir), []string{"integration", "e2e"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: build-tags = %v, want %v", name, got, want)
		}
	}
	if got := golangciConfigBuildTags(t.TempDir()); got != nil {
		t.Errorf("no config: build-tags = %v, want none", got)
	}
}

// tagAwareStub reports a noctx finding only when it is run under
// --build-tags=integration, and logs every argv it is given.
const tagAwareStub = `echo "$*" >> "$STUB_LOG"
for a in "$@"; do
  case "$a" in
    --build-tags=*integration*)
      echo "a/a_integration_test.go:7:2: net/http.Get must not be called (noctx)"
      exit 1;;
  esac
done
echo "0 issues."`

func TestGolangciLaneLintsBuildTaggedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through shell subprocesses; runs in task test")
	}
	dir := writeTagTree(t, map[string]string{
		"go.mod":                  "module example.com/m\n\ngo 1.22\n",
		"a/a.go":                  "package a\n",
		"a/a_integration_test.go": "//go:build integration\n\npackage a\n",
		"b/b.go":                  "package b\n",
	})
	log := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("STUB_LOG", log)
	fakeGolangciLint(t, tagAwareStub)

	rc := testRunCtx(t, false)
	rc.cwd = dir
	fs, gated := collectGolangciLintJSON(rc)

	if !gated {
		t.Fatalf("a finding in an integration-tagged file did not gate: %+v", fs)
	}
	var found bool
	for _, f := range fs {
		if f.File == "a/a_integration_test.go" && f.Rule == "noctx" {
			found = true
		}
	}
	if !found {
		t.Errorf("the noctx finding under --build-tags=integration was not reported: %+v", fs)
	}

	raw, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 2 {
		t.Fatalf("want the default pass and one tagged pass, got %d runs:\n%s", len(calls), raw)
	}
	if strings.Contains(calls[0], "--build-tags") {
		t.Errorf("the first pass must stay on the default tags: %s", calls[0])
	}
	if !strings.Contains(calls[1], "--build-tags=integration") || !strings.HasSuffix(calls[1], " ./a") {
		t.Errorf("the tagged pass must name the tags and only the packages holding tagged files: %s", calls[1])
	}
}

// A project with no tagged file pays for no second run.
func TestGolangciLaneRunsOncePerTreeWithoutTaggedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through shell subprocesses; runs in task test")
	}
	dir := writeTagTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"a/a.go": "package a\n",
	})
	log := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("STUB_LOG", log)
	fakeGolangciLint(t, tagAwareStub)

	rc := testRunCtx(t, false)
	rc.cwd = dir
	if fs, gated := collectGolangciLintJSON(rc); gated || len(fs) != 0 {
		t.Fatalf("clean tree gated: gated=%v %+v", gated, fs)
	}
	raw, _ := os.ReadFile(log)
	if n := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); n != 1 {
		t.Errorf("want one golangci-lint run, got %d:\n%s", n, raw)
	}
}

// forge e2e on main, 2026-10-23: every scaffold noun failed lint on
// tools/tools.go. Its `//go:build tools` tag exists to pin tool deps with
// blank imports of `package main` programs ("is a program, not an importable
// package"); a pass under it can only fail, so it never gets one.
func TestDiscoverBuildTagsSkipsToolPinsAndNonProjectTags(t *testing.T) {
	dir := writeTagTree(t, map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.22\n",
		"tools/tools.go":    "//go:build tools\n// +build tools\n\n// Package tools pins tools.\npackage tools\n\nimport (\n\t_ \"connectrpc.com/connect/cmd/protoc-gen-connect-go\"\n)\n",
		"pin/pin.go":        "//go:build buildtools\n\npackage pin\n\nimport _ \"example.com/x/cmd/prog\"\n",
		"neg/neg.go":        "//go:build !integration\n\npackage neg\n",
		"os/os.go":          "//go:build windows\n\npackage os\n",
		"ver/ver.go":        "//go:build go1.21\n\npackage ver\n",
		"ign/ign.go":        "//go:build ignore\n\npackage main\n",
		"mix/mix.go":        "//go:build linux && !cgo\n\npackage mix\n",
		"real/real_test.go": "//go:build integration\n\npackage real\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
	})
	got := discoverBuildTags(dir)
	want := []buildTagPackages{{tag: "integration", dirs: []string{"./real"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverBuildTags:\n got: %+v\nwant: %+v", got, want)
	}
}
