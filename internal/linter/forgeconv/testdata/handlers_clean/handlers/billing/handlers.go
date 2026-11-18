// Fixture: a handler file that constructs ONE connect.NewError ad-hoc
// and never re-rolls the mapping switch. Must NOT be flagged.
//
//nolint:all
package billing

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// Sentinel for a single domain-specific failure. One arm, no name match.
var ErrStoreNotConfigured = errors.New("billing: store not configured")

// One-off use of connect.NewError; no switch, no errors.Is. Should not flag.
func GetThing(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	return nil
}
