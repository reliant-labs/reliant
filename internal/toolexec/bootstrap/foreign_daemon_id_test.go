package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIsForeignDaemonIDError(t *testing.T) {
	legacy := connect.NewError(connect.CodePermissionDenied, errors.New("daemon id is owned by another user"))
	otherDenial := connect.NewError(connect.CodePermissionDenied, errors.New("token is bound to a non-daemon resource"))
	retagged := connect.NewError(connect.CodeUnauthenticated, errors.New("daemon id is owned by another user"))

	cases := map[string]struct {
		err  error
		want bool
	}{
		"the gateway's refusal":                    {NewForeignDaemonIDError(), true},
		"wrapped, as the runtime returns it":       {fmt.Errorf("daemon connection failed (not retrying): %w", NewForeignDaemonIDError()), true},
		"a gateway that predates the reason":       {legacy, true},
		"any other permission denial":              {otherDenial, false},
		"a credential problem with the same words": {retagged, false},
		"a plain error":                            {errors.New("daemon id is owned by another user"), false},
		"nil":                                      {nil, false},
	}
	for name, tc := range cases {
		if got := IsForeignDaemonIDError(tc.err); got != tc.want {
			t.Errorf("%s: IsForeignDaemonIDError = %v, want %v", name, got, tc.want)
		}
	}
}

func TestNewForeignDaemonIDErrorCarriesReasonNotOwner(t *testing.T) {
	err := NewForeignDaemonIDError()
	if err.Code() != connect.CodePermissionDenied {
		t.Fatalf("code = %s, want permission_denied", err.Code())
	}
	if got := err.Meta().Get("x-forge-error-reason"); got != ForeignDaemonIDReason {
		t.Fatalf("reason = %q, want %q", got, ForeignDaemonIDReason)
	}
}

// The foreign id is renamed, never deleted, and the instance then reads as
// having no saved identity — which is what makes the next registration ask
// the gateway for a fresh one.
func TestSetAsideDaemonIDRenamesAndKeepsTheOldID(t *testing.T) {
	dataDir := t.TempDir()
	if err := WriteDaemonID(dataDir, "another-accounts-daemon"); err != nil {
		t.Fatal(err)
	}

	first, err := SetAsideDaemonID(dataDir)
	if err != nil {
		t.Fatalf("SetAsideDaemonID: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(first), DaemonIDFileName+".foreign-") {
		t.Fatalf("set-aside name %q does not say what it is", first)
	}
	data, err := os.ReadFile(first)
	if err != nil || strings.TrimSpace(string(data)) != "another-accounts-daemon" {
		t.Fatalf("set-aside file must keep the old id: %q, %v", data, err)
	}
	if got := ReadDaemonID(dataDir); got != "" {
		t.Fatalf("after setting aside, the instance must have no saved id, got %q", got)
	}

	// A second recovery in the same second must not overwrite the first.
	if err := WriteDaemonID(dataDir, "yet-another"); err != nil {
		t.Fatal(err)
	}
	second, err := SetAsideDaemonID(dataDir)
	if err != nil {
		t.Fatalf("second SetAsideDaemonID: %v", err)
	}
	if second == first {
		t.Fatalf("second set-aside overwrote the first: %s", second)
	}
	if data, _ := os.ReadFile(first); strings.TrimSpace(string(data)) != "another-accounts-daemon" {
		t.Fatalf("first set-aside file changed: %q", data)
	}
}

func TestSetAsideDaemonIDWithNothingToMove(t *testing.T) {
	if _, err := SetAsideDaemonID(t.TempDir()); err == nil {
		t.Fatal("setting aside a missing id must report it, not invent a file")
	}
	if _, err := SetAsideDaemonID(""); err == nil {
		t.Fatal("an empty data dir must be refused")
	}
}
