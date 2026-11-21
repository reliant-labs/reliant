package hostinfra

// The LOCAL TELEMETRY AGENT: the OpenTelemetry Collector `forge env up` runs
// as a host process to pull the telemetry an env declares (forge.Telemetry)
// for what runs on the developer's machine — host-run engines, host
// processes, compose services on their published ports — into the env's
// local collector.
//
// # Why a host process and not a container
//
// HostInfra engines and forge.OnHost processes bind 127.0.0.1. A container
// cannot reach the host's loopback without host networking (which Docker
// Desktop does not give it), and binding the dev services to the docker
// bridge instead would publish them on the LAN. The ClickStack collector also
// cannot write profiles. So the agent runs beside the engines it scrapes, on
// the same loopback, and exports OTLP to the published ports of the local
// collector and the profiles collector.
//
// # What this file owns, and what it does not
//
// The agent is mechanism here and content in the caller: internal/cli builds
// the pipelines from the env's targets (pkg/telemetry/collector) and hands
// over the configuration; this file fetches the pinned collector (the
// supervised-binary release cache, the same pin as the cluster agent's image),
// adds the two things only the supervisor knows — its own health endpoint and
// switching off the collector's self-telemetry listener — writes the file,
// and starts, adopts or restarts the process.
//
// The configuration is regenerated on every `forge env up`. A running agent
// whose configuration and arguments are byte-identical, and whose credential
// files hold what they held when it started, is adopted; any difference
// restarts it, after the new configuration passed the collector's
// own `validate` (a configuration that would not load never replaces one that
// runs). Like the engines, the agent outlives `forge env up` and is stopped by
// `forge env down`.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// OtelcolContribVersion is the pinned otelcol-contrib release: the same
// collector as the cluster agent's image (kcl/base.k OTEL_COLLECTOR_IMAGE),
// so a configuration pkg/telemetry/collector renders behaves the same on a
// laptop and on a node.
const OtelcolContribVersion = "0.162.0"

// otelcolChecksums is each release archive's sha256, copied from the
// `<asset>.sha256` files published beside each asset of
//
//	https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v0.162.0
var otelcolChecksums = map[string]string{
	"otelcol-contrib_0.162.0_darwin_amd64.tar.gz":  "91a6e7a0f5e1981986c897db4535de5ba8d7fc45d4205809aecb9ebebf68c71c",
	"otelcol-contrib_0.162.0_darwin_arm64.tar.gz":  "d5e11974d2e4adac3cc001a25137aad52f6e77ec3034ba7735cac7c219a5f97a",
	"otelcol-contrib_0.162.0_linux_amd64.tar.gz":   "fcc063749f730f8c21fe29f2d340ff174f5f1c5885bd3156fb6c985a3036fcc3",
	"otelcol-contrib_0.162.0_linux_arm64.tar.gz":   "ecf6a917e2b53beb1703b19a84bc4e112a2fcaba549639cf6b0ba58446bfd7d2",
	"otelcol-contrib_0.162.0_windows_amd64.tar.gz": "b371829181e89947a1a6570b8b6c38c20d85e2d215b76bf042f89b365b9e5c3d",
	"otelcol-contrib_0.162.0_windows_arm64.tar.gz": "391e0556c1b1933a5079714b241889e34c84c59e22152e384d94af33e076528b",
}

var otelcolRelease = binaryRelease{
	engine:    "otelcol-contrib",
	version:   OtelcolContribVersion,
	checksums: otelcolChecksums,
	asset: func(goos, goarch string) string {
		return fmt.Sprintf("otelcol-contrib_%s_%s_%s.tar.gz", OtelcolContribVersion, goos, goarch)
	},
	url: func(asset string) string {
		return fmt.Sprintf("https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v%s/%s", OtelcolContribVersion, asset)
	},
	binary: func(goos string) string {
		if goos == "windows" {
			return "otelcol-contrib.exe"
		}
		return "otelcol-contrib"
	},
	versionArgs:   []string{"--version"},
	versionNeedle: "otelcol-contrib version " + OtelcolContribVersion,
	approxSize:    "~110 MB",
}

// EnsureOtelcolContrib returns the path of the verified pinned collector,
// downloading it on first use.
func EnsureOtelcolContrib(ctx context.Context) (string, error) {
	return otelcolRelease.ensure(ctx)
}

