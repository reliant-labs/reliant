// Copyright (c) 2025 Reliant Labs

package alerting_test

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/alerting"
)

type oneRow struct{}

func (oneRow) Query(context.Context, string, string, alerting.QueryOptions) ([]alerting.Row, error) {
	return []alerting.Row{{"service": "api", "value": 912.0}}, nil
}

type printNotifier struct{}

func (printNotifier) Notify(_ context.Context, alerts []alerting.Alert) error {
	for _, a := range alerts {
		fmt.Println(a.Labels["alertname"], a.Labels["service"])
	}
	return nil
}

// A single-owner deployment: a rules file, one ClickHouse, in-memory state.
// Swap oneRow for chquery.New(conn), printNotifier for
// alerting.NewAlertmanagerNotifier(nil, "http://alertmanager:9093"), and the
// MemoryStore for pgstore.New(db, "") to run it for real.
func Example() {
	ev, _ := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier: oneRow{}, Notifier: printNotifier{}, Store: alerting.NewMemoryStore(),
	})
	rule := alerting.RuleSpec{
		Alert: "SlowService",
		Expr:  "SELECT service, quantile(0.95)(duration_ms) AS value FROM spans GROUP BY service HAVING value > 800",
	}.Rule("self", "prod", map[string]string{"reliant_org_id": "self"})

	_, _ = ev.EvaluateRule(context.Background(), rule, nil)
	_ = time.Second
	// Output: SlowService api
}
