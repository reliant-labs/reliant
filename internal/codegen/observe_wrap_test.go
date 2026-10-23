package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func writeObserveDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestDetectObserveChainSeam_Present(t *testing.T) {
	dir := writeObserveDir(t, map[string]string{
		"contract.go":      "package x\n\ntype Service interface{ Do() }\n",
		"observe_chain.go": "package x\n\nimport \"github.com/reliant-labs/forge/pkg/observe\"\n\nfunc newObserveChain() *observe.ComponentChain { return nil }\n",
	})
	if !DetectObserveChainSeam(dir) {
		t.Fatal("owned observe_chain.go seam should be detected")
	}
}

func TestDetectObserveChainSeam_GeneratedDecoratorDoesNotCount(t *testing.T) {
	// The generated decorator references newObserveChain but must NOT itself
	// count as the seam (it lives in a _gen.go file, which is skipped) — so a
	// package whose seam was deleted is correctly seen as opted-out even while
	// a stale decorator lingers.
	dir := writeObserveDir(t, map[string]string{
		"contract.go":       "package x\n\ntype Service interface{ Do() }\n",
		"middleware_gen.go": "package x\n\nfunc NewServiceWithForgeMiddleware(inner Service) Service { return &forgeMiddlewareService{inner: inner, chain: newObserveChain()} }\n",
	})
	if DetectObserveChainSeam(dir) {
		t.Fatal("a reference to newObserveChain in a _gen.go file must not count as the seam")
	}
}

func TestDetectObserveChainSeam_TestFilesIgnored(t *testing.T) {
	dir := writeObserveDir(t, map[string]string{
		"observe_chain_test.go": "package x\n\nfunc newObserveChain() interface{} { return nil }\n",
	})
	if DetectObserveChainSeam(dir) {
		t.Fatal("seam declared only in a _test.go file must not count")
	}
}

func TestDetectObserveChainSeam_Absent(t *testing.T) {
	dir := writeObserveDir(t, map[string]string{
		"contract.go": "package x\n\ntype Service interface{ Do() }\n",
		"service.go":  "package x\n\ntype service struct{}\n",
	})
	if DetectObserveChainSeam(dir) {
		t.Fatal("package without the seam must not be detected as instrumented")
	}
}

func TestDetectObserveChainSeam_MissingDir(t *testing.T) {
	if DetectObserveChainSeam(filepath.Join(t.TempDir(), "nope")) {
		t.Fatal("missing dir should not detect a seam")
	}
}
