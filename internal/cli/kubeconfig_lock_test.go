package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWithKubeconfigLock_ConcurrentWritersLoseNothing is the lost update
// three parallel `forge cluster up`s produced: each writer reads the whole
// kubeconfig, adds its own context and writes the file back, so an unlocked
// writer that read before another's write erases that context. Under the
// lock every writer's entry survives.
func TestWithKubeconfigLock_ConcurrentWritersLoseNothing(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- withKubeconfigLock(func() error {
				data, err := os.ReadFile(kubeconfig)
				if err != nil {
					return err
				}
				time.Sleep(5 * time.Millisecond) // widen the read-modify-write window
				return os.WriteFile(kubeconfig, fmt.Appendf(data, "context-%d\n", i), 0o600)
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	for i := range writers {
		if !strings.Contains(string(data), fmt.Sprintf("context-%d\n", i)) {
			t.Errorf("context-%d was lost by a concurrent writer:\n%s", i, data)
		}
	}
	if _, err := os.Stat(kubeconfig + kubeconfigLockSuffix); err != nil {
		t.Errorf("the lock must live beside the kubeconfig $KUBECONFIG names: %v", err)
	}
}

func TestDefaultKubeconfigPath_FirstKUBECONFIGEntry(t *testing.T) {
	first, second := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)
	if got := defaultKubeconfigPath(); got != first {
		t.Fatalf("defaultKubeconfigPath() = %q, want the first $KUBECONFIG entry %q", got, first)
	}
}

// The create must never write the kubeconfig itself, whatever the caller
// passed: that write is the unlocked one.
func TestWithoutKubeconfigWrites(t *testing.T) {
	got := withoutKubeconfigWrites([]string{"cluster", "create", "x", "--kubeconfig-update-default=true", "--config", "k.yaml"})
	want := []string{"cluster", "create", "x", "--config", "k.yaml", "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false"}
	if !slices.Equal(got, want) {
		t.Fatalf("withoutKubeconfigWrites = %v, want %v", got, want)
	}
}
