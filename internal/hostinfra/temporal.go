package hostinfra

// Temporal as a HOST PROCESS: the Temporal CLI's development server
// (`temporal server start-dev`), supervised like every other supervised-binary
// engine (supervised.go).
//
// # What is not obvious
//
//   - The state PERSISTS (`--db-filename` under the data directory). The dev
//     server defaults to in-memory, and that is the wrong default beside a
//     persistent postgres: after a restart the app's rows would name workflow
//     executions the server has never heard of. Delete the data directory to
//     start clean, as with every other engine.
//   - The declared namespaces are created at launch (`--namespace`). An app's
//     worker that polls a namespace that does not exist fails in a loop, and a
//     namespace created by hand is a setup step a teammate's clone skips.
//   - Only the frontend port is declared. The HTTP API and metrics listeners
//     default to free ports the server picks itself; the Web UI is off unless
//     `temporal_ui_port` names a port for it. Every fixed default the dev
//     server has (7233, 8233) is a collision between two stacks on one machine.
//   - Readiness is "every declared namespace describes", asked through the
//     same binary. A frontend accepts connections before the namespaces it was
//     told to create exist, and a worker started in that window fails.
//   - SQLite runs in WAL mode with synchronous=NORMAL. The default
//     (rollback journal, FULL) fsyncs on every commit; on a loaded machine
//     that makes activity heartbeats time out, which reads as a bug in the app.
//     A crash can lose the last few transactions — acceptable for a dev store.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// EngineTemporal matches the `engine` discriminator on the KCL HostInfra schema.
const EngineTemporal = "temporal"

// TemporalCLIVersion is the pinned Temporal CLI release, whose dev server is
// what this engine runs (CLI 1.9.1 embeds Temporal Server 1.32.0).
const TemporalCLIVersion = "1.9.1"

// temporalChecksums is each release archive's sha256, copied from
//
//	https://github.com/temporalio/cli/releases/download/v1.9.1/checksums.txt
var temporalChecksums = map[string]string{
	"temporal_cli_1.9.1_darwin_amd64.tar.gz":  "48cdd6c84c56e27ae8e4d9a47052b89289a9c552dee029b49dd312c6fb72873f",
	"temporal_cli_1.9.1_darwin_arm64.tar.gz":  "41e0425378fcb4fb5766340b97435e20fe47bbff2d7bf644ec2d51f7662b7c56",
	"temporal_cli_1.9.1_linux_amd64.tar.gz":   "09a0326a51db84d02735e53542b9ebd8c4758daf47482a9ab0abce15844e60d5",
	"temporal_cli_1.9.1_linux_arm64.tar.gz":   "6c57c352d52fc3df34412376fd9ba6f74b7e3ace8e426e6cba8600156d36a145",
	"temporal_cli_1.9.1_windows_amd64.tar.gz": "42637464c337a3da203fbd4e8c688e4b2d511fd42bd19c337c0422e923a7b619",
	"temporal_cli_1.9.1_windows_arm64.tar.gz": "cfdfe43e69a2261191148294b1f7e3a7088ef8d3dc1ab1909b115b1784b6c6fe",
}

var temporalRelease = binaryRelease{
	engine:    EngineTemporal,
	version:   TemporalCLIVersion,
	checksums: temporalChecksums,
	asset: func(goos, goarch string) string {
		return fmt.Sprintf("temporal_cli_%s_%s_%s.tar.gz", TemporalCLIVersion, goos, goarch)
	},
	url: func(asset string) string {
		return fmt.Sprintf("https://github.com/temporalio/cli/releases/download/v%s/%s", TemporalCLIVersion, asset)
	},
	binary: func(goos string) string {
		if goos == "windows" {
			return "temporal.exe"
		}
		return "temporal"
	},
	versionArgs:   []string{"--version"},
	versionNeedle: "temporal version " + TemporalCLIVersion,
	approxSize:    "~50 MB",
}

var temporalEngine = supervisedEngine{
	release:      temporalRelease,
	args:         temporalArgs,
	ready:        temporalReady,
	readyTimeout: 2 * time.Minute,
}

// temporalNamespaces is what the instance must serve: the declared
// namespaces, or the always-present "default" when none are declared.
func temporalNamespaces(spec Spec) []string {
	if len(spec.TemporalNamespaces) == 0 {
		return []string{"default"}
	}
	return spec.TemporalNamespaces
}

// temporalArgs is the dev server's argv.
func temporalArgs(spec Spec, dataDir string) []string {
	args := []string{
		"server", "start-dev",
		"--ip", "127.0.0.1",
		"--port", strconv.Itoa(spec.Port),
		"--db-filename", filepath.Join(dataDir, "temporal.db"),
		"--sqlite-pragma", "journal_mode=wal",
		"--sqlite-pragma", "synchronous=normal",
		"--log-level", "warn",
	}
	if spec.TemporalUIPort > 0 {
		args = append(args, "--ui-port", strconv.Itoa(spec.TemporalUIPort))
	} else {
		args = append(args, "--headless")
	}
	for _, ns := range spec.TemporalNamespaces {
		args = append(args, "--namespace", ns)
	}
	return args
}

// temporalReady performs one readiness probe: every namespace the instance
// must serve describes on its frontend.
func temporalReady(ctx context.Context, spec Spec, bin string) bool {
	if !portListening(spec.Port) {
		return false
	}
	address := "127.0.0.1:" + strconv.Itoa(spec.Port)
	for _, ns := range temporalNamespaces(spec) {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := exec.CommandContext(probeCtx, bin, "operator", "namespace", "describe", // #nosec G204 -- forge's own cache entry; args from the declaration
			"--namespace", ns, "--address", address).Run()
		cancel()
		if err != nil {
			return false
		}
	}
	return true
}
