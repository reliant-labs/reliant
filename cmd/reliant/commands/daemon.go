// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/spf13/cobra"
)

const toolsDaemonLogFilename = "tools-daemon.log"

func toolsDaemonLogPath(dataDir string) string {
	return filepath.Join(dataDir, "logs", toolsDaemonLogFilename)
}

func setupToolsDaemonLogging(dataDir string) {
	logging.SetupWithRotation(slog.LevelInfo, false, &logging.RotationConfig{
		Filename:   toolsDaemonLogPath(dataDir),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
}

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the local tools daemon",
		Long: `The tools daemon runs on your local machine and provides tool execution
capabilities (shell, file operations, MCP servers, terminal sessions) to
the Reliant cloud platform via a bidirectional gRPC stream.`,
	}

	cmd.AddCommand(newDaemonRegisterCmd())
	cmd.AddCommand(newDaemonStartCmd())
	cmd.AddCommand(newDaemonStatusCmd())
	cmd.AddCommand(newDaemonStopCmd())
	cmd.AddCommand(newDaemonLogsCmd())

	return cmd
}

// registerDaemon performs the daemon registration flow:
// 1. Ensures user is logged in (runs OAuth if not)
// 2. Calls CreateDaemonToken RPC to get a PAT
// 3. Writes daemon credentials to local file
//
// The daemon PAT is minted by, and scoped to, conn's server — the one the
// resolved context names — so registering never silently targets a different
// server than the rest of the CLI.
//
// nonInteractive, when true, forbids the OAuth login step from opening a
// browser or starting the local login-page HTTP server: it is passed straight
// through to auth.Login, which returns auth.ErrNonInteractiveLoginRequired
// instead of running the interactive flow. Callers that must idle rather than
// fail outright (see waitForCredentialsNonInteractive) detect that sentinel
// with errors.Is.
func registerDaemon(ctx context.Context, cmd *cobra.Command, conn *connection, nonInteractive bool) error {
	apiURL, gwURL := conn.ServerURL, conn.GatewayURL

	accessToken, err := auth.ReadAccessTokenFromAuthFile()
	if err != nil {
		return fmt.Errorf("reading auth file: %w", err)
	}
	if accessToken == "" {
		if !nonInteractive {
			fmt.Fprintln(cmd.OutOrStdout(), "Not logged in. Starting authentication...")
		}
		result, err := auth.Login(ctx, auth.LoginOptions{NonInteractive: nonInteractive})
		if err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
		if err := auth.WriteAuthSession(result.AccessToken, result.RefreshToken, result.UserID, result.Email); err != nil {
			return fmt.Errorf("saving credentials: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s\n", result.Email)
		accessToken = result.AccessToken
	}

	// Step 2: Call CreateDaemonToken RPC on DaemonTokenService (JWT-authed).
	// The token's name is a human-facing label in `auth token list`, not a
	// lookup key (daemon-kind PATs are found by hash, and revoked by name or id
	// only among api-kind tokens). Stamping the stable instance id into it
	// keeps two registrations from the same machine recognizable as such even
	// if the hostname flipped between them.
	tokenName := instanceid.Label()

	logging.Info("Registering daemon via CreateDaemonToken",
		"api_url", apiURL, "token_name", tokenName, "instance_id", instanceid.ID())

	// Registration is JWT-only (a PAT cannot mint a daemon PAT), so the bearer
	// is the login session rather than the connection's resolved token.
	httpClient := conn.httpClientWithBearer(accessToken)
	client := reliantv1connect.NewDaemonTokenServiceClient(httpClient, apiURL)

	resp, err := client.CreateDaemonToken(ctx, connect.NewRequest(&reliantv1.CreateDaemonTokenRequest{
		Name: tokenName,
	}))
	if err != nil {
		logging.Error("CreateDaemonToken failed", "error", err, "code", connect.CodeOf(err), "api_url", apiURL)
		return conn.annotate(fmt.Errorf("creating daemon token: %w", err))
	}
	logging.Info("Daemon token created successfully", "token_id", resp.Msg.GetTokenId())

	// Step 3: Write daemon credentials. The server identifies the user from the
	// PAT on every call, so the client doesn't store user_id.
	creds := &auth.DaemonCredentials{
		PAT:          resp.Msg.GetToken(),
		ServerURL:    apiURL,
		GatewayURL:   gwURL,
		RegisteredAt: time.Now().UTC(),
	}
	if err := auth.WriteDaemonCredentials(creds); err != nil {
		return fmt.Errorf("saving daemon credentials: %w", err)
	}

	credsPath, _ := auth.DaemonCredentialsFilePath()
	fmt.Fprintln(cmd.OutOrStdout(), "Daemon registered successfully")
	fmt.Fprintf(cmd.OutOrStdout(), "  Credentials: %s\n", credsPath)
	fmt.Fprintf(cmd.OutOrStdout(), "  Server:      %s\n", conn.describeServer())
	fmt.Fprintf(cmd.OutOrStdout(), "  Gateway:     %s\n", conn.describeGateway())
	return nil
}

// daemonPATRenewBefore is how far ahead of a stored PAT's known expiry the
// daemon proactively re-mints, so it never boots on a credential about to lapse
// mid-run. Only applies to creds that carry an expiry; daemon-kind PATs are
// non-expiring and are unaffected.
const daemonPATRenewBefore = 24 * time.Hour

// daemonCredsExpiringSoon reports whether stored creds carry a known expiry that
// has already lapsed or falls within daemonPATRenewBefore of now. Non-expiring
// creds (nil creds or nil ExpiresAt — every CreateDaemonToken / managed mint)
// never expire and always return false.
func daemonCredsExpiringSoon(creds *auth.DaemonCredentials, now time.Time) bool {
	if creds == nil || creds.ExpiresAt == nil {
		return false
	}
	return !creds.ExpiresAt.After(now.Add(daemonPATRenewBefore))
}

// ensureDaemonCredentials returns existing daemon credentials or creates new ones.
// This is the shared credential resolution logic used by both `open` and `daemon start`.
//
// If no credentials exist, runs the registration flow: prompts for sign-in
// (if not already, and unless nonInteractive is set — see registerDaemon),
// then mints a PAT via CreateDaemonToken. Most users will instead use
// `--token` to paste a PAT minted from the web UI.
func ensureDaemonCredentials(ctx context.Context, cmd *cobra.Command, conn *connection, nonInteractive bool) (*auth.DaemonCredentials, error) {
	apiURL, gwURL := conn.ServerURL, conn.GatewayURL

	creds, err := auth.ReadDaemonCredentials(apiURL)
	if err != nil {
		return nil, fmt.Errorf("reading daemon credentials: %w", err)
	}
	if creds != nil {
		// A stored PAT that carries a known expiry and has lapsed (or is about to)
		// can no longer authenticate the daemon; returning it would fail the
		// gateway reach-out fatally. Proactively drop it and fall through to
		// re-mint a fresh — non-expiring — daemon PAT. Non-expiring creds
		// (ExpiresAt == nil), which is every CreateDaemonToken / managed-daemon
		// mint, skip this and are returned as-is.
		if daemonCredsExpiringSoon(creds, time.Now()) {
			logging.Warn("stored daemon PAT is expired or near expiry — re-minting",
				"expiresAt", creds.ExpiresAt.Format(time.RFC3339))
			if !nonInteractive {
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon credentials expired. Re-registering...")
			}
			_ = auth.DeleteDaemonCredentials(apiURL)
			creds = nil
		} else {
			// Update gateway URL if it changed (e.g. flag override). The in-memory
			// creds are authoritative for this boot; persisting the drift back to
			// disk is an optimization, so a read-only / mounted credentials file
			// (managed-daemon dial-out mode) must not fail the boot.
			if gwURL != "" && creds.GatewayURL != gwURL {
				creds.GatewayURL = gwURL
				persistDaemonCredentials(creds)
			}
			return creds, nil
		}
	} else if !nonInteractive {
		fmt.Fprintln(cmd.OutOrStdout(), "No daemon credentials found. Registering...")
	}

	if err := registerDaemon(ctx, cmd, conn, nonInteractive); err != nil {
		return nil, fmt.Errorf("daemon registration failed: %w", err)
	}

	// Re-read the credentials we just wrote
	creds, err = auth.ReadDaemonCredentials(apiURL)
	if err != nil || creds == nil {
		return nil, fmt.Errorf("failed to read daemon credentials after registration")
	}
	return creds, nil
}

// daemonCredentialPollInterval is how often a non-interactive daemon with no
// usable credentials re-checks disk for one appearing there. In practice this
// is Electron's own pre-mint path (electron/src/daemon-creds.js) writing
// ~/.reliant/daemon.json after the user signs in through Electron's login
// page and respawns the daemon — but polling also covers the case where the
// daemon is left running and the file simply appears underneath it. A stat
// plus small JSON read is cheap enough to afford every few seconds.
//
// A var, not a const, so tests can shrink it rather than spending real wall
// clock time waiting out the production interval.
var daemonCredentialPollInterval = 3 * time.Second

// waitForCredentialsNonInteractive is what a non-interactive daemon does
// instead of running the OAuth login flow: it stays resident and idle,
// publishing daemonstate.StreamAwaitingCredentials so `daemon status` and
// Electron's own health check can report "waiting for sign-in" honestly
// instead of as a crash, and re-checks the credentials file on disk until one
// appears or ctx is cancelled (SIGINT/SIGTERM are wired to ctx cancellation by
// watchShutdownSignals, so this loop stays responsive to graceful shutdown).
func waitForCredentialsNonInteractive(ctx context.Context, cmd *cobra.Command, conn *connection, dataDir string) (*auth.DaemonCredentials, error) {
	fmt.Fprintln(cmd.OutOrStdout(), "No daemon credentials found and running non-interactively — waiting for sign-in...")
	if err := daemonstate.SetStream(dataDir, daemonstate.StreamAwaitingCredentials, "waiting for credentials to appear on disk"); err != nil {
		logging.Warn("failed to publish awaiting-credentials daemon state", "error", err)
	}

	// Check immediately before the first tick: a credential can already be on
	// disk (e.g. a slow registerDaemon call lost a race with Electron's own
	// pre-mint), and there is no reason to make that case wait a full poll
	// interval.
	if creds := pollDaemonCredentials(cmd, conn); creds != nil {
		return creds, nil
	}

	ticker := time.NewTicker(daemonCredentialPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if creds := pollDaemonCredentials(cmd, conn); creds != nil {
				return creds, nil
			}
		}
	}
}

