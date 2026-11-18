package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// The user-administration verbs of controlplane.v1.IdentityService. Each maps
// 1:1 to the rpc of the same name; the control plane decides who may call it.
const (
	procIdentitySetRoles       = "controlplane.v1.IdentityService/SetUserRoles"
	procIdentityResetPassword  = "controlplane.v1.IdentityService/SendPasswordReset"
	procIdentityLockUser       = "controlplane.v1.IdentityService/LockUser"
	procIdentityUnlockUser     = "controlplane.v1.IdentityService/UnlockUser"
	procIdentityDeleteUser     = "controlplane.v1.IdentityService/DeleteUser"
	procIdentityRevokeSessions = "controlplane.v1.IdentityService/RevokeSessions"
)

// maxIdentityLookupPages bounds the email → user id search, so a substring
// that matches a whole directory cannot loop forever.
const maxIdentityLookupPages = 10

func looksLikeEmail(s string) bool { return strings.Contains(s, "@") }

// resolveIdentityUser turns --user into a user id. A value without an "@" is
// an id and is used as given. An email is looked up with ListUsers and must
// match exactly one user, ignoring case: the query is a substring search, so
// "a@x.com" also finds "ba@x.com".
func resolveIdentityUser(ctx context.Context, client cloudCaller, base map[string]any, user string) (id, email string, err error) {
	if !looksLikeEmail(user) {
		return user, "", nil
	}
	token := ""
	for page := 0; page < maxIdentityLookupPages; page++ {
		req := map[string]any{"orgId": base["orgId"], "environmentId": base["environmentId"], "query": user, "pageSize": 100, "pageToken": token}
		var doc identityUsersDocument
		if err := client.Call(ctx, procIdentityUsers, req, &doc); err != nil {
			return "", "", fmt.Errorf("look up %s: %w", user, err)
		}
		var found []identityUserWire
		for _, u := range doc.Users {
			if strings.EqualFold(u.Email, user) {
				found = append(found, u)
			}
		}
		switch len(found) {
		case 1:
			return found[0].UserID, found[0].Email, nil
		case 0:
		default:
			return "", "", fmt.Errorf("%d users have the email %q; refusing to guess: pass --user <user id> (forge cloud identity users lists ids)", len(found), user)
		}
		if doc.NextPageToken == "" {
			break
		}
		token = doc.NextPageToken
	}
	return "", "", fmt.Errorf("no user with the email %q in this environment (forge cloud identity users lists them)", user)
}

type identityUserFlags struct {
	cloudIdentityFlags
	user string
}

func (f *identityUserFlags) register(cmd *cobra.Command) {
	f.cloudIdentityFlags.register(cmd)
	cmd.Flags().StringVar(&f.user, "user", "", "The user, by user id or exact email address (required)")
	_ = cmd.MarkFlagRequired("user")
}

func (f *identityUserFlags) validate() error {
	if strings.TrimSpace(f.user) == "" || utf8.RuneCountInString(f.user) > 254 {
		return fmt.Errorf("--user must be a user id or an email address (at most 254 characters)")
	}
	return nil
}

// targetUser resolves the environment and the user: the request body every
// per-user rpc starts from.
func (f *identityUserFlags) targetUser(ctx context.Context, env string) (cloudCaller, map[string]any, string, error) {
	client, req, err := f.target(ctx, env)
	if err != nil {
		return nil, nil, "", err
	}
	id, email, err := resolveIdentityUser(ctx, client, req, f.user)
	if err != nil {
		return nil, nil, "", err
	}
	req["userId"] = id
	label := id
	if email != "" {
		label = email + " (user " + id + ")"
	} else if looksLikeEmail(f.user) {
		label = f.user
	}
	return client, req, label, nil
}

type identityActionDocument struct {
	jsonEnvelope
	UserID          string                    `json:"userId"`
	Action          string                    `json:"action"`
	Roles           []string                  `json:"roles,omitempty"`
	SessionsRevoked int32                     `json:"sessionsRevoked,omitempty"`
	Code            *identityPasswordCodeWire `json:"code,omitempty"`
}

