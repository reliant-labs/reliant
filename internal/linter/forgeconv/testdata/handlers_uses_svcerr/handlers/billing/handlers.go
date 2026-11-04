// Fixture: importing forge/pkg/svcerr does NOT suppress the rule — a
// mapper beside svcerr.Wrap is still the mapper svcerr replaces.
//
//nolint:all
package billing

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

var ErrLegacy = errors.New("legacy")

// A suspect-named helper next to the svcerr import is still flagged.
func toConnectError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrLegacy):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, svcerr.ErrNotFound):
		return svcerr.Wrap(err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("internal"))
	}
}
