// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runenv"
)

// rootConfigProcesses are the commands that load the root config
// (proto/config/v1 ReliantConfig) and build their logger from it.
var rootConfigProcesses = [][]string{
	{"server", "api"},
	{"server", "worker"},
	{"server", "gateway"},
	{"daemon", "start"},
}

// rootConfigEnv is every variable the root config, or the logger built from
// it, reads, plus the retired DEBUG switches the cases prove are not read.
var rootConfigEnv = []string{"RELIANT_LOG_LEVEL", "LOG_FORMAT", "DEBUG", "RELIANT_DEV_DEBUG", "RELIANT_CONFIG"}

// TestRootConfigEnvContract pins that RELIANT_LOG_LEVEL and LOG_FORMAT, now
// read through the root config instead of by internal/logging, keep exactly
// the effect they had: same names, same parsing (any case, surrounding space,
// a value that names nothing warned about and ignored), same defaults, and an
// empty value still meaning unset. control-plane sets both on reliant
// processes by name (the workspace operator gives every daemon pod
// LOG_FORMAT=json and RELIANT_LOG_LEVEL=info), so the names are a contract.
//
// The flags are new; they take precedence over the variables, as every
// reliant flag does.
func TestRootConfigEnvContract(t *testing.T) {
	type effect struct {
		level slog.Level
		json  bool
		warns string // the warning the logger writes once installed, if any
	}
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want effect
	}{
		{name: "nothing set", want: effect{level: slog.LevelInfo}},

		{name: "RELIANT_LOG_LEVEL=debug", env: map[string]string{"RELIANT_LOG_LEVEL": "debug"}, want: effect{level: slog.LevelDebug}},
		{name: "RELIANT_LOG_LEVEL=WARN", env: map[string]string{"RELIANT_LOG_LEVEL": "WARN"}, want: effect{level: slog.LevelWarn}},
		{name: "RELIANT_LOG_LEVEL=' Error '", env: map[string]string{"RELIANT_LOG_LEVEL": " Error "}, want: effect{level: slog.LevelError}},
		{name: "RELIANT_LOG_LEVEL=warning", env: map[string]string{"RELIANT_LOG_LEVEL": "warning"}, want: effect{level: slog.LevelWarn}},
		{name: "RELIANT_LOG_LEVEL=info", env: map[string]string{"RELIANT_LOG_LEVEL": "info"}, want: effect{level: slog.LevelInfo}},
		{name: "RELIANT_LOG_LEVEL unrecognised", env: map[string]string{"RELIANT_LOG_LEVEL": "verbose"}, want: effect{level: slog.LevelInfo, warns: "Unrecognised RELIANT_LOG_LEVEL"}},
		{name: "RELIANT_LOG_LEVEL empty is unset", env: map[string]string{"RELIANT_LOG_LEVEL": ""}, want: effect{level: slog.LevelInfo}},
		{name: "DEBUG=true is not read", env: map[string]string{"RELIANT_LOG_LEVEL": "warn", "DEBUG": "true"}, want: effect{level: slog.LevelWarn}},
		{name: "DEBUG=true without RELIANT_LOG_LEVEL is not read", env: map[string]string{"DEBUG": "true"}, want: effect{level: slog.LevelInfo}},
		{name: "RELIANT_DEV_DEBUG=true is retired", env: map[string]string{"RELIANT_DEV_DEBUG": "true"}, want: effect{level: slog.LevelInfo}},

		{name: "LOG_FORMAT=json", env: map[string]string{"LOG_FORMAT": "json"}, want: effect{level: slog.LevelInfo, json: true}},
		{name: "LOG_FORMAT=JSON", env: map[string]string{"LOG_FORMAT": "JSON"}, want: effect{level: slog.LevelInfo, json: true}},
		{name: "LOG_FORMAT=' json '", env: map[string]string{"LOG_FORMAT": " json "}, want: effect{level: slog.LevelInfo, json: true}},
		{name: "LOG_FORMAT=text", env: map[string]string{"LOG_FORMAT": "text"}, want: effect{level: slog.LevelInfo}},
		{name: "LOG_FORMAT unrecognised", env: map[string]string{"LOG_FORMAT": "logfmt"}, want: effect{level: slog.LevelInfo, warns: "Unrecognised LOG_FORMAT"}},
		{name: "LOG_FORMAT empty is unset", env: map[string]string{"LOG_FORMAT": ""}, want: effect{level: slog.LevelInfo}},

		{name: "workspace operator's daemon pod", env: map[string]string{"LOG_FORMAT": "json", "RELIANT_LOG_LEVEL": "info"}, want: effect{level: slog.LevelInfo, json: true}},

		{name: "--log-level outranks RELIANT_LOG_LEVEL", env: map[string]string{"RELIANT_LOG_LEVEL": "warn"}, args: []string{"--log-level=error"}, want: effect{level: slog.LevelError}},
		{name: "--log-format outranks LOG_FORMAT", env: map[string]string{"LOG_FORMAT": "text"}, args: []string{"--log-format", "json"}, want: effect{level: slog.LevelInfo, json: true}},
	}

	for _, process := range rootConfigProcesses {
		for _, tc := range cases {
			t.Run(strings.Join(process, " ")+"/"+tc.name, func(t *testing.T) {
				// Dev, so DEBUG is reachable and the level is the one asked for.
				// The root config load below hands it to runenv.
				t.Setenv("RELIANT_ENV", "dev")
				t.Setenv("NODE_ENV", "")
				for _, name := range rootConfigEnv {
					t.Setenv(name, "") // restores the original afterwards
					require.NoError(t, os.Unsetenv(name))
				}
				for name, value := range tc.env {
					t.Setenv(name, value)
				}

				settings := loadLogSettings(t, process, tc.args...)

				assert.Equal(t, tc.want.level, logging.GetLogLevel(settings), "level")

				out := installedLoggerOutput(t, settings)
				line := lineContaining(out, "contract-marker")
				require.NotEmpty(t, line, "the marker was not logged:\n%s", out)
				assert.Equal(t, tc.want.json, json.Valid([]byte(line)), "JSON line format: %s", line)
				if tc.want.warns != "" {
					assert.Contains(t, out, tc.want.warns)
				} else {
					assert.NotContains(t, out, "Unrecognised")
				}
			})
		}
	}
}