// simpleIdentityVerb builds a per-user verb whose only output is that it
// happened: lock, unlock.
func simpleIdentityVerb(use, short, long, proc, action, done string) *cobra.Command {
	var f identityUserFlags
	cmd := &cobra.Command{
		Use: use + " <env> --user <id|email>", Short: short, Long: long,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.validate(); err != nil {
				return err
			}
			client, req, label, err := f.targetUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var ignored map[string]any
			if err := client.Call(cmd.Context(), proc, req, &ignored); err != nil {
				return fmt.Errorf("identity %s: %w", use, err)
			}
			return writeIdentityAction(cmd, f.json, identityActionDocument{UserID: fmt.Sprint(req["userId"]), Action: action}, fmt.Sprintf("%s %s.\n", done, label))
		},
	}
	f.register(cmd)
	return cmd
}

func writeIdentityAction(cmd *cobra.Command, asJSON bool, doc identityActionDocument, text string) error {
	doc.stamp(nil)
	if asJSON {
		return writeJSONDocument(cmd.OutOrStdout(), doc)
	}
	_, err := io.WriteString(cmd.OutOrStdout(), text)
	return err
}

func newCloudIdentitySetRolesCmd() *cobra.Command {
	var f identityUserFlags
	var roles []string
	var none bool
	cmd := &cobra.Command{
		Use: "set-roles <env> --user <id|email> --role <role>", Short: "Replace a user's console-managed roles (org admin)",
		Long: `Replace the user's console-managed roles with exactly the --role values. Roles
the environment declares in deploy/kcl/identity.k are left as they are, and
naming one is refused. To remove every console-managed role, pass --none.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.validate(); err != nil {
				return err
			}
			if none == (len(roles) > 0) {
				return fmt.Errorf("pass --role (repeatable) to set the roles, or --none to remove every console-managed role")
			}
			if len(roles) > 20 {
				return fmt.Errorf("at most 20 --role flags")
			}
			for _, role := range roles {
				if strings.TrimSpace(role) == "" || utf8.RuneCountInString(role) > 200 {
					return fmt.Errorf("each --role must be nonempty and at most 200 characters")
				}
			}
			client, req, label, err := f.targetUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if roles == nil {
				roles = []string{}
			}
			req["roles"] = roles
			var resp struct {
				User identityUserWire `json:"user"`
			}
			if err := client.Call(cmd.Context(), procIdentitySetRoles, req, &resp); err != nil {
				return fmt.Errorf("set identity user roles: %w", err)
			}
			got := resp.User.Roles
			return writeIdentityAction(cmd, f.json,
				identityActionDocument{UserID: fmt.Sprint(req["userId"]), Action: "set-roles", Roles: got},
				fmt.Sprintf("Roles of %s are now: %s.\n", label, joinRoles(got)))
		},
	}
	f.register(cmd)
	cmd.Flags().StringArrayVar(&roles, "role", nil, "Console-managed role the user should hold (repeat for several)")
	cmd.Flags().BoolVar(&none, "none", false, "Remove every console-managed role")
	return cmd
}

func joinRoles(roles []string) string {
	if len(roles) == 0 {
		return "(none)"
	}
	return strings.Join(roles, ", ")
}

func newCloudIdentityResetCmd() *cobra.Command {
	var f identityUserFlags
	var returnCode bool
	cmd := &cobra.Command{
		Use: "reset <env> --user <id|email>", Short: "Issue a user a one-time password-set code (org admin)",
		Long: `Issue the user a one-time password-set code. It is emailed as a reset link
where the environment declares smtp; with --return-code it is printed here
instead, for you to deliver over a channel you control (it is a credential).
Without smtp ('status' shows "Mail: none") the control plane refuses a reset
without --return-code rather than send nothing. The user's current password
keeps working until the code is redeemed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.validate(); err != nil {
				return err
			}
			client, req, label, err := f.targetUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if returnCode {
				req["returnCode"] = true
			}
			var resp struct {
				Code *identityPasswordCodeWire `json:"code"`
			}
			if err := client.Call(cmd.Context(), procIdentityResetPassword, req, &resp); err != nil {
				return fmt.Errorf("identity reset: %w", err)
			}
			if returnCode && (resp.Code == nil || resp.Code.Code == "") {
				return fmt.Errorf("identity reset: the control plane issued no code for %s", label)
			}
			doc := identityActionDocument{UserID: fmt.Sprint(req["userId"]), Action: "reset", Code: resp.Code}
			doc.stamp(nil)
			if f.json {
				return writeJSONDocument(cmd.OutOrStdout(), doc)
			}
			out := cmd.OutOrStdout()
			if resp.Code == nil {
				_, err := fmt.Fprintf(out, "Password reset link emailed to %s.\n", label)
				return err
			}
			fmt.Fprintf(out, "Password-set code issued for %s.\n", label)
			writePasswordCode(out, resp.Code)
			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&returnCode, "return-code", false, "Print the password-set code instead of emailing it (it is a credential; needed where the environment declares no smtp)")
	return cmd
}

