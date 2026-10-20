// Copyright (c) 2025 Reliant Labs
//go:build !linux && !darwin

package osutil

import "errors"

// ParentPID is not implemented on this platform; IsSelfOrAncestor then
// answers false.
func ParentPID(int) (int, error) {
	return 0, errors.ErrUnsupported
}
