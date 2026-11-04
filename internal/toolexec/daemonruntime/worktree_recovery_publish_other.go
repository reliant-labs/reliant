//go:build !linux

package daemonruntime

import "fmt"

func publishRecoveryDirectory(from, to string) error {
	return fmt.Errorf("atomic no-overwrite recovery publication is unsupported on this platform")
}