// pollDaemonCredentials is one credential-check attempt shared by the
// immediate check and every subsequent tick in
// waitForCredentialsNonInteractive. Returns nil (never an error) when there is
// nothing usable yet — a bad read or a not-yet-valid credential is exactly the
// same as "nothing yet" to the caller, which just waits for the next tick.
func pollDaemonCredentials(cmd *cobra.Command, conn *connection) *auth.DaemonCredentials {
	creds, err := auth.ReadDaemonCredentials(conn.ServerURL)
	if err != nil {
		logging.Warn("error re-checking daemon credentials while awaiting sign-in", "error", err)
		return nil
	}
	if creds == nil {
		return nil
	}
	// A credential can appear on disk already past its expiry (a stale mount,
	// a slow write racing a clock) — keep waiting for a good one rather than
	// handing the caller something that will fail the very first gateway
	// reach-out.
	if daemonCredsExpiringSoon(creds, time.Now()) {
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Daemon credentials found — resuming startup")
	return creds
}

// resolveOrAwaitCredentials calls ensureDaemonCredentials and, when running
// non-interactively with no usable credentials, falls into
// waitForCredentialsNonInteractive instead of surfacing
// auth.ErrNonInteractiveLoginRequired as a fatal error. This is the seam used
// by both the initial credential resolution and the auth-failure retry path
// in `daemon start`, so neither can regress into popping a browser.
func resolveOrAwaitCredentials(ctx context.Context, cmd *cobra.Command, conn *connection, dataDir string, nonInteractive bool) (*auth.DaemonCredentials, error) {
	creds, err := ensureDaemonCredentials(ctx, cmd, conn, nonInteractive)
	if err == nil {
		return creds, nil
	}
	if nonInteractive && errors.Is(err, auth.ErrNonInteractiveLoginRequired) {
		return waitForCredentialsNonInteractive(ctx, cmd, conn, dataDir)
	}
	return nil, err
}

// registerOrAwaitCredentials is resolveOrAwaitCredentials's counterpart for
// the auth-failure retry path: the caller has already deleted stale
// credentials and needs a fresh registration, but a non-interactive daemon
// must idle rather than fail when that requires a login it cannot run.
func registerOrAwaitCredentials(ctx context.Context, cmd *cobra.Command, conn *connection, dataDir string, nonInteractive bool) (*auth.DaemonCredentials, error) {
	regErr := registerDaemon(ctx, cmd, conn, nonInteractive)
	if regErr == nil {
		newCreds, readErr := auth.ReadDaemonCredentials(conn.ServerURL)
		if readErr != nil || newCreds == nil {
			return nil, fmt.Errorf("failed to read credentials after re-registration")
		}
		return newCreds, nil
	}
	if nonInteractive && errors.Is(regErr, auth.ErrNonInteractiveLoginRequired) {
		return waitForCredentialsNonInteractive(ctx, cmd, conn, dataDir)
	}
	return nil, fmt.Errorf("re-registration failed: %w", regErr)
}

// persistDaemonCredentials best-effort writes daemon credentials to disk.
//
// Persisting is an optimization: the supplied creds remain authoritative for
// the running daemon whether or not the write lands. In managed-daemon
// dial-out mode the credentials file is a read-only mounted Kubernetes Secret,
// so a write attempt fails with EROFS/EACCES. We must tolerate that and let
// the daemon boot rather than crashing. A genuinely unexpected write error on
// a writable path is still surfaced as a warning (we never have anything to
// fail hard on here — callers that *require* a successful first write call
// auth.WriteDaemonCredentials directly and propagate its error).
func persistDaemonCredentials(creds *auth.DaemonCredentials) {
	err := auth.WriteDaemonCredentials(creds)
	if err == nil {
		return
	}
	if isReadOnlyOrPermissionErr(err) {
		logging.Warn("credentials file is read-only (managed/mounted creds); skipping persist, using provided credentials",
			"error", err)
		return
	}
	logging.Warn("failed to persist daemon credentials; continuing with in-memory credentials",
		"error", err)
}

// isReadOnlyOrPermissionErr reports whether err indicates the target path is
// not writable because the filesystem is read-only or permission was denied —
// the signature of a read-only mounted Secret.
func isReadOnlyOrPermissionErr(err error) bool {
	return errors.Is(err, os.ErrPermission) ||
		errors.Is(err, syscall.EROFS) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM)
}

