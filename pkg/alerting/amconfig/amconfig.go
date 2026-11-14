// Copyright (c) 2025 Reliant Labs

// Package amconfig renders an Alertmanager configuration from stored receivers
// and routes. It is the part of alerting where a bug becomes org A's alert
// reaching org B's Slack.
//
// It is the ONE renderer: the control plane renders every org's configuration
// with it from its Postgres rows, and `forge env up` renders a local stack's
// configuration with it from the env's KCL (one org, "local"), so an alert
// routes the same way on a laptop as in production. The package knows nothing
// about where receivers are stored or how the config reaches Alertmanager.
//
// An org is the unit of routing isolation: a self-hoster with one team uses one.
//
// # The routing invariant
//
// The rendered root route has the BLACKHOLE receiver as its default and one
// child route per org, each matched STRICTLY on `reliant_org_id="<org>"` with
// continue: false. Everything below an org's route is that org's own: its env
// routes (matched by labels the rule author set) and its receivers. Nothing an
// alert, a rule or an env route says can move it out of its org's subtree,
// because:
//
//   - reliant_org_id is set by the evaluator from the RuleSource's identity and
//     can never come from a rule (forge/pkg/alerting strips reliant_*);
//   - every child's own matchers are ANDed under the org matcher, so they
//     narrow, never widen;
//   - env routes and receivers are loaded per org and an env route naming a
//     receiver the org does not have is DROPPED at render time (and reported),
//     so a route can only ever point at the receiver of its own org;
//   - receiver names are namespaced per org ("<org>/<name>"), so two orgs that
//     both call a receiver "oncall" cannot collide into one;
//   - an org with no receivers gets no route and therefore the blackhole.
//
// # Autopilot receivers
//
// An autopilot receiver (a Reliant Autopilot automation) is reached by no env
// route. It gets a route of its own at the TOP of its org's subtree, with
// continue: true, so every alert of the org that passes its filter reaches it
// and then carries on to the org's own routing exactly as before. Three things
// keep that true:
//
//   - the route sits inside the org's subtree, so it narrows the org matcher
//     like any child and cannot see another org's alerts;
//   - it is scoped to the environments its automation's project owns
//     (reliant_env_id, resolved by the caller into Autopilot.EnvIDs) — an
//     autopilot receiver with no environment in scope gets no route at all;
//   - the org's own routing is wrapped in ONE catch-all child that keeps the
//     org's default receiver. Alertmanager uses a parent's receiver only when
//     no child matched, so without the wrapper a match on the autopilot route
//     would silently cost the org its default (in-app) delivery.
//
// The filter is a severity match OR an alert-name match (the platform's issue
// alerts, whatever their severity); Alertmanager ANDs a route's matchers, so the
// OR is two children of a blackhole parent, the first match winning.
//
// TestNoRouteDeliversAcrossOrgs enumerates this property over the real
// Alertmanager routing tree (dispatch.NewRoute), not over the YAML.
//
// # Secrets
//
// Receiver secrets are never in the config. Each is a file in the mounted
// Secret, referenced through the receiver's *_file fields (slack api_url_file,
// pagerduty routing_key_file, webhook http_config.authorization.credentials_file,
// email auth_password_file). The renderer returns the set of files the Secret
// must contain; it never sees a value.
package amconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Blackhole is the default receiver: it has no integrations, so an alert that
// reaches it is delivered to no one.
const Blackhole = "blackhole"

// TestLabel selects exactly one receiver of an org for a test notification. Only
// the platform sets it: the evaluator drops every "reliant_" label a rule declares
// and routeNode refuses a matcher on one, so no rule can reach a receiver this way.
const TestLabel = "reliant_test_receiver"

// OrgLabel is the label every route is scoped by.
const OrgLabel = "reliant_org_id"

// SecretDir is where the Alertmanager Secret is mounted.
const SecretDir = "/etc/alertmanager/secrets"

// Receiver types, as stored.
const (
	TypeSlack     = "slack"
	TypeWebhook   = "webhook"
	TypeEmail     = "email"
	TypePagerDuty = "pagerduty"
	TypeInApp     = "reliant_inapp"
	TypeAutopilot = "autopilot"
)

// Receiver is one stored receiver.
type Receiver struct {
	OrgID string
	Name  string
	Type  string
	// SecretRef is the file name (inside SecretDir) holding the secret, or "".
	SecretRef string
	Settings  map[string]string
	// Autopilot is where a TypeAutopilot receiver's route reaches; nil for
	// every other type (and an autopilot receiver without it gets no route).
	Autopilot *AutopilotRoute
}

