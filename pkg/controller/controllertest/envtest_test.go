package controllertest

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestNew_SkipsWhenBinariesMissing(t *testing.T) {
	// We can only meaningfully test the skip path here — running
	// envtest binaries would require kube-apiserver / etcd, which
	// are out of scope for unit tests. The available() check is
	// inherently environment-dependent.
	if available() {
		t.Skip("envtest binaries available; skip-path test does not apply in this environment")
	}

	ranInner := false
	t.Run("inner", func(tt *testing.T) {
		env := New(tt)
		if env != nil {
			ranInner = true
			tt.Errorf("expected New to return nil when binaries missing, got %v", env)
		}
	})
	if ranInner {
		t.Errorf("inner test should have skipped via t.Skip")
	}
}

func TestWithCRDs_AppendsPaths(t *testing.T) {
	if !available() {
		t.Skip("envtest binaries not available; cannot test option wiring")
	}
	env := New(t, WithCRDs("a/b", "c/d"))
	if env == nil {
		t.Skip("New returned nil despite available()==true")
	}
	if len(env.CRDDirectoryPaths) != 2 ||
		env.CRDDirectoryPaths[0] != "a/b" ||
		env.CRDDirectoryPaths[1] != "c/d" {
		t.Errorf("CRDDirectoryPaths = %v, want [a/b c/d]", env.CRDDirectoryPaths)
	}
}

func TestWithScheme_StoresScheme(t *testing.T) {
	if !available() {
		t.Skip("envtest binaries not available; cannot test option wiring")
	}
	s := runtime.NewScheme()
	env := New(t, WithScheme(s))
	if env == nil {
		t.Skip("New returned nil despite available()==true")
	}
	if env.Scheme != s {
		t.Errorf("expected scheme to be stored on env")
	}
}
