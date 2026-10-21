// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// `reliant open` was removed: its help named a `reliant .` command that never
// existed, its --background killed the daemon it had just started, and its
// project registration posted to a REST route the API no longer serves and
// ignored the failure. `daemon start` plus the app cover what it promised.
func TestOpenCommandIsGone(t *testing.T) {
	for _, sub := range NewRootCmd().Commands() {
		require.NotEqual(t, "open", sub.Name(), "`reliant open` was removed")
	}
}

// `daemon start --background` was advertised in --help and answered "not yet
// implemented". An unimplemented flag is removed, not listed.
func TestDaemonStartHasNoBackgroundFlag(t *testing.T) {
	start, _, err := NewRootCmd().Find([]string{"daemon", "start"})
	require.NoError(t, err)
	require.Nil(t, start.Flags().Lookup("background"))
}
