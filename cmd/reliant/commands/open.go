// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

func newOpenCmd() *cobra.Command {
	var (
		noBrowser  bool
		noDaemon   bool
		background bool
	)

	cmd := &cobra.Command{
		Use:   "open [path]",
		Short: "Open a project in Reliant",
		Long: `Opens a project in the Reliant cloud platform. This is the primary command
for starting a Reliant session:

  1. Detects the project from the given path (defaults to current directory)
  2. Ensures you are authenticated (runs login flow if needed)
  3. Registers the project with the cloud API
  4. Starts the tools daemon for local tool execution
  5. Opens the Reliant web UI in your browser

This is the "reliant ." command.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// 1. Resolve path
			projectPath := "."
			if len(args) > 0 {
				projectPath = args[0]
			}
			absPath, err := filepath.Abs(projectPath)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}
			projectPath = absPath

			// 2. Detect / create .reliant/ directory
			reliantDir := filepath.Join(projectPath, ".reliant")
			if _, err := os.Stat(reliantDir); os.IsNotExist(err) {
				fmt.Printf("No .reliant/ directory found in %s — creating one.\n", projectPath)
				if err := os.MkdirAll(reliantDir, 0755); err != nil {
					return fmt.Errorf("creating .reliant directory: %w", err)
				}
				// Write minimal config
				configPath := filepath.Join(reliantDir, "config.yaml")
				if err := os.WriteFile(configPath, []byte("# Reliant project configuration\n"), 0644); err != nil {
					return fmt.Errorf("writing default config: %w", err)
				}
			}

			// 3. Check auth
			var accessToken string
			var userID string

			accessToken, err = auth.ReadAccessTokenFromAuthFile()
			if err != nil {
				return fmt.Errorf("reading auth file: %w", err)
			}
			userID, err = auth.ReadUserIDFromAuthFile()
			if err != nil {
				return fmt.Errorf("reading user ID from auth file: %w", err)
			}

			if (accessToken == "" || userID == "") && !noDaemon {
				fmt.Println("Not logged in. Starting login flow...")
				result, loginErr := auth.Login(ctx, auth.LoginOptions{})
				if loginErr != nil {
					return fmt.Errorf("login failed: %w", loginErr)
				}
				if err := auth.WriteAuthSession(result.AccessToken, result.RefreshToken, result.UserID, result.Email); err != nil {
					return fmt.Errorf("saving auth session: %w", err)
				}
				accessToken = result.AccessToken
				userID = result.UserID
				fmt.Printf("Logged in as %s\n", result.Email)
			}

			// 4. Best-effort project registration with cloud API
			var projectID string
			if accessToken != "" {
				projectID = registerProject(ctx, serverURL, accessToken, projectPath)
			}

			// 5. Start daemon (unless --no-daemon)
			daemonCtx, daemonCancel := context.WithCancel(ctx)
			defer daemonCancel()

			if !noDaemon {
				if accessToken == "" || userID == "" {
					fmt.Println("Skipping daemon: not authenticated.")
				} else {
					dataDir := filepath.Join(projectPath, ".reliant", "data")
					if err := os.MkdirAll(dataDir, 0755); err != nil {
						return fmt.Errorf("creating data directory: %w", err)
					}

					grpcURL := serverURL
					tlsMode := bootstrap.TLSModeTLS
					if strings.HasPrefix(grpcURL, "http://") {
						tlsMode = bootstrap.TLSModeH2C
					}

					bootCfg := bootstrap.DaemonBootstrapConfig{
						UserID:      userID,
						AuthToken:   accessToken,
						GRPCURL:     grpcURL,
						TLSMode:     tlsMode,
						ProjectRoot: projectPath,
						DataDir:     dataDir,
					}

					fmt.Println("Starting tools daemon...")
					go func() {
						if err := daemonruntime.Start(daemonCtx, daemonruntime.StartOptions{
							BootstrapConfig: bootCfg,
							WorkingDir:      projectPath,
						}); err != nil && daemonCtx.Err() == nil {
							fmt.Fprintf(os.Stderr, "Daemon error: %v\n", err)
						}
					}()
				}
			}

			// 6. Open browser (unless --no-browser)
			if !noBrowser {
				browserURL := serverURL
				if projectID != "" {
					browserURL = serverURL + "/p/" + projectID
				}
				fmt.Printf("Opening %s in browser...\n", browserURL)
				if err := openBrowserURL(browserURL); err != nil {
					fmt.Fprintf(os.Stderr, "Could not open browser: %v\nPlease visit: %s\n", err, browserURL)
				}
			}

			// 7. Block on signal (unless --background)
			if background {
				fmt.Println("Daemon started in background.")
				return nil
			}

			fmt.Println("Reliant is running. Press Ctrl+C to stop.")
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh

			fmt.Println("\nShutting down...")
			return nil
		},
	}

	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Skip opening the browser")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "Skip starting the tools daemon")
	cmd.Flags().BoolVar(&background, "background", false, "Start daemon in background and return to shell")

	return cmd
}

// registerProject does a best-effort POST to register the project with the cloud API.
// Returns the project_id on success, or empty string on failure.
func registerProject(ctx context.Context, srvURL, accessToken, projectPath string) string {
	projectName := filepath.Base(projectPath)
	reqBody, err := json.Marshal(map[string]string{"name": projectName, "path": projectPath})
	if err != nil {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, "POST", srvURL+"/api/v1/projects", bytes.NewReader(reqBody))
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return ""
	}

	var result struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	return result.ProjectID
}

// openBrowserURL opens the given URL in the user's default browser.
func openBrowserURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}
