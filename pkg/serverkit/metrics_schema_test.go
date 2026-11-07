package serverkit_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// The declared-metrics schema is introspection of the process, so it rides the
// loopback debug listener beside pprof and never the public edge.
func TestRun_ServesMetricsSchemaOnTheDebugListenerOnly(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM through shutdownAndWait.
	mainAddr := freeAddr(t)
	debugAddr := freeAddr(t)

	cfg := baseConfig(mainAddr)
	cfg.PprofAddr = debugAddr
	cfg.ServiceName = "schemasvc"
	cfg.ServiceVersion = "v9.9.9"
	errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: emptyHandler()})
	waitReady(t, mainAddr, 2*time.Second)

	body := getWithin(t, "http://"+debugAddr+metrics.SchemaPath, 2*time.Second)
	schema, err := metrics.ParseSchema(body)
	if err != nil {
		t.Fatalf("%s is not a metrics schema: %v\n%s", metrics.SchemaPath, err, head(body))
	}
	if schema.Service != "schemasvc" || schema.Version != "v9.9.9" {
		t.Errorf("service/version = %q/%q, want schemasvc/v9.9.9", schema.Service, schema.Version)
	}
	have := map[string]bool{}
	for _, m := range schema.Metrics {
		have[m.Name] = true
	}
	for _, want := range []string{"rpc.server.calls", "go.memory.used", "go.sql.query_timing"} {
		if !have[want] {
			t.Errorf("served schema does not list built-in %s", want)
		}
	}

	resp, err := http.Get("http://" + mainAddr + metrics.SchemaPath)
	if err != nil {
		t.Fatalf("probe public addr: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("public listener answered %s with %d — the schema must stay on the debug listener", metrics.SchemaPath, resp.StatusCode)
	}

	if runErr := shutdownAndWait(t, errCh, 5*time.Second); runErr != nil {
		t.Fatalf("Run returned %v after shutdown", runErr)
	}
}

// ENVIRONMENT=development must make an undeclared attribute loud.
func TestRun_DevelopmentSelectsThePanickingMetricsMode(t *testing.T) {
	requireSelfSIGTERM(t)
	mainAddr := freeAddr(t)
	cfg := baseConfig(mainAddr)
	cfg.Environment = serverkit.EnvDevelopment
	// Development enforces CORS, so Run refuses to serve without a factory.
	passthrough := func([]string, bool) func(http.Handler) http.Handler {
		return func(h http.Handler) http.Handler { return h }
	}
	errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: emptyHandler(), CORSMiddleware: passthrough})
	waitReady(t, mainAddr, 2*time.Second)
	defer func() {
		_ = shutdownAndWait(t, errCh, 5*time.Second)
		metrics.Default().SetMode(metrics.ModeProduction)
	}()

	if got := metrics.Default().Mode(); got != metrics.ModeDevelopment {
		t.Errorf("mode = %v under ENVIRONMENT=development, want ModeDevelopment", got)
	}
}
