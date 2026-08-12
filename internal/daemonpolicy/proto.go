// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// WirePolicy is the JSON form of a policy, carried on the NATS daemon-command
// envelope between the API server and the daemon gateway.
//
// It exists separately from the proto message because that hop is JSON, not
// protobuf. Keeping both encodings in this package means a field added to one
// is visibly missing from the other, rather than being silently dropped in
// transit — the failure mode that once left the daemon's enforcement gate
// receiving a nil policy for every request and therefore allowing everything.
type WirePolicy struct {
	GrantID       string   `json:"grant_id,omitempty"`
	Tools         []string `json:"tools,omitempty"`
	PathRoot      string   `json:"path_root,omitempty"`
	ExecMode      string   `json:"exec_mode,omitempty"`
	ExecAllowlist []string `json:"exec_allowlist,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
}

// ToWire converts a policy to its JSON envelope form. A nil policy yields nil,
// which marshals away entirely and leaves the request unrestricted.
func ToWire(p *Policy) *WirePolicy {
	if p == nil {
		return nil
	}
	out := &WirePolicy{
		GrantID:       p.GrantID,
		Tools:         setToSlice(p.Tools),
		PathRoot:      p.PathRoot,
		ExecMode:      string(p.ExecMode),
		ExecAllowlist: setToSlice(p.ExecAllowlist),
	}
	if !p.ExpiresAt.IsZero() {
		out.ExpiresAt = p.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

// WireToProto converts the JSON envelope form into the proto message the
// daemon receives. A nil wire policy yields nil.
func WireToProto(w *WirePolicy) *reliantv1.ConnectorPolicy {
	if w == nil {
		return nil
	}
	return &reliantv1.ConnectorPolicy{
		GrantId:       w.GrantID,
		AllowedTools:  w.Tools,
		PathRoot:      w.PathRoot,
		ExecMode:      w.ExecMode,
		ExecAllowlist: w.ExecAllowlist,
		ExpiresAt:     w.ExpiresAt,
	}
}

// FromProto converts a wire policy into an enforceable one. A nil message
// yields a nil Policy, which means unrestricted — the first-party path.
//
// An unparseable expiry is treated as already-expired rather than as
// no-expiry, so a malformed grant cannot outlive its intended bound.
func FromProto(p *reliantv1.ConnectorPolicy) *Policy {
	if p == nil {
		return nil
	}

	out := &Policy{
		GrantID:       p.GetGrantId(),
		Tools:         sliceToSet(p.GetAllowedTools()),
		PathRoot:      p.GetPathRoot(),
		ExecMode:      ExecMode(p.GetExecMode()),
		ExecAllowlist: sliceToSet(p.GetExecAllowlist()),
	}

	if raw := p.GetExpiresAt(); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			// Fail closed: an expiry we cannot read becomes one already past.
			out.ExpiresAt = time.Unix(0, 0)
		} else {
			out.ExpiresAt = ts
		}
	}

	return out
}

// ToProto converts a Policy into its wire form. A nil Policy yields nil,
// leaving the request unrestricted.
func ToProto(p *Policy) *reliantv1.ConnectorPolicy {
	if p == nil {
		return nil
	}

	out := &reliantv1.ConnectorPolicy{
		GrantId:       p.GrantID,
		AllowedTools:  setToSlice(p.Tools),
		PathRoot:      p.PathRoot,
		ExecMode:      string(p.ExecMode),
		ExecAllowlist: setToSlice(p.ExecAllowlist),
	}
	if !p.ExpiresAt.IsZero() {
		out.ExpiresAt = p.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

func sliceToSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func setToSlice(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	items := make([]string, 0, len(set))
	for item, ok := range set {
		if ok {
			items = append(items, item)
		}
	}
	return items
}
