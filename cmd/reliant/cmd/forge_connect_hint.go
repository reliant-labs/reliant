// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"os"
	"strings"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// forgeConnectHintEnv is the contract with forge's `env deploy` (forge PR D):
// when set, it is the command a person would run to connect THIS machine to
// Reliant as an agent sandbox, and forge prints it after a successful hosted
// deploy (only at an interactive terminal, never in CI). Unset means "say
// nothing". reliant only ever suggests; it never registers a machine.
const forgeConnectHintEnv = "FORGE_HOST_CONNECT_HINT"

// connectHintCommand is the hint's command for the default server.
const connectHintCommand = "reliant daemon start --attach ."

// connectHintDeps are what forgeConnectHint reads, so tests need no home
// directory, login or running daemon.
type connectHintDeps struct {
	getenv func(string) string
	// lookup is the CLI login for a server (cliauth.Lookup).
	lookup func(server string) (credentials.Credential, string, error)
	// state is this machine's sandbox state (localSandboxState).
	state func(server, account string) sandboxState
	// defaultServer is the server this build targets with nothing set.
	defaultServer string
}

// forgeConnectHint returns the FORGE_HOST_CONNECT_HINT value for forge's
// arguments, or "" when no hint is due: the command is not `env deploy`, the
// hint is already set (the caller's wins), this is an agent's shell (an
// agent must not be told to register the machine it runs on), there is no
// CLI login for the server (nobody to connect as), or a daemon of the
// login's account is already connected here.
func forgeConnectHint(fargs []string, d connectHintDeps) string {
	if !isEnvDeploy(fargs) {
		return ""
	}
	if d.getenv(forgeConnectHintEnv) != "" || d.getenv(daemonpolicy.AgentShellEnv) != "" {
		return ""
	}
	server := strings.TrimSpace(d.getenv(envServerURL))
	if server == "" {
		server = d.defaultServer
	}
	if server == "" {
		return ""
	}
	cred, _, err := d.lookup(server)
	if err != nil {
		return ""
	}
	// An old login names no account: "" is the server's default daemon
	// credential, as ReadDaemonCredentials documents.
	if d.state(server, cliauth.AccountOf(cred).ID) == sandboxConnected {
		return ""
	}
	if strings.TrimRight(server, "/") != strings.TrimRight(d.defaultServer, "/") {
		return connectHintCommand + " --server " + server
	}
	return connectHintCommand
}

// isEnvDeploy reports whether forge's arguments run `env deploy`: the words
// "env" then "deploy" among them, flags before or between ignored.
func isEnvDeploy(fargs []string) bool {
	var words []string
	for _, a := range fargs {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	for i := 0; i+1 < len(words); i++ {
		if words[i] == "env" && words[i+1] == "deploy" {
			return true
		}
	}
	return false
}

// exportForgeConnectHint sets FORGE_HOST_CONNECT_HINT for the forge about to
// run fargs, when one is due. It never fails the command.
func exportForgeConnectHint(fargs []string) {
	defaultServer, _ := buildConnectionDefaults()
	hint := forgeConnectHint(fargs, connectHintDeps{
		getenv: os.Getenv, //nolint:forbidigo // the hint contract and the agent-shell marker are process signals, not configuration
		lookup: func(server string) (credentials.Credential, string, error) {
			c, p, err := cliauth.Lookup(server)
			if err == nil && strings.TrimSpace(c.Token) == "" {
				err = errors.New("empty login")
			}
			return c, p, err
		},
		state:         localSandboxState,
		defaultServer: defaultServer,
	})
	if hint != "" {
		_ = os.Setenv(forgeConnectHintEnv, hint)
	}
}
