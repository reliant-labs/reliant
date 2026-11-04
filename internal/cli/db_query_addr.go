package cli

// `forge db query --addr host:port`: the bring-your-own-route transport. For a
// database forge cannot route to itself (a real cluster env, a database behind
// a VPN or a bastion) the user supplies the address and the network path to it
// (a VPN, `kubectl port-forward`, a bastion); forge builds no fallback
// transport and dials that address directly.
//
// Everything past the connection is the other transports': the same
// dbquery.Run guard, caps, timeout and formats, and the env's data_access
// policy. Credentials too: a cluster database's forge_ro role and CA come from
// its Secrets through the user's kube context, an OnHost / OnCompose workload
// keeps its resolved DATABASE_URL with only the host and port replaced. A
// hosted env is refused: the control plane executes those queries, and the
// user holds none of their credentials.

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
)

// dbQueryTransportAddr is scope.transport of a query that dialled --addr.
const dbQueryTransportAddr = "addr"

func errDBQueryAddrHosted(o dbQueryOptions) error {
	return exitCodeError{code: exitWrong, msg: fmt.Sprintf("--addr cannot be used on %s: its database is hosted, so the control plane executes the query (as the env's read-only role, audited) and you hold no credentials for it; drop --addr", o.env)}
}

// validateDBQueryAddr checks --addr and --sslmode before anything is dialled.
func validateDBQueryAddr(o dbQueryOptions) error {
	if o.addr == "" {
		if o.sslmode != "" {
			return exitCodeError{code: exitWrong, msg: "--sslmode applies to --addr only"}
		}
		return nil
	}
	host, port, err := net.SplitHostPort(o.addr)
	if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		return exitCodeError{code: exitWrong, msg: "--addr: want host:port, for example localhost:5432 (the value is not echoed: it may carry a credential)"}
	}
	switch o.sslmode {
	case "", kubedial.SSLModeVerifyFull, kubedial.SSLModeRequire, kubedial.SSLModeDisable:
	default:
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("--sslmode %q: use %s, %s or %s", o.sslmode, kubedial.SSLModeVerifyFull, kubedial.SSLModeRequire, kubedial.SSLModeDisable)}
	}
	return nil
}

// dsnAtAddr is the connection a workload's DATABASE_URL describes, dialled at
// addr instead of the host and port the URL names (its fallbacks, which
// sslmode=prefer adds, included). The error never quotes the URL: it can carry
// a password.
func dsnAtAddr(dsn, addr string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("not a valid connection URL")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errors.New("--addr: want host:port")
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, errors.New("--addr: want host:port")
	}
	// pgx resolved the URL's host into the TLS server name already, so a
	// verify-full URL still verifies against the name it declared.
	cfg.Host, cfg.Port = host, uint16(port)
	for _, f := range cfg.Fallbacks {
		f.Host, f.Port = host, uint16(port)
	}
	return cfg, nil
}