// AutopilotRoute is the resolved filter of an autopilot receiver.
type AutopilotRoute struct {
	// EnvIDs are the environments whose alerts reach it. Empty: none do.
	EnvIDs []string
	// Severities are the severity label values that reach it; a single "*"
	// is every alert.
	Severities []string
	// AlwaysAlertnames reach it whatever their severity.
	AlwaysAlertnames []string
}

// Route is one env route: labels the rule author set, mapped to receiver names.
type Route struct {
	OrgID     string
	EnvID     string
	Position  int
	Matchers  map[string]string
	Receivers []string
	Continue  bool
}

// Settings are the process-wide parts of the configuration.
type Settings struct {
	// InAppURL is the endpoint the reliant_inapp receivers post to: the
	// control plane's in-app notification webhook, or a local stack's alert
	// log sink.
	InAppURL string
	// InAppTokenFile holds the bearer token that endpoint requires.
	InAppTokenFile string
	// AllowHTTPWebhooks lets a webhook receiver's url be plain http. Only a
	// LOCAL stack sets it, where every receiver is the developer's own machine.
	// A renderer serving several orgs must leave it false: a customer-supplied http URL
	// could name a cluster-internal service, and Alertmanager would post to it
	// from inside the cluster.
	AllowHTTPWebhooks bool
	// SMTP is Resend's relay. Empty Host disables email receivers (they are
	// dropped and reported, never rendered half-configured).
	SMTP SMTP
	// GroupWait / GroupInterval / RepeatInterval default to 30s / 5m / 4h.
	GroupWait, GroupInterval, RepeatInterval string
}

// SMTP is the email relay. The password is a FILE, like every secret.
type SMTP struct {
	Host         string // host:port, e.g. smtp.resend.com:587
	From         string
	Username     string // Resend: "resend"
	PasswordFile string
}

// Input is everything the renderer reads.
type Input struct {
	Receivers []Receiver
	Routes    []Route
	Settings  Settings
}

// Rendered is the renderer's result.
type Rendered struct {
	// YAML is the Alertmanager configuration.
	YAML []byte
	// Hash identifies the configuration; a syncer compares it to what is live.
	Hash string
	// Files are the secret files the Secret must contain: name -> the org and
	// receiver that need it (the caller fills in the values from its own
	// secret storage).
	Files []SecretFile
	// Skipped says what was left out and why. Never an error: one bad receiver
	// must not take every other org's alerting down.
	Skipped []string
}

// SecretFile is one file the mounted Secret must hold.
type SecretFile struct {
	Name     string
	OrgID    string
	Receiver string
}

