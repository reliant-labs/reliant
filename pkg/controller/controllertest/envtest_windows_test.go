//go:build windows

package controllertest

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

// The Windows stub never has envtest binaries: whatever the environment says,
// available() is false and New skips the calling test, because
// controller-runtime's envtest does not compile there.
func TestWindowsStubIsNeverAvailable(t *testing.T) {
	t.Setenv("KUBEBUILDER_ASSETS", `C:\envtest\bin`)
	if available() {
		t.Error("available() = true on Windows; the stub has no envtest to run")
	}
}

func TestWindowsNewSkipsAndReturnsNil(t *testing.T) {
	t.Setenv("KUBEBUILDER_ASSETS", `C:\envtest\bin`)
	var env *Environment
	skipped := true
	t.Run("inner", func(tt *testing.T) {
		env = New(tt, WithCRDs("crds"))
		skipped = false // not reached when New skips, as it must
	})
	if !skipped {
		t.Error("New returned instead of skipping the test")
	}
	if env != nil {
		t.Errorf("New returned %+v, want nil", env)
	}
}

func TestWindowsOptionsAndLifecycle(t *testing.T) {
	env := &Environment{}
	s := runtime.NewScheme()
	WithCRDs("a", "b")(env)
	WithCRDs("c")(env)
	WithScheme(s)(env)
	if got := env.CRDDirectoryPaths; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("CRDDirectoryPaths = %v, want [a b c]", got)
	}
	if env.Scheme != s {
		t.Error("WithScheme did not store the scheme")
	}
	if cfg, err := env.Start(); cfg != nil || !errors.Is(err, errUnsupported) {
		t.Errorf("Start() = %v, %v; want nil, errUnsupported", cfg, err)
	}
	if err := env.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}
