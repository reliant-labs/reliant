// Copyright (c) 2026 Reliant Labs
package proctree

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/reliant-labs/reliant/internal/credsandbox"
)

// sandboxInitCmd is a command shaped as credsandbox.Apply leaves one.
func sandboxInitCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Args = []string{self, credsandbox.HelperArg}
	return cmd
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Only a credsandbox init is accounted: any other leader is inspectable, and
// what it holds is not the daemon's to vouch for.
func TestPrepare_AccountsOnlyASandboxInit(t *testing.T) {
	if (&Limiter{}).Prepare(exec.Command("true"), Owner{}).footprint != nil {
		t.Error("an ordinary command must not be accounted")
	}
	cmd := sandboxInitCmd(t)
	cmd.Dir = t.TempDir()
	if fp := (&Limiter{}).Prepare(cmd, Owner{}).footprint; fp == nil {
		t.Error("a sandbox init must be accounted")
	}
}

// The footprint is the init's cwd, executable and every named file it is
// handed, resolved as /proc shows them; os.Pipe ends are not paths.
func TestAccountedFootprint(t *testing.T) {
	dir, files := t.TempDir(), t.TempDir()
	out, err := os.Create(filepath.Join(files, "out.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

	cmd := sandboxInitCmd(t)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = w
	cmd.ExtraFiles = []*os.File{r}
	got, ok := accountedFootprint(cmd)
	if !ok {
		t.Fatal("every path is nameable")
	}
	want := []string{resolved(t, dir), resolved(t, cmd.Path), resolved(t, out.Name())}
	if !slices.Equal(got, want) {
		t.Errorf("footprint %q, want %q", got, want)
	}

	cmd.Dir = ""
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := accountedFootprint(cmd); !ok || got[0] != resolved(t, wd) {
		t.Errorf("no Dir runs in the daemon's own cwd: %q %v", got, ok)
	}

	rel, err := os.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rel.Close() })
	cmd.Stdin = rel
	if _, ok := accountedFootprint(cmd); ok {
		t.Error("a file known only by a relative name cannot be accounted for")
	}
}
