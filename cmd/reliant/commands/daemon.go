// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/spf13/cobra"
)

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
func registerDaemon(cmd *cobra.Command, grpcURL string) error {
	// Step 1: Ensure we have an access token (OAuth login if needed)
	accessToken, err := auth.ReadAccessTokenFromAuthFile()
	if err != nil {
		return fmt.Errorf("reading auth file: %w", err)
	}
	if accessToken == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Not logged in. Starting authentication...")
		opts := auth.LoginOptions{
			ServerURL: firstNonEmpty(os.Getenv("SUPABASE_URL")),
			AnonKey:   os.Getenv("SUPABASE_ANON_KEY"),
		}
		result, err := auth.Login(cmd.Context(), opts)
		if err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
		if err := auth.WriteAuthSession(result.AccessToken, result.RefreshToken, result.UserID, result.Email); err != nil {
			return fmt.Errorf("saving credentials: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s\n", result.Email)
		accessToken = result.AccessToken
	}

	// Step 2: Call CreateDaemonToken RPC
	hostname, _ := os.Hostname()

	httpClient := &http.Client{
		Transport: &bearerAuthTransport{token: accessToken},
	}
	client := reliantv1connect.NewDaemonRegistryServiceClient(httpClient, grpcURL)

	resp, err := client.CreateDaemonToken(cmd.Context(), connect.NewRequest(&reliantv1.CreateDaemonTokenRequest{
		Name: hostname,
	}))
	if err != nil {
		return fmt.Errorf("creating daemon token: %w", err)
	}

	// Step 3: Write daemon credentials
	creds := &auth.DaemonCredentials{
		PAT:          resp.Msg.GetToken(),
		UserID:       resp.Msg.GetUserId(),
		ServerURL:    grpcURL,
		RegisteredAt: time.Now().UTC(),
	}
	if err := auth.WriteDaemonCredentials(creds); err != nil {
		return fmt.Errorf("saving daemon credentials: %w", err)
	}

	credsPath, _ := auth.DaemonCredentialsFilePath()
	fmt.Fprintln(cmd.OutOrStdout(), "Daemon registered successfully")
	fmt.Fprintf(cmd.OutOrStdout(), "  Credentials: %s\n", credsPath)
	fmt.Fprintf(cmd.OutOrStdout(), "  Server:      %s\n", grpcURL)
	return nil
}

// bearerAuthTransport injects a Bearer token into HTTP requests.
type bearerAuthTransport struct {
	token string
}

func (t *bearerAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
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
			// Check if already registered
			creds, err := auth.ReadDaemonCredentials()
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

			return registerDaemon(cmd, serverURL)
		},
	}

	return cmd
}

func newDaemonStartCmd() *cobra.Command {
	var (
		port       string
		grpcURL    string
		dataDir    string
		token      string
		userID     string
		background bool
		tlsCert    string
		tlsKey     string
		tlsMode    string
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
  3. --token / DAEMON_PAT flags (manual override)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if background {
				// TODO: implement background fork/detach
				return fmt.Errorf("--background is not yet implemented")
			}

			logging.Setup(slog.LevelInfo)

			// Credential resolution: daemon creds file > auto-register > manual flags > error
			if token == "" || userID == "" {
				creds, err := auth.ReadDaemonCredentials()
				if err != nil {
					return fmt.Errorf("reading daemon credentials: %w", err)
				}

				if creds != nil {
					// Use daemon credentials file
					if token == "" {
						token = creds.PAT
					}
					if userID == "" {
						userID = creds.UserID
					}
					if grpcURL == "" {
						grpcURL = creds.ServerURL
					}
				} else {
					// No daemon creds — try auto-register if logged in
					accessToken, _ := auth.ReadAccessTokenFromAuthFile()
					if accessToken != "" {
						targetURL := grpcURL
						if targetURL == "" {
							targetURL = serverURL
						}
						fmt.Fprintln(cmd.OutOrStdout(), "No daemon credentials found. Auto-registering...")
						if err := registerDaemon(cmd, targetURL); err != nil {
							return fmt.Errorf("auto-registration failed: %w", err)
						}
						// Re-read the credentials we just wrote
						creds, err = auth.ReadDaemonCredentials()
						if err != nil || creds == nil {
							return fmt.Errorf("failed to read daemon credentials after registration")
						}
						token = creds.PAT
						userID = creds.UserID
						if grpcURL == "" {
							grpcURL = creds.ServerURL
						}
					}
				}
			}

			if token == "" || userID == "" {
				return fmt.Errorf("not registered. Run 'reliant daemon register' to set up this machine")
			}

			// Determine TLS mode: explicit flag/env > cert/key presence > h2c.
			parsedTLSMode := bootstrap.TLSModeH2C

			if grpcURL == "" {
				grpcURL = fmt.Sprintf("http://localhost:%s", port)
			}
			if tlsMode != "" {
				parsedTLSMode = bootstrap.TLSMode(tlsMode)
			} else if tlsCert != "" && tlsKey != "" {
				parsedTLSMode = bootstrap.TLSModeTLS
				if !cmd.Flags().Changed("grpc-url") {
					grpcURL = fmt.Sprintf("https://localhost:%s", port)
				}
			} else if strings.HasPrefix(grpcURL, "https://") {
				parsedTLSMode = bootstrap.TLSModeTLS
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			logging.Info("Starting tools-daemon", "port", port, "tls_mode", string(parsedTLSMode), "data_dir", dataDir)

			// Clean up background processes on shutdown.
			defer shell.GetBackgroundManager().KillAllRunning()
			defer shell.GetProcessMonitor().Stop()

			err := daemonruntime.Start(ctx, daemonruntime.StartOptions{
				BootstrapConfig: bootstrap.DaemonBootstrapConfig{
					UserID:    userID,
					AuthToken: token,
					GRPCURL:   grpcURL,
					TLSMode:   parsedTLSMode,
					DataDir:   dataDir,
				},
			})
			if err != nil {
				return fmt.Errorf("tools-daemon exited with error: %w", err)
			}

			logging.Info("tools-daemon shut down gracefully")
			return nil
		},
	}

	cmd.Flags().StringVar(&port, "port", envOrDefault("TOOLS_DAEMON_PORT", "9190"), "Daemon listen port")
	cmd.Flags().StringVar(&grpcURL, "grpc-url", envOrDefault("DAEMON_GRPC_URL", ""), "gRPC server URL to connect to")
	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("DAEMON_DATA_DIR", "./data"), "Data directory")
	cmd.Flags().StringVar(&token, "token", envOrDefault("DAEMON_PAT", ""), "Personal access token (overrides daemon credentials file)")
	cmd.Flags().StringVar(&userID, "user-id", envOrDefault("DAEMON_AUTH_USER_ID", ""), "User ID (overrides daemon credentials file)")
	cmd.Flags().BoolVar(&background, "background", false, "Run daemon in background (detached)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", envOrDefault("TLS_CERT_FILE", ""), "TLS certificate file path")
	cmd.Flags().StringVar(&tlsKey, "tls-key", envOrDefault("TLS_KEY_FILE", ""), "TLS key file path")
	cmd.Flags().StringVar(&tlsMode, "tls-mode", envOrDefault("DAEMON_TLS_MODE", ""), "TLS mode (tls, insecure_tls_skip_verify, or h2c)")

	return cmd
}

