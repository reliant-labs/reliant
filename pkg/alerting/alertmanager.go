// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// AlertmanagerNotifier posts to an Alertmanager v2 API. Give it every replica of
// an HA Alertmanager (they gossip and de-duplicate); an alert is delivered if
// ANY replica accepts it.
type AlertmanagerNotifier struct {
	urls   []string
	client *http.Client
	// GeneratorURL, when set, is attached to every alert.
	GeneratorURL string
}

// NewAlertmanagerNotifier builds a notifier. baseURLs are scheme://host:port.
// A nil client gets a 10s-timeout default.
func NewAlertmanagerNotifier(client *http.Client, baseURLs ...string) *AlertmanagerNotifier {
	if client == nil {
		client = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 10 * time.Second})
	}
	urls := make([]string, 0, len(baseURLs))
	for _, u := range baseURLs {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			urls = append(urls, u)
		}
	}
	return &AlertmanagerNotifier{urls: urls, client: client}
}

type amAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

// Notify posts alerts to every replica.
func (n *AlertmanagerNotifier) Notify(ctx context.Context, alerts []Alert) error {
	if len(alerts) == 0 {
		return nil
	}
	if len(n.urls) == 0 {
		return fmt.Errorf("alerting: no Alertmanager URL configured")
	}
	wire := make([]amAlert, len(alerts))
	for i, a := range alerts {
		wire[i] = amAlert{Labels: a.Labels, Annotations: a.Annotations, StartsAt: a.StartsAt.UTC(), EndsAt: a.EndsAt.UTC(), GeneratorURL: n.GeneratorURL}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	var errs []string
	ok := false
	for _, u := range n.urls {
		if err := n.post(ctx, u+"/api/v2/alerts", body); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ok = true
	}
	if !ok {
		return fmt.Errorf("alerting: no Alertmanager accepted the alerts: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (n *AlertmanagerNotifier) post(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
