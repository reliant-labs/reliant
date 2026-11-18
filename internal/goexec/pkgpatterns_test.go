package goexec

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const vetFailing = "package x\n\nimport \"fmt\"\n\nfunc F() { fmt.Printf(\"%d\", \"not an int\") }\n"

func TestExcludeNodeModules(t *testing.T) {
	if testing.Short() {
		t.Skip("runs `go list` in a temp module; runs in task test")
	}
	base := map[string]string{
		"go.mod":          "module example.com/m\n\ngo 1.24\n",
		"main.go":         "package main\n\nfunc main() {}\n",
		"internal/a/a.go": "package a\n",
	}
	with := func(extra map[string]string) string {
		files := map[string]string{}
		for k, v := range base {
			files[k] = v
		}
		for k, v := range extra {
			files[k] = v
		}
		return writeTree(t, files)
	}

	t.Run("drops packages under node_modules at any depth", func(t *testing.T) {
		dir := with(map[string]string{
			"web/node_modules/x/bad.go":                   vetFailing,
			"web/node_modules/flatted/golang/pkg/f/f.go":  "package f\n",
			"apps/site/node_modules/y/bad.go":             vetFailing,
			"apps/site/node_modules/@scope/pkg/nested.go": "package nested\n",
		})
		got := ExcludeNodeModules(context.Background(), dir, nil, []string{"./..."})
		want := []string{".", "./internal/a"}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("no node_modules leaves patterns untouched", func(t *testing.T) {
		dir := with(nil)
		in := []string{"./..."}
		if got := ExcludeNodeModules(context.Background(), dir, nil, in); !slices.Equal(got, in) {
			t.Fatalf("got %v, want %v", got, in)
		}
	})

	t.Run("patterns without a wildcard are not rewritten", func(t *testing.T) {
		dir := with(map[string]string{"web/node_modules/x/bad.go": vetFailing})
		in := []string{"./internal/a"}
		if got := ExcludeNodeModules(context.Background(), dir, nil, in); !slices.Equal(got, in) {
			t.Fatalf("got %v, want %v", got, in)
		}
	})

	t.Run("a scoped wildcard stays scoped", func(t *testing.T) {
		dir := with(map[string]string{
			"web/node_modules/x/bad.go": vetFailing,
			"web/real/real.go":          "package real\n",
		})
		got := ExcludeNodeModules(context.Background(), dir, nil, []string{"./web/..."})
		want := []string{"./web/real"}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	// The module reached through a symlink, named by $PWD: go list then
	// reports its package dirs under that spelling, not the resolved one.
	// On macOS every t.TempDir() is reached that way (/var -> /private/var),
	// which is why lint's TestGolangciLintIgnoresNodeModules and
	// TestContractLinterIgnoresNodeModules failed there and not on Linux.
	// The symlink here is the module dir itself, which also covers the walk
	// for a node_modules dir, which does not descend into a symlinked root.
	t.Run("a module reached through a symlink named by PWD", func(t *testing.T) {
		real := with(map[string]string{"web/node_modules/x/bad.go": vetFailing})
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("cannot symlink: %v", err)
		}
		got := ExcludeNodeModules(context.Background(), link, append(Env(), "PWD="+link), []string{"./..."})
		want := []string{".", "./internal/a"}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("a broken module returns the patterns for the caller to report", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"go.mod":                    "this is not a go.mod\n",
			"web/node_modules/x/bad.go": vetFailing,
		})
		in := []string{"./..."}
		if got := ExcludeNodeModules(context.Background(), dir, nil, in); !slices.Equal(got, in) {
			t.Fatalf("got %v, want %v", got, in)
		}
	})
}
