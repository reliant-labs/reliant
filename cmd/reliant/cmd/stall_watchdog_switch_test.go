// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
	"github.com/reliant-labs/reliant/pkg/config"
)

const stallWatchdogEnv = "RELIANT_RECONCILER_STALL_WATCHDOG"

// loadAPIProcess loads `server api`'s ServerProcessConfig with
// RELIANT_RECONCILER_STALL_WATCHDOG set to value, or unset when set is false.
func loadAPIProcess(t *testing.T, value string, set bool) (*config.ServerProcessConfig, error) {
	t.Helper()
	isolateServerRoleEnv(t)
	t.Setenv(stallWatchdogEnv, "") // restores the original afterwards
	require.NoError(t, os.Unsetenv(stallWatchdogEnv))
	if set {
		t.Setenv(stallWatchdogEnv, value)
	}
	return config.LoadServerProcessConfig(findServerCmd(t, "api"))
}

// assertReconcilerMode follows a loaded ServerProcessConfig to the mode
// app.NewWorkflowReconciler hands the reconciler (compose.go passes it
// ServerProcessConfig.reconciler; ReconcilerStallWatchdog is its reading).
func assertReconcilerMode(t *testing.T, process *config.ServerProcessConfig, want reconciliation.StallWatchdogMode) {
	t.Helper()
	require.Equal(t, want, app.ReconcilerStallWatchdog(process.GetReconciler()), "the reconciler's stall watchdog mode")
}

// TestStallWatchdogSwitchReachesTheReconciler pins reconciler.stall_watchdog
// on the path production takes: an env's KCL (config_gen.workloadEnvMap for
// the api root, where the reconciler runs) projects
// RELIANT_RECONCILER_STALL_WATCHDOG, `server api` loads its
// ServerProcessConfig from it, and the reconciler is built in that mode. At
// the schema default, and in this repo's dev and demo envs, it is off.
func TestStallWatchdogSwitchReachesTheReconciler(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/stall_watchdog_switch.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		want     reconciliation.StallWatchdogMode
	}{
		{"watchdog_default", reconciliation.StallWatchdogOff},
		{"dev_api", reconciliation.StallWatchdogOff},
		{"demo_api", reconciliation.StallWatchdogOff},
		{"watchdog_report", reconciliation.StallWatchdogReport},
		{"watchdog_enforce", reconciliation.StallWatchdogEnforce},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env[stallWatchdogEnv]
			require.True(t, ok, "the api server's projected env has no %s: %v", stallWatchdogEnv, env)
			require.Equal(t, string(tc.want), got)

			process, err := loadAPIProcess(t, got.(string), true)
			require.NoError(t, err)
			assertReconcilerMode(t, process, tc.want)
		})
	}
}

// The fast half, for the merge tier: deploy/kcl/config_gen.k (read by this
// process, so the test cache sees an edit) still defaults the watchdog off
// and projects it into the api root's env; with the variable unset, as
// every env ships, the reconciler is built off; set, in report or enforce;
// and a value outside the three is refused at load.
func TestStallWatchdogSwitchIsOffByDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "kcl", "config_gen.k"))
	require.NoError(t, err)
	kcl := string(raw)
	// require.True, not Contains: a failure would print the whole file.
	require.True(t, regexp.MustCompile(`(?m)^schema ServerProcessConfigReconciler:\n(?:    #.*\n)*    stall_watchdog: str = "off"$`).MatchString(kcl),
		"config_gen.k: the KCL schema's reconciler.stall_watchdog default is no longer off")
	require.True(t, strings.Contains(kcl, `"`+stallWatchdogEnv+`" = c.reconciler.stall_watchdog`),
		"config_gen.k: serverProcessConfigEnvMap no longer projects reconciler.stall_watchdog as %s", stallWatchdogEnv)
	start := strings.Index(kcl, `    "api" = Root {`)
	require.GreaterOrEqual(t, start, 0, "config_gen.k's ROOTS has no api root")
	api := kcl[start:]
	if next := strings.Index(api[1:], "\n    \""); next >= 0 {
		api = api[:next+1]
	}
	require.True(t, strings.Contains(api, `"`+stallWatchdogEnv+`",`), "config_gen.k: the api root's env no longer carries %s", stallWatchdogEnv)

	process, err := loadAPIProcess(t, "", false)
	require.NoError(t, err)
	require.Equal(t, "off", process.GetReconciler().GetStallWatchdog())
	assertReconcilerMode(t, process, reconciliation.StallWatchdogOff)

	for _, mode := range []reconciliation.StallWatchdogMode{reconciliation.StallWatchdogReport, reconciliation.StallWatchdogEnforce} {
		process, err = loadAPIProcess(t, string(mode), true)
		require.NoError(t, err)
		assertReconcilerMode(t, process, mode)
	}

	_, err = loadAPIProcess(t, "kill", true)
	require.Error(t, err, "a value outside off|report|enforce is refused, never read as on")
}
