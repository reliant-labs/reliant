// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
)

// RELIANT_PAYLOAD_BLOB_GC_HORIZON reaches the API server's payload GC; unset
// it is claimcheck's default, and a horizon it cannot honour stops the server
// (it used to be a WARN and the default).
func TestServerAPIPayloadGCHorizon(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("RELIANT_PAYLOAD_BLOB_GC_HORIZON", "")
	require.NoError(t, os.Unsetenv("RELIANT_PAYLOAD_BLOB_GC_HORIZON"))
	load := func() (apiOptions, error) {
		cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
		require.NoError(t, err)
		var opts apiOptions
		return opts, loadServerAPIConfig(cmd, &opts)
	}
	opts, err := load()
	require.NoError(t, err)
	assert.Equal(t, claimcheck.DefaultGCHorizon, opts.PayloadGCHorizon)

	t.Setenv("RELIANT_PAYLOAD_BLOB_GC_HORIZON", "48h")
	opts, err = load()
	require.NoError(t, err)
	assert.Equal(t, 48*time.Hour, opts.PayloadGCHorizon)

	for _, bad := range []string{"garbage", "0s", "-1h"} {
		t.Setenv("RELIANT_PAYLOAD_BLOB_GC_HORIZON", bad)
		_, err = load()
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "RELIANT_PAYLOAD_BLOB_GC_HORIZON")
	}
}
