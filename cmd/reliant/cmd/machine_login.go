// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/reliant-labs/reliant/internal/vendorcli"
	"github.com/reliant-labs/reliant/pkg/config"
)

// `reliant machine login <vendor>` signs a vendor agent CLI (Claude Code,
// Codex, Gemini CLI, Cursor Agent, GitHub Copilot CLI) in on one of the
// user's machines, so the workflows that run it there (`requires: {cli_login:
// [...]}`) run signed in. Reliant never sees the vendor's credential: it runs
// the vendor's own, unmodified CLI, which writes its credential to its own
// file under $HOME on that machine, and reads back only a verdict.
//
// On this computer's own daemon it runs vendorcli's recipe right here, on this
// terminal (same user, same $HOME), with no server involved. On another
// machine it is a terminal session there (TerminalCLILoginWSPath) running the
// recipe that machine's daemon holds: the server sends only the vendor's id.
//
// Signing in is a person's act, so an agent's shell is refused it; --status,
// which reads only verdicts, is allowed.

var errAgentCannotSignInCLIs = errors.New("an agent cannot sign a vendor CLI in from its shell: signing in is the user's act. Ask the user to run 'reliant machine login <vendor>' in their own terminal ('reliant machine login --status' shows what is signed in)")

// machineLoginState is a vendor CLI's state as this command prints it, and
// its --json value.
type machineLoginState string

const (
	loginSignedIn     machineLoginState = "signed_in"
	loginSignedOut    machineLoginState = "signed_out"
	loginExpired      machineLoginState = "expired"
	loginNotInstalled machineLoginState = "not_installed"
	loginUnknown      machineLoginState = "unknown"
)

func (s machineLoginState) words() string {
	switch s {
	case loginSignedIn:
		return "signed in"
	case loginSignedOut:
		return "signed out"
	case loginExpired:
		return "sign-in expired"
	case loginNotInstalled:
		return "not installed"
	}
	return "unknown"
}

func stateFromVendorCLI(s vendorcli.State) machineLoginState {
	switch s {
	case vendorcli.StateSignedIn:
		return loginSignedIn
	case vendorcli.StateSignedOut:
		return loginSignedOut
	case vendorcli.StateExpired:
		return loginExpired
	case vendorcli.StateMissing:
		return loginNotInstalled
	}
	return loginUnknown
}

func stateFromProto(s reliantv1.CLILoginState) machineLoginState {
	switch s {
	case reliantv1.CLILoginState_CLI_LOGIN_STATE_SIGNED_IN:
		return loginSignedIn
	case reliantv1.CLILoginState_CLI_LOGIN_STATE_SIGNED_OUT:
		return loginSignedOut
	case reliantv1.CLILoginState_CLI_LOGIN_STATE_EXPIRED:
		return loginExpired
	case reliantv1.CLILoginState_CLI_LOGIN_STATE_NOT_INSTALLED:
		return loginNotInstalled
	}
	return loginUnknown
}

// vendorLogin is one row of a status report.
type vendorLogin struct {
	Vendor string            `json:"vendor"`
	Name   string            `json:"name"`
	State  machineLoginState `json:"state"`
	// OffPath: found only in ~/.local/bin, which is not on the PATH the
	// check ran with (vendorcli.Detail).
	OffPath bool `json:"off_path,omitempty"`
}

// loginReport is `machine login --status --json`.
type loginReport struct {
	Machine loginReportMachine `json:"machine"`
	Logins  []vendorLogin      `json:"logins"`
}

type loginReportMachine struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// loginTarget is the machine a login or status is for.
type loginTarget struct {
	id   string
	name string
	// local: the machine is this computer's own daemon, so the recipe runs
	// here on this terminal.
	local bool
	// info is the registry's record, when it could be read.
	info *reliantv1.DaemonInfo
}

// loginMachineArg is how messages and --machine hints name a machine, as
// daemonplacement.MachineArg does: the name its owner gave it, else its id.
func loginMachineArg(d *reliantv1.DaemonInfo) string {
	if name := strings.TrimSpace(d.GetName()); name != "" && name != d.GetDaemonId() {
		return name
	}
	return ""
}

func (t loginTarget) display() string {
	if t.name != "" {
		return t.name
	}
	return t.id
}

// localScripts runs vendorcli's scripts on this computer. A variable so tests
// can fake the shell.
var localScripts scriptRunner = shScripts{}

