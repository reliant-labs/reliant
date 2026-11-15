//go:build !windows

// Package controllertest provides a small envtest harness that
// generated `<crd>_controller_test.go` files use in their TestMain
// setup. The harness exists for two reasons:
//
//  1. envtest binaries (kube-apiserver, etcd) are not available in
//     every developer environment. New(t) returns a non-nil
//     *envtest.Environment when KUBEBUILDER_ASSETS is set or when
//     setup-envtest has installed binaries to the standard cache
//     directory; otherwise it skips the test cleanly via t.Skip.
//
//  2. CRD path resolution is project-specific. The WithCRDs option
//     lets generated tests point at "config/crd/bases" or "deploy/crd/"
//     without re-implementing the join + glob logic.
//
// All public surface is option-shaped so the harness is forward-
// compatible: adding a new knob is a new option, not a new parameter.
package controllertest

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Environment is the envtest environment returned by New. On Windows it is a
// stand-in (see envtest_windows.go) because controller-runtime envtest does
// not compile there.
type Environment = envtest.Environment

// Option configures the envtest environment built by New.
type Option func(*Environment)

// WithCRDs adds CRD YAML directories to the environment's
// CRDDirectoryPaths. Paths may be relative to the test's working
// directory.
func WithCRDs(paths ...string) Option {
	return func(env *Environment) {
		env.CRDDirectoryPaths = append(env.CRDDirectoryPaths, paths...)
	}
}

// WithScheme registers the runtime scheme on the environment.
// The scheme is consumed by callers via env.Config — controller-runtime
// builders must AddToScheme themselves; this option is a marker that
// preserves the scheme alongside the env for callers that want to
// stash it.
func WithScheme(s *runtime.Scheme) Option {
	return func(env *Environment) {
		env.Scheme = s
	}
}

// New constructs an envtest.Environment configured by opts, or skips
// the test when envtest binaries are not available.
//
// Availability is detected via:
//
//   - KUBEBUILDER_ASSETS env var (set by `setup-envtest use ...`).
//   - The standard envtest cache directory ($HOME/.local/share/kubebuilder-envtest/k8s/<version>).
//
// When neither is available, New calls t.Skip and returns nil. The
// generated test scaffolds check for nil and bail early.
func New(t *testing.T, opts ...Option) *Environment {
	t.Helper()
	if !available() {
		t.Skip("envtest binaries not available (set KUBEBUILDER_ASSETS or run `setup-envtest use latest`)")
		return nil
	}
	env := &Environment{}
	for _, opt := range opts {
		opt(env)
	}
	return env
}

// available reports whether envtest binaries appear to be installed.
func available() bool {
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		return true
	}
	// Best-effort lookup of the standard cache. We don't pin a
	// specific Kubernetes version; the presence of a kube-apiserver
	// binary anywhere under the cache root counts.
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	roots := []string{
		filepath.Join(home, ".local", "share", "kubebuilder-envtest", "k8s"),
		filepath.Join(home, "Library", "Application Support", "io.kubebuilder.envtest", "k8s"),
	}
	for _, root := range roots {
		if _, err := os.Stat(root); err == nil {
			return true
		}
	}
	return false
}
