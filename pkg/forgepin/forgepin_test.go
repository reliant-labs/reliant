package forgepin

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/forgepinhook"
)

const forgeModule = "github.com/reliant-labs/forge/"

// forgeImportsAllowed are the forge packages this one may import. Each must
// itself import only the standard library and this list.
var forgeImportsAllowed = map[string]string{
	forgeModule + "internal/forgepinhook": "../../internal/forgepinhook",
}

// This package exists so a host can name a pinned forge without linking the
// forge CLI: reliant's skill catalog reached `cli.ResolvePinnedForge` and
// linked ~1,900 packages (KCL, docker, the cloud SDKs, k8s) into every test
// binary above it. So it, and every forge package it imports, imports the
// standard library and nothing else. A new third-party or forge import here
// puts that weight back: make the CLI hand the answer over through
// internal/forgepinhook instead.
//
// The walk parses the files in-process, so the test cache sees every input.
func TestImportsStayStandardLibrary(t *testing.T) {
	seen := map[string]bool{}
	var walk func(importPath, dir string)
	walk = func(importPath, dir string) {
		if seen[importPath] {
			return
		}
		seen[importPath] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		files := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			files++
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", filepath.Join(dir, name), err)
			}
			for _, spec := range f.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				if !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
					continue // the standard library
				}
				next, ok := forgeImportsAllowed[path]
				if !ok {
					t.Errorf("%s (%s) imports %s: %s must import only the standard library (and %v)", importPath, name, path, forgeModule+"pkg/forgepin", keys(forgeImportsAllowed))
					continue
				}
				walk(path, next)
			}
		}
		if files == 0 {
			t.Fatalf("%s: no Go files in %s", importPath, dir)
		}
	}
	walk(forgeModule+"pkg/forgepin", ".")
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func swapHook(t *testing.T, f func(context.Context, string, string, io.Writer) (forgepinhook.Result, bool, error)) {
	t.Helper()
	prev := forgepinhook.Resolve
	t.Cleanup(func() { forgepinhook.Resolve = prev })
	forgepinhook.Resolve = f
}

// Without the forge CLI linked there is no embedded forge to decide, and
// Resolve says so rather than answering "run the embedded forge".
func TestResolveWithoutTheCLI(t *testing.T) {
	swapHook(t, nil)
	got, ok, err := Resolve(context.Background(), t.TempDir(), "skill load", io.Discard)
	if !errors.Is(err, ErrNotEmbedded) || ok || got.Path != "" {
		t.Fatalf("got %+v, ok %v, err %v; want ErrNotEmbedded", got, ok, err)
	}
}

// Resolve hands the CLI's arguments through and its answer back.
func TestResolveUsesTheCLIsAnswer(t *testing.T) {
	var gotRoot, gotCommand string
	swapHook(t, func(_ context.Context, root, command string, _ io.Writer) (forgepinhook.Result, bool, error) {
		gotRoot, gotCommand = root, command
		return forgepinhook.Result{Path: "/cache/forge", Key: "pin:v1.2.3", Want: "forge_version v1.2.3", Source: "cache"}, true, nil
	})
	got, ok, err := Resolve(context.Background(), "/p", "skill load", io.Discard)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	want := Forge{Path: "/cache/forge", Key: "pin:v1.2.3", Want: "forge_version v1.2.3", Source: "cache"}
	if got.Path != want.Path || got.Key != want.Key || got.Want != want.Want || got.Source != want.Source || got.Args != nil {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if gotRoot != "/p" || gotCommand != "skill load" {
		t.Errorf("CLI asked about %q %q, want /p \"skill load\"", gotRoot, gotCommand)
	}

	// "Run the embedded forge" and "the pin cannot be had" pass through
	// with no forge named.
	swapHook(t, func(context.Context, string, string, io.Writer) (forgepinhook.Result, bool, error) {
		return forgepinhook.Result{Path: "ignored"}, false, nil
	})
	if got, ok, err := Resolve(context.Background(), "/p", "version", io.Discard); ok || err != nil || got.Path != "" {
		t.Errorf("run-here answer: got %+v, ok %v, err %v", got, ok, err)
	}
	boom := errors.New("could not be obtained")
	swapHook(t, func(context.Context, string, string, io.Writer) (forgepinhook.Result, bool, error) {
		return forgepinhook.Result{}, false, boom
	})
	if _, ok, err := Resolve(context.Background(), "/p", "skill load", io.Discard); ok || !errors.Is(err, boom) {
		t.Errorf("failure: ok %v, err %v", ok, err)
	}
}

// Resolve returns the CLI's Forge whole: every field the CLI set, Args,
// Version, Pin and Checkout included, passed through unchanged. An earlier
// Resolve copied four fields and silently dropped the rest.
func TestResolvePassesEveryFieldThrough(t *testing.T) {
	full := forgepinhook.Result{
		Path: "/host/reliant", Key: "self:v1.2.3:/host/reliant", Want: "forge_version v1.2.3",
		Source: SourceSelf, Args: []string{"forge"}, Version: "v1.2.3",
		Pin: "v1.2.3", Checkout: "/src/forge",
	}
	// Every field is set, so a field added to both types but left out of
	// this fixture fails here instead of passing vacuously.
	rv := reflect.ValueOf(full)
	for i := range rv.NumField() {
		if rv.Field(i).IsZero() {
			t.Fatalf("fixture leaves %s unset", rv.Type().Field(i).Name)
		}
	}
	swapHook(t, func(context.Context, string, string, io.Writer) (forgepinhook.Result, bool, error) {
		return full, true, nil
	})
	got, ok, err := Resolve(context.Background(), "/p", "skill load", io.Discard)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	want := Forge{
		Path: "/host/reliant", Key: "self:v1.2.3:/host/reliant", Want: "forge_version v1.2.3",
		Source: SourceSelf, Args: []string{"forge"}, Version: "v1.2.3",
		Pin: "v1.2.3", Checkout: "/src/forge",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
}
