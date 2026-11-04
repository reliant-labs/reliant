package cli

// `forge obs alerts run` — the local alerting loop's evaluator, the
// scaffolded `alerts` host workload. It evaluates the rules document
// `forge env up` renders, continuously, against the local ClickStack (the
// same Querier `check` uses), posts what fires to the local Alertmanager, and
// receives the in-app receiver's deliveries, printing one line per alert: the
// process's output is .forge/logs/<env>/alerts.log, the local stand-in for
// in-app notifications.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/alerting/amconfig"
)

type obsAlertsRunOpts struct {
	env          string
	alertmanager string
	listen       string
	timeout      time.Duration
	maxRows      int
}

func newObsAlertsRunCmd() *cobra.Command {
	o := obsAlertsRunOpts{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Evaluate the env's alert rules continuously and deliver what fires through the local Alertmanager",
		Long: `The local alerting loop's evaluator, which ` + "`forge env up`" + ` runs as the ` + "`alerts`" + ` host
workload (deploy/kcl/dev/main.k). It

  * evaluates the rules ` + "`forge env up`" + ` rendered into ` + clickstack.AlertRulesFile + `
    (the env's forge.AlertRule entries plus the generated defaults, exactly what
    ` + "`forge env shape`" + ` records) on their intervals, ` + "`for`" + ` applied, against the
    local ClickStack;
  * posts every firing and resolved alert to the local Alertmanager
    (--alertmanager), labelled reliant_org_id="` + clickstack.AlertOrg + `" and reliant_env_id=<env>,
    which routes it with the control plane's own configuration renderer;
  * receives the in-app receiver's deliveries on --listen and prints one line
    per alert, so .forge/logs/<env>/alerts.log is the local in-app inbox.

The rules file is re-read when it changes, so a re-run of ` + "`forge env up`" + ` updates
the rules without a restart.

  ` + Name() + ` obs alerts run --env dev --alertmanager http://127.0.0.1:9093 --listen 0.0.0.0:9095`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsAlertsRun(cmd.Context(), obsout.NewSanitizingWriter(cmd.OutOrStdout()), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.env, "env", "dev", "environment whose rules to evaluate")
	f.StringVar(&o.alertmanager, "alertmanager", "", "the local Alertmanager's base URL (required)")
	f.StringVar(&o.listen, "listen", "", "address of the in-app sink the local Alertmanager delivers to, host:port (required). It must be reachable from the Alertmanager container, so not 127.0.0.1")
	f.DurationVar(&o.timeout, "timeout", 20*time.Second, "per-rule query timeout")
	f.IntVar(&o.maxRows, "max-rows", 200, "a rule returning more rows than this fails its evaluation")
	return cmd
}

func runObsAlertsRun(ctx context.Context, stdout io.Writer, o obsAlertsRunOpts) error {
	if o.alertmanager == "" || o.listen == "" {
		return errors.New("--alertmanager and --listen are required (the scaffolded `alerts` workload in deploy/kcl/<env>/main.k passes both)")
	}
	dir, err := requireProjectDir("obs alerts run")
	if err != nil {
		return err
	}
	tokenPath := filepath.Join(dir, clickstack.AlertmanagerDir, filepath.Base(amconfig.SecretDir), clickstack.AlertsInAppTokenFile)
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("reading the in-app receiver's token (%s): %w — `forge env up %s` renders it", tokenPath, err, o.env)
	}
	token := strings.TrimSpace(string(raw))

	out := &syncWriter{w: stdout}
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return fmt.Errorf("the in-app sink cannot listen on %s: %w", o.listen, err)
	}
	srv := &http.Server{Handler: alertSinkHandler(token, out), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("in-app sink stopped", "error", err)
		}
	}()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	ev, err := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier:  clickhouseAlertQuerier{run: alertQueryRunnerFor(o.env)},
		Notifier: alerting.NewAlertmanagerNotifier(nil, o.alertmanager),
		Store:    alerting.NewMemoryStore(),
		Logger:   log,
		Limits:   alerting.Limits{MaxExecutionTime: o.timeout, MaxInstancesPerRule: o.maxRows},
	})
	if err != nil {
		return err
	}
	src := &alerting.FileRuleSource{
		Path:  filepath.Join(dir, clickstack.AlertRulesFile),
		Owner: clickstack.AlertOrg, Env: o.env,
		Identity: map[string]string{amconfig.OrgLabel: clickstack.AlertOrg, "reliant_env_id": o.env},
		Logger:   log,
	}
	sched, err := alerting.NewScheduler(alerting.SchedulerDeps{
		Rules: src, Store: alerting.NewMemoryStore(), Eval: ev, Shards: 1, Logger: log,
	})
	if err != nil {
		return err
	}
	log.Info("evaluating alert rules", "env", o.env, "rules", src.Path, "alertmanager", o.alertmanager, "in_app_sink", o.listen)
	if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// amWebhook is Alertmanager's webhook payload (version 4), the part printed.
type amWebhook struct {
	Receiver string `json:"receiver"`
	Status   string `json:"status"`
	Alerts   []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
	} `json:"alerts"`
}

// alertSinkHandler receives the in-app receiver's deliveries and prints one
// line per alert:
//
//	ALERT firing   CheckoutSlow severity=critical service=api — checkout p95 is 1.2s
func alertSinkHandler(token string, out io.Writer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST "+clickstack.AlertsSinkPath, func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var p amWebhook
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&p); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		for _, a := range p.Alerts {
			fmt.Fprintln(out, alertLine(a.Status, a.Labels, a.Annotations))
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// alertLine is one delivered alert, for a human reading alerts.log.
func alertLine(status string, labels, annotations map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ALERT %-8s %s", status, labels["alertname"])
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == "alertname" || strings.HasPrefix(k, alerting.ReservedPrefix) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%s", k, labels[k])
	}
	if s := annotations["summary"]; s != "" {
		fmt.Fprintf(&b, " — %s", s)
	}
	return b.String()
}

// syncWriter serializes the sink's lines and the logger's onto one stream.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
