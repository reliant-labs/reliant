package trigger

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// The activation approval card is one string: it is what the owner reads,
// what the approval stores, and part of what the approval hash covers. It is
// laid out so a person can read it as plain text and the app can render it
// with structure (frontends/web/src/lib/approvalCard.ts):
//
//	<one plain sentence: what turning this on does>
//	<Label>: <value>
//	...
//
//	Technical details
//	<one technical line per row>
//
// Every value is flattened to one line, so no value can forge a row or the
// details heading.
const (
	// cardDetailsHeading introduces the technical lines. The web client
	// splits on "\n\n" + cardDetailsHeading + "\n"; keep the two in step.
	cardDetailsHeading = "Technical details"
	// cardValueLimit bounds one displayed value (a workflow title, a list).
	cardValueLimit = 240
)

// activationCard is everything the card shows.
type activationCard struct {
	Operation string
	// Workflow is the workflow's display title (its title, else its ref).
	Workflow string
	Reason   string
	Row      *core.Trigger
	// Inputs are the effective workflow inputs; only scope keys are read.
	Inputs map[string]any
	// Connection is the activation's own connection, nil when it is the
	// default resolved at run time.
	Connection   *core.Connection
	MachineName  string
	Capabilities []string
	// Summary is the dependency closure's disclosure (activation_snapshot.go).
	Summary []string
	// SafeRow is the allowlisted JSON projection of the activation row.
	SafeRow string
}

var repoKeys = []string{"repo", "repos", "repository"}

var integrationNames = map[string]string{"github": "GitHub", "slack": "Slack", "gmail": "Gmail", "twilio": "Twilio", "http": "HTTP", "linear": "Linear", "google": "Google"}

func integrationName(id string) string {
	if name, ok := integrationNames[strings.ToLower(id)]; ok {
		return name
	}
	return discloseScope(id)
}

// oneLine flattens value so it cannot add a row, and bounds it.
func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > cardValueLimit {
		value = string(runes[:cardValueLimit]) + "…"
	}
	return value
}

func (c activationCard) String() string {
	type row struct{ label, value string }
	rows := []row{
		{"Approval needed", c.Reason},
		{"Repositories", c.repositories()},
		{"Connection", c.connection()},
		{"Acts as", c.actingAs()},
		{"Tools and capabilities", c.tools()},
		{"Machine", c.machine()},
		{"Later edits", "Edits to the workflow apply to its future runs without a new approval."},
	}
	lines := []string{oneLine(c.lead())}
	for _, r := range rows {
		lines = append(lines, r.label+": "+oneLine(r.value))
	}
	lines = append(lines, "", cardDetailsHeading)
	lines = append(lines, c.details()...)
	return strings.Join(lines, "\n")
}

func (c activationCard) lead() string {
	verb := "Turn on"
	switch c.Operation {
	case tools.ToolUpdateTriggerActivation:
		verb = "Change"
	case tools.ToolEnableTrigger:
		verb = "Turn back on"
	}
	where := "in this project"
	if repos := c.repoList(); len(repos) > 0 {
		where = "for " + strings.Join(repos, ", ")
	}
	sentence := verb + " " + c.Workflow + " " + where + ": it will " + c.when()
	if acts := c.actsOn(); acts != "" {
		sentence += ", and " + acts
	}
	return sentence + "."
}

// when says what starts a run, from the trigger's source configuration.
func (c activationCard) when() string {
	var config map[string]any
	_ = json.Unmarshal(c.Row.Config, &config)
	switch c.Row.Kind {
	case core.TriggerKindSchedule:
		if cron, ok := config["cron"]; ok {
			return "run on a schedule (" + strings.Trim(discloseScope(cron), "[]") + ")"
		}
		if interval, ok := config["interval"]; ok {
			return "run every " + discloseScope(interval)
		}
		return "run on a schedule"
	case core.TriggerKindIntegration:
		name := "an integration"
		if id, ok := config["integration"].(string); ok {
			name = integrationName(id)
		}
		if events, ok := config["events"]; ok {
			return "run on each " + name + " event it watches (" + strings.Trim(discloseScope(events), "[]") + ")"
		}
		return "run on each " + name + " event it watches"
	case core.TriggerKindWebhook:
		return "run each time its webhook receives a request"
	case core.TriggerKindWorkflowEvent:
		return "run when another workflow's run ends"
	}
	return "run when its trigger fires"
}

