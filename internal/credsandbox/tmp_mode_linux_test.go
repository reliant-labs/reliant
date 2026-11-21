// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// The private /tmp and /var/tmp stand-ins are what the machine's are: sticky
// and writable by everyone (drwxrwxrwt). Without the sticky bit forge refuses
// to run a tool from under them, which failed a sandboxed gate's
// TestFetchedToolChecks ("/tmp is world-writable without the sticky bit").
func TestMakePrivateTmpDirs_AreStickyAndWorldWritable(t *testing.T) {
	run := t.TempDir()
	if err := makePrivateTmpDirs(run); err != nil {
		t.Fatal(err)
	}
	for _, d := range privateTmpDirs {
		fi, err := os.Stat(filepath.Join(run, d.entry))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := fi.Mode(), os.ModeDir|os.ModeSticky|0o777; got != want {
			t.Errorf("%s stand-in has mode %v, want %v", d.path, got, want)
		}
	}
}
