// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: daemon bootstrap config and daemon-id file persistence; local file only
package bootstrap

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DaemonIDFileName holds the stable server-assigned daemon id, inside the
// instance's own data directory.
//
// It lives HERE, next to the runtime record, rather than in the credentials
// store, because identity is per INSTANCE and a credential is per ACCOUNT.
// Keyed by origin in ~/.reliant/daemon.json, every worktree on one machine read
// back the same id and re-asserted it in DaemonRegister; the gateway trusts that
// field verbatim, so each registration evicted the other daemon holding the same
// id and the two fought until one was killed. Distinct data directories alone do
// not fix that — the id has to be distinct too, and the data directory is
// already the one thing that is per instance.
const DaemonIDFileName = "daemon-id"

// DaemonIDPath is where ReadDaemonID and WriteDaemonID keep the id for the
// instance that owns dataDir.
func DaemonIDPath(dataDir string) string {
	return filepath.Join(dataDir, DaemonIDFileName)
}

// ReadDaemonID returns the stable daemon id previously assigned to this
// instance, or "" when there is none.
//
// Absence is the first-ever-registration case and is not an error: the daemon
// registers with an empty id, the gateway mints one, and WriteDaemonID records
// it. An unreadable or corrupt file is treated the same way — re-registering as
// new is always recoverable, while refusing to start is not.
func ReadDaemonID(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	data, err := os.ReadFile(DaemonIDPath(dataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteDaemonID records this instance's assigned daemon id.
func WriteDaemonID(dataDir, daemonID string) error {
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("cannot persist daemon id: no data directory")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data directory %s: %w", dataDir, err)
	}
	return os.WriteFile(DaemonIDPath(dataDir), []byte(strings.TrimSpace(daemonID)+"\n"), 0o600)
}

// TLSMode controls daemon transport security/protocol behavior.
type TLSMode string

const (
	TLSModeTLS                   TLSMode = "tls"
	TLSModeInsecureTLSSkipVerify TLSMode = "insecure_tls_skip_verify"
	TLSModeH2C                   TLSMode = "h2c"
	TLSModeDisabled              TLSMode = "disabled" // alias for h2c
)

// DaemonBootstrapConfig is the explicit launcher-provided config for tools-daemon.
// user_id is no longer carried here — the gateway derives it from the PAT and
// returns it in the RegistrationAck.
type DaemonBootstrapConfig struct {
	AuthToken string
	GRPCURL   string
	TLSMode   TLSMode
	DataDir   string
	Name      string // Human-friendly daemon name (default: hostname)

	// ServerURL is the API server origin (scheme://host:port) these
	// credentials belong to. Empty in server mode (the gateway dials in and
	// already knows our identity).
	ServerURL string

	// DaemonID is the stable identity the server previously assigned to THIS
	// INSTANCE, read from DataDir at startup (see ReadDaemonID). The daemon
	// re-asserts it in its registration message so identity survives restarts
	// and hostname changes. Empty on first-ever registration.
	//
	// Per instance, not per origin: the gateway trusts this field verbatim, so
	// two daemons asserting one id evict each other on every registration.
	DaemonID string

	// ServerMode, when true, makes the daemon listen on ListenPort for
	// incoming gateway connections instead of dialing out.
	ServerMode bool
	ListenPort int // default 9190

	// Verbose mirrors the CLI's --verbose flag. It selects who the daemon's
	// stdout is FOR.
	//
	// Off (a person ran `reliant daemon start` in a terminal): stdout carries
	// short human status lines only. The structured log still goes to the
	// rotating file in DataDir, so nothing is lost.
	//
	// On (a supervisor spawned us, or someone is debugging): stdout carries the
	// full structured log AND the `@@RELIANT_STREAM <state>` machine notices a
	// parent parses to learn of a connect immediately instead of waiting on its
	// 250ms stat-poll of daemon-state.json.
	//
	// Electron always passes --verbose for exactly that reason (see
	// electron/src/backend-manager.js buildDaemonArgs). The on-disk record
	// remains the source of truth in both modes, so a daemon started without
	// this flag is still fully observable — just one poll interval slower.
	Verbose bool
}

// NormalizeGatewayURL maps the gateway address forms an operator can plausibly
// be handed onto the two schemes the daemon's HTTP/2 transport can actually
// dial.
//
// `forge cluster urls` prints the daemon gateway as grpc://host:port, which is
// the natural thing to paste into --grpc-url. golang.org/x/net/http2 rejects
// any scheme other than http/https ("http2: unsupported scheme"), and
// connect-go surfaces that on a bidi stream as `write envelope: EOF` — so the
// daemon appeared to start and then silently served nothing. grpc:// is
// unambiguous (plaintext h2c) and grpcs:// is unambiguous (TLS), so both are
// accepted and rewritten rather than rejected. Everything else is rejected up
// front naming the schemes that work: a clear error at startup beats a process
// that runs forever without a stream.
func NormalizeGatewayURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("missing daemon gateway URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid daemon gateway URL %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "http", "https":
		return trimmed, nil
	case "grpc":
		parsed.Scheme = "http"
		return parsed.String(), nil
	case "grpcs":
		parsed.Scheme = "https"
		return parsed.String(), nil
	default:
		return "", fmt.Errorf(
			"unsupported daemon gateway URL %q: scheme %q cannot be dialed — use http:// (or grpc://) for plaintext h2c, https:// (or grpcs://) for TLS",
			raw, parsed.Scheme)
	}
}

// GatewayURL returns the dialable gateway URL for this config.
func (c DaemonBootstrapConfig) GatewayURL() (string, error) {
	return NormalizeGatewayURL(c.GRPCURL)
}

func (c DaemonBootstrapConfig) Validate() error {
	if !c.ServerMode && strings.TrimSpace(c.AuthToken) == "" {
		return fmt.Errorf("missing required daemon PAT (run 'reliant daemon register' to set up credentials)")
	}
	if !c.ServerMode {
		if _, err := NormalizeGatewayURL(c.GRPCURL); err != nil {
			return err
		}
	}
	switch c.TLSMode {
	case TLSModeTLS, TLSModeInsecureTLSSkipVerify, TLSModeH2C, TLSModeDisabled:
		return nil
	case "":
		if c.ServerMode {
			return nil // TLS mode is optional in server mode
		}
		return fmt.Errorf("invalid RELIANT_DAEMON_TLS_MODE %q", c.TLSMode)
	default:
		return fmt.Errorf("invalid RELIANT_DAEMON_TLS_MODE %q", c.TLSMode)
	}
}
