package obsbackend

// FAIL CLOSED ON INSTANCE NARROWING.
//
// A read narrowed to dev instances sends instanceIds / instanceUsers in its
// scope. Those are new fields, and a control plane that predates them does not
// refuse them: Connect's JSON codec drops unknown fields, so it answers for the
// WHOLE env, and a document that said scope.instance would be presenting
// another stack's (or a teammate's) telemetry as this one's.
//
// So the cloud backend trusts a narrowed answer only when the response says
// the server applied the narrowing: every response to a request that sent an
// instance filter must carry
//
//	"instanceScope": {"instanceIds": [...], "instanceUsers": [...]}
//
// echoing the request's values verbatim (in any order). A response without it,
// or with different values, is exit 2: forge could not look, with
// --all-instances named as the explicit way to read the whole env.
//
// The acknowledgement is per response rather than a capability probed once,
// because a probe and the read can land on different control-plane replicas
// during a rollout, and because a server can wire one service before another:
// only the answer itself can say the answer was narrowed.

import (
	"encoding/json"
	"slices"
	"strings"
)

// instanceEcho is the response field a narrowed read must carry.
type instanceEcho struct {
	InstanceIDs   []string `json:"instanceIds"`
	InstanceUsers []string `json:"instanceUsers"`
}

// sentInstances finds the instance narrowing a request carries, wherever its
// scope sits in the body. narrowed is false when it carries none.
func sentInstances(req any) (sent instanceEcho, narrowed bool, err error) {
	b, err := json.Marshal(req)
	if err != nil {
		return instanceEcho{}, false, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return instanceEcho{}, false, err
	}
	walkInstances(v, &sent)
	return sent, len(sent.InstanceIDs)+len(sent.InstanceUsers) > 0, nil
}

func walkInstances(v any, into *instanceEcho) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			switch k {
			case "instanceIds":
				into.InstanceIDs = append(into.InstanceIDs, stringsOf(x)...)
			case "instanceUsers":
				into.InstanceUsers = append(into.InstanceUsers, stringsOf(x)...)
			default:
				walkInstances(x, into)
			}
		}
	case []any:
		for _, x := range t {
			walkInstances(x, into)
		}
	}
}

func stringsOf(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// checkInstanceEcho refuses a narrowed read's answer unless the server
// acknowledged applying exactly the narrowing sent.
func checkInstanceEcho(procedure string, raw json.RawMessage, sent instanceEcho) error {
	var resp struct {
		InstanceScope *instanceEcho `json:"instanceScope"`
	}
	_ = json.Unmarshal(raw, &resp)
	name := procedure[strings.LastIndex(procedure, "/")+1:]
	if resp.InstanceScope == nil {
		return Undetermined("the control plane did not confirm it narrowed %s to the instance: it predates instance scopes and would have answered for the whole env, so nothing is shown; --all-instances reads the whole env", name)
	}
	if !sameSet(resp.InstanceScope.InstanceIDs, sent.InstanceIDs) || !sameSet(resp.InstanceScope.InstanceUsers, sent.InstanceUsers) {
		return Undetermined("the control plane narrowed %s to instances %v / users %v, not the %v / %v asked for; nothing is shown",
			name, resp.InstanceScope.InstanceIDs, resp.InstanceScope.InstanceUsers, sent.InstanceIDs, sent.InstanceUsers)
	}
	return nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// wireInstanceNarrowing is the narrowing of a read that has no scope of its
// own (an issue read by id), as request fields beside its id: instanceIds and
// instanceUsers, acknowledged like any other.
type wireInstanceNarrowing struct {
	InstanceIDs   []string `json:"instanceIds,omitempty"`
	InstanceUsers []string `json:"instanceUsers,omitempty"`
}

func (c Cloud) instanceNarrowing() wireInstanceNarrowing {
	return wireInstanceNarrowing{InstanceIDs: c.Instances.IDs, InstanceUsers: c.Instances.Users}
}

// RefuseSQL is the refusal of raw SQL (a --sql statement, a sql panel) on a
// backend narrowed to dev instances: the statement is its author's, and
// nothing narrows it, so its rows would be the whole env's under the
// instance's scope. nil when i narrows nothing.
func (i Instances) RefuseSQL(what string) error {
	if !i.Narrows() {
		return nil
	}
	return Wrong("%s runs its author's SQL, which forge cannot narrow to an instance; pass --all-instances to run it over the whole env", what)
}