// actsOn names where its writes land, when it holds a connection.
func (c activationCard) actsOn() string {
	integrations := map[string]bool{}
	if c.Connection != nil {
		integrations[c.Connection.IntegrationID] = true
	}
	for _, item := range c.Capabilities {
		if rest, ok := strings.CutPrefix(item, "connection:"); ok {
			id, _, _ := strings.Cut(rest, "/")
			if id != "" && id != dynamicCapability {
				integrations[id] = true
			}
		}
	}
	if len(integrations) == 0 {
		return ""
	}
	names := make([]string, 0, len(integrations))
	for id := range integrations {
		names = append(names, integrationName(id))
	}
	sort.Strings(names)
	out := "use " + strings.Join(names, " and ")
	if integrations["github"] {
		out += " as " + c.actingAs()
	}
	return out
}

// repoList is the repositories the activation is LIMITED to: its source's
// match filters, which every event must satisfy. Workflow inputs are not a
// limit — an input named repo need not constrain anything the workflow does,
// and an event mapping overrides it per event — so they are never listed
// here, and never put a repository in the lead (inputRepos).
func (c activationCard) repoList() []string {
	var config map[string]any
	_ = json.Unmarshal(c.Row.Config, &config)
	var values []any
	for _, key := range []string{"match", "match_any"} {
		if scope, ok := config[key].(map[string]any); ok {
			for _, k := range repoKeys {
				if v, ok := scope[k]; ok {
					values = append(values, v)
				}
			}
		}
	}
	return discloseRepos(values...)
}

// inputRepos are the repositories the effective workflow inputs name: what a
// run starts with, not a boundary on it.
func (c activationCard) inputRepos() []string {
	var values []any
	for _, k := range repoKeys {
		if v, ok := c.Inputs[k]; ok {
			values = append(values, v)
		}
	}
	return discloseRepos(values...)
}

