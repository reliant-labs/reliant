// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg, warnings := ParseConfig("", "", "", "")
	require.Empty(t, warnings)
	require.Equal(t, Config{Mode: ModeAuto, ScratchStripIdle: 48 * time.Hour, ScratchRemoveIdle: 7 * 24 * time.Hour, WorktreeNodeModulesIdle: 72 * time.Hour}, cfg)
}

func TestParseConfig_BadValuesFallBackWithAWarning(t *testing.T) {
	t.Parallel()
	cfg, warnings := ParseConfig("sometimes", "1h", "soon", "2h")
	require.Len(t, warnings, 4)
	require.Equal(t, ModeAuto, cfg.Mode)
	require.Equal(t, minScratchStripIdle, cfg.ScratchStripIdle, "raised to its floor")
	require.Equal(t, DefaultScratchRemoveIdle, cfg.ScratchRemoveIdle, "unparsable: the default")
	require.Equal(t, minWorktreeNodeModulesIdle, cfg.WorktreeNodeModulesIdle)
}

// The whole-directory age is never below the strip age: a directory is never
// removed whole before its strip was due.
func TestParseConfig_RemoveIsNeverBeforeStrip(t *testing.T) {
	t.Parallel()
	cfg, warnings := ParseConfig("on", "100h", "80h", "")
	require.Len(t, warnings, 1)
	require.Equal(t, 100*time.Hour, cfg.ScratchRemoveIdle)
}

// Mode auto is on exactly where the storage reaper runs; on and dry-run run
// anywhere; off never.
func TestConfig_Effective(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode        Mode
		reaper      bool
		run, dryRun bool
	}{
		{ModeAuto, true, true, false},
		{ModeAuto, false, false, false},
		{"", true, true, false},
		{ModeOn, false, true, false},
		{ModeDryRun, false, true, true},
		{ModeDryRun, true, true, true},
		{ModeOff, true, false, false},
	} {
		run, dryRun := Config{Mode: tc.mode}.Effective(tc.reaper)
		require.Equal(t, tc.run, run, "%s reaper=%v", tc.mode, tc.reaper)
		require.Equal(t, tc.dryRun, dryRun, "%s reaper=%v", tc.mode, tc.reaper)
	}
}
