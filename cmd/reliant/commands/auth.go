// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
		Long: `Authenticate with the Reliant cloud platform. Credentials are stored in
the platform-specific auth file and shared with the tools daemon and
desktop app.`,
	}

	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	cmd.AddCommand(newAuthServeCmd())

	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Reliant",
		Long: `Authenticates with the Reliant cloud platform using OAuth 2.0 PKCE flow.

Opens your browser for authentication and starts a temporary local HTTP
server to receive the OAuth callback. Credentials are stored in the
platform auth file for use by the daemon and desktop app.

Supported providers: google, github, apple

  macOS:   ~/Library/Application Support/reliant/auth/reliant-auth.json
  Linux:   ~/.config/reliant/auth/reliant-auth.json
  Windows: %APPDATA%\reliant\auth\reliant-auth.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := auth.Login(cmd.Context(), auth.LoginOptions{})
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			if err := auth.WriteAuthSession(result.AccessToken, result.RefreshToken, result.UserID, result.Email); err != nil {
				return fmt.Errorf("saving credentials: %w", err)
			}

			authPath, _ := auth.CurrentAuthFilePath()
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s\nCredentials written to %s\n", result.Email, authPath)
			return nil
		},
	}

	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current authentication status",
		Long:  `Displays the currently authenticated user, token expiry, and server URL.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			authPath, _ := auth.CurrentAuthFilePath()

			accessToken, err := auth.ReadAccessTokenFromAuthFile()
			if err != nil {
				return fmt.Errorf("reading auth file: %w", err)
			}
			if accessToken == "" {
				if jsonOutput {
					fmt.Fprintln(cmd.OutOrStdout(), `{"logged_in": false}`)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "Not logged in")
					fmt.Fprintf(cmd.OutOrStdout(), "Run 'reliant auth login' to authenticate\n")
				}
				return fmt.Errorf("not authenticated")
			}

			userID, _ := auth.ReadUserIDFromAuthFile()

			// Decode JWT claims (without verification — we just want display info)
			email, expiry, err := decodeJWTClaims(accessToken)
			if err != nil {
				// Token exists but can't decode — still show what we have
				email = "(unknown)"
			}

			if jsonOutput {
				info := map[string]interface{}{
					"logged_in": true,
					"user_id":   userID,
					"email":     email,
					"auth_file": authPath,
					"server":    serverURL,
				}
				if !expiry.IsZero() {
					info["expires_at"] = expiry.Format(time.RFC3339)
					info["expired"] = time.Now().After(expiry)
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s\n", email)
			fmt.Fprintf(cmd.OutOrStdout(), "  User ID:   %s\n", userID)
			if !expiry.IsZero() {
				until := time.Until(expiry)
				if until > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  Token:     expires in %s\n", until.Truncate(time.Second))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  Token:     expired %s ago\n", (-until).Truncate(time.Second))
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Auth file: %s\n", authPath)
			fmt.Fprintf(cmd.OutOrStdout(), "  Server:    %s\n", serverURL)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

// decodeJWTClaims extracts email and expiry from a JWT without verification.
func decodeJWTClaims(token string) (email string, expiry time.Time, err error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return "", time.Time{}, fmt.Errorf("invalid JWT format")
	}

	// Base64url decode the payload (part 1)
	payload := parts[1]
	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("decoding JWT payload: %w", err)
	}

	var claims struct {
		Email string  `json:"email"`
		Exp   float64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return "", time.Time{}, fmt.Errorf("parsing JWT claims: %w", err)
	}

	if claims.Exp > 0 {
		expiry = time.Unix(int64(claims.Exp), 0)
	}
	return claims.Email, expiry, nil
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out of Reliant",
		Long:  `Removes stored credentials from the auth file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			authPath, err := auth.CurrentAuthFilePath()
			if err != nil {
				return fmt.Errorf("finding auth file: %w", err)
			}

			if err := os.Remove(authPath); err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "Already logged out (no auth file found)")
					return nil
				}
				return fmt.Errorf("removing auth file: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Logged out")
			fmt.Fprintln(cmd.OutOrStdout(), "Note: any running daemon may still have cached credentials until restarted")
			return nil
		},
	}
}
