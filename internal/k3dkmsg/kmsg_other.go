//go:build !linux

package k3dkmsg

import (
	"errors"
	"io/fs"
	"os"
)

// statDevice: the workaround only applies on Linux (Host.needed), so off Linux
// this only has to tell present from absent.
func statDevice(path string) (State, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return StateMissing, nil
	} else if err != nil {
		return StateMissing, err
	}
	return StateOther, nil
}
