// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// machineFixture is a home directory with a credential store, a project and a
// scratch directory, as the Files tab's machine view meets them.
func machineFixture(t *testing.T) (home, project string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	project = filepath.Join(home, "proj")
	for f, body := range map[string]string{
		filepath.Join(home, ".ssh", "id_rsa"):                        "fixture: not a key\n",
		filepath.Join(project, "README.md"):                          "readme\n",
		filepath.Join(home, ".reliant", "scratch", "c1", "notes.md"): "notes\n",
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, project
}

func listMachineDir(t *testing.T, path, root string) (fsListMachineDirResponse, error) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"path": path, "viewer_root": root})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleFSListMachineDir(context.Background(), payload)
	if err != nil {
		return fsListMachineDirResponse{}, err
	}
	var resp struct {
		fsListMachineDirResponse
		ViewerGuarded bool `json:"viewer_guarded"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.ViewerGuarded {
		t.Fatalf("list %s: reply is not marked viewer_guarded", path)
	}
	return resp.fsListMachineDirResponse, nil
}

func entryNames(entries []fsMachineDirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// Outside the project the machine view lists the home directory (the
// default), "~/…" paths and absolute ones, in name order with absolute entry
// paths; the scratch directory an agent wrote to is one click away.
func TestFSListMachineDir_ListsAnywhereOutsideTheProject(t *testing.T) {
	home, project := machineFixture(t)

	got, err := listMachineDir(t, "", project)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != home || got.HomeDir != home {
		t.Fatalf("empty path listed %q (home %q), want the home directory %q", got.Path, got.HomeDir, home)
	}
	if names := strings.Join(entryNames(got.Entries), ","); names != ".reliant,.ssh,proj" {
		t.Fatalf("home lists %s, want .reliant,.ssh,proj (hidden entries included, the client filters them)", names)
	}
	for _, e := range got.Entries {
		if e.Path != filepath.Join(home, e.Name) || !e.IsDir {
			t.Errorf("entry %+v: want an absolute directory path", e)
		}
	}

	got, err = listMachineDir(t, "~/.reliant/scratch/c1", project)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != filepath.Join(home, ".reliant", "scratch", "c1") || len(got.Entries) != 1 || got.Entries[0].Name != "notes.md" || got.Entries[0].IsDir {
		t.Fatalf("scratch listing = %+v", got)
	}
	if got.Entries[0].Size != int64(len("notes\n")) || got.Entries[0].Modified == "" {
		t.Errorf("a file entry carries its size and mtime: %+v", got.Entries[0])
	}

	if _, err := listMachineDir(t, "relative/dir", project); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("a relative path = %v, want a refusal naming absolute paths", err)
	}
}

// A credential store is never listed — directly, below it, or through a
// symlink that leads into it — while the directory that holds it is.
func TestFSListMachineDir_RefusesCredentialStores(t *testing.T) {
	home, project := machineFixture(t)
	link := filepath.Join(home, "keys")
	if err := os.Symlink(filepath.Join(home, ".ssh"), link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(home, ".ssh"), "~/.ssh", link} {
		if _, err := listMachineDir(t, p, project); !errors.Is(err, daemonpolicy.ErrViewerCredential) {
			t.Errorf("list %s = %v, want ErrViewerCredential", p, err)
		}
	}
	if _, err := listMachineDir(t, home, project); err != nil {
		t.Errorf("the home directory itself must list: %v", err)
	}
}

func TestFSListMachineDir_ProtectsSymlinkTargetMetadata(t *testing.T) {
	home, project := machineFixture(t)
	for name, target := range map[string]string{
		"keys":  filepath.Join(home, ".ssh"),
		"notes": filepath.Join(home, ".ssh", "id_rsa"),
	} {
		if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := listMachineDir(t, home, project)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range got.Entries {
		if e.Name != "keys" && e.Name != "notes" {
			continue
		}
		found++
		if !e.IsSymlink || e.IsDir || e.Size != 0 || e.Modified != "" {
			t.Errorf("protected link exposes target metadata: %+v", e)
		}
	}
	if found != 2 {
		t.Fatalf("listed %d protected links, want 2 name-only entries", found)
	}
}

func TestFSListMachineDir_RejectsSwappedSymlinkMetadata(t *testing.T) {
	home, project := machineFixture(t)
	link := filepath.Join(home, "link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	previous := viewerOpened
	t.Cleanup(func() { viewerOpened = previous })
	swapped := false
	viewerOpened = func(path string) {
		if path != link {
			return
		}
		swapped = true
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(home, ".ssh"), link); err != nil {
			t.Fatal(err)
		}
	}
	got, err := listMachineDir(t, home, project)
	if err != nil {
		t.Fatal(err)
	}
	if !swapped {
		t.Fatal("symlink metadata did not pass through the guarded open")
	}
	for _, e := range got.Entries {
		if e.Name == "link" {
			if !e.IsSymlink || e.IsDir || e.Size != 0 || e.Modified != "" {
				t.Fatalf("swapped link exposes target metadata: %+v", e)
			}
			return
		}
	}
	t.Fatal("swapped link must remain a name-only entry")
}

func TestFSListMachineDir_RejectsSymlinkSwapAfterOpenGuard(t *testing.T) {
	for _, credential := range []string{".ssh", filepath.Join(".ssh", "id_rsa")} {
		t.Run(credential, func(t *testing.T) {
			home, project := machineFixture(t)
			link := filepath.Join(home, "link")
			if err := os.Symlink(project, link); err != nil {
				t.Fatal(err)
			}
			previous := machineSymlinkOpened
			t.Cleanup(func() { machineSymlinkOpened = previous })
			swapped := false
			machineSymlinkOpened = func(path string) {
				if path != link {
					return
				}
				// This hook runs only after openForRead's identity and
				// credential checks have succeeded, but before Stat.
				swapped = true
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, credential), link); err != nil {
					t.Fatal(err)
				}
			}
			got, err := listMachineDir(t, home, project)
			if err != nil {
				t.Fatal(err)
			}
			if !swapped {
				t.Fatal("symlink did not pass the open-time guard")
			}
			for _, e := range got.Entries {
				if e.Name == "link" {
					if !e.IsSymlink || e.IsDir || e.Size != 0 || e.Modified != "" {
						t.Fatalf("late-swapped link exposes target metadata: %+v", e)
					}
					return
				}
			}
			t.Fatal("late-swapped link must remain a name-only entry")
		})
	}
}

// A symlink is listed as what it points at (a link to a directory expands),
// marked as a link; a dangling one is still listed, as a file with no mtime.
func TestFSListMachineDir_Symlinks(t *testing.T) {
	home, project := machineFixture(t)
	dir := filepath.Join(home, "links")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(project, filepath.Join(dir, "to-proj")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "nowhere"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	got, err := listMachineDir(t, dir, project)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]fsMachineDirEntry{}
	for _, e := range got.Entries {
		byName[e.Name] = e
	}
	if e := byName["to-proj"]; !e.IsSymlink || !e.IsDir {
		t.Errorf("link to a directory = %+v, want is_symlink and is_dir", e)
	}
	if e := byName["dangling"]; !e.IsSymlink || e.IsDir || e.Modified != "" {
		t.Errorf("dangling link = %+v, want a symlink file with no mtime", e)
	}

	// Listing THROUGH a symlinked directory keeps the path the user went to.
	got, err = listMachineDir(t, filepath.Join(dir, "to-proj"), project)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != filepath.Join(dir, "to-proj") || len(got.Entries) != 1 {
		t.Errorf("listing through a link = %+v", got)
	}
}

// A file is "not a directory" (the server's NotADirectory reason), a missing
// path is the os's not-exist error, and an unreadable directory fails with
// permission denied rather than listing nothing.
func TestFSListMachineDir_Refusals(t *testing.T) {
	home, project := machineFixture(t)

	_, err := listMachineDir(t, filepath.Join(project, "README.md"), project)
	if !errors.Is(err, errNotADirectory) || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("listing a file = %v, want errNotADirectory", err)
	}
	if _, err := listMachineDir(t, filepath.Join(home, "missing"), project); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("listing a missing path = %v, want not-exist", err)
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this user from reading a directory")
	}
	locked := filepath.Join(home, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := listMachineDir(t, locked, project); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("listing an unreadable directory = %v, want permission denied", err)
	}
}

// The command serves only the viewer: without a workspace root there is no
// guard to apply, so it refuses rather than listing unguarded.
func TestFSListMachineDir_RequiresAViewerRoot(t *testing.T) {
	home, _ := machineFixture(t)
	payload, _ := json.Marshal(map[string]any{"path": home})
	if _, err := handleFSListMachineDir(context.Background(), payload); err == nil {
		t.Fatal("a listing without viewer_root must be refused")
	}
}

// One listing is bounded: a huge directory comes back as its first entries
// in name order, marked truncated.
func TestReadMachineDirCapsEntries(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"c", "a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := machineDirEntryCap
	machineDirEntryCap = 2
	t.Cleanup(func() { machineDirEntryCap = prev })
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	entries, truncated, err := readMachineDir(f, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || strings.Join(entryNames(entries), ",") != "a,b" {
		t.Fatalf("capped listing = %v truncated=%v, want a,b truncated", entryNames(entries), truncated)
	}
}
