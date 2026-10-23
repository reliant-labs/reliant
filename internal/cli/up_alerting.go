package cli

// The local alerting loop. Beside the local ClickStack, `forge env up` runs
//
//   - an Alertmanager (docker-compose.yml's `alertmanager`), configured by the
//     control plane's own renderer (forge/pkg/alerting/amconfig) from the
//     env's declared alerts, with one org ("local") and the env name as its
//     env id, so a route reaches the same receiver here as in production;
//   - the evaluator (`forge obs alerts run`, the scaffolded `alerts` host
//     workload), which evaluates the env's rules continuously against the
//     local ClickHouse, posts what fires to that Alertmanager, and receives
//     the in-app receiver's deliveries into .forge/logs/<env>/alerts.log.
//
// This file renders what both read, before compose starts the container:
// the Alertmanager directory (configuration + secret files) and the rules
// document.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/alerting/amconfig"
	"github.com/reliant-labs/forge/pkg/release"
)

// localAlertingOn reports whether the env runs the local alerting loop.
func localAlertingOn(e *KCLEntities) bool {
	return e != nil && e.Observability != nil && e.Observability.Enabled &&
		e.Observability.AlertmanagerPort > 0 && e.Observability.AlertsPort > 0
}

// localAlerting is what one render produced, for the caller to report.
type localAlerting struct {
	Rules     int
	Receivers []string
	Skipped   []string
	Hash      string
}

// prepareLocalAlerting renders the local Alertmanager's directory and the
// evaluator's rules document for env. A no-op when the env runs no local
// loop. store is the env's secret store (receiver secrets are read from it
// by key and written as files; their values never reach the config).
func prepareLocalAlerting(projectDir, env string, e *KCLEntities, store map[string]string) (*localAlerting, error) {
	if !localAlertingOn(e) {
		return nil, nil
	}
	alerts, routes, err := resolveShapeAlerts(projectDir, env, e)
	if err != nil {
		return nil, err
	}
	doc, err := localRulesDocument(alerts, env)
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(projectDir, clickstack.AlertmanagerDir)
	secretsDir := filepath.Join(dir, filepath.Base(amconfig.SecretDir))
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		return nil, err
	}
	token, err := localInAppToken(secretsDir)
	if err != nil {
		return nil, err
	}

	var decl []AlertReceiverEntity
	if e.Observability.Alerts != nil {
		decl = e.Observability.Alerts.Receivers
	}
	in, values, skipped := localAlertInput(env, e.Observability.AlertsPort, decl, routes, store)
	r, err := amconfig.Render(in)
	if err != nil {
		return nil, err
	}

	// Secret files first, then the config that names them: a reload between
	// the two writes must never load a config naming a file that is not there.
	values[clickstack.AlertsInAppTokenFile] = token
	want := map[string]bool{clickstack.AlertsInAppTokenFile: true}
	for _, f := range r.Files {
		v, ok := values[f.Name]
		if !ok {
			return nil, fmt.Errorf("local alerting: the renderer wants secret file %s, which nothing provided", f.Name)
		}
		// World-readable on purpose: the container runs as nobody and reads
		// it through a bind mount. A dev secret, under the gitignored .forge.
		if err := atomicfile.WriteFile(filepath.Join(secretsDir, f.Name), []byte(v), 0o644); err != nil {
			return nil, err
		}
		want[f.Name] = true
	}
	if err := removeStaleFiles(secretsDir, want); err != nil {
		return nil, err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, clickstack.AlertmanagerConfigFile), r.YAML, 0o644); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicfile.WriteFile(filepath.Join(projectDir, clickstack.AlertRulesFile), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}

	out := &localAlerting{Rules: len(doc.Alerts), Skipped: append(skipped, r.Skipped...), Hash: r.Hash}
	for _, rc := range in.Receivers {
		out.Receivers = append(out.Receivers, rc.Name)
	}
	return out, nil
}

