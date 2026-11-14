package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/devtelemetry"
)

// devReplayRequested is an environment's explicit ingestion opt-in. Legacy
// environments and disabled browser telemetry never request a replay grant.
func devReplayRequested(e *KCLEntities) bool {
	return e != nil && e.Observability != nil && e.Observability.Enabled &&
		e.Observability.Signals.Browser && e.Observability.Signals.Replay
}

// stopReplayForReloads only withdraws capture from future page loads. It
// never enables it late, nor claims to stop an already-running SDK. The token
// controls ingestion independently; prior tokens retain their normal TTL.
func (r *devTelemetryRun) stopReplayForReloads() error {
	docs := map[string]string{}
	e := &KCLEntities{Frontends: r.frontends}
	cfg := runtimeDocInventory(&config.ProjectConfig{}, e)
	for _, f := range cfg.Frontends {
		staticDir := frontendStaticDir(f.Type)
		if staticDir == "" {
			continue
		}
		dir, inTree := f.Dir(r.projectDir)
		path := filepath.Join(r.projectDir, outOfTreeRuntimeConfigRel(f.Name))
		if inTree {
			path = filepath.Join(r.projectDir, dir, staticDir, codegen.FrontendConfigJSFile)
		}
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		_, value, ok := strings.Cut(string(body), "window.__FORGE_CONFIG__ = ")
		if !ok {
			return fmt.Errorf("cannot withdraw replay from unreadable runtime config %s", path)
		}
		var values map[string]any
		decoder := json.NewDecoder(strings.NewReader(strings.TrimSuffix(strings.TrimSpace(value), ";")))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return err
		}
		if values == nil {
			return fmt.Errorf("runtime config %s is not an object", path)
		}
		values["OTEL_REPLAY"] = "false"
		b, err := json.MarshalIndent(values, "", "  ")
		if err != nil {
			return err
		}
		docs[f.Name] = codegen.GenerateFrontendConfigJS(f.Name, r.env, string(b))
	}
	if _, err := writeFrontendRuntimeDocs(cfg, r.projectDir, docs); err != nil {
		return err
	}
	if r.log != nil {
		fmt.Fprintln(r.log, "session replay ingestion stopped for the renewed grant; reload/restart to update capture (older grants retain their normal expiry)")
	}
	return nil
}

func printLocalReplayAvailability(e *KCLEntities, facts devTelemetryFacts, mode devTelemetryMode, reason string, w io.Writer) {
	// This is the local decision's safety action, independent of whether a
	// hosted fallback notice is relevant. A destination without a reachable
	// local proxy upstream is not a collector; disabled observability is off.
	if !localReplayAvailable(e) {
		disableDevReplay(e)
		fmt.Fprintln(w, "[up] session recording: unavailable without an enabled local collector; capture is off")
	}
	if !facts.ControlPlane || !facts.TelemetryOn {
		return
	}
	fmt.Fprintf(w, "[up] telemetry: local (%s)\n", reason)
	if !envHasNoTelemetryStore(e) {
		return
	}
	hint := ""
	if mode.Value != devTelemetryLocal {
		hint = fmt.Sprintf("; sign in to Reliant (`%s login`) to ship it to your org's hosted store", Name())
	}
	fmt.Fprintf(w, "[up]   this env runs no local store (no container, no ClickStack), so nothing is exported or kept%s\n", hint)
}

func localReplayAvailable(e *KCLEntities) bool {
	if !devReplayRequested(e) {
		return false
	}
	o := e.Observability
	return o.LocalEndpoint != "" || o.LocalPort > 0 || e.HasLocalStore()
}

var errDevReplayWithdrawal = errors.New("renewed grant committed but replay capture withdrawal failed")

// Stop only this stack's frontend servers/proxies when their served capture
// policy cannot be made safe. The existing ledger fences process ownership.
var stopReplayFrontends = func(projectDir, env string, names []string) error {
	_, err := stopStackScopedWithFacts(projectIDForDir(projectDir), env, names, newOSProcFacts())
	return err
}

// The grant is already committed. A failure here must stop the keeper, not
// retry a mint against the old expiry or claim future capture was disabled.
func (r *devTelemetryRun) replayWithdrawalFailed(cause error, now time.Time) error {
	names := make([]string, 0, len(r.frontends))
	for _, f := range r.frontends {
		if f.Type == "vite" || f.Type == "nextjs" {
			names = append(names, "frontend:"+f.Name)
		}
	}
	if len(names) > 0 {
		if stopErr := stopReplayFrontends(r.projectDir, r.env, names); stopErr != nil {
			cause = errors.Join(cause, fmt.Errorf("stopping frontend(s): %w", stopErr))
		}
	}
	err := fmt.Errorf("%w: cannot confirm capture is off; repair runtime config and restart: %v", errDevReplayWithdrawal, cause)
	r.mu.Lock()
	g := r.grant
	r.mu.Unlock()
	_ = devtelemetry.WriteState(r.projectDir, devtelemetry.State{Env: r.env, ExpiresAt: g.expires, RenewedAt: now, Retention: g.Retention, RenewError: err.Error()})
	if r.log != nil {
		fmt.Fprintln(r.log, err)
	}
	return err
}

func disableDevReplay(e *KCLEntities) {
	for i := range e.Frontends {
		f := &e.Frontends[i]
		if f.Type != "vite" && f.Type != "nextjs" {
			continue
		}
		config := make(map[string]string, len(f.RuntimeConfig)+1)
		for k, v := range f.RuntimeConfig {
			config[k] = v
		}
		config["OTEL_REPLAY"] = "false"
		f.RuntimeConfig = config
	}
}
