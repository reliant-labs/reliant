// Copyright (c) 2025 Reliant Labs
package db

import (
	"errors"
	"net"
	"syscall"
)

// withoutPeerClosedErrors drops, from an error returned by closing a pool, the
// parts that only say the server end had already closed the connection.
//
// Closing a pgx connection sends Terminate and then a TLS close_notify. At
// shutdown PgBouncer has often dropped an idle client already (it closes
// idle clients on its own schedule, and is rolled in the same deploy), so the
// close_notify write fails: "tls: failed to send closeNotify alert (but
// connection was closed anyway): write tcp ...: write: broken pipe". The
// connection is closed either way — crypto/tls says so in the message — so
// reporting it as "Error closing database" on every rollout reported nothing.
//
// Anything else stays an error. A joined error keeps its other members.
func withoutPeerClosedErrors(err error) error {
	kept, _ := dropPeerClosedErrors(err)
	return kept
}

// dropPeerClosedErrors is withoutPeerClosedErrors, also reporting whether it
// dropped anything — so an error it did not touch is returned as it was, not
// rebuilt.
func dropPeerClosedErrors(err error) (kept error, dropped bool) {
	if err == nil {
		return nil, false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var members []error
		for _, member := range joined.Unwrap() {
			memberKept, memberDropped := dropPeerClosedErrors(member)
			dropped = dropped || memberDropped
			if memberKept != nil {
				members = append(members, memberKept)
			}
		}
		if !dropped {
			return err, false
		}
		return errors.Join(members...), true
	}
	if isPeerClosedError(err) {
		return nil, true
	}
	return err, false
}

// isPeerClosedError reports whether err is a write or close on a connection
// the other end had already torn down. crypto/tls wraps the failed
// close_notify write with %w, so the syscall error is reachable through it.
func isPeerClosedError(err error) bool {
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}