// scriptRunner runs a POSIX sh script.
type scriptRunner interface {
	// Output runs script and returns its stdout.
	Output(ctx context.Context, script string) (string, error)
	// Interactive runs script on the given terminal streams and returns its
	// exit code.
	Interactive(ctx context.Context, script string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

type shScripts struct{}

func (shScripts) Output(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", script).Output()
	return string(out), err
}

func (shScripts) Interactive(ctx context.Context, script string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	c := exec.CommandContext(ctx, "sh", "-c", script)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// stdinIsTerminal reports whether the person is at a terminal. A variable for
// tests.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func newMachineLoginCmd() *cobra.Command {
	var (
		machine string
		status  bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "login <vendor> | --status [vendor...]",
		Short: "Sign a vendor agent CLI (Claude Code, Codex, Gemini, Cursor, Copilot) in on a machine",
		Long: `Sign a vendor's agent CLI in on one of your machines, so the workflows that
run it there (requires: {cli_login: [...]}) run signed in. The vendor is one
of: ` + strings.Join(vendorcli.IDs(), ", ") + `.

It installs the CLI under the machine's home directory when it is missing,
then runs the vendor's own sign-in: open the URL it prints (or enter its
code), finish signing in, and come back. Reliant never sees the credential:
the vendor's CLI writes it to its own file on that machine, and Reliant
reads back only whether it is signed in.

The machine is this computer's own Reliant machine unless --machine names
another (an id or name; 'reliant machine list' shows yours). On this
computer the sign-in runs right here; on another machine it runs there,
through Reliant, in this terminal.

--status reports each vendor's CLI on the machine as signed in, signed out,
sign-in expired or not installed (--json for a script), and changes nothing.

A machine whose disk is ephemeral forgets the sign-in when it stops. On a
machine other people can use, your personal sign-in is usable by everyone
with a shell there: for shared or team use, use your organization's API keys.

  reliant machine login claude
  reliant machine login codex --machine build-box
  reliant machine login --status --machine build-box`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if !status && inAgentShell() {
				return errAgentCannotSignInCLIs
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && !status {
				return errors.New("--json goes with --status")
			}
			vendors, err := parseLoginVendors(args, status)
			if err != nil {
				return err
			}
			if !status && !stdinIsTerminal() {
				return errors.New("signing in is interactive: run 'reliant machine login' in a terminal")
			}
			target, err := resolveLoginTarget(cmd, machine)
			if err != nil {
				return err
			}
			if status {
				logins, err := checkLogins(cmd, target, vendors)
				if err != nil {
					return err
				}
				return printLogins(cmd.OutOrStdout(), target, logins, asJSON)
			}
			return runMachineLogin(cmd, target, vendors[0])
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "The machine to sign in on, by id or name (default: this computer's)")
	cmd.Flags().BoolVar(&status, "status", false, "Report which vendor CLIs are signed in on the machine, and change nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "With --status: print the report as JSON")
	// --data-dir, --account, --workspace: which local daemon instance is this
	// computer's (proto/reliant/config/v1 MachineLoginConfig).
	config.RegisterMachineLoginConfigFlags(cmd)
	return cmd
}

// parseLoginVendors checks the vendor arguments: exactly one to sign in, any
// number (none: all) for --status.
func parseLoginVendors(args []string, status bool) ([]vendorcli.Vendor, error) {
	valid := strings.Join(vendorcli.IDs(), ", ")
	if !status && len(args) != 1 {
		return nil, fmt.Errorf("name one vendor CLI to sign in: one of %s", valid)
	}
	if len(args) == 0 {
		return vendorcli.All(), nil
	}
	seen := map[string]bool{}
	var out []vendorcli.Vendor
	for _, a := range args {
		v, ok := vendorcli.Lookup(strings.ToLower(strings.TrimSpace(a)))
		if !ok {
			return nil, fmt.Errorf("unknown vendor CLI %q: want one of %s", a, valid)
		}
		if !seen[v.ID] {
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// localDaemons is the daemon ids of this computer: the instance the command's
// config names (an explicit --data-dir / DAEMON_DATA_DIR, as a cloud machine's
// pod sets), and every instance under ~/.reliant/instances for this server.
//
// preferred is the one a bare `machine login` means: the explicitly named
// instance's, else the one running daemon's, else the one daemon there is.
// It is "" when that is ambiguous or there is none; all is every id here, for
// telling whether a --machine is this computer.
func localDaemons(cmd *cobra.Command) (preferred string, all map[string]bool, candidates []string, err error) {
	cfg, err := config.LoadMachineLoginConfig(cmd)
	if err != nil {
		return "", nil, nil, err
	}
	conn, err := resolveServer(cmd)
	if err != nil {
		return "", nil, nil, err
	}
	all = map[string]bool{}
	inst := daemonInstance(cfg.GetInstance())
	explicit := ""
	if strings.TrimSpace(inst.dataDir) != "" {
		explicit = bootstrap.ReadDaemonID(inst.dataDir)
	} else if dir, err := resolveDaemonDataDir(inst, conn.ServerURL); err == nil {
		if id := bootstrap.ReadDaemonID(dir); id != "" {
			all[id] = true
		}
	}
	if explicit != "" {
		all[explicit] = true
	}
	var running, idle []string
	origin := ""
	if key, err := daemoninstance.Resolve(conn.ServerURL, "", "x"); err == nil {
		origin = key.OriginSlug() + "/"
	}
	instances, _ := daemonstate.ListDefault()
	for _, i := range instances {
		if origin != "" && !strings.HasPrefix(i.Slug, origin) {
			continue
		}
		id := bootstrap.ReadDaemonID(i.DataDir)
		if id == "" {
			continue
		}
		all[id] = true
		if i.Locked {
			running = appendUnique(running, id)
		} else {
			idle = appendUnique(idle, id)
		}
	}
	switch {
	case explicit != "":
		return explicit, all, []string{explicit}, nil
	case len(running) == 1:
		return running[0], all, running, nil
	case len(running) == 0 && len(idle) == 1:
		return idle[0], all, idle, nil
	}
	candidates = append(running, idle...)
	sort.Strings(candidates)
	return "", all, candidates, nil
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// resolveLoginTarget picks the machine: --machine through the registry (by id
// or name), else this computer's own daemon.
func resolveLoginTarget(cmd *cobra.Command, machine string) (loginTarget, error) {
	preferred, local, candidates, err := localDaemons(cmd)
	if err != nil {
		return loginTarget{}, err
	}
	if ref := strings.TrimSpace(machine); ref != "" {
		client, err := registryClient(cmd)
		if err != nil {
			return loginTarget{}, err
		}
		d, err := resolveRegistryMachine(cmd.Context(), client, ref)
		if err != nil {
			return loginTarget{}, err
		}
		return loginTarget{id: d.GetDaemonId(), name: loginMachineArg(d), local: local[d.GetDaemonId()], info: d}, nil
	}
	if preferred == "" {
		if len(candidates) > 1 {
			return loginTarget{}, fmt.Errorf("this computer has several Reliant machines (%s): name one with --machine <name|id>", strings.Join(candidates, ", "))
		}
		return loginTarget{}, errors.New("no Reliant machine runs on this computer: name the machine with --machine <name|id> ('reliant machine list' shows yours)")
	}
	target := loginTarget{id: preferred, local: true}
	// The name and the machine's record are for the messages only: a local
	// sign-in needs no server, so a registry that cannot be read is no error.
	if client, err := registryClient(cmd); err == nil {
		if d, err := resolveRegistryMachine(cmd.Context(), client, preferred); err == nil {
			target.name, target.info = loginMachineArg(d), d
		}
	}
	return target, nil
}

// checkLogins reports each vendor's state on the target: by running the
// status script here for this computer, else through the API.
func checkLogins(cmd *cobra.Command, target loginTarget, vendors []vendorcli.Vendor) ([]vendorLogin, error) {
	ids := make([]string, 0, len(vendors))
	for _, v := range vendors {
		ids = append(ids, v.ID)
	}
	if target.local {
		out, err := localScripts.Output(cmd.Context(), vendorcli.StatusScript(ids))
		if err != nil {
			return nil, fmt.Errorf("checking the vendor CLIs on this computer: %w", err)
		}
		details := vendorcli.ParseStatusDetail(out)
		logins := make([]vendorLogin, 0, len(vendors))
		for _, v := range vendors {
			d := details[v.ID]
			logins = append(logins, vendorLogin{Vendor: v.ID, Name: v.Name, State: stateFromVendorCLI(d.State), OffPath: d.OffPath})
		}
		return logins, nil
	}
	client, err := registryClient(cmd)
	if err != nil {
		return nil, err
	}
	resp, err := client.CheckMachineCLILogins(cmd.Context(), connect.NewRequest(&reliantv1.CheckMachineCLILoginsRequest{
		DaemonId: target.id, Vendors: ids,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return nil, fmt.Errorf("this Reliant server predates 'reliant machine login --status' for another machine: %w", err)
		}
		return nil, explainRegistryErr(err)
	}
	logins := make([]vendorLogin, 0, len(resp.Msg.GetLogins()))
	for _, l := range resp.Msg.GetLogins() {
		logins = append(logins, vendorLogin{Vendor: l.GetVendor(), Name: l.GetName(), State: stateFromProto(l.GetState()), OffPath: l.GetOffPath()})
	}
	return logins, nil
}

func printLogins(out io.Writer, target loginTarget, logins []vendorLogin, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(loginReport{Machine: loginReportMachine{ID: target.id, Name: target.name}, Logins: logins})
	}
	fmt.Fprintf(out, "Agent CLIs on %s:\n", target.display())
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, l := range logins {
		fmt.Fprintf(w, "  %s (%s)\t%s\n", l.Name, l.Vendor, l.State.words())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, l := range logins {
		if !l.OffPath {
			continue
		}
		where := "your PATH: add ~/.local/bin to it"
		if !target.local {
			where = "the PATH the machine's daemon gives run steps: update Reliant on " + target.display()
		}
		fmt.Fprintf(out, "  hint: %s is installed in ~/.local/bin, which is not on %s\n", l.Name, where)
	}
	return nil
}

// runMachineLogin runs the vendor's sign-in on the target, then checks and
// prints the verdict. It fails unless the vendor's CLI ends up signed in.
func runMachineLogin(cmd *cobra.Command, target loginTarget, vendor vendorcli.Vendor) error {
	out := cmd.ErrOrStderr()
	for _, warning := range loginWarnings(cmd, target) {
		fmt.Fprintf(out, "warning: %s\n", warning)
	}
	fmt.Fprintf(out, "Signing %s in on %s. Follow the vendor's prompts below.\n\n", vendor.Name, target.display())
	if target.local {
		script, err := vendorcli.LoginScript(vendor.ID)
		if err != nil {
			return err
		}
		code, err := localScripts.Interactive(cmd.Context(), script, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		if err != nil {
			return fmt.Errorf("running the %s sign-in: %w", vendor.Name, err)
		}
		if code != 0 {
			fmt.Fprintf(out, "\nThe %s sign-in exited with code %d.\n", vendor.Name, code)
		}
	} else if err := runRemoteCLILogin(cmd, target, vendor); err != nil {
		return err
	}
	logins, err := checkLogins(cmd, target, []vendorcli.Vendor{vendor})
	if err != nil {
		return fmt.Errorf("checking the sign-in: %w", err)
	}
	fmt.Fprintln(out)
	if err := printLogins(out, target, logins, false); err != nil {
		return err
	}
	if len(logins) != 1 || logins[0].State != loginSignedIn {
		state := loginUnknown
		if len(logins) == 1 {
			state = logins[0].State
		}
		return fmt.Errorf("%s is %s on %s", vendor.Name, state.words(), target.display())
	}
	return nil
}

// loginWarnings says what a sign-in on target will not do: outlive an
// ephemeral disk, or stay personal on a machine other people can use. The
// machine's record is read best-effort: a self-hosted server has no control
// plane, and no ephemeral or organization machines either.
func loginWarnings(cmd *cobra.Command, target loginTarget) []string {
	var warnings []string
	if pools := target.info.GetPools(); len(pools) > 0 {
		warnings = append(warnings, fmt.Sprintf("%s is in machine pool %s: every run the pool places on it uses this sign-in",
			target.display(), strings.Join(pools, ", ")))
	}
	client, err := machineClient(cmd.Context(), cmd)
	if err != nil {
		return warnings
	}
	d, err := resolveMachine(cmd.Context(), client, target.id)
	if err != nil {
		return warnings
	}
	if d.GetDiskMode() == controlplanev1.DaemonDiskMode_DAEMON_DISK_MODE_EPHEMERAL {
		warnings = append(warnings, fmt.Sprintf("%s has an ephemeral disk: the sign-in lasts only until it stops", target.display()))
	}
	if d.GetOwnerType() == controlplanev1.OwnerType_OWNER_TYPE_ORGANIZATION {
		warnings = append(warnings, fmt.Sprintf("%s is shared through your organization: anyone with a shell on it can use your personal sign-in. For team use, use your organization's API keys", target.display()))
	}
	return warnings
}
