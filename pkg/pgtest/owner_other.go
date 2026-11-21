//go:build !linux && !darwin

package pgtest

// pidSpaceID: no way here to name the pid space reliably (Windows has no boot
// id an unprivileged process can read), so scratch databases created on this
// platform record no owner and are never reaped by liveness. They are still
// dropped by their cleanup, and with the server when it is torn down.
func pidSpaceID() (string, bool) { return "", false }

// processStartToken: unreadable here; see pidSpaceID.
func processStartToken(int) (string, bool) { return "", false }
