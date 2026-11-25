package controlplane

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	userv1 "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1"
)

// ErrAccountDeletionConfirmed is the control plane refusing a request because
// the caller's platform account is VERIFIABLY deleted: its account row is
// tombstoned, and the control plane writes the tombstone only after it settled
// the wallet, tore down the daemons and removed the sign-in identity.
//
// It is recognised only by the control plane's AccountDeletionConfirmed error
// detail, never by the refusal's code, message or reason alone. The control
// plane sends the same refusal without the detail when it could not check
// (its tombstone lookup failed), and a caller that destroys data on "the
// platform account is gone" must not do so then.
var ErrAccountDeletionConfirmed = errors.New("controlplane: the platform account is confirmed deleted")

// classifyAccountDeleted wraps err in ErrAccountDeletionConfirmed when the
// control plane's refusal carries the AccountDeletionConfirmed detail, and
// returns it unchanged otherwise. The wrap keeps the *connect.Error reachable,
// so its code, message and reason still classify the error.
func classifyAccountDeleted(err error) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	for _, d := range ce.Details() {
		v, decodeErr := d.Value()
		if decodeErr != nil {
			continue
		}
		if _, ok := v.(*userv1.AccountDeletionConfirmed); ok {
			return fmt.Errorf("%w: %w", ErrAccountDeletionConfirmed, err)
		}
	}
	return err
}