// credentialsFromToken reads a PAT from stdin and constructs daemon credentials.
// Supports both interactive (prompt) and piped input.
//
// The stdin read runs in a background goroutine and the main path selects on
// ctx.Done() so Ctrl+C unblocks the wait. `bufio.Scanner.Scan` blocks in
// `syscall.read(2)` and cannot itself be canceled — the goroutine outlives
// this function in that case, but the process is on its way out so the leak
// is bounded.
func credentialsFromToken(ctx context.Context, cmd *cobra.Command, conn *connection) (*auth.DaemonCredentials, error) {
	apiURL, gwURL := conn.ServerURL, conn.GatewayURL

	stat, _ := os.Stdin.Stat()
	isPiped := (stat.Mode() & os.ModeCharDevice) == 0

	if !isPiped {
		fmt.Fprint(cmd.OutOrStdout(), "Paste your access token: ")
	}

	type readResult struct {
		token string
		err   error
	}
	readCh := make(chan readResult, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			readCh <- readResult{token: strings.TrimSpace(scanner.Text())}
			return
		}
		if err := scanner.Err(); err != nil {
			readCh <- readResult{err: err}
			return
		}
		readCh <- readResult{}
	}()

	var token string
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-readCh:
		if res.err != nil {
			return nil, res.err
		}
		token = res.token
	}

	if token == "" {
		if isPiped {
			return nil, fmt.Errorf("no token provided via stdin")
		}
		return nil, fmt.Errorf("no token provided")
	}

	if !auth.IsPATFormat(token) {
		return nil, fmt.Errorf("invalid token format (expected rlnt_pat_...)")
	}

	// We don't pre-validate the token over the network. The stream connect
	// the daemon makes next does PAT auth on every call, so a bad token
	// surfaces with a clear CodeUnauthenticated on the very first reach-out.
	// Skipping a redundant probe keeps the server's PAT auth surface
	// confined to the daemon listener.
	creds := &auth.DaemonCredentials{
		PAT:          token,
		ServerURL:    apiURL,
		GatewayURL:   gwURL,
		RegisteredAt: time.Now().UTC(),
	}

	if err := auth.WriteDaemonCredentials(creds); err != nil {
		return nil, fmt.Errorf("saving daemon credentials: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\u2713 Token accepted (host: %s)\n", instanceid.Label())
	return creds, nil
}

func newDaemonRegisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register this machine as a daemon",
		Long: `Registers this machine to connect to the Reliant cloud platform as a daemon.

If not already logged in, opens your browser for OAuth authentication.
Creates a long-lived access token for the daemon and stores it locally.

After registering, run 'reliant daemon start' to connect.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Registration mints a credential for a specific server; resolve it
			// the same way every other command does. resolveServer (not
			// resolveConnection) because registering is how you get a credential
			// — requiring one first would be circular.
			conn, err := resolveServer(cmd)
			if err != nil {
				return err
			}

			// Check if already registered
			creds, err := auth.ReadDaemonCredentials(conn.ServerURL)
			if err != nil {
				return fmt.Errorf("reading daemon credentials: %w", err)
			}
			if creds != nil {
				credsPath, _ := auth.DaemonCredentialsFilePath()
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon is already registered")
				fmt.Fprintf(cmd.OutOrStdout(), "  Credentials: %s\n", credsPath)
				fmt.Fprintf(cmd.OutOrStdout(), "  Server:      %s\n", creds.ServerURL)
				fmt.Fprintf(cmd.OutOrStdout(), "\nTo re-register, delete the credentials file and run this command again.\n")
				return nil
			}

			// `daemon register` is always an explicit, interactive CLI
			// invocation — never spawned by Electron — so it never passes
			// nonInteractive.
			return registerDaemon(cmd.Context(), cmd, conn, false)
		},
	}

	return cmd
}

// daemonShutdownGrace bounds the whole graceful shutdown. It is shorter than
// daemonStopGrace so the daemon's own exit normally beats `daemon stop`'s
// deadline and stop never has to escalate.
const daemonShutdownGrace = 10 * time.Second

// watchShutdownSignals turns SIGINT/SIGTERM into cancellation and guarantees
// the process actually exits.
//
//	first signal  → cancel ctx (graceful shutdown)
//	second signal → exit 130 (the interactive escape hatch)
//	grace elapses → exit 1
//
// The deadline is the part that matters for an unattended daemon, which no one
// is sitting at a terminal to Ctrl+C twice. Graceful shutdown walks code that
// can block on a peer that stopped reading, on a child that ignores SIGTERM, or
// on any future defer nobody audited — and a shutdown path that can hang
// forever is exactly what makes "signal it and assume" look acceptable in the
// tools that drive it. Bounding the whole path is cheaper and more durable than
// proving every step inside it terminates.
func watchShutdownSignals(sigCh <-chan os.Signal, w io.Writer, cancel context.CancelFunc, grace time.Duration, exit func(int)) {
	<-sigCh
	fmt.Fprintln(w, "\nShutting down — press Ctrl+C again to force-exit")
	cancel()

	select {
	case <-sigCh:
		fmt.Fprintln(w, "Force-exiting")
		exit(130)
	case <-time.After(grace):
		fmt.Fprintf(w, "Graceful shutdown did not finish within %s — force-exiting\n", grace)
		exit(1)
	}
}

func newDaemonStartCmd() *cobra.Command {
	var (
		port           string
		grpcURL        string
		dataDir        string
		background     bool
		tlsCert        string
		tlsKey         string
		tlsMode        string
		useToken       bool
		daemonName     string
		serverMode     bool
		listenPort     int
		nonInteractive bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the tools daemon",
		Long: `Starts the tools daemon in the foreground (default) or background.

The daemon connects to the Reliant cloud platform and provides local tool
execution capabilities.

