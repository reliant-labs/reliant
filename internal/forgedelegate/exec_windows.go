//go:build windows

package forgedelegate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// markerOwner is the pid a marker addressed to this process names: Windows
// has no exec(2), so the forge that wrote it stays alive as this process's
// parent, relaying its exit status. A child of this process has this
// process, not that one, as its parent.
func markerOwner() int { return os.Getppid() }

// Exec runs the forge at path with args and exits with its status: Windows has
// no exec(2), so this process stays alive as the parent and relays the exit
// code. Stdio is inherited, and a console interrupt reaches both processes.
// It returns only if the program could not be started.
func Exec(path string, args, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &exit):
		os.Exit(exit.ExitCode())
	}
	return fmt.Errorf("run %s: %w", path, err)
}
