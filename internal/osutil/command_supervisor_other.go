//go:build !linux

package osutil

import (
	"fmt"
	"os/exec"
	"time"
)

func SuperviseCommand(cmd *exec.Cmd, grace time.Duration) error {
	return fmt.Errorf("run-owned command supervision unsupported on this platform; shutdown cannot be confirmed")
}
