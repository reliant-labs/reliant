// Copyright (c) 2025 Reliant Labs
package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/serverworker"
)

// PPROF_PORT=0 turns the API server's and the worker's diagnostics listener
// off, as an empty PPROF_ADDR does the gateway's; it is not refused as a
// port outside 1-65535.
func TestServerPprofPortZeroIsOff(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("PPROF_PORT", "0")

	api := loadServerAPIOptions(t)
	assert.Zero(t, api.PprofPort)

	opts, err := serverRoles[1].load(t)
	require.NoError(t, err)
	assert.Zero(t, opts.(serverworker.Options).PprofPort)
}

// A PPROF_PORT that is neither 0 nor a TCP port still stops the server at
// load, naming the setting.
func TestServerPprofPortOutOfRangeFailsBoot(t *testing.T) {
	for _, value := range []string{"-1", "70000"} {
		for _, role := range serverRoles[:2] {
			t.Run(role.path[1]+" PPROF_PORT="+value, func(t *testing.T) {
				isolateServerRoleEnv(t)
				t.Setenv("PPROF_PORT", value)
				_, err := role.load(t)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "PPROF_PORT")
			})
		}
	}
}