Credential resolution order:
  1. Daemon credentials file (created by 'reliant daemon register')
  2. If logged in but not registered, auto-registers and creates credentials
  3. If not logged in, prompts for login and then auto-registers — unless
     --non-interactive (or RELIANT_DAEMON_NON_INTERACTIVE) is set, in which
     case the daemon never opens a browser or runs the login flow itself. It
     instead stays resident and idle, publishing "awaiting_credentials" in its
     runtime state, and polls for a credentials file to appear on disk (see
     'reliant daemon status' and internal/toolexec/daemonstate). This is the
     mode Electron spawns in: its own login page owns interactive sign-in, and
     the daemon must never pop a second one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if background {
				// TODO: implement background fork/detach
				return fmt.Errorf("--background is not yet implemented")
			}

			// Keep the foreground stdout stream used by Electron/task while also
			// persisting an independently rotated tools-daemon log. Do not share
			// reliant.log with the API server: dev-electron can run both processes
			// against the same data root, and separate lumberjack writers must not
			// rotate the same file.
			setupToolsDaemonLogging(dataDir)
			defer logging.Close() //nolint:errcheck

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sigCh := make(chan os.Signal, 2)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigCh)
			go watchShutdownSignals(sigCh, cmd.ErrOrStderr(), cancel, daemonShutdownGrace, os.Exit)

			// The daemon runtime publishes the record `daemon status` and
			// `daemon stop` read (PID, binary identity, gateway-stream state);
			// see internal/toolexec/daemonstate.

			// Clean up background processes on shutdown.
			defer shell.GetBackgroundManager().KillAllRunning()
			defer shell.GetProcessMonitor().Stop()

			// In server mode, skip credential resolution — the gateway dials
			// into us and already knows our identity from the NATS connect command.
			if serverMode {
				logging.Info("Starting tools-daemon in server mode",
					"listen_port", listenPort, "data_dir", dataDir)

				err := daemonruntime.Start(ctx, daemonruntime.StartOptions{
					BootstrapConfig: bootstrap.DaemonBootstrapConfig{
						ServerMode: true,
						ListenPort: listenPort,
						DataDir:    dataDir,
						Name:       daemonName,
					},
				})
				if err != nil && !errors.Is(err, context.Canceled) {
					return fmt.Errorf("tools-daemon exited with error: %w", err)
				}
				logging.Info("tools-daemon shut down gracefully")
				return nil
			}

			// --- Client mode: resolve credentials for outbound connection ---
			// resolveServer, not resolveConnection: the daemon authenticates
			// with its own PAT, so it must not require a CLI credential to
			// learn which server/gateway it belongs to.
			conn, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			logging.Info("Daemon target resolved", "server", conn.describeServer(), "gateway", conn.describeGateway())

			var creds *auth.DaemonCredentials
			if useToken {
				creds, err = credentialsFromToken(ctx, cmd, conn)
				if err != nil {
					return err
				}
			} else {
				creds, err = resolveOrAwaitCredentials(ctx, cmd, conn, dataDir, nonInteractive)
				if err != nil {
					return err
				}
			}

			// Gateway URL for the bidi stream: flag > credentials > derive from server
			daemonGRPCURL := grpcURL
			if daemonGRPCURL == "" {
				daemonGRPCURL = creds.GatewayURL
			}
			if daemonGRPCURL == "" {
				daemonGRPCURL = fmt.Sprintf("http://localhost:%s", port)
			}

			// Normalize before the TLS-mode inference below, which keys off the
			// scheme: `forge cluster urls` prints grpc://host:port, and grpcs://
			// must imply TLS just as https:// does.
			daemonGRPCURL, err = bootstrap.NormalizeGatewayURL(daemonGRPCURL)
			if err != nil {
				return err
			}

			// Determine TLS mode: explicit flag/env > cert/key presence > h2c.
			parsedTLSMode := bootstrap.TLSModeH2C

			if tlsMode != "" {
				parsedTLSMode = bootstrap.TLSMode(tlsMode)
			} else if tlsCert != "" && tlsKey != "" {
				parsedTLSMode = bootstrap.TLSModeTLS
				if !cmd.Flags().Changed("grpc-url") {
					daemonGRPCURL = fmt.Sprintf("https://localhost:%s", port)
				}
			} else if strings.HasPrefix(daemonGRPCURL, "https://") {
				parsedTLSMode = bootstrap.TLSModeTLS
			}

			logging.Info("Starting tools-daemon", "port", port, "tls_mode", string(parsedTLSMode), "gateway_url", daemonGRPCURL, "data_dir", dataDir)

			startDaemon := func(c *auth.DaemonCredentials) error {
				return daemonruntime.Start(ctx, daemonruntime.StartOptions{
					BootstrapConfig: bootstrap.DaemonBootstrapConfig{
						AuthToken:  c.PAT,
						GRPCURL:    daemonGRPCURL,
						TLSMode:    parsedTLSMode,
						DataDir:    dataDir,
						Name:       daemonName,
						ServerURL:  conn.ServerURL,
						DaemonID:   c.DaemonID,
						ServerMode: false,
						ListenPort: listenPort,
					},
				})
			}

			logging.Info("Connecting daemon to gateway", "gateway_url", daemonGRPCURL)
			err = startDaemon(creds)
			if err != nil {
				code := connect.CodeOf(err)
				isAuthFail := code == connect.CodeUnauthenticated || code == connect.CodePermissionDenied

				// If the user pasted a token explicitly with --token, a rejection
				// is a real error — don't silently flip into Supabase OAuth, that
				// would surprise the user who chose to use a specific PAT.
				if isAuthFail && useToken {
					_ = auth.DeleteDaemonCredentials(conn.ServerURL)
					return fmt.Errorf("token rejected by gateway %s (%s) — verify the PAT is correct, not revoked, and was minted by %s",
						conn.describeGateway(), code.String(), conn.describeServer())
				}

				// Register flow: stale creds get cleaned up and we re-run the
				// Supabase login + CreateDaemonToken handshake.
				if isAuthFail {
					logging.Warn("Daemon gateway authentication failed — deleting stale credentials and re-registering",
						"error", err, "code", code.String(), "gateway_url", daemonGRPCURL)
					_ = auth.DeleteDaemonCredentials(conn.ServerURL)

					if !nonInteractive {
						fmt.Fprintln(cmd.OutOrStdout(), "Credentials expired or revoked. Re-registering...")
					}
					newCreds, regErr := registerOrAwaitCredentials(ctx, cmd, conn, dataDir, nonInteractive)
					if regErr != nil {
						return fmt.Errorf("re-registration failed: %w (original: %v)", regErr, err)
					}

					logging.Info("Re-registered successfully, retrying connection...")
					if retryErr := startDaemon(newCreds); retryErr != nil {
						return fmt.Errorf("tools-daemon exited with error after re-registration: %w", retryErr)
					}

					logging.Info("tools-daemon shut down gracefully")
					return nil
				}
				return fmt.Errorf("tools-daemon exited with error: %w", err)
			}

			logging.Info("tools-daemon shut down gracefully")
			return nil
		},
	}

	cmd.Flags().StringVar(&port, "port", envOrDefault("TOOLS_DAEMON_PORT", "9190"), "Daemon listen port")
	cmd.Flags().StringVar(&grpcURL, "grpc-url", envOrDefault("DAEMON_GRPC_URL", ""), "gRPC server URL to connect to")
	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("DAEMON_DATA_DIR", "./data"), "Data directory")

	cmd.Flags().BoolVar(&background, "background", false, "Run daemon in background (detached)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", envOrDefault("TLS_CERT_FILE", ""), "TLS certificate file path")
	cmd.Flags().StringVar(&tlsKey, "tls-key", envOrDefault("TLS_KEY_FILE", ""), "TLS key file path")
	cmd.Flags().StringVar(&tlsMode, "tls-mode", envOrDefault("DAEMON_TLS_MODE", ""), "TLS mode (tls, insecure_tls_skip_verify, h2c, or disabled)")
	cmd.Flags().BoolVar(&useToken, "token", false, "Read a PAT from stdin and use it as the daemon credential")
	cmd.Flags().StringVar(&daemonName, "name", "", "Human-friendly daemon name (default: <instance-id>@<hostname>)")
	cmd.Flags().BoolVar(&serverMode, "server-mode", envOrDefault("DAEMON_SERVER_MODE", "") == "true", "Listen for incoming gateway connections instead of dialing out")
	cmd.Flags().IntVar(&listenPort, "listen-port", envOrDefaultInt("DAEMON_LISTEN_PORT", 9190), "Port to listen on in server mode")
	// Precedence: explicit --non-interactive on the command line always wins
	// (cobra only applies this default when the flag is absent); otherwise
	// RELIANT_DAEMON_NON_INTERACTIVE; otherwise false (interactive, the
	// existing CLI behavior). Electron spawns the daemon with this flag set
	// so it never opens a browser — Electron's own login page owns
	// interactive sign-in. See registerDaemon / waitForCredentialsNonInteractive.
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", envOrDefaultBool("RELIANT_DAEMON_NON_INTERACTIVE", false),
		"Never open a browser or run interactive login; idle and wait for credentials to appear on disk instead")

	return cmd
}

