package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// memoizeKCLRenders makes each DISTINCT render of a fixture project evaluate
// its KCL once for the rest of the test.
//
// A hosted `forge env build` / `forge env deploy` evaluates an env's
// declaration about fifteen times (ledger, platform guard, capacity preflight,
// shape, bundle recorder, ...), each seconds of CPU on a loaded machine, and
// every one of them is byte-identical for a fixture that never changes. That
// made TestHostedDeployRefusals 40-90s of KCL evaluation around a few
// milliseconds of refusal logic.
//
// The key is everything that decides the output: the render's directory, env,
// its extra and release bindings, the option bindings, and the CONTENT of forge.yaml
// and every file under the project's deploy/kcl, so a test that rewrites its KCL
// re-evaluates. Only a project whose KCL reads no other file, port state or
// clock belongs here; a test that needs those must not call this.
func memoizeKCLRenders(t *testing.T) {
	t.Helper()
	type result struct {
		raw []byte
		err error
	}
	var (
		mu    sync.Mutex
		cache = map[string]result{}
	)
	prev := renderKCLRawHook
	renderKCLRawHook = func(ctx context.Context, projectDir, env string, extra ...string) ([]byte, error) {
		key := strings.Join([]string{
			projectDir, env, strings.Join(extra, "\x00"),
			strings.Join(kclrender.ReleaseDArgs(ctx), "\x00"),
			strings.Join(activeRenderOptionDArgs(), "\x00"),
			kclTreeDigest(projectDir),
		}, "\x01")
		mu.Lock()
		defer mu.Unlock()
		if r, ok := cache[key]; ok {
			return append([]byte(nil), r.raw...), r.err
		}
		raw, err := prev(ctx, projectDir, env, extra...)
		cache[key] = result{append([]byte(nil), raw...), err}
		return raw, err
	}
	t.Cleanup(func() { renderKCLRawHook = prev })
}

// kclTreeDigest hashes the path and bytes of every file under the project's
// deploy/kcl, and of forge.yaml (the frontend outputs binding reads it).
func kclTreeDigest(projectDir string) string {
	h := sha256.New()
	hashFile := func(path string) {
		if b, err := os.ReadFile(path); err == nil {
			fmt.Fprintf(h, "%s\x00%d\x00", path, len(b))
			h.Write(b)
		}
	}
	hashFile(filepath.Join(projectDir, "forge.yaml"))
	_ = filepath.WalkDir(filepath.Join(projectDir, "deploy", "kcl"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			hashFile(path)
		}
		return nil
	})
	return fmt.Sprintf("%x", h.Sum(nil))
}

// TestMemoizeKCLRenders_EvaluatesEachDistinctRenderOnce pins the memo's
// contract: a repeat is served from memory, while a change to the KCL, the
// release binding or the extra bindings evaluates again.
func TestMemoizeKCLRenders_EvaluatesEachDistinctRenderOnce(t *testing.T) {
	evaluated := 0
	prev := renderKCLRawHook
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		evaluated++
		return []byte(`{"output":{}}`), nil
	}
	t.Cleanup(func() { renderKCLRawHook = prev })
	memoizeKCLRenders(t)

	dir := t.TempDir()
	main := filepath.Join(dir, "deploy", "kcl", "dev", "main.k")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMain := func(body string) {
		t.Helper()
		if err := os.WriteFile(main, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	render := func(ctx context.Context, extra ...string) {
		t.Helper()
		if _, err := renderKCLRawHook(ctx, dir, "dev", extra...); err != nil {
			t.Fatal(err)
		}
	}
	bg := context.Background()
	steps := []struct {
		name string
		do   func()
		want int
	}{
		{"first render evaluates", func() { writeMain("a = 1"); render(bg) }, 1},
		{"a repeat is served from memory", func() { render(bg) }, 1},
		{"another env evaluates", func() {
			if _, err := renderKCLRawHook(bg, dir, "prod"); err != nil {
				t.Fatal(err)
			}
		}, 2},
		{"extra bindings evaluate again", func() { render(bg, "image_tag=\"x\"") }, 3},
		{"a release binding evaluates again", func() { render(kclrender.WithRelease(bg, "v1")) }, 4},
		{"an edited main.k evaluates again", func() { writeMain("a = 2"); render(bg) }, 5},
		{"and is then served from memory", func() { render(bg) }, 5},
	}
	for _, s := range steps {
		s.do()
		if evaluated != s.want {
			t.Fatalf("%s: %d evaluations, want %d", s.name, evaluated, s.want)
		}
	}
}