// loadLogSettings loads the root config the way process's RunE does, after
// parsing args, and returns the logger settings it hands internal/logging.
func loadLogSettings(t *testing.T, process []string, args ...string) logging.Settings {
	t.Helper()
	runenv.ConfigureForTest(t, "") // loadRootConfig sets the tier; undo it after t
	cmd, _, err := NewRootCmd().Find(process)
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(args))
	cfg, err := loadRootConfig(cmd)
	require.NoError(t, err)
	return logSettings(cfg)
}

// installedLoggerOutput installs the logger a server installs for settings,
// logs one marker through it (at ERROR, so every level lets it through), and
// returns everything it wrote.
func installedLoggerOutput(t *testing.T, settings logging.Settings) string {
	t.Helper()
	prevOutput, prevDefault := logging.DefaultOutput, slog.Default()
	t.Cleanup(func() {
		logging.DefaultOutput = prevOutput
		slog.SetDefault(prevDefault)
	})
	var buf bytes.Buffer
	logging.DefaultOutput = &buf
	logging.Setup(settings, logging.GetLogLevel(settings))
	logging.Error("contract-marker")
	return buf.String()
}

func lineContaining(out, marker string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return ""
}

// The root config's file layer is RELIANT_CONFIG (or --config): a file keyed
// like the message, below env and flags. FORGE_CONFIG, the name every forge
// binary used to share, means nothing to reliant, so a shell that exports it
// for another app cannot stop a reliant process from starting.
func TestRootConfigFileLayer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reliant.yaml")
	require.NoError(t, os.WriteFile(file, []byte("log:\n  level: warn\n  format: json\n"), 0o600))

	cases := []struct {
		name string
		env  map[string]string
		args []string
		want logging.Settings
	}{
		{name: "RELIANT_CONFIG", env: map[string]string{"RELIANT_CONFIG": file}, want: logging.Settings{Level: "warn", Format: "json"}},
		{name: "--config", args: []string{"--config", file}, want: logging.Settings{Level: "warn", Format: "json"}},
		{name: "env outranks the file", env: map[string]string{"RELIANT_CONFIG": file, "RELIANT_LOG_LEVEL": "error"}, want: logging.Settings{Level: "error", Format: "json"}},
		{name: "FORGE_CONFIG is not reliant's", env: map[string]string{"FORGE_CONFIG": filepath.Join(t.TempDir(), "missing.yaml")}},
	}
	for _, process := range rootConfigProcesses {
		for _, tc := range cases {
			t.Run(strings.Join(process, " ")+"/"+tc.name, func(t *testing.T) {
				for _, name := range append(rootConfigEnv, "FORGE_CONFIG") {
					t.Setenv(name, "")
					require.NoError(t, os.Unsetenv(name))
				}
				for name, value := range tc.env {
					t.Setenv(name, value)
				}
				assert.Equal(t, tc.want, loadLogSettings(t, process, tc.args...))
			})
		}
	}
}