func newDaemonStatusCmd() *cobra.Command {
	var dataDir string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check daemon status",
		Long:  `Shows the current status of the tools daemon: whether it's running, connected, and its uptime.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pidFile := filepath.Join(dataDir, "daemon.pid")
			data, err := os.ReadFile(pidFile)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "No daemon running (no PID file found)")
					fmt.Fprintf(cmd.OutOrStdout(), "  PID file: %s\n", pidFile)
					return fmt.Errorf("daemon not running")
				}
				return fmt.Errorf("reading PID file: %w", err)
			}

			pidStr := strings.TrimSpace(string(data))
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				return fmt.Errorf("invalid PID in %s: %q", pidFile, pidStr)
			}

			// Check if process is alive (signal 0 = check existence)
			process, err := os.FindProcess(pid)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Daemon PID %d not found (stale PID file)\n", pid)
				return fmt.Errorf("daemon not running")
			}

			if err := process.Signal(syscall.Signal(0)); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Daemon PID %d is not running (stale PID file)\n", pid)
				return fmt.Errorf("daemon not running")
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Daemon is running")
			fmt.Fprintf(cmd.OutOrStdout(), "  PID:      %d\n", pid)
			fmt.Fprintf(cmd.OutOrStdout(), "  PID file: %s\n", pidFile)
			return nil
		},
	}

	cmd.Flags().StringVar(&dataDir, "data-dir", envOrDefault("DAEMON_DATA_DIR", "./data"), "Data directory")

	return cmd
}

func newDaemonStopCmd() *cobra.Command {
	var (
		force   bool
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the tools daemon",
		Long:  `Sends a graceful shutdown signal to the running tools daemon. Use --force to kill immediately.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pidFile := filepath.Join(dataDir, "daemon.pid")
			data, err := os.ReadFile(pidFile)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "No daemon running (no PID file found)")
					return nil
				}
				return fmt.Errorf("reading PID file: %w", err)
			}

			pidStr := strings.TrimSpace(string(data))
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				return fmt.Errorf("invalid PID in %s: %q", pidFile, pidStr)
			}

			process, err := os.FindProcess(pid)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Daemon PID %d not found\n", pid)
				_ = os.Remove(pidFile)
				return nil
			}

			if force {
				fmt.Fprintf(cmd.OutOrStdout(), "Force killing daemon (PID %d)...\n", pid)
				if err := process.Signal(syscall.SIGKILL); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Process already stopped\n")
				}
				_ = os.Remove(pidFile)
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon killed")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Stopping daemon (PID %d)...\n", pid)
			if err := process.Signal(syscall.SIGTERM); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Process already stopped\n")
				_ = os.Remove(pidFile)
				return nil
			}

			// Wait for process to exit (up to 10 seconds)
			done := make(chan struct{})
			go func() {
				_, _ = process.Wait()
				close(done)
			}()

			select {
			case <-done:
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon stopped")
			case <-time.After(10 * time.Second):
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon did not stop in 10s, force killing...")
				_ = process.Signal(syscall.SIGKILL)
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon killed")
			}

			_ = os.Remove(pidFile)
			return nil
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
			logPath := filepath.Join(dataDir, "logs", "reliant.log")

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