// localRulesDocument is the env's resolved rules as the evaluator runs them:
// the platform's own rules dropped (the control plane raises those from its
// events, and there is nothing local to query), the env id bound.
func localRulesDocument(alerts []release.ShapeAlert, env string) (alerting.RulesDocument, error) {
	doc := alerting.RulesDocument{Alerts: alerting.RuleSet{}}
	for _, a := range alerts {
		if obsdash.IsPlatformExpr(a.Expr) {
			continue
		}
		spec, err := ruleSpecOf(a)
		if err != nil {
			return doc, fmt.Errorf("alert %s: %w", a.Alert, err)
		}
		spec.Expr = obsdash.BindEnvID(spec.Expr, env)
		doc.Alerts = append(doc.Alerts, spec)
	}
	if err := doc.Alerts.Validate(); err != nil {
		return doc, err
	}
	return doc, nil
}

// localAlertInput maps the env's declaration onto the renderer's input: one
// org, the always-present in-app receiver (the evaluator's alert-log sink),
// the declared receivers and the shape's routes. It returns the secret file
// values by file name, and what it left out and why.
func localAlertInput(env string, alertsPort int, decl []AlertReceiverEntity, routes []release.ShapeAlertRoute, store map[string]string) (amconfig.Input, map[string]string, []string) {
	org := clickstack.AlertOrg
	in := amconfig.Input{Settings: amconfig.Settings{
		InAppURL:          "http://" + forwarderHostAlias + ":" + strconv.Itoa(alertsPort) + clickstack.AlertsSinkPath,
		InAppTokenFile:    amconfig.SecretDir + "/" + clickstack.AlertsInAppTokenFile,
		AllowHTTPWebhooks: true,
	}}
	in.Receivers = append(in.Receivers, amconfig.Receiver{OrgID: org, Name: amconfig.DefaultReceiverName, Type: amconfig.TypeInApp})

	values := map[string]string{}
	var skipped []string
	for _, d := range decl {
		rc := amconfig.Receiver{OrgID: org, Name: d.Name, Type: d.Type, Settings: map[string]string{}}
		if d.URL != "" {
			rc.Settings["url"] = hostReachableEndpoint(d.URL)
		}
		if d.Channel != "" {
			rc.Settings["channel"] = d.Channel
		}
		if d.Secret != "" {
			v, ok := store[d.Secret]
			if !ok || v == "" {
				skipped = append(skipped, fmt.Sprintf("receiver %q: secret %s is not in the %s secret store (forge secret set --env %s %s)", d.Name, d.Secret, env, env, d.Secret))
				continue
			}
			rc.SecretRef = org + "." + d.Name
			values[rc.SecretRef] = v
		}
		if d.Type == amconfig.TypeAutopilot {
			rc.Autopilot = &amconfig.AutopilotRoute{EnvIDs: []string{env}, Severities: d.Severities, AlwaysAlertnames: d.AlwaysAlertnames}
		}
		in.Receivers = append(in.Receivers, rc)
	}
	for i, r := range routes {
		in.Routes = append(in.Routes, amconfig.Route{
			OrgID: org, EnvID: env, Position: i, Matchers: r.Match, Receivers: []string{r.Receiver}, Continue: r.Continue,
		})
	}
	return in, values, skipped
}

// localInAppToken is the in-app receiver's bearer: made once, then kept, so a
// re-render does not invalidate what a running evaluator already read.
func localInAppToken(secretsDir string) (string, error) {
	p := filepath.Join(secretsDir, clickstack.AlertsInAppTokenFile)
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	return tok, atomicfile.WriteFile(p, []byte(tok), 0o644)
}

// removeStaleFiles deletes the files in dir that are not in keep: a receiver
// removed from the declaration takes its secret with it.
func removeStaleFiles(dir string, keep map[string]bool) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, ent := range ents {
		if ent.IsDir() || keep[ent.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, ent.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// reloadLocalAlertmanager asks a running local Alertmanager to load the
// configuration just rendered. Not running is not an error: compose starts
// it on that configuration.
func reloadLocalAlertmanager(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/-/reload", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil // not up yet
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("the local Alertmanager refused the configuration: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

// printLocalAlerting is the one informational block env up prints.
func printLocalAlerting(w io.Writer, env string, la *localAlerting) {
	if la == nil {
		return
	}
	fmt.Fprintf(w, "  local alerting: %d rule(s) → Alertmanager → %s (in-app: .forge/logs/%s/%s.log)\n",
		la.Rules, strings.Join(la.Receivers, ", "), env, clickstack.AlertsWorkload)
	for _, s := range la.Skipped {
		fmt.Fprintf(w, "    not routed: %s\n", s)
	}
}
