//go:build windows

// Windows variant of the controllertest harness. controller-runtime's envtest
// package does not compile for windows in any released version (through
// v0.25.2): pkg/internal/testing/process/signal_windows.go declares
// signalProcess, which process.go also declares. Fixed on upstream main
// (declares signalProcessImpl) but unreleased:
// https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/internal/testing/process/signal_windows.go
// Remove this file, and the !windows tag on envtest.go, once a release ships
// the fix. The exported API mirrors envtest.go so generated tests compile.
package controllertest

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

// Environment mirrors the fields and methods of envtest.Environment that
// callers use; it cannot start anything on Windows.
type Environment struct {
	CRDDirectoryPaths []string
	Scheme            *runtime.Scheme
	Config            *rest.Config
}

var errUnsupported = errors.New("controllertest: envtest is unsupported on windows (controller-runtime signalProcess redeclared bug)")

// Start always fails on Windows.
func (e *Environment) Start() (*rest.Config, error) { return nil, errUnsupported }

// Stop is a no-op on Windows.
func (e *Environment) Stop() error { return nil }

// Option configures the environment built by New.
type Option func(*Environment)

// WithCRDs adds CRD YAML directories to the environment.
func WithCRDs(paths ...string) Option {
	return func(env *Environment) {
		env.CRDDirectoryPaths = append(env.CRDDirectoryPaths, paths...)
	}
}

// WithScheme registers the runtime scheme on the environment.
func WithScheme(s *runtime.Scheme) Option {
	return func(env *Environment) { env.Scheme = s }
}

// New always skips the test on Windows and returns nil.
func New(t *testing.T, opts ...Option) *Environment {
	t.Helper()
	t.Skip("envtest is unavailable on windows: controller-runtime's envtest does not compile there (upstream signalProcess redeclared; fixed on main, unreleased)")
	return nil
}

func available() bool { return false }
