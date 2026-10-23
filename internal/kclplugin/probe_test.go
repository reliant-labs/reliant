package kclplugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnloadableRuntimeIsAnErrorNotACrash: kcl-lang.io/lib panics when it
// cannot extract libkcl. Before install recovered it, that panic crashed forge
// from whichever goroutine touched KCL first (seen as `forge doctor` exiting 2
// with a stack trace). The failure must come back as an error from every
// entry point — Ready, Probe, and every later Serialized call, since the
// library's Once is spent — and never as a panic or a nil-client crash.
//
// Runs in a fresh process: the failure is permanent for the process that
// suffers it, so it cannot share this test binary with tests that need KCL.
func TestUnloadableRuntimeIsAnErrorNotACrash(t *testing.T) {
	if os.Getenv("KCLPLUGIN_UNLOADABLE_CHILD") == "1" {
		if err := Ready(); err == nil {
			t.Fatal("Ready: want an error for an unextractable runtime")
		}
		if err := Probe(); err == nil {
			t.Fatal("Probe: want an error, got a pass")
		}
		// A second evaluation after the failure must still be an error,
		// not a nil-client panic.
		if err := Probe(); err == nil || !strings.Contains(err.Error(), "load the KCL runtime") {
			t.Fatalf("second Probe: want the remembered load error, got %v", err)
		}
		return
	}
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	// A KCL_LIB_HOME beneath a regular FILE can never be created, on every
	// OS and regardless of privileges — unlike a chmod'd directory, which
	// root and Windows ignore.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUnloadableRuntimeIsAnErrorNotACrash$", "-test.v")
	cmd.Env = append(os.Environ(), "KCLPLUGIN_UNLOADABLE_CHILD=1", "KCL_LIB_HOME="+filepath.Join(blocker, "kcl"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child failed (a panic here is the regression): %v\n%s", err, out)
	}
}

func TestProbeRoundTripsThroughThePlugin(t *testing.T) {
	if err := Probe(); err != nil {
		t.Fatalf("Probe on a working build must pass: %v", err)
	}
}

// TestProbeFailsWhenTheCallbackCannotAnswer pins that Probe checks the
// round-tripped VALUE, not just that evaluation returned: with the probe
// method removed from the namespace, KCL still loads but the call fails, and
// Probe must say so rather than report a working plugin.
func TestProbeFailsWhenTheCallbackCannotAnswer(t *testing.T) {
	saved := methods
	t.Cleanup(func() { methods = saved })
	methods = map[string]methodSpec{}
	for k, v := range saved {
		if k != "probe" {
			methods[k] = v
		}
	}

	err := Probe()
	if err == nil {
		t.Fatal("Probe passed with kcl_plugin.forge.probe unregistered; doctor would report a broken plugin as working")
	}
	if !strings.Contains(err.Error(), "probe") {
		t.Errorf("error should name the failing call, got: %v", err)
	}
}
