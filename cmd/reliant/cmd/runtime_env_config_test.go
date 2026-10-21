// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/runenv"
)

// TestRuntimeEnvContract pins that the environment tier, now read through the
// root config (RuntimeConfig.environment) and handed to internal/runenv by
// each process's load, resolves exactly as runenv's own env read did:
// RELIANT_ENV, else NODE_ENV (an empty one is unset), parsed fail-closed.
// control-plane sets RELIANT_ENV=dev on the dev stack's reliant processes and
// RELIANT_ENV=e2e on the e2e pods, and the desktop app's dev shell exports
// NODE_ENV=development to the daemon, so all three spellings are contracts.
// NODE_ENV is a permanent name: reading it must print nothing.
func TestRuntimeEnvContract(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want runenv.Environment
	}{
		{name: "nothing set", want: runenv.Prod},
		{name: "RELIANT_ENV=dev", env: map[string]string{"RELIANT_ENV": "dev"}, want: runenv.Dev},
		{name: "RELIANT_ENV=e2e", env: map[string]string{"RELIANT_ENV": "e2e"}, want: runenv.Dev},
		{name: "RELIANT_ENV=Development", env: map[string]string{"RELIANT_ENV": "Development"}, want: runenv.Dev},
		{name: "RELIANT_ENV=test", env: map[string]string{"RELIANT_ENV": "test"}, want: runenv.Test},
		{name: "RELIANT_ENV=prod", env: map[string]string{"RELIANT_ENV": "prod"}, want: runenv.Prod},
		{name: "RELIANT_ENV=staging fails closed", env: map[string]string{"RELIANT_ENV": "staging"}, want: runenv.Prod},
		{name: "RELIANT_ENV empty is unset", env: map[string]string{"RELIANT_ENV": ""}, want: runenv.Prod},
		{name: "NODE_ENV=development", env: map[string]string{"NODE_ENV": "development"}, want: runenv.Dev},
		{name: "NODE_ENV=production", env: map[string]string{"NODE_ENV": "production"}, want: runenv.Prod},
		{name: "empty RELIANT_ENV falls through to NODE_ENV", env: map[string]string{"RELIANT_ENV": "", "NODE_ENV": "development"}, want: runenv.Dev},
		{name: "RELIANT_ENV outranks NODE_ENV", env: map[string]string{"RELIANT_ENV": "prod", "NODE_ENV": "development"}, want: runenv.Prod},
	}
	for _, process := range rootConfigProcesses {
		for _, tc := range cases {
			t.Run(strings.Join(process, " ")+"/"+tc.name, func(t *testing.T) {
				for _, name := range append(rootConfigEnv, "RELIANT_ENV", "NODE_ENV") {
					t.Setenv(name, "")
					require.NoError(t, os.Unsetenv(name))
				}
				for name, value := range tc.env {
					t.Setenv(name, value)
				}
				runenv.ConfigureForTest(t, "") // undone after t, whatever the load sets

				cmd, _, err := NewRootCmd().Find(process)
				require.NoError(t, err)
				var stderr bytes.Buffer
				cmd.SetErr(&stderr)
				_, err = loadRootConfig(cmd)
				require.NoError(t, err)

				assert.Equal(t, tc.want, runenv.Get())
				assert.Empty(t, stderr.String(), "NODE_ENV is a permanent name: reading it prints nothing")
			})
		}
	}
}