var (
	orgIDRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	nameRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	labelRe    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	emailRe    = regexp.MustCompile(`^[^@\s,<>]+@[^@\s,<>]+\.[^@\s,<>]+$`)
	secretRefs = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// RecvName is the Alertmanager receiver name for an org's receiver.
func RecvName(org, name string) string { return org + "/" + name }

// Render builds the configuration. It is pure: same Input, same bytes.
func Render(in Input) (Rendered, error) {
	set := in.Settings
	if set.GroupWait == "" {
		set.GroupWait = "30s"
	}
	if set.GroupInterval == "" {
		set.GroupInterval = "5m"
	}
	if set.RepeatInterval == "" {
		set.RepeatInterval = "4h"
	}

	var out Rendered
	skip := func(format string, a ...any) { out.Skipped = append(out.Skipped, fmt.Sprintf(format, a...)) }

	byOrg := map[string]map[string]Receiver{}
	for _, r := range in.Receivers {
		if !orgIDRe.MatchString(r.OrgID) {
			skip("receiver %q: org id %q is not usable as a routing matcher", r.Name, r.OrgID)
			continue
		}
		if !nameRe.MatchString(r.Name) {
			skip("org %s: receiver name %q is invalid", r.OrgID, r.Name)
			continue
		}
		if byOrg[r.OrgID] == nil {
			byOrg[r.OrgID] = map[string]Receiver{}
		}
		byOrg[r.OrgID][r.Name] = r
	}

	var (
		recvs    []map[string]any
		orgs     = sortedKeys(byOrg)
		children []map[string]any
	)
	recvs = append(recvs, map[string]any{"name": Blackhole})

	for _, org := range orgs {
		live := map[string]bool{} // names that rendered
		for _, name := range sortedKeys(byOrg[org]) {
			r := byOrg[org][name]
			node, files, err := receiverNode(r, set)
			if err != nil {
				skip("org %s: receiver %q: %v", org, name, err)
				continue
			}
			recvs = append(recvs, node)
			out.Files = append(out.Files, files...)
			live[name] = true
		}
		if len(live) == 0 {
			continue // no usable receiver: the org falls through to the blackhole
		}

		// Test routes come FIRST inside the org's subtree, one per receiver, so a
		// test alert reaches that receiver alone and not whatever the env routes
		// would also have chosen. They sit under the org matcher: another org's
		// alert carrying the same label value still cannot leave its own subtree.
		var tests, autopilots, own []map[string]any
		for _, name := range sortedKeys(byOrg[org]) {
			if live[name] {
				tests = append(tests, map[string]any{
					"matchers": []string{matcher(TestLabel, name)},
					"receiver": RecvName(org, name),
					"continue": false,
				})
			}
		}
		// Autopilot routes next, each continue: true: they observe, and the
		// alert goes on to the org's own routing below.
		for _, name := range sortedKeys(byOrg[org]) {
			if r := byOrg[org][name]; live[name] && r.Type == TypeAutopilot {
				if node := autopilotNode(org, name, r.Autopilot); node != nil {
					autopilots = append(autopilots, node)
				}
			}
		}
		for _, rt := range orgRoutes(in.Routes, org) {
			node, err := routeNode(rt, org, live)
			if err != nil {
				skip("org %s env %s route %d: %v", org, rt.EnvID, rt.Position, err)
				continue
			}
			own = append(own, node)
		}
		// the org's default: with no env route matching, the org's in-app
		// receiver (or first receiver) gets it.
		def := defaultReceiver(org, byOrg[org], live)
		routes := tests
		if len(autopilots) > 0 {
			// A matched autopilot route would otherwise count as "a child
			// matched" and cost the org its default receiver: the org's own
			// routing becomes one catch-all child that keeps it (see the
			// package comment).
			wrapper := map[string]any{"receiver": def, "continue": false}
			if len(own) > 0 {
				wrapper["routes"] = own
			}
			routes = append(append(routes, autopilots...), wrapper)
		} else {
			routes = append(routes, own...)
		}
		child := map[string]any{
			"matchers": []string{matcher(OrgLabel, org)},
			"receiver": def,
			"continue": false,
		}
		if len(routes) > 0 {
			child["routes"] = routes
		}
		children = append(children, child)
	}

	root := map[string]any{
		"receiver":        Blackhole,
		"group_by":        []string{OrgLabel, "reliant_env_id", "alertname"},
		"group_wait":      set.GroupWait,
		"group_interval":  set.GroupInterval,
		"repeat_interval": set.RepeatInterval,
	}
	if len(children) > 0 {
		root["routes"] = children
	}
	cfg := map[string]any{
		"global": map[string]any{
			"resolve_timeout": "5m",
		},
		"route":     root,
		"receivers": recvs,
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return Rendered{}, fmt.Errorf("amconfig: encoding: %w", err)
	}
	sum := sha256.Sum256(b)
	out.YAML, out.Hash = b, hex.EncodeToString(sum[:])
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	return out, nil
}

func orgRoutes(all []Route, org string) []Route {
	var out []Route
	for _, r := range all {
		if r.OrgID == org {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EnvID != out[j].EnvID {
			return out[i].EnvID < out[j].EnvID
		}
		return out[i].Position < out[j].Position
	})
	return out
}

// routeNode is one env route, scoped to its org and env. The matchers it adds
// are the rule author's; the env matcher keeps one env's routes from catching
// another env's alerts.
func routeNode(rt Route, org string, live map[string]bool) (map[string]any, error) {
	if len(rt.Receivers) == 0 {
		return nil, errors.New("names no receiver")
	}
	var ms []string
	if rt.EnvID != "" {
		ms = append(ms, matcher("reliant_env_id", rt.EnvID))
	}
	for _, k := range sortedKeys(rt.Matchers) {
		if !labelRe.MatchString(k) {
			return nil, fmt.Errorf("matcher label %q is not a label name", k)
		}
		if strings.HasPrefix(strings.ToLower(k), "reliant_") {
			return nil, fmt.Errorf("matcher label %q is reserved: the platform sets it", k)
		}
		ms = append(ms, matcher(k, rt.Matchers[k]))
	}
	for _, name := range rt.Receivers {
		if !live[name] {
			return nil, fmt.Errorf("receiver %q does not exist in this org", name)
		}
	}
	// The first receiver is the route's; further ones become continue-chained
	// siblings in the SAME org subtree.
	node := map[string]any{"receiver": RecvName(org, rt.Receivers[0]), "continue": rt.Continue || len(rt.Receivers) > 1}
	if len(ms) > 0 {
		node["matchers"] = ms
	}
	if len(rt.Receivers) > 1 {
		var sub []map[string]any
		for i, name := range rt.Receivers {
			sub = append(sub, map[string]any{"receiver": RecvName(org, name), "continue": i < len(rt.Receivers)-1})
		}
		node["receiver"] = RecvName(org, rt.Receivers[0])
		node["routes"] = sub
		node["continue"] = rt.Continue
	}
	return node, nil
}

// DefaultReceiverName is the platform-owned in-app receiver every org with rules
// gets (the syncer adds it). It is the org's default, so what an org names its own
// receivers can never change where unrouted alerts go.
const DefaultReceiverName = "reliant-inapp"

// defaultReceiver is the Alertmanager name of where an org's alerts go when no
// env route matches: the platform's in-app receiver if the org has it, else any
// in-app receiver of the org's (alphabetically first), else the org's
// alphabetically first receiver. Never an autopilot receiver: one is reached
// only through its filtered route, so an org whose only receivers are
// autopilot ones defaults to the blackhole.
func defaultReceiver(org string, recvs map[string]Receiver, live map[string]bool) string {
	if live[DefaultReceiverName] && recvs[DefaultReceiverName].Type != TypeAutopilot {
		return RecvName(org, DefaultReceiverName)
	}
	var names []string
	for n := range live {
		if recvs[n].Type != TypeAutopilot {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return Blackhole
	}
	sort.Strings(names)
	for _, n := range names {
		if recvs[n].Type == TypeInApp {
			return RecvName(org, n)
		}
	}
	return RecvName(org, names[0])
}

// autopilotNode is an autopilot receiver's route: scoped to its environments,
// continue: true, and delivering on a severity match OR an alert-name match.
// nil when nothing could reach it.
func autopilotNode(org, name string, ap *AutopilotRoute) map[string]any {
	if ap == nil || len(ap.EnvIDs) == 0 {
		return nil
	}
	recv := RecvName(org, name)
	node := map[string]any{
		"matchers": []string{regexMatcher("reliant_env_id", ap.EnvIDs)},
		"continue": true,
	}
	if len(ap.Severities) == 1 && ap.Severities[0] == "*" {
		node["receiver"] = recv
		return node
	}
	// The parent delivers nowhere: it is reached only to try its children,
	// and an alert neither child takes stops here (and, continue: true, goes
	// on to the org's own routing).
	node["receiver"] = Blackhole
	var sub []map[string]any
	if len(ap.Severities) > 0 {
		sub = append(sub, map[string]any{"matchers": []string{regexMatcher("severity", ap.Severities)}, "receiver": recv, "continue": false})
	}
	if len(ap.AlwaysAlertnames) > 0 {
		sub = append(sub, map[string]any{"matchers": []string{regexMatcher("alertname", ap.AlwaysAlertnames)}, "receiver": recv, "continue": false})
	}
	if len(sub) == 0 {
		return nil
	}
	node["routes"] = sub
	return node
}

// regexMatcher matches a label equal to any of values. Each value is matched
// literally (Alertmanager anchors the whole expression).
func regexMatcher(name string, values []string) string {
	vs := make([]string, len(values))
	for i, v := range values {
		vs[i] = regexp.QuoteMeta(v)
	}
	return name + "=~" + quote(strings.Join(vs, "|"))
}

func receiverNode(r Receiver, set Settings) (map[string]any, []SecretFile, error) {
	name := RecvName(r.OrgID, r.Name)
	node := map[string]any{"name": name}
	var files []SecretFile
	file := func() (string, error) {
		if !secretRefs.MatchString(r.SecretRef) {
			return "", fmt.Errorf("has no usable secret file name (%q)", r.SecretRef)
		}
		files = append(files, SecretFile{Name: r.SecretRef, OrgID: r.OrgID, Receiver: r.Name})
		return path.Join(SecretDir, r.SecretRef), nil
	}
	switch r.Type {
	case TypeSlack:
		f, err := file()
		if err != nil {
			return nil, nil, err
		}
		c := map[string]any{"api_url_file": f, "send_resolved": true,
			"title": slackTitle, "fallback": slackTitle, "text": slackText, "link_names": false}
		if ch := r.Settings["channel"]; ch != "" {
			c["channel"] = ch
		}
		node["slack_configs"] = []any{c}
	case TypePagerDuty:
		f, err := file()
		if err != nil {
			return nil, nil, err
		}
		node["pagerduty_configs"] = []any{map[string]any{"routing_key_file": f, "send_resolved": true}}
	case TypeWebhook:
		u, err := url.Parse(r.Settings["url"])
		if err != nil || u.Host == "" || !(u.Scheme == "https" || (u.Scheme == "http" && set.AllowHTTPWebhooks)) {
			if set.AllowHTTPWebhooks {
				return nil, nil, errors.New("settings.url must be an http or https URL")
			}
			return nil, nil, errors.New("settings.url must be an https URL")
		}
		c := map[string]any{"url": u.String(), "send_resolved": true, "max_alerts": 20}
		if r.SecretRef != "" {
			f, err := file()
			if err != nil {
				return nil, nil, err
			}
			c["http_config"] = map[string]any{"authorization": map[string]any{"type": "Bearer", "credentials_file": f}}
		}
		node["webhook_configs"] = []any{c}
	case TypeEmail:
		if set.SMTP.Host == "" || set.SMTP.From == "" || set.SMTP.PasswordFile == "" {
			return nil, nil, errors.New("email is not configured on this server")
		}
		var tos []string
		for _, a := range strings.Split(r.Settings["to"], ",") {
			if a = strings.TrimSpace(a); a != "" {
				if !emailRe.MatchString(a) {
					return nil, nil, fmt.Errorf("%q is not an email address", a)
				}
				tos = append(tos, a)
			}
		}
		if len(tos) == 0 || len(tos) > 10 {
			return nil, nil, errors.New("settings.to needs 1-10 addresses")
		}
		var ecs []any
		for _, to := range tos {
			ecs = append(ecs, map[string]any{
				"to": to, "from": set.SMTP.From, "smarthost": set.SMTP.Host,
				"auth_username": set.SMTP.Username, "auth_password_file": set.SMTP.PasswordFile,
				"require_tls": true, "send_resolved": true,
			})
		}
		node["email_configs"] = ecs
	case TypeAutopilot:
		u, err := url.Parse(r.Settings["url"])
		if err != nil || u.Host == "" || !(u.Scheme == "https" || (u.Scheme == "http" && ClusterInternal(u.Hostname()))) {
			return nil, nil, errors.New("settings.url must be an https URL (http only for a cluster-internal host)")
		}
		// The bearer is reliant's webhook token for the automation: without
		// it every delivery would be refused, so it is required.
		f, err := file()
		if err != nil {
			return nil, nil, err
		}
		node["webhook_configs"] = []any{map[string]any{
			"url": u.String(), "send_resolved": true, "max_alerts": 20,
			"http_config": map[string]any{"authorization": map[string]any{"type": "Bearer", "credentials_file": f}},
		}}
	case TypeInApp:
		if set.InAppURL == "" {
			return nil, nil, errors.New("the in-app webhook is not configured on this server")
		}
		c := map[string]any{"url": set.InAppURL, "send_resolved": true, "max_alerts": 50}
		if set.InAppTokenFile != "" {
			c["http_config"] = map[string]any{"authorization": map[string]any{"type": "Bearer", "credentials_file": set.InAppTokenFile}}
		}
		node["webhook_configs"] = []any{c}
	default:
		return nil, nil, fmt.Errorf("unknown type %q", r.Type)
	}
	return node, files, nil
}

// ClusterInternal reports a host only a cluster (or this machine) resolves, the
// one place plain http is acceptable for an autopilot receiver: the hop never
// leaves the cluster network.
func ClusterInternal(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "127.0.0.1" || h == "::1" ||
		strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".svc.cluster.local") || strings.HasSuffix(h, ".internal")
}

// slackEscape renders a template expression for Slack mrkdwn. Slack treats &, <
// and > in message text as control characters: <!channel>, <@U123> and
// <https://evil|text> are a broadcast ping, a mention and a disguised link. A
// label or annotation can carry attacker-chosen text (an issue title is
// whatever a browser sent), so every one is escaped to &amp; &lt; &gt;, which
// Slack displays as the literal character.
func slackEscape(expr string) string {
	return `{{ ` + expr + ` | reReplaceAll "&" "&amp;" | reReplaceAll "<" "&lt;" | reReplaceAll ">" "&gt;" }}`
}

var (
	slackTitle = slackEscape(`.CommonLabels.alertname`)
	slackText  = `{{ range .Alerts }}*` + slackEscape(`.Labels.alertname`) + `* {{ if .Labels.service }}(` + slackEscape(`.Labels.service`) + `){{ end }}
` + slackEscape(`.Annotations.summary`) + `
{{ end }}`
)

// matcher is one Alertmanager equality matcher, quoted so a value with a quote,
// backslash or newline cannot end the matcher early.
func matcher(name, value string) string {
	return name + "=" + quote(value)
}

func quote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
