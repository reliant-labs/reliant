package trigger

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"google.golang.org/protobuf/types/known/structpb"
)

const unknownScope = "dynamic/unbounded (unknown or sensitive value withheld)"

var scopeIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@*# -]{0,159}$`)
var targetKeys = map[string]bool{"owner": true, "repo": true, "repository": true, "repos": true, "channel": true, "channel_id": true, "environment": true, "env": true, "project": true, "project_id": true, "namespace": true, "cluster": true, "branch": true, "ref": true, "workflow": true, "phone_number": true, "to": true, "method": true, "url": true}

// Identifiers are not secret detectors. Only semantic scope fields are eligible;
// suspicious credentials, arbitrary literals, URL userinfo/query/fragments and
// oversized values are withheld and explicitly labeled rather than concealed.
func discloseScope(value any) string {
	switch v := value.(type) {
	case string:
		lower := strings.ToLower(v)
		for _, marker := range []string{"secret", "password", "token", "bearer ", "sk-", "ghp_", "ghs_", "api_key", "apikey", "{{"} {
			if strings.Contains(lower, marker) {
				return unknownScope
			}
		}
		if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") {
			u, err := url.Parse(v)
			if err != nil || u.User != nil {
				return unknownScope
			}
			v = u.Scheme + "://" + u.Host + u.EscapedPath()
		}
		if !scopeIdentifier.MatchString(v) {
			return unknownScope
		}
		return v
	case []string:
		if len(v) > 16 {
			return unknownScope
		}
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, discloseScope(s))
		}
		return "[" + strings.Join(out, ", ") + "]"
	case []any:
		if len(v) > 16 {
			return unknownScope
		}
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, discloseScope(s))
		}
		return "[" + strings.Join(out, ", ") + "]"
	default:
		return unknownScope
	}
}

// tainted names the inputs an unbounded event supplies (activation_event_inputs.go):
// a target expression that reads one is dynamic, whatever it evaluates to now.
func targetDisclosure(params map[string]*structpb.Value, values map[string]any, identity string, tainted map[string]bool) string {
	keys := make([]string, 0)
	for key := range params {
		if targetKeys[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := []string{}
	evaluator := runtime.NewCELContextBuilder().WithWorkflow("", identity).WithInputs(values)
	for _, key := range keys {
		value := params[key].AsInterface()
		if text, ok := value.(string); ok && strings.Contains(text, "{{") {
			if referencesTainted(text, tainted) {
				out = append(out, key+"="+unknownScope)
				continue
			}
			resolved, err := evaluator.EvalString(text)
			if err != nil {
				out = append(out, key+"="+unknownScope)
				continue
			}
			value = resolved
		}
		out = append(out, key+"="+discloseScope(value))
	}
	if len(out) == 0 {
		return "targets: " + unknownScope
	}
	return "targets: " + strings.Join(out, "; ") + "; unspecified targets remain dynamic/unbounded; non-scope parameters withheld"
}
func nodePrivilegeDisclosure(node *pb.Node, values map[string]any, identity string, tainted map[string]bool) []string {
	eval := runtime.NewCELContextBuilder().WithWorkflow("", identity).WithInputs(values)
	resolveName := func(raw string) string {
		if strings.Contains(raw, "{{") {
			if referencesTainted(raw, tainted) {
				return ""
			}
			v, err := eval.EvalString(raw)
			if err != nil {
				return ""
			}
			s, _ := v.(string)
			return s
		}
		return raw
	}
	if a := node.GetAction(); a != nil {
		uses := resolveName(model.CelStringValue(a.GetUses()))
		action, err := catalog.MustBuiltin().Resolve(uses)
		if err != nil {
			return []string{"Integration action: " + unknownScope + "; " + targetDisclosure(a.GetWith(), values, identity, tainted)}
		}
		mode := "READ"
		if action.Spec.GetMutates() {
			mode = "MUTATES external state"
		}
		if uses == "http/request@1" {
			mode = "HTTP request; may mutate external state (inspect disclosed method/target; dynamic method is unbounded)"
		}
		connection := resolveName(model.CelStringValue(a.GetConnection()))
		if connection == "" && strings.Contains(model.CelStringValue(a.GetConnection()), "{{") {
			connection = unknownScope
		}
		if connection == "" {
			connection = "default/delegated at runtime (dynamic identity)"
		} else {
			connection = discloseScope(connection)
		}
		hosted := resolveName(model.CelStringValue(a.GetHostedUse()))
		if hosted == "" && strings.Contains(model.CelStringValue(a.GetHostedUse()), "{{") {
			connection += "; hosted grant=" + unknownScope
		}
		if hosted != "" {
			connection += "; hosted grant=" + discloseScope(hosted)
		}
		return []string{fmt.Sprintf("Integration action: %s [%s]; connection=%s; %s", uses, mode, connection, targetDisclosure(a.GetWith(), values, identity, tainted))}
	}
	if a := node.GetInvokeTool(); a != nil {
		name := resolveName(model.CelStringValue(a.GetTool()))
		known := false
		for _, entry := range tools.GetToolRegistry() {
			if entry.Name == name {
				known = true
				break
			}
		}
		if !known {
			name = unknownScope
		}
		return []string{"Invoked tool: " + name + "; connection identity dynamic/default unless fixed by tool configuration; " + targetDisclosure(a.GetParams(), values, identity, tainted)}
	}
	if node.GetRun() != nil {
		return []string{"Shell/process execution: arbitrary command authority on the selected machine; command withheld; targets dynamic/unbounded"}
	}
	return nil
}
func sourceDisclosure(row *core.Trigger) string {
	var config map[string]any
	if json.Unmarshal(row.Config, &config) != nil {
		return "Trigger source: " + string(row.Kind) + "; scope " + unknownScope
	}
	out := []string{"Trigger source: " + string(row.Kind)}
	keys := []string{"cron", "interval", "timezone", "integration", "events", "workflows", "outcomes", "source"}
	for _, key := range keys {
		if value, ok := config[key]; ok {
			out = append(out, key+"="+discloseScope(value))
		}
	}
	scoped := false
	for _, key := range []string{"match", "match_any"} {
		if scope, ok := config[key].(map[string]any); ok {
			names := make([]string, 0, len(scope))
			for name := range scope {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if targetKeys[name] {
					out = append(out, key+"."+name+"="+discloseScope(scope[name]))
					scoped = true
				} else {
					out = append(out, key+".(non-scope attribute)="+unknownScope)
				}
			}
		}
	}
	if row.Kind == core.TriggerKindWebhook {
		out = append(out, "Webhook sender: token/HMAC holders may deliver events; payload-derived targets dynamic/unbounded")
	}
	if !scoped && row.Kind != core.TriggerKindSchedule {
		out = append(out, "source target scope: dynamic/unbounded (no concrete allowlisted match)")
	}
	if row.Filter != "" {
		out = append(out, "additional event filter: present, expression withheld; predicate scope dynamic/unknown")
	}
	return strings.Join(out, "; ")
}
