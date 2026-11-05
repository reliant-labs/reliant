// Fixture mirroring the cpnext duplication this lint targets.
// DO NOT auto-format / clean up — the shape IS the test.
//
//nolint:all
package billing

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

// Local sentinels stand in for internal/svcerr.Err* — the real
// duplication shape didn't import svcerr from forge/pkg either.
var (
	ErrNotFound         = errors.New("not found")
	ErrAlreadyExists    = errors.New("already exists")
	ErrPermissionDenied = errors.New("permission denied")
	ErrInvalidArgument  = errors.New("invalid argument")
)

// Sentinel name AND body shape: this is the canonical duplication.
func toConnectError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrInvalidArgument):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}
}

// Suspect by name only (body trivial). Still flagged.
func mapServiceError(err error) error {
	return err
}
