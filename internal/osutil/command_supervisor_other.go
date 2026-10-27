//go:build !linux

package osutil

import (
	"fmt"
	"os/exec"
	"time"
)

// Preserve ordinary command execution. Only cancellation proof is unsupported;
// it must not turn every macOS/Windows shell call into a launch failure.
func PrepareCommandSupervision(cmd *exec.Cmd, grace time.Duration) (*CommandSupervision, error) {
	finish := ApplyGracefulCancelChecked(cmd, grace)
	return &CommandSupervision{fallback: func() error {
		if err := finish(); err != nil {
			return fmt.Errorf("cancellation unconfirmed on this platform: %w", err)
		}
		return nil
	}}, nil
}