// TelemetryAgentName names the agent: its log is
// .forge/logs/<env>/telemetry-agent.log, beside every other process of the env.
const TelemetryAgentName = "telemetry-agent"

// healthExtension is the agent's own readiness endpoint, added to the
// caller's configuration.
const healthExtension = "health_check/forge_agent"

// agentReadyTimeout bounds the wait for a started agent to report healthy.
// A collector that loads is healthy in well under a second; the slack is a
// loaded machine's.
var agentReadyTimeout = time.Minute

// TelemetryAgentDir is where the agent of env keeps its configuration, pid
// and credentials: .forge/observability/<env>/ (gitignored under .forge).
func TelemetryAgentDir(projectDir, env string) string {
	return filepath.Join(projectDir, ".forge", "observability", env)
}

// TelemetryAgentSecretsDir is where the caller writes the credentials the
// agent's scrapes read (pkg/telemetry/collector Options.MountRoot): each
// SecretMount at <dir>/<SecretMount.Path>, 0600.
func TelemetryAgentSecretsDir(projectDir, env string) string {
	return filepath.Join(TelemetryAgentDir(projectDir, env), "secrets")
}

// TelemetryAgentConfigPath is the configuration file the agent runs.
func TelemetryAgentConfigPath(projectDir, env string) string {
	return filepath.Join(TelemetryAgentDir(projectDir, env), TelemetryAgentName+".yaml")
}

func telemetryAgentPIDFile(projectDir, env string) string {
	return filepath.Join(TelemetryAgentDir(projectDir, env), TelemetryAgentName+".pid")
}

// telemetryAgentCredentialsFile records the CredentialsRevision the running
// agent was started with. 0600 beside the credentials, never in the 0644
// configuration: it is a digest of their contents.
func telemetryAgentCredentialsFile(projectDir, env string) string {
	return filepath.Join(TelemetryAgentDir(projectDir, env), TelemetryAgentName+".credentials")
}

// readRevision is the recorded revision, "" when none was recorded.
func readRevision(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- forge's own file
	if err != nil {
		return ""
	}
	return string(b)
}

// TelemetryAgent is one env's agent, as the caller renders it.
type TelemetryAgent struct {
	// Env names the env: the agent's directory and log.
	Env string
	// Config is the collector configuration: receivers, processors,
	// exporters and pipelines. The supervisor adds its health extension and
	// turns the collector's own metrics listener off (a fixed :8888 would
	// collide between two envs), so Config must name neither.
	Config map[string]any
	// FeatureGates are passed as --feature-gates (e.g.
	// service.profilesSupport for profiles pipelines).
	FeatureGates []string
	// CredentialsRevision fingerprints the contents of the credential files
	// Config names ("" when it names none). The collector reads them at
	// start and the configuration names only their paths, so a running
	// agent is adopted only when it was started with this revision too: a
	// rotated token or CA restarts it.
	CredentialsRevision string
}

// TelemetryAgentState is what StartTelemetryAgent did.
type TelemetryAgentState string

const (
	// AgentStarted means no agent was running, and one is now.
	AgentStarted TelemetryAgentState = "started"
	// AgentRestarted means a running agent had another configuration and
	// was replaced.
	AgentRestarted TelemetryAgentState = "restarted"
	// AgentUnchanged means a running agent already had this configuration.
	AgentUnchanged TelemetryAgentState = "unchanged"
)

// TelemetryAgentResult reports a start.
type TelemetryAgentResult struct {
	State      TelemetryAgentState
	PID        int
	ConfigPath string
	LogPath    string
}

