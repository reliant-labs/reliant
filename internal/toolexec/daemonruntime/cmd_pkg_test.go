// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

func callPkgListCommands(t *testing.T, workingDir string) (*pkgmgr.CommandListResponse, error) {
	t.Helper()
	payload, err := json.Marshal(pkgListCommandsRequest{WorkingDir: workingDir})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respBytes, err := handlePkgListCommands(context.Background(), payload)
	if err != nil {
		return nil, err
	}
	var resp pkgmgr.CommandListResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return &resp, nil
}

func findCommand(cmds []pkgmgr.Command, name string) *pkgmgr.Command {
	for i := range cmds {
		if cmds[i].Name == name {
			return &cmds[i]
		}
	}
	return nil
}

// A directory with a Taskfile and a package.json returns discovered commands
// from both — this is the case that returned empty on a cloud daemon before the
// discovery was moved onto the daemon's filesystem.
func TestHandlePkgListCommands_DiscoversTaskfileAndNPM(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "Taskfile.yml"), `version: '3'
tasks:
  build:
    desc: Build it
    cmds:
      - echo building
  test:
    cmds:
      - echo testing
`)
	writeFileT(t, filepath.Join(dir, "package.json"), `{"name":"demo","scripts":{"dev":"vite","lint":"eslint ."}}`)

	resp, err := callPkgListCommands(t, dir)
	if err != nil {
		t.Fatalf("handlePkgListCommands: %v", err)
	}

	taskCmds := resp.Commands[pkgmgr.PackageTypeTaskfile]
	if findCommand(taskCmds, "build") == nil || findCommand(taskCmds, "test") == nil {
		t.Fatalf("expected taskfile build+test commands, got %+v", taskCmds)
	}

	npmCmds := resp.Commands[pkgmgr.PackageTypeNPM]
	if findCommand(npmCmds, "dev") == nil || findCommand(npmCmds, "lint") == nil {
		t.Fatalf("expected npm dev+lint commands, got %+v", npmCmds)
	}

	var haveTask, haveNPM bool
	for _, dt := range resp.DetectedTypes {
		haveTask = haveTask || dt == pkgmgr.PackageTypeTaskfile
		haveNPM = haveNPM || dt == pkgmgr.PackageTypeNPM
	}
	if !haveTask || !haveNPM {
		t.Errorf("detected types = %v, want both taskfile and npm", resp.DetectedTypes)
	}
}

// A directory that exists but has no manifests is a legitimately EMPTY result,
// not an error — the distinction that keeps "nothing to run here" from looking
// like a failure.
func TestHandlePkgListCommands_ExistingDirNoManifests_EmptyNotError(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "README.md"), "no manifests here")

	resp, err := callPkgListCommands(t, dir)
	if err != nil {
		t.Fatalf("expected no error for a manifest-free dir, got %v", err)
	}
	if len(resp.Commands) != 0 {
		t.Errorf("expected empty commands, got %+v", resp.Commands)
	}
	if len(resp.DetectedTypes) != 0 {
		t.Errorf("expected no detected types, got %v", resp.DetectedTypes)
	}
}

// A nonexistent working dir is a loud, specific error carrying the sentinel the
// proxy maps to NotFound — never a silent empty list.
func TestHandlePkgListCommands_MissingDir_Errors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := callPkgListCommands(t, missing)
	if err == nil {
		t.Fatal("expected an error for a missing working dir, got nil")
	}
	if !strings.Contains(err.Error(), pkgDirNotExistPrefix) {
		t.Errorf("error = %q, want it to contain the NotFound sentinel %q", err, pkgDirNotExistPrefix)
	}
}

// A path that exists but is a file (not a directory) is treated the same as a
// missing dir: loud, with the NotFound sentinel.
func TestHandlePkgListCommands_NotADirectory_Errors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := callPkgListCommands(t, file)
	if err == nil {
		t.Fatal("expected an error when working dir is a file, got nil")
	}
	if !strings.Contains(err.Error(), pkgDirNotExistPrefix) {
		t.Errorf("error = %q, want it to contain the NotFound sentinel %q", err, pkgDirNotExistPrefix)
	}
}

// An empty working_dir is rejected outright.
func TestHandlePkgListCommands_EmptyWorkingDir_Errors(t *testing.T) {
	_, err := callPkgListCommands(t, "")
	if err == nil {
		t.Fatal("expected an error for an empty working_dir, got nil")
	}
}