// daemonProcessAlive reports whether pid names a live process. Signal 0 is the
// existence check.
func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// printDaemonRuntimeRecord writes the identifying detail behind the headline:
// which process, which binary, which gateway, and how long the stream has been
// in its current state.
func printDaemonRuntimeRecord(w io.Writer, state daemonstate.State, recordPath string) {
	now := time.Now().UTC()
	fmt.Fprintf(w, "  PID:      %d\n", state.PID)

	if state.Executable != "" {
		binary := state.Executable
		if !state.BinaryModTime.IsZero() {
			binary = fmt.Sprintf("%s (built %s, %s ago)",
				binary,
				state.BinaryModTime.Local().Format(time.RFC3339),
				now.Sub(state.BinaryModTime).Round(time.Second))
		}
		fmt.Fprintf(w, "  Binary:   %s\n", binary)
	}
	if state.Revision != "" {
		revision := state.Revision
		if state.Dirty {
			revision += " (built from a dirty tree)"
		}
		fmt.Fprintf(w, "  Revision: %s\n", revision)
	}
	if state.GatewayURL != "" {
		gateway := state.GatewayURL
		if state.TLSMode != "" {
			gateway = fmt.Sprintf("%s (%s)", gateway, state.TLSMode)
		}
		fmt.Fprintf(w, "  Gateway:  %s\n", gateway)
	}

	stream := string(state.Stream)
	if stream == "" {
		stream = "unknown"
	}
	if !state.StreamChangedAt.IsZero() {
		stream = fmt.Sprintf("%s for %s", stream, now.Sub(state.StreamChangedAt).Round(time.Second))
	}
	fmt.Fprintf(w, "  Stream:   %s\n", stream)
	if !state.Stream.Established() {
		if state.ConnectedAt.IsZero() {
			fmt.Fprintln(w, "            never connected since this daemon started")
		} else {
			fmt.Fprintf(w, "            last connected %s\n", state.ConnectedAt.Local().Format(time.RFC3339))
		}
	}
	printDaemonStreamStability(w, state, now)
	if state.StreamDetail != "" {
		fmt.Fprintf(w, "  Error:    %s\n", state.StreamDetail)
	}
	fmt.Fprintf(w, "  Record:   %s\n", recordPath)
}

