package architecture

import (
	"go/ast"
	"go/build"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSerializerReachabilityIncludesCyclesAndTestOnlyImports(t *testing.T) {
	const module = "example.test/app"
	pkg := func(imports, tests []string) *modulePkg {
		return &modulePkg{bp: &build.Package{Imports: imports, TestImports: tests}}
	}
	idx := &moduleIndex{
		modulePath: module,
		reach:      map[string]bool{},
		pkgs: map[string]*modulePkg{
			module + "/a":          pkg([]string{module + "/b", module + corePkgSuffix}, nil),
			module + "/b":          pkg([]string{module + "/a"}, nil),
			module + "/serializer": pkg([]string{module + "/b"}, nil),
			module + "/testonly":   pkg(nil, []string{module + "/b"}),
			module + "/unrelated":  pkg(nil, nil),
		},
	}
	// Query a first: the old recursive memoisation permanently cached b=false
	// when b encountered the in-progress a, before a's edge to core was visited.
	for _, name := range []string{"a", "b", "serializer", "testonly"} {
		if !idx.reachesCore(module + "/" + name) {
			t.Errorf("%s can reach core but was excluded", name)
		}
	}
	if idx.reachesCore(module + "/unrelated") {
		t.Fatal("unrelated package reaches core")
	}
}

func TestSerializerScanRejectsMalformedAndMissingInputs(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "malformed.go")
	if err := os.WriteFile(bad, []byte("package malformed\nfunc encode( {\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{bad, filepath.Join(dir, "missing.go")} {
		t.Run(filepath.Base(name), func(t *testing.T) {
			c := &checker{fset: token.NewFileSet(), parsed: map[string]*ast.File{}}
			err := c.prefetch([]string{name})
			if err == nil || !strings.Contains(err.Error(), filepath.Base(name)) {
				t.Fatalf("input must fail with its path, got %v", err)
			}
			if verdict(&scanResult{checked: 1, parseErr: err}) == nil {
				t.Fatal("a parser failure passed because another serializer call was counted")
			}
		})
	}
}
