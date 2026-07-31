// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkingDir(t *testing.T) {
	dir := t.TempDir()

	t.Run("empty means daemon cwd", func(t *testing.T) {
		if err := ValidateWorkingDir(""); err != nil {
			t.Errorf("ValidateWorkingDir(%q) = %v, want nil", "", err)
		}
	})

	t.Run("existing directory passes", func(t *testing.T) {
		if err := ValidateWorkingDir(dir); err != nil {
			t.Errorf("ValidateWorkingDir(%q) = %v, want nil", dir, err)
		}
	})

	// The kernel reports a chdir ENOENT as "fork/exec /bin/bash: no such file
	// or directory" — naming the shell, not the directory. The message must
	// name the directory or it sends the caller hunting for a missing shell.
	t.Run("missing directory is named", func(t *testing.T) {
		missing := filepath.Join(dir, "gone")
		err := ValidateWorkingDir(missing)
		if err == nil {
			t.Fatal("ValidateWorkingDir on a missing directory = nil, want an error")
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error = %q, want it to name %q", err, missing)
		}
		if strings.Contains(err.Error(), "bash") {
			t.Errorf("error = %q, must not blame the shell binary", err)
		}
	})

	t.Run("a file where a directory is expected is named", func(t *testing.T) {
		file := filepath.Join(dir, "afile")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := ValidateWorkingDir(file)
		if err == nil {
			t.Fatal("ValidateWorkingDir on a regular file = nil, want an error")
		}
		if !strings.Contains(err.Error(), file) {
			t.Errorf("error = %q, want it to name %q", err, file)
		}
	})
}
