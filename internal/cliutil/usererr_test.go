package cliutil

import (
	"errors"
	"strings"
	"testing"
)

func TestUserErr_AllClauses(t *testing.T) {
	err := UserErr("forge generate (validate generated code)",
		"go build failed",
		"handlers/billing/handlers_mutate.go:42",
		"ensure all referenced types are imported")
	want := "forge generate (validate generated code): go build failed (at handlers/billing/handlers_mutate.go:42). Fix: ensure all referenced types are imported."
	if err.Error() != want {
		t.Errorf("got\n  %q\nwant\n  %q", err.Error(), want)
	}
}

func TestUserErr_NoFix(t *testing.T) {
	err := UserErr("forge project new", "project name is required", "", "")
	want := "forge project new: project name is required."
	if err.Error() != want {
		t.Errorf("got\n  %q\nwant\n  %q", err.Error(), want)
	}
}

func TestUserErr_FixOnly(t *testing.T) {
	err := UserErr("forge pack add api-key",
		"pack 'api-key' depends on 'audit-log' which is not installed",
		"",
		"run 'forge pack add audit-log api-key' (auto-installs in topological order)")
	if !strings.Contains(err.Error(), "Fix: run 'forge pack add audit-log api-key'") {
		t.Errorf("Fix clause missing: %q", err.Error())
	}
	if strings.Contains(err.Error(), " at ") {
		t.Errorf("expected no `at` clause, got %q", err.Error())
	}
}

func TestUserErrf_FormatsArgs(t *testing.T) {
	err := UserErrf("forge scaffold service", "invalid service name %q: %s", "go", "go is a reserved keyword")
	if err.Error() != `forge scaffold service: invalid service name "go": go is a reserved keyword.` {
		t.Errorf("got %q", err.Error())
	}
}

// WrapUserErr should preserve errors.Is on the inner error.
func TestWrapUserErr_PreservesIs(t *testing.T) {
	sentinel := errors.New("disk full")
	wrapped := WrapUserErr("forge generate",
		"failed to write checksums",
		"",
		"free disk space and retry",
		sentinel)
	if !errors.Is(wrapped, sentinel) {
		t.Errorf("errors.Is should reach the inner sentinel; got %v", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "disk full") {
		t.Errorf("inner message should appear in error string: %q", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "Fix: free disk space and retry") {
		t.Errorf("Fix clause missing: %q", wrapped.Error())
	}
}

// Always trailing period — uniform shape regardless of Fix clause.
func TestUserErr_AlwaysEndsWithPeriod(t *testing.T) {
	cases := []error{
		UserErr("ctx", "what", "", ""),
		UserErr("ctx", "what", "at:1", ""),
		UserErr("ctx", "what", "", "fix"),
		UserErr("ctx", "what", "at:1", "fix"),
	}
	for _, e := range cases {
		if !strings.HasSuffix(e.Error(), ".") {
			t.Errorf("expected trailing period, got %q", e.Error())
		}
	}
}