// printDaemonStreamStability reports whether the stream has HELD, not just
// whether it is up right now. An instantaneous sample of a stream that flaps
// every few seconds says "connected" nearly every time it is taken.
func printDaemonStreamStability(w io.Writer, state daemonstate.State, now time.Time) {
	if state.Sessions <= 1 && state.LastDisconnectAt.IsZero() {
		if state.Stream.Established() {
			fmt.Fprintf(w, "  Stability: stable — no reconnects since this daemon started\n")
		}
		return
	}
	verdict := "stable"
	if !state.Stable(now) {
		verdict = "UNSTABLE"
	}
	fmt.Fprintf(w, "  Stability: %s — %d stream sessions since this daemon started",
		verdict, state.Sessions)
	if !state.LastDisconnectAt.IsZero() {
		fmt.Fprintf(w, ", last dropped %s ago", now.Sub(state.LastDisconnectAt).Round(time.Second))
	}
	fmt.Fprintln(w)
	if verdict == "UNSTABLE" {
		fmt.Fprintf(w, "             a stream that dropped within the last %s will lose in-flight\n", daemonstate.StabilityWindow)
		fmt.Fprintln(w, "             tool calls; do not start long work against it")
	}
}

func newDaemonStatusCmd() *cobra.Command {
	var dataDir string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check daemon status",
		Long: `Reports whether a tools daemon process exists, whether its gateway stream is
actually established, and which binary it is running.

Tool execution happens inside the daemon, over that stream — a daemon process
whose stream never came up serves nothing. "Running" therefore means
"connected", not "a PID exists", and this command exits non-zero whenever the
stream is not established.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			recordPath := daemonstate.Path(dataDir)

			state, err := daemonstate.Read(dataDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(out, "No daemon running (no runtime record found)")
					fmt.Fprintf(out, "  Record:   %s\n", recordPath)
					return fmt.Errorf("daemon not running")
				}
				return fmt.Errorf("reading daemon runtime record: %w", err)
			}

			if !daemonProcessAlive(state.PID) {
				fmt.Fprintf(out, "Daemon PID %d is not running (stale runtime record)\n", state.PID)
				fmt.Fprintf(out, "  Record:   %s\n", recordPath)
				return fmt.Errorf("daemon not running")
			}

			now := time.Now().UTC()
			switch {
			case state.Stream == daemonstate.StreamConnected && !state.Stable(now):
				fmt.Fprintln(out, "Daemon's gateway stream is established but FLAPPING — it is not serving reliably")
			case state.Stream == daemonstate.StreamConnected:
				fmt.Fprintln(out, "Daemon is running and its gateway stream is established")
			case state.Stream == daemonstate.StreamUnknown:
				fmt.Fprintln(out, "Daemon process exists but its gateway stream state is unknown")
			case state.Stream == daemonstate.StreamAwaitingCredentials:
				fmt.Fprintln(out, "Daemon is running but has no usable credentials — waiting for sign-in")
			default:
				fmt.Fprintf(out, "Daemon process exists but its gateway stream is NOT established (%s)\n", state.Stream)
			}
			printDaemonRuntimeRecord(out, state, recordPath)

			if state.Stream == daemonstate.StreamUnknown {
				return fmt.Errorf("daemon gateway stream state is unknown — it may be serving nothing")
			}
			if !state.Stream.Established() {
				return fmt.Errorf("daemon gateway stream is not established (%s) — it can serve no tool calls", state.Stream)
			}
			// Established is not the same as usable. A stream that dropped
			// moments ago will drop again mid-run and take every in-flight tool
			// call with it, so exit non-zero: this command is used as a
			// pre-flight, and a pre-flight that passes on a flapping stream is
			// worse than no pre-flight.
			if !state.Stable(now) {
				return fmt.Errorf(
					"daemon gateway stream is flapping — %d sessions, last dropped %s ago (needs %s unbroken)",
					state.Sessions, now.Sub(state.LastDisconnectAt).Round(time.Second), daemonstate.StabilityWindow)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("DAEMON_DATA_DIR", "./data"), "Data directory")

	return cmd
}

// Stop timings. The daemon force-exits itself at daemonShutdownGrace, so a
// daemon running current code always beats daemonStopGrace and `stop` never
// needs to escalate; escalation exists for a daemon so wedged that even its own
// watchdog cannot run, and for older binaries that have none.
const (
	daemonStopGrace = 12 * time.Second
	daemonKillGrace = 5 * time.Second
	daemonStopPoll  = 50 * time.Millisecond
)

// processControl is the seam over signalling and liveness. It exists so the
// stop path can be tested against a process that survives SIGKILL — the one
// case where `stop` must refuse to claim success, and a case no real process
// can be made to reproduce.
type processControl struct {
	alive  func(pid int) bool
	signal func(pid int, sig syscall.Signal) error
}

func realProcessControl() processControl {
	return processControl{
		alive: daemonProcessAlive,
		signal: func(pid int, sig syscall.Signal) error {
			process, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return process.Signal(sig)
		},
	}
}

// awaitExit polls until pid is gone or timeout elapses, reporting whether it
// went. Polling with signal 0 is the ONLY way to observe a non-child process
// exiting: os.Process.Wait works on children, and against anything else it
// returns ECHILD in about 20 microseconds. `stop` used to wait on it in a
// goroutine and discard the error, so the "wait up to 10 seconds" it advertised
// was a 20-microsecond no-op, the escalation branch behind it was unreachable,
// and "Daemon stopped" was printed over a daemon that was still running.
func awaitExit(pc processControl, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !pc.alive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(daemonStopPoll)
	}
}

// stopDaemonProcess signals the daemon and does not return success until the
// process is gone.
//
// The runtime record is cleared only once the process is confirmed dead. A
// missing record over a live process is strictly worse than a stale one: it
// makes the daemon invisible to `daemon status` (which reports "no daemon
// running") and to `daemon start` (which then starts a second one against the
// same gateway identity, and the pair evict each other indefinitely).
func stopDaemonProcess(w io.Writer, pc processControl, dataDir string, pid int, force bool) error {
	if pid <= 0 || !pc.alive(pid) {
		fmt.Fprintf(w, "Daemon PID %d is not running (stale runtime record)\n", pid)
		return clearDaemonRecord(w, dataDir)
	}

	sig, verb, grace := syscall.SIGTERM, "Stopping", daemonStopGrace
	if force {
		sig, verb, grace = syscall.SIGKILL, "Force killing", daemonKillGrace
	}

	fmt.Fprintf(w, "%s daemon (PID %d)...\n", verb, pid)
	if err := pc.signal(pid, sig); err != nil {
		if !pc.alive(pid) {
			fmt.Fprintf(w, "Daemon PID %d had already exited\n", pid)
			return clearDaemonRecord(w, dataDir)
		}
		return fmt.Errorf("signalling daemon PID %d with %v: %w", pid, sig, err)
	}

	if awaitExit(pc, pid, grace) {
		fmt.Fprintf(w, "Daemon stopped (PID %d exited)\n", pid)
		return clearDaemonRecord(w, dataDir)
	}

	if !force {
		fmt.Fprintf(w, "Daemon PID %d did not exit within %s — escalating to SIGKILL\n", pid, grace)
		if err := pc.signal(pid, syscall.SIGKILL); err != nil && pc.alive(pid) {
			return fmt.Errorf("daemon PID %d ignored SIGTERM and could not be killed: %w", pid, err)
		}
		if awaitExit(pc, pid, daemonKillGrace) {
			fmt.Fprintf(w, "Daemon killed (PID %d did not exit on SIGTERM)\n", pid)
			return clearDaemonRecord(w, dataDir)
		}
	}

	// Nothing was cleared. Say what is still true so the next command can act
	// on it rather than rediscovering it.
	fmt.Fprintf(w, "Daemon PID %d is STILL RUNNING after SIGKILL\n", pid)
	fmt.Fprintf(w, "  Record:   %s (kept — a live daemon must stay visible)\n", daemonstate.Path(dataDir))
	fmt.Fprintf(w, "  It still holds its gateway registration. Starting another daemon now\n")
	fmt.Fprintf(w, "  would make the two evict each other. Investigate PID %d first.\n", pid)
	return fmt.Errorf("daemon PID %d is still running after SIGTERM and SIGKILL", pid)
}

// clearDaemonRecord removes the runtime record of a process confirmed dead.
func clearDaemonRecord(w io.Writer, dataDir string) error {
	if err := daemonstate.Clear(dataDir); err != nil {
		fmt.Fprintf(w, "Warning: could not remove the runtime record: %v\n", err)
		return fmt.Errorf("removing daemon runtime record: %w", err)
	}
	return nil
}

func newDaemonStopCmd() *cobra.Command {
	var (
		force   bool
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the tools daemon",
		Long: `Sends a graceful shutdown signal to the running tools daemon and waits for it
to actually exit. Use --force to SIGKILL immediately.

If the daemon does not exit within the grace period this escalates to SIGKILL,
and if it survives that, the command exits non-zero and leaves the runtime
record in place. A daemon reported as stopped while it is still running keeps
its gateway registration, and the next 'daemon start' then registers a second
daemon under the same identity — the two evict each other until one is killed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := daemonstate.Read(dataDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "No daemon running (no runtime record found)")
					return nil
				}
				return fmt.Errorf("reading daemon runtime record: %w", err)
			}
			return stopDaemonProcess(cmd.OutOrStdout(), realProcessControl(), dataDir, state.PID, force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force kill (SIGKILL) instead of graceful shutdown")
	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("DAEMON_DATA_DIR", "./data"), "Data directory")

	return cmd
}