// discloseRepos flattens repository values (a string, a comma list, or a
// list of either) into distinct disclosed names, in order.
func discloseRepos(values ...any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(value any) {
		var values []any
		switch v := value.(type) {
		case []any:
			values = v
		case []string:
			for _, s := range v {
				values = append(values, s)
			}
		default:
			values = []any{v}
		}
		for _, v := range values {
			s, ok := v.(string)
			if !ok || s == "" {
				continue
			}
			for _, part := range strings.Split(s, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				d := discloseScope(part)
				if d == unknownScope || seen[d] {
					continue
				}
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	for _, v := range values {
		add(v)
	}
	return out
}

func (c activationCard) repositories() string {
	if repos := c.repoList(); len(repos) > 0 {
		return strings.Join(repos, ", ")
	}
	var named []string
	if inputs := c.inputRepos(); len(inputs) > 0 {
		named = append(named, "its inputs name "+strings.Join(inputs, ", "))
	}
	var steps []string
	seen := map[string]bool{}
	for _, line := range c.Summary {
		if rest, ok := strings.CutPrefix(line, "Integration action: "); ok {
			for _, repo := range actionRepos(rest) {
				if !seen[repo] {
					seen[repo] = true
					steps = append(steps, repo)
				}
			}
		}
	}
	if len(steps) > 0 {
		named = append(named, "its steps name "+strings.Join(steps, ", "))
	}
	if len(named) > 0 {
		out := "Not limited by this activation; " + strings.Join(named, ", and ")
		if c.Row.Kind != core.TriggerKindSchedule {
			out += "; events are not limited to them"
		}
		return out
	}
	if c.Row.Kind == core.TriggerKindSchedule {
		return "Not limited by this activation: its steps choose"
	}
	return "Chosen by each event; this approval does not limit them"
}

// actionRepos are the repositories an integration action's disclosed
// targets name ("targets: owner=acme; repo=releases" is acme/releases).
func actionRepos(disclosure string) []string {
	_, targets, ok := strings.Cut(disclosure, "targets: ")
	if !ok {
		return nil
	}
	values := map[string]string{}
	for _, pair := range strings.Split(targets, "; ") {
		key, value, ok := strings.Cut(pair, "=")
		if ok && value != unknownScope {
			values[key] = strings.Trim(value, "[]")
		}
	}
	var out []string
	for _, key := range repoKeys {
		value := values[key]
		if value == "" {
			continue
		}
		for _, repo := range strings.Split(value, ", ") {
			if owner := values["owner"]; owner != "" && !strings.Contains(repo, "/") {
				repo = owner + "/" + repo
			}
			out = append(out, repo)
		}
	}
	return out
}

func (c activationCard) connection() string {
	if c.Connection == nil {
		return "Your default connection, chosen when it runs"
	}
	out := integrationName(c.Connection.IntegrationID)
	account := ""
	switch {
	case c.Connection.AccountLabel != nil && *c.Connection.AccountLabel != "":
		account = *c.Connection.AccountLabel
	case c.Connection.ExternalAccountID != nil && *c.Connection.ExternalAccountID != "":
		account = *c.Connection.ExternalAccountID
	}
	if account != "" {
		out += ", account " + discloseScope(account)
	}
	if len(c.Connection.Scopes) > 0 {
		out += " (scopes: " + strings.Trim(discloseScope(c.Connection.Scopes), "[]") + ")"
	}
	return out
}

func (c activationCard) actingAs() string {
	if c.Row.ActingIdentity.OrOwner() == core.ActingIdentityApp {
		return "the Reliant GitHub app (a bot account)"
	}
	return "you, through your own GitHub account"
}

func (c activationCard) machine() string {
	if c.Row.NoMachine {
		return "None: it runs on Reliant's servers"
	}
	if c.MachineName == "" {
		return "Your machine"
	}
	return c.MachineName
}

// tools turns the closure's disclosure into what a person decides on: each
// action it takes, the agent's tools, and the credentials it is handed. The
// full lines stay in the technical details.
func (c activationCard) tools() string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, line := range c.Summary {
		switch {
		case strings.HasPrefix(line, "Integration action: "):
			rest := strings.TrimPrefix(line, "Integration action: ")
			uses, _, _ := strings.Cut(rest, " [")
			uses, _, _ = strings.Cut(uses, ";")
			what := uses + " (makes changes)"
			switch {
			case strings.Contains(rest, "[READ]"):
				what = uses + " (reads)"
			case strings.Contains(rest, "[HTTP request"):
				what = uses + " (HTTP request, may make changes)"
			}
			if repos := actionRepos(rest); len(repos) > 0 {
				what += " on " + strings.Join(repos, ", ")
			}
			add(what)
		case strings.HasPrefix(line, "Agent tool privileges: UNRESTRICTED"):
			add("an agent with every tool, including tools added later")
		case strings.HasPrefix(line, "Agent tool privileges: "):
			list, _, _ := strings.Cut(strings.TrimPrefix(line, "Agent tool privileges: "), ";")
			add("an agent with tools: " + list)
		case strings.HasPrefix(line, "Invoked tool: "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "Invoked tool: "), ";")
			add("tool " + name)
		case strings.HasPrefix(line, "Shell/process execution"):
			add("shell commands on the machine")
		}
	}
	for _, item := range c.Capabilities {
		switch {
		case item == "secret:machine_credentials":
			add("the machine's credentials")
		case strings.HasPrefix(item, "secret:hosted_use/"):
			add("hosted credential " + strings.TrimPrefix(item, "secret:hosted_use/"))
		case item == approvalsDecideCapability:
			add("approve or deny requests on your behalf")
		case strings.HasPrefix(item, "deploy:"):
			add("a deploy to " + strings.TrimPrefix(item, "deploy:"))
		case item == "connection:github/git_publish":
			add("git push to GitHub")
		}
	}
	if len(out) == 0 {
		return "None that act outside Reliant"
	}
	return strings.Join(out, "; ")
}

// details are the exact, technical lines the card is built from: what an
// auditor checks, kept out of the plain-language rows above.
func (c activationCard) details() []string {
	out := []string{"Operation: " + c.Operation}
	if !c.Row.NoMachine && c.Row.DaemonID != "" {
		out = append(out, "Machine id: "+discloseScope(c.Row.DaemonID))
	}
	out = append(out, "Integration identity: "+string(c.Row.ActingIdentity.OrOwner()))
	if c.Connection != nil {
		account := "unknown"
		if c.Connection.ExternalAccountID != nil {
			account = *c.Connection.ExternalAccountID
		}
		out = append(out, "Connection: id "+discloseScope(c.Connection.ID)+"; integration "+discloseScope(c.Connection.IntegrationID)+"; account "+discloseScope(account))
	} else {
		out = append(out, "Connection: default/delegated at runtime (dynamic identity)")
	}
	if len(c.Capabilities) > 0 {
		out = append(out, "Capabilities: "+strings.Join(c.Capabilities, ", "))
	}
	out = append(out, "Activation: "+c.SafeRow, sourceDisclosure(c.Row))
	for _, line := range c.Summary {
		out = append(out, oneLineDetail(line))
	}
	return out
}

// oneLineDetail flattens a technical line without truncating it: the
// details are complete.
func oneLineDetail(value string) string { return strings.Join(strings.Fields(value), " ") }
