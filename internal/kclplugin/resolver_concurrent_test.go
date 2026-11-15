package kclplugin

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestResolverArmingIsRaceFree drives the writers that re-arm the process's
// resolve_port resolver (UsePortStore, UsePortStoreReadOnly,
// ResetDefaultResolverForTest) against the reader that the plugin callback runs
// on KCL's goroutines (resolve_port). forge renders several envs concurrently
// (`forge env status`, topology), each arming the read-only store first, so
// these genuinely overlap. Meaningful only under -race.
func TestResolverArmingIsRaceFree(t *testing.T) {
	t.Cleanup(ResetDefaultResolverForTest)
	dir := t.TempDir()

	const workers, iters = 8, 40
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		store := filepath.Join(dir, "ports-"+string(rune('a'+w))+".json")
		wg.Add(4)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				UsePortStoreReadOnly(store)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				UsePortStore(store)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				ResetDefaultResolverForTest()
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				out := invoke("kcl_plugin.forge.resolve_port", `["svc", 0]`, "")
				if strings.Contains(out, "__kcl_PanicInfo__") {
					t.Errorf("resolve_port failed: %s", out)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestUsePortStoreReadOnlyKeepsWritableStore pins the documented contract that
// arming the read-only store never downgrades a writable one — now that the
// check and the swap are a single critical section.
func TestUsePortStoreReadOnlyKeepsWritableStore(t *testing.T) {
	t.Cleanup(ResetDefaultResolverForTest)
	ResetDefaultResolverForTest()
	path := filepath.Join(t.TempDir(), "ports-dev.json")

	UsePortStore(path)
	UsePortStoreReadOnly(filepath.Join(t.TempDir(), "other.json"))

	if _, err := defaultResolver.Resolve("svc", 0); err != nil {
		t.Fatal(err)
	}
	if cur := defaultResolver; cur.readOnly || cur.storePath != path {
		t.Fatalf("read-only arming replaced the writable store: readOnly=%v path=%q want %q", cur.readOnly, cur.storePath, path)
	}
}
