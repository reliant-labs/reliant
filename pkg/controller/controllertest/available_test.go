//go:build !windows

// The envtest harness is real only off Windows (envtest.go); the Windows stub
// has its own contract, pinned in envtest_windows_test.go.

package controllertest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

func TestAvailable(t *testing.T) {
	// Pin the home directory to an empty temp dir so the developer's own
	// envtest cache cannot change the outcome.
	cacheDirs := []struct {
		name string
		path []string
	}{
		{"linux cache", []string{".local", "share", "kubebuilder-envtest", "k8s"}},
		{"macOS cache", []string{"Library", "Application Support", "io.kubebuilder.envtest", "k8s"}},
	}

	t.Run("KUBEBUILDER_ASSETS set", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KUBEBUILDER_ASSETS", "/opt/envtest/bin")
		if !available() {
			t.Error("available() = false with KUBEBUILDER_ASSETS set")
		}
	})

	t.Run("nothing installed", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KUBEBUILDER_ASSETS", "")
		if available() {
			t.Error("available() = true with no env var and no cache directory")
		}
	})

	for _, c := range cacheDirs {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(append([]string{home}, c.path...)...), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("KUBEBUILDER_ASSETS", "")
			if !available() {
				t.Errorf("available() = false with %s present", filepath.Join(c.path...))
			}
		})
	}

	t.Run("no resolvable home directory", func(t *testing.T) {
		if runtime.GOOS == "plan9" {
			t.Skip("os.UserHomeDir reads $home, not $HOME, on plan9")
		}
		t.Setenv("HOME", "")
		t.Setenv("KUBEBUILDER_ASSETS", "")
		if available() {
			t.Error("available() = true although the home directory cannot be resolved")
		}
	})
}

// TestNewAppliesOptionsInOrder runs with the binaries "installed" (the env
// var is only a marker: New builds the Environment but starts nothing).
func TestNewAppliesOptionsInOrder(t *testing.T) {
	t.Setenv("KUBEBUILDER_ASSETS", "/opt/envtest/bin")
	s := k8sruntime.NewScheme()

	env := New(t, WithCRDs("config/crd/bases"), WithCRDs("deploy/crd", "extra"), WithScheme(s))
	if env == nil {
		t.Fatal("New returned nil although envtest binaries are marked available")
	}
	want := []string{"config/crd/bases", "deploy/crd", "extra"}
	if len(env.CRDDirectoryPaths) != len(want) {
		t.Fatalf("CRDDirectoryPaths = %v, want %v", env.CRDDirectoryPaths, want)
	}
	for i := range want {
		if env.CRDDirectoryPaths[i] != want[i] {
			t.Errorf("CRDDirectoryPaths[%d] = %q, want %q", i, env.CRDDirectoryPaths[i], want[i])
		}
	}
	if env.Scheme != s {
		t.Error("WithScheme did not store the scheme")
	}
}

func TestNewWithoutOptionsReturnsAnEmptyEnvironment(t *testing.T) {
	t.Setenv("KUBEBUILDER_ASSETS", "/opt/envtest/bin")
	env := New(t)
	if env == nil {
		t.Fatal("New returned nil although envtest binaries are marked available")
	}
	if len(env.CRDDirectoryPaths) != 0 || env.Scheme != nil {
		t.Errorf("New() = %+v, want an unconfigured environment", env)
	}
}