func newCloudIdentityDeleteCmd() *cobra.Command {
	var f identityUserFlags
	var yes bool
	cmd := &cobra.Command{
		Use: "delete <env> --user <id|email>", Short: "Delete a user's account (org admin; asks first)",
		Long: `Remove the account. This cannot be undone. A user who holds a declared role is
refused: the next reconcile would recreate them. Without --yes the command asks
on the terminal and refuses when stdin is not one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.validate(); err != nil {
				return err
			}
			client, req, label, err := f.targetUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := confirmIdentityDelete(cmd.InOrStdin(), cmd.ErrOrStderr(), label, args[0])
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("aborted: %s was not deleted (pass --yes to skip the question)", label)
				}
			}
			var ignored map[string]any
			if err := client.Call(cmd.Context(), procIdentityDeleteUser, req, &ignored); err != nil {
				return fmt.Errorf("delete identity user: %w", err)
			}
			return writeIdentityAction(cmd, f.json, identityActionDocument{UserID: fmt.Sprint(req["userId"]), Action: "delete"}, fmt.Sprintf("Deleted %s.\n", label))
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Delete without asking")
	return cmd
}

// confirmIdentityDelete asks once, defaulting to no. A closed or empty stdin
// is a refusal, never a yes.
func confirmIdentityDelete(in io.Reader, out io.Writer, label, env string) (bool, error) {
	fmt.Fprintf(out, "Delete %s from %s? This cannot be undone. [y/N] ", label, env)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("not deleting %s: no answer on stdin (pass --yes to delete without asking)", label)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func newCloudIdentityRevokeSessionsCmd() *cobra.Command {
	var f identityUserFlags
	cmd := &cobra.Command{
		Use: "revoke-sessions <env> --user <id|email>", Short: "End a user's sign-in sessions (org admin)",
		Long: `End every sign-in session the user holds. Access tokens already issued stay
valid until they expire.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.validate(); err != nil {
				return err
			}
			client, req, label, err := f.targetUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var resp struct {
				SessionsRevoked int32 `json:"sessionsRevoked"`
			}
			if err := client.Call(cmd.Context(), procIdentityRevokeSessions, req, &resp); err != nil {
				return fmt.Errorf("revoke identity sessions: %w", err)
			}
			return writeIdentityAction(cmd, f.json,
				identityActionDocument{UserID: fmt.Sprint(req["userId"]), Action: "revoke-sessions", SessionsRevoked: resp.SessionsRevoked},
				fmt.Sprintf("Revoked %d session(s) of %s.\n", resp.SessionsRevoked, label))
		},
	}
	f.register(cmd)
	return cmd
}

func newCloudIdentityUserVerbs() []*cobra.Command {
	return []*cobra.Command{
		newCloudIdentitySetRolesCmd(),
		newCloudIdentityResetCmd(),
		simpleIdentityVerb("lock", "Block a user from signing in (org admin)",
			"Block sign-in for the user. Reversible with `forge cloud identity unlock`.",
			procIdentityLockUser, "lock", "Locked"),
		simpleIdentityVerb("unlock", "Let a locked user sign in again (org admin)",
			"Undo `forge cloud identity lock`.",
			procIdentityUnlockUser, "unlock", "Unlocked"),
		newCloudIdentityDeleteCmd(),
		newCloudIdentityRevokeSessionsCmd(),
	}
}