// StartTelemetryAgent brings env's agent up with a's configuration, adopting
// a running one whose configuration is identical, and returns once the agent
// reports healthy.
func StartTelemetryAgent(ctx context.Context, projectDir string, a TelemetryAgent) (TelemetryAgentResult, error) {
	res := TelemetryAgentResult{
		ConfigPath: TelemetryAgentConfigPath(projectDir, a.Env),
		LogPath:    LogPath(projectDir, a.Env, TelemetryAgentName),
	}
	bin, err := otelcolRelease.ensure(ctx)
	if err != nil {
		return res, fmt.Errorf("telemetry agent: %w", err)
	}
	dir := TelemetryAgentDir(projectDir, a.Env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("telemetry agent: %w", err)
	}
	pidFile := telemetryAgentPIDFile(projectDir, a.Env)

	pid, port, running := runningAgent(pidFile, bin)
	if !running {
		if port, err = freeLoopbackPort(); err != nil {
			return res, fmt.Errorf("telemetry agent: pick a health port: %w", err)
		}
	}
	doc, err := RenderTelemetryAgentConfig(a, port)
	if err != nil {
		return res, fmt.Errorf("telemetry agent: %w", err)
	}

	revFile := telemetryAgentCredentialsFile(projectDir, a.Env)
	if running {
		cur, rerr := os.ReadFile(res.ConfigPath) // #nosec G304 -- forge's own file
		if rerr == nil && bytes.Equal(cur, doc) && readRevision(revFile) == a.CredentialsRevision {
			if err := waitAgentHealthy(ctx, pid, port); err != nil {
				return res, fmt.Errorf("telemetry agent (pid %d): %w\n  logs: %s", pid, err, shortPath(projectDir, res.LogPath))
			}
			res.State, res.PID = AgentUnchanged, pid
			return res, nil
		}
	}

	// A configuration that does not load never replaces one that runs.
	if err := validateAgentConfig(ctx, bin, dir, doc, a.FeatureGates); err != nil {
		return res, fmt.Errorf("telemetry agent: %w", err)
	}
	res.State = AgentStarted
	if running {
		if _, err := stopAgent(pidFile, bin); err != nil {
			return res, fmt.Errorf("telemetry agent: replace pid %d: %w", pid, err)
		}
		res.State = AgentRestarted
	}
	if err := atomicfile.WriteFile(res.ConfigPath, doc, 0o644); err != nil { // #nosec G306 -- names credential FILES, never values
		return res, fmt.Errorf("telemetry agent: write %s: %w", res.ConfigPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(res.LogPath), 0o755); err != nil {
		return res, fmt.Errorf("telemetry agent: %w", err)
	}
	logFile, err := os.OpenFile(res.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 -- forge's own log path
	if err != nil {
		return res, fmt.Errorf("telemetry agent: open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(bin, agentArgs(res.ConfigPath, a.FeatureGates)...) // #nosec G204 -- forge's own verified cache entry and config
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return res, fmt.Errorf("telemetry agent: start: %w", err)
	}
	res.PID = cmd.Process.Pid
	if err := writePIDFile(pidFile, res.PID, port); err != nil {
		_ = cmd.Process.Kill()
		return res, fmt.Errorf("telemetry agent: record pid: %w", err)
	}
	if err := atomicfile.WriteFile(revFile, []byte(a.CredentialsRevision), 0o600); err != nil {
		_ = cmd.Process.Kill()
		return res, fmt.Errorf("telemetry agent: record credentials revision: %w", err)
	}
	_ = cmd.Process.Release()
	if err := waitAgentHealthy(ctx, res.PID, port); err != nil {
		return res, fmt.Errorf("telemetry agent (pid %d): %w\n  logs: %s", res.PID, err, shortPath(projectDir, res.LogPath))
	}
	return res, nil
}

// StopTelemetryAgent stops env's agent and reports whether one was running.
// Only a process provably the agent this project started is signalled.
func StopTelemetryAgent(projectDir, env string) (bool, error) {
	return stopAgent(telemetryAgentPIDFile(projectDir, env), otelcolRelease.binaryPath())
}

// TelemetryAgentRunning reports the pid of env's running agent.
func TelemetryAgentRunning(projectDir, env string) (int, bool) {
	pid, _, ok := runningAgent(telemetryAgentPIDFile(projectDir, env), otelcolRelease.binaryPath())
	return pid, ok
}

// RenderTelemetryAgentConfig is the file StartTelemetryAgent writes: a's
// configuration plus the health extension on healthPort and the collector's
// self-telemetry listener off, preceded by a header naming the arguments (so
// a change of feature gates is a change of the file, and restarts the agent).
func RenderTelemetryAgentConfig(a TelemetryAgent, healthPort int) ([]byte, error) {
	cfg := map[string]any{}
	for k, v := range a.Config {
		cfg[k] = v
	}
	ext := copySection(cfg, "extensions")
	if _, taken := ext[healthExtension]; taken {
		return nil, fmt.Errorf("the configuration already defines %s, which the supervisor owns", healthExtension)
	}
	ext[healthExtension] = map[string]any{"endpoint": net.JoinHostPort("127.0.0.1", strconv.Itoa(healthPort))}
	service := copySection(cfg, "service")
	var exts []any
	switch cur := service["extensions"].(type) {
	case []any:
		exts = append(exts, cur...)
	case []string:
		for _, e := range cur {
			exts = append(exts, e)
		}
	}
	service["extensions"] = append(exts, healthExtension)
	if _, set := service["telemetry"]; set {
		return nil, errors.New("the configuration sets service.telemetry, which the supervisor owns")
	}
	// The collector's own metrics would listen on a fixed :8888 in every
	// env; nothing reads them locally. Its logs go to stderr: the log file.
	service["telemetry"] = map[string]any{"metrics": map[string]any{"level": "none"}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal the agent configuration: %w", err)
	}
	header := "# The local telemetry agent of this env, written by `forge env up` from the\n" +
		"# env's declared telemetry (forge.Telemetry). Regenerated on every run: edit the\n" +
		"# declaration in deploy/kcl/<env>/main.k, never this file.\n" +
		"# args: " + strings.Join(agentArgs("<this file>", a.FeatureGates), " ") + "\n"
	return append([]byte(header), body...), nil
}

// copySection returns cfg[key] as a fresh map (so the caller's map is never
// written), storing it back.
func copySection(cfg map[string]any, key string) map[string]any {
	out := map[string]any{}
	if cur, ok := cfg[key].(map[string]any); ok {
		for k, v := range cur {
			out[k] = v
		}
	}
	cfg[key] = out
	return out
}

func agentArgs(configPath string, gates []string) []string {
	args := []string{"--config=" + configPath}
	if len(gates) > 0 {
		args = append(args, "--feature-gates="+strings.Join(gates, ","))
	}
	return args
}

// validateAgentConfig runs the collector's own `validate` over doc: every
// component exists, every key is one it reads, every OTTL statement compiles
// and every credentials file it names exists.
func validateAgentConfig(ctx context.Context, bin, dir string, doc []byte, gates []string) error {
	tmp, err := os.CreateTemp(dir, TelemetryAgentName+"-validate-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(doc); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(vctx, bin, append([]string{"validate"}, agentArgs(name, gates)...)...).CombinedOutput() // #nosec G204 -- forge's own verified cache entry
	if err != nil {
		return fmt.Errorf("the generated configuration does not load in otelcol-contrib %s: %v\n%s", OtelcolContribVersion, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runningAgent reads the pidfile: the pid and health port of a live agent
// that is provably the one this project started.
func runningAgent(pidFile, bin string) (pid, port int, ok bool) {
	pid, port, recorded := readPIDFile(pidFile)
	if !recorded || !processIsAlive(pid) {
		return 0, 0, false
	}
	if verifyRecordedPid(pid, pidFile, bin) != verdictOurs {
		return 0, 0, false
	}
	return pid, port, true
}

// stopAgent is supervisedEngine.stop for the agent's pidfile.
func stopAgent(pidFile, bin string) (bool, error) {
	pid, _, ok := readPIDFile(pidFile)
	if !ok || !processIsAlive(pid) {
		_ = os.Remove(pidFile)
		return false, nil
	}
	switch verifyRecordedPid(pid, pidFile, bin) {
	case verdictForeign:
		_ = os.Remove(pidFile)
		return false, nil
	case verdictUnknown:
		return false, fmt.Errorf("%s names pid %d but its identity could not be verified; not killing it", filepath.Base(pidFile), pid)
	case verdictOurs:
	}
	if err := terminateProcess(pid); err != nil {
		if isProcessGone(err) {
			_ = os.Remove(pidFile)
			return false, nil
		}
		return false, fmt.Errorf("stop the telemetry agent (pid %d): %w", pid, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !processIsAlive(pid) {
			_ = os.Remove(pidFile)
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if verifyRecordedPid(pid, pidFile, bin) == verdictOurs {
		_ = killProcess(pid)
	}
	_ = os.Remove(pidFile)
	return true, nil
}

// waitAgentHealthy blocks until the agent's health endpoint answers 200, its
// process dies, or agentReadyTimeout passes.
func waitAgentHealthy(ctx context.Context, pid, port int) error {
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/"
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(agentReadyTimeout)
	for {
		if !processIsAlive(pid) {
			return errors.New("the collector exited during startup")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the collector did not report healthy on %s within %s", url, agentReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// freeLoopbackPort is a port nothing listens on now.
func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
