//go:build !windows

package perplatform

import "errors"

// lock does real work on Unix.
func lock(fd int) error {
	if fd < 0 {
		return errors.New("bad fd")
	}
	return nil
}

// probe is a stub on Unix too.
func probe(string) error { return nil }
