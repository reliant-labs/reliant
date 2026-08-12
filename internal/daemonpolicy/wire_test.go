// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"encoding/json"
	"testing"
	"time"
)

// TestPolicySurvivesTheWire is the regression guard for the defect that made
// every daemon-side check dead code: the policy was enforced at dispatch, but
// the NATS envelope between the API server and the gateway silently dropped
// it, so the daemon received nil and allowed everything.
//
// The unit tests all passed, because each layer was correct in isolation. This
// test exercises the seam instead: encode exactly as the router does, decode
// exactly as the bridge does, and assert the policy still binds.
func TestPolicySurvivesTheWire(t *testing.T) {
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	original := &Policy{
		GrantID:       "grant-wire",
		Tools:         map[string]bool{"fs.read_file": true, "exec.run": true},
		PathRoot:      "/workspace",
		ExecMode:      ExecAllowlist,
		ExecAllowlist: map[string]bool{"git": true},
		ExpiresAt:     expires,
	}

	// Encode side: the shape daemon_router_nats.go marshals.
	envelope := struct {
		RequestID   string      `json:"request_id"`
		CommandType string      `json:"command_type"`
		Payload     []byte      `json:"payload"`
		TimeoutMs   int32       `json:"timeout_ms"`
		Policy      *WirePolicy `json:"policy,omitempty"`
	}{
		RequestID:   "req-1",
		CommandType: "fs.read_file",
		TimeoutMs:   1000,
		Policy:      ToWire(original),
	}

	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// Decode side: the shape nats_bridge.go unmarshals.
	var decoded struct {
		RequestID   string      `json:"request_id"`
		CommandType string      `json:"command_type"`
		Payload     []byte      `json:"payload"`
		TimeoutMs   int32       `json:"timeout_ms"`
		Policy      *WirePolicy `json:"policy,omitempty"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if decoded.Policy == nil {
		t.Fatal("policy was dropped in transit; the daemon would allow everything")
	}

	// Complete the hop the daemon performs: wire → proto → enforceable.
	rebuilt := FromProto(WireToProto(decoded.Policy))
	if rebuilt == nil {
		t.Fatal("policy did not survive reconstruction on the daemon side")
	}

	if rebuilt.GrantID != original.GrantID {
		t.Errorf("grant id: got %q want %q", rebuilt.GrantID, original.GrantID)
	}
	if rebuilt.PathRoot != original.PathRoot {
		t.Errorf("path root: got %q want %q", rebuilt.PathRoot, original.PathRoot)
	}
	if rebuilt.ExecMode != original.ExecMode {
		t.Errorf("exec mode: got %q want %q", rebuilt.ExecMode, original.ExecMode)
	}
	if !rebuilt.Tools["fs.read_file"] || !rebuilt.Tools["exec.run"] {
		t.Errorf("tools did not survive: %v", rebuilt.Tools)
	}
	if !rebuilt.ExecAllowlist["git"] {
		t.Errorf("exec allowlist did not survive: %v", rebuilt.ExecAllowlist)
	}
	if !rebuilt.ExpiresAt.Equal(expires) {
		t.Errorf("expiry: got %s want %s", rebuilt.ExpiresAt, expires)
	}

	// The reconstructed policy must actually enforce, not merely carry data.
	if err := rebuilt.Check("fs.delete", []byte(`{"path":"/workspace/a"}`)); err == nil {
		t.Error("reconstructed policy did not deny an ungranted command")
	}
	if err := rebuilt.Check("fs.read_file", []byte(`{"path":"/etc/passwd"}`)); err == nil {
		t.Error("reconstructed policy did not confine paths")
	}
}

// TestFirstPartyEnvelopeCarriesNoPolicy confirms the absent-policy case stays
// absent, so unrestricted traffic is not accidentally given an empty policy —
// which, failing closed, would deny everything.
func TestFirstPartyEnvelopeCarriesNoPolicy(t *testing.T) {
	envelope := struct {
		Policy *WirePolicy `json:"policy,omitempty"`
	}{
		Policy: ToWire(nil),
	}

	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("an unrestricted request should carry no policy field, got %s", raw)
	}

	var decoded struct {
		Policy *WirePolicy `json:"policy,omitempty"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := FromProto(WireToProto(decoded.Policy)); got != nil {
		t.Error("absent policy must reconstruct as nil (unrestricted), not as an empty policy")
	}
}
