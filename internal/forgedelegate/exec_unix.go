//go:build !windows

package forgedelegate

import (
	"fmt"
	"os"
	"syscall"
)

// markerOwner is the pid a marker addressed to this process names: exec(2)
// keeps the pid, so the forge that wrote it before exec'ing is this process.
// A child this process starts has another pid, whatever its environment says.
func markerOwner() int { return os.Getpid() }

// Exec replaces this process with the forge at path, running args (the forge
// arguments, no program name). The process keeps its pid, its stdio and its
// signals, so nothing between the caller and the delegated forge can swallow
// an interrupt or an exit status. It returns only on failure.
func Exec(path string, args, env []string) error {
	if err := syscall.Exec(path, append([]string{path}, args...), env); err != nil {
		return fmt.Errorf("exec %s: %w", path, err)
	}
	return nil
}