func newDaemonLogsCmd() *cobra.Command {
	var (
		follow  bool
		lines   int
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail daemon logs",
		Long:  `Streams daemon log output. Defaults to the last 50 lines with live follow.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logPath := toolsDaemonLogPath(dataDir)

			f, err := os.Open(logPath)
			if err != nil {
				return fmt.Errorf("opening log file %s: %w", logPath, err)
			}
			defer f.Close()

			// Read the last N lines
			tailLines, err := readLastLines(f, lines)
			if err != nil {
				return fmt.Errorf("reading log file: %w", err)
			}
			for _, line := range tailLines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}

			if !follow {
				return nil
			}

			// Follow mode: poll for new content
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			scanner := bufio.NewScanner(f)
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					for scanner.Scan() {
						fmt.Fprintln(cmd.OutOrStdout(), scanner.Text())
					}
				}
			}
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "Follow log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show")
	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("DAEMON_DATA_DIR", "./data"), "Data directory containing logs")

	return cmd
}

// readLastLines reads the last n lines from a file.
func readLastLines(f *os.File, n int) ([]string, error) {
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := stat.Size()
	if size == 0 {
		return nil, nil
	}

	// Read the whole file (or last 1MB if huge) and split into lines
	readSize := size
	const maxRead = 1 << 20 // 1MB
	if readSize > maxRead {
		if _, err := f.Seek(-maxRead, 2); err != nil {
			return nil, err
		}
		readSize = maxRead
	}

	buf := make([]byte, readSize)
	nRead, err := f.Read(buf)
	if err != nil {
		return nil, err
	}
	buf = buf[:nRead]

	allLines := strings.Split(string(buf), "\n")
	// Remove trailing empty line from final newline
	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}

	if len(allLines) <= n {
		return allLines, nil
	}
	return allLines[len(allLines)-n:], nil
}
