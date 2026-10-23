//go:build !windows

package forgedelegate

import (
	"fmt"
	"syscall"
)

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
