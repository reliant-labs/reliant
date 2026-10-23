//go:build windows

package forgedelegate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

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
