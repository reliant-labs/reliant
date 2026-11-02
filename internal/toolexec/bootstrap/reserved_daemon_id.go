package bootstrap

import (
	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ReservedDaemonIDReason is the stable machine-readable reason the gateway
// attaches, as svcerr.ReasonHeader metadata, when it refuses a registration
// because the asserted daemon id is reserved for daemon-bound credentials and
// the caller presented an unbound one (a login token).
//
// A daemon id that a daemon-bound credential has ever connected as — every
// managed cloud machine — can only be connected to by such a credential from
// then on. The account owns the machine either way; what is refused is a
// login token standing in for the machine's own credential, which would let
// any daemon of the account take over the machine's identity and its live
// connection.
//
// Like ForeignDaemonIDReason it names the condition, not a credential
// failure: the token was accepted and only the asserted identity was refused,
// so a daemon must never answer it by discarding its credential.
const ReservedDaemonIDReason = "daemon_id_requires_bound_credential"

// reservedDaemonIDMessage is the refusal's text.
const reservedDaemonIDMessage = "daemon id is reserved for its daemon-bound credential; a login token cannot connect as it"

// NewReservedDaemonIDError is the gateway's refusal of an unbound credential
// asserting a daemon id reserved for daemon-bound credentials.
func NewReservedDaemonIDError() *connect.Error {
	return svcerr.ToConnect(svcerr.WithReason(svcerr.PermissionDenied(reservedDaemonIDMessage), ReservedDaemonIDReason))
}

// IsReservedDaemonIDError reports whether err, or anything it wraps, is the
// gateway refusing a daemon id because it is reserved for daemon-bound
// credentials: the *connect.Error a daemon receives over the wire, or the same
// refusal still in process.
func IsReservedDaemonIDError(err error) bool {
	if err == nil || svcerr.Code(err) != connect.CodePermissionDenied {
		return false
	}
	return svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader) == ReservedDaemonIDReason
}
