//go:build integration

package observe

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/deploy"
)

// TestHeadersFileSmokeAgainstContribCollector proves, against a real
// opentelemetry-collector-contrib at the version forge pins, that a bearer
// token can be rotated on both sides of the export with no process restart:
//
//   - DIRECT: the forge runtime exports with OTEL_EXPORTER_OTLP_HEADERS_FILE to a
//     collector whose receiver checks tokens from a file. The runtime's file and
//     the collector's accept-list rotate; export continues; the old token is then
//     refused by the collector.
//   - AGENT: the runtime exports unauthenticated to a collector wired from the
//     node agent forge renders (pkg/deploy: bearertokenauth{filename}, the
//     exporter's auth reference, the Secret volume layout). Its token file is a
//     kubelet-style symlink-swap volume, started EMPTY (the Secret does not
//     exist yet) and then filled and rotated.
//
// Run: RUN_HEADERS_FILE_SMOKE=1 go test -tags integration -count=1 \
// -run TestHeadersFileSmoke -timeout 10m ./pkg/observe/
func TestHeadersFileSmokeAgainstContribCollector(t *testing.T) {
	if testing.Short() || os.Getenv("RUN_HEADERS_FILE_SMOKE") != "1" {
		t.Skip("boots two collector containers; set RUN_HEADERS_FILE_SMOKE=1 outside -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable: " + err.Error())
	}
	const image = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.162.0"

	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	network := "f2smoke-net-" + id
	backendName, agentName := "f2smoke-backend-"+id, "f2smoke-agent-"+id
	root := t.TempDir()
	// The collector runs as an unprivileged uid; the host dirs must be
	// traversable and the output dir writable by it.
	for _, d := range []string{"auth", "out", "agent-auth", "conf"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(filepath.Join(root, d), 0o777)
	}
	_ = os.Chmod(root, 0o755)

	run("network", "create", network)
	t.Cleanup(func() {
		// Containers by explicit name, with their anonymous volumes; never a
		// pattern.
		for _, n := range []string{agentName, backendName} {
			_ = exec.Command("docker", "rm", "-fv", n).Run()
		}
		_ = exec.Command("docker", "network", "rm", network).Run()
	})

	// ── backend: a receiver that checks bearer tokens read from a file ──────
	tokensFile := filepath.Join(root, "auth", "tokens")
	setBackendTokens := func(tokens ...string) {
		t.Helper()
		tmp := tokensFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(strings.Join(tokens, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, tokensFile); err != nil {
			t.Fatal(err)
		}
	}
	setBackendTokens("tok-old")
	writeConf(t, root, "backend.yaml", `
extensions:
  bearertokenauth:
    filename: /auth/tokens
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
        auth:
          authenticator: bearertokenauth
exporters:
  file:
    path: /out/traces.jsonl
    flush_interval: 500ms
service:
  extensions: [bearertokenauth]
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [file]
`)
	run("run", "-d", "--name", backendName, "--network", network,
		"-p", "127.0.0.1::4318",
		"-v", filepath.Join(root, "conf", "backend.yaml")+":/etc/otelcol/config.yaml:ro",
		"-v", filepath.Join(root, "auth")+":/auth:ro",
		"-v", filepath.Join(root, "out")+":/out",
		image, "--config=/etc/otelcol/config.yaml")
	backendURL := publishedURL(t, run, backendName)
	waitUntil(t, 60*time.Second, "backend collector to accept connections", func() bool {
		return probe(backendURL, "") != 0
	})

	spans := func() string {
		b, _ := os.ReadFile(filepath.Join(root, "out", "traces.jsonl"))
		return string(b)
	}
	// exportUntilSeen emits a uniquely named span each second until the
	// collector's output holds one of this phase, or the deadline passes.
	exportUntilSeen := func(tp interface{ ForceFlush(context.Context) error }, phase string) {
		t.Helper()
		waitUntil(t, 60*time.Second, "a span named "+phase+" to reach the collector", func() bool {
			_, span := otel.Tracer("smoke").Start(context.Background(), phase)
			span.End()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tp.ForceFlush(ctx)
			time.Sleep(time.Second)
			return strings.Contains(spans(), phase)
		})
	}
	// exportNeverSeen proves a phase's spans are refused: they are exported for
	// longer than the headers file's recheck interval and none arrives.
	exportNeverSeen := func(tp interface{ ForceFlush(context.Context) error }, phase string) {
		t.Helper()
		deadline := time.Now().Add(headersFileRecheck + 4*time.Second)
		for time.Now().Before(deadline) {
			_, span := otel.Tracer("smoke").Start(context.Background(), phase)
			span.End()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = tp.ForceFlush(ctx)
			cancel()
			time.Sleep(time.Second)
		}
		if strings.Contains(spans(), phase) {
			t.Fatalf("a span of phase %q arrived but its token must have been refused:\n%s", phase, spans())
		}
	}

	setup := func(t *testing.T, endpoint string, env map[string]string) (interface{ ForceFlush(context.Context) error }, func()) {
		t.Helper()
		clearOTelEnv(t)
		snapshotGlobals(t)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
		t.Setenv("OTEL_METRICS_EXPORTER", "none")
		t.Setenv("OTEL_PROFILES_EXPORTER", "none")
		for k, v := range env {
			t.Setenv(k, v)
		}
		shutdown, _, err := Setup(context.Background(), Config{ServiceName: "headers-file-smoke"})
		if err != nil {
			t.Fatal(err)
		}
		tp := otel.GetTracerProvider().(interface{ ForceFlush(context.Context) error })
		return tp, func() { _ = shutdown(context.Background()) }
	}

	t.Run("direct: runtime reads OTEL_EXPORTER_OTLP_HEADERS_FILE", func(t *testing.T) {
		headers := filepath.Join(t.TempDir(), "otlp-headers")
		writeHeadersFile(t, headers, "authorization=Bearer tok-old")
		tp, stop := setup(t, backendURL, map[string]string{EnvOTLPHeadersFile: headers})
		defer stop()

		exportUntilSeen(tp, "direct-old")

		// Overlap: the collector accepts both while the runtime moves over.
		setBackendTokens("tok-old", "tok-new")
		writeHeadersFile(t, headers, "authorization=Bearer tok-new")
		exportUntilSeen(tp, "direct-new")

		// The collector rotates: the old token is refused, new traffic still flows.
		setBackendTokens("tok-new")
		waitUntil(t, 30*time.Second, "the collector to refuse the old token", func() bool {
			return probe(backendURL, "tok-old") == http.StatusUnauthorized
		})
		if got := probe(backendURL, "tok-new"); got != http.StatusOK {
			t.Fatalf("the new token must be accepted, got %d", got)
		}
		exportUntilSeen(tp, "direct-after")

		// A runtime still holding the OLD token (the env-var form, read once)
		// is rejected: this is the failure the file removes.
		tp2, stop2 := setup(t, backendURL, map[string]string{"OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer tok-old"})
		defer stop2()
		exportNeverSeen(tp2, "direct-stale-env")
	})

	t.Run("agent: bearertokenauth{filename} on a kubelet-style volume", func(t *testing.T) {
		cfg := agentSmokeConfig(t, "http://"+backendName+":4318")
		writeConf(t, root, "agent.yaml", cfg)
		// The Secret does not exist yet: an empty volume, like an optional one.
		agentAuth := filepath.Join(root, "agent-auth")
		run("run", "-d", "--name", agentName, "--network", network,
			"-p", "127.0.0.1::4318",
			"-v", filepath.Join(root, "conf", "agent.yaml")+":/etc/otelcol/config.yaml:ro",
			"-v", agentAuth+":/etc/otel-auth:ro",
			image, "--config=/etc/otelcol/config.yaml")
		agentURL := publishedURL(t, run, agentName)
		waitUntil(t, 60*time.Second, "agent collector to accept connections", func() bool {
			return probe(agentURL, "") != 0
		})

		tp, stop := setup(t, agentURL, nil)
		defer stop()

		setBackendTokens("tok-new")
		exportNeverSeen(tp, "agent-no-token")
		if state := run("inspect", "-f", "{{.State.Running}}", agentName); state != "true" {
			t.Fatalf("the agent must survive a missing token Secret, running=%s\n%s", state, run("logs", "--tail", "30", agentName))
		}

		// The Secret appears: the agent picks it up without a restart.
		kubeletWrite(t, agentAuth, "tok-new")
		exportUntilSeen(tp, "agent-new")

		// Rotate both sides, collector accepting both during the overlap.
		setBackendTokens("tok-new", "tok-new2")
		kubeletWrite(t, agentAuth, "tok-new2")
		exportUntilSeen(tp, "agent-mid")
		setBackendTokens("tok-new2")
		waitUntil(t, 30*time.Second, "the collector to refuse the previous token", func() bool {
			return probe(backendURL, "tok-new") == http.StatusUnauthorized
		})
		exportUntilSeen(tp, "agent-after")

		if restarts := run("inspect", "-f", "{{.RestartCount}}", agentName); restarts != "0" {
			t.Fatalf("the agent restarted %s times; renewal must not restart it", restarts)
		}
	})
}

// agentSmokeConfig builds a runnable collector from the node agent forge
// renders: the bearer extension and the destination exporter are taken from
// pkg/deploy's own AgentConfig, so this fails if the rendered shape stops
// working. Only what needs a Kubernetes node (enrichment, filelog,
// persistent queue) is left out, and https becomes http for the local backend.
func agentSmokeConfig(t *testing.T, endpoint string) string {
	t.Helper()
	spec := deploy.ObservabilityAgentSpec{
		Endpoint:    "https://otlp.example.com",
		Protocol:    "http/protobuf",
		TokenSecret: &deploy.ObservabilitySecretKey{Name: "otlp-token", Key: "token"},
		Signals:     deploy.ObservabilitySignals{Traces: true},
	}
	rendered, err := spec.AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &full); err != nil {
		t.Fatal(err)
	}
	ext := full["extensions"].(map[string]any)["bearertokenauth"]
	exp := full["exporters"].(map[string]any)["otlphttp/destination"].(map[string]any)
	exp["endpoint"] = endpoint
	delete(exp["sending_queue"].(map[string]any), "storage")
	cfg := map[string]any{
		"extensions": map[string]any{"bearertokenauth": ext},
		"receivers":  map[string]any{"otlp": map[string]any{"protocols": map[string]any{"http": map[string]any{"endpoint": "0.0.0.0:4318"}}}},
		"exporters":  map[string]any{"otlphttp/destination": exp},
		"service": map[string]any{
			"extensions": []any{"bearertokenauth"},
			"pipelines": map[string]any{"traces": map[string]any{
				"receivers": []any{"otlp"}, "exporters": []any{"otlphttp/destination"},
			}},
		},
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// kubeletWrite replaces the token the way the kubelet refreshes a Secret
// volume: a new timestamped directory, an atomic swap of the ..data symlink,
// then removal of the previous directory. token -> ..data/token never changes.
func kubeletWrite(t *testing.T, dir, token string) {
	t.Helper()
	ts := fmt.Sprintf("..%d", time.Now().UnixNano())
	if err := os.Mkdir(filepath.Join(dir, ts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ts, "token"), []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Readlink(filepath.Join(dir, "..data"))
	tmp := filepath.Join(dir, "..data_tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(ts, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "token")); err != nil {
		if err := os.Symlink(filepath.Join("..data", "token"), filepath.Join(dir, "token")); err != nil {
			t.Fatal(err)
		}
	}
	if prev != "" {
		_ = os.RemoveAll(filepath.Join(dir, prev))
	}
}

func writeConf(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, "conf", name)
	if err := os.WriteFile(p, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// publishedURL resolves the host URL of a container's published 4318.
func publishedURL(t *testing.T, run func(...string) string, name string) string {
	t.Helper()
	out := run("port", name, "4318/tcp")
	line := strings.Fields(strings.Split(out, "\n")[0])
	if len(line) == 0 {
		t.Fatalf("no published port for %s: %q", name, out)
	}
	return "http://" + line[len(line)-1]
}

// probe POSTs an empty (valid) OTLP traces request with the bearer token and
// returns the HTTP status, or 0 when the connection fails.
func probe(base, token string) int {
	req, err := http.NewRequest(http.MethodPost, base+"/v1/traces", bytes.NewReader(nil))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}
