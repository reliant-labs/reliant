// Copyright (c) 2025 Reliant Labs

package daemonwire_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// TestDisconnectCommandGoldenWire pins the subject and the exact bytes of the
// disconnect command. The control plane publishes these bytes and every
// gateway replica decodes them, and the two never deploy together, so a
// renamed field or subject is a wire break even when both sides compile. The
// control plane carries the same literals in its own golden test.
func TestDisconnectCommandGoldenWire(t *testing.T) {
	if got, want := daemonwire.SubjectDisconnectCommand, "daemon.v1.commands.disconnect"; got != want {
		t.Fatalf("SubjectDisconnectCommand = %q, want %q", got, want)
	}
	cases := []struct {
		name  string
		value daemonwire.DisconnectCommand
		want  string
	}{
		{
			name:  "every field",
			value: daemonwire.DisconnectCommand{DaemonID: "d-1", Reason: "credential revoked", RequestID: "r-1"},
			want:  `{"daemon_id":"d-1","reason":"credential revoked","request_id":"r-1"}`,
		},
		{
			name:  "only the daemon",
			value: daemonwire.DisconnectCommand{DaemonID: "d-1"},
			want:  `{"daemon_id":"d-1"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("wire bytes changed\n got: %s\nwant: %s", got, tc.want)
			}
			decoded, err := daemonwire.UnmarshalDisconnectCommand([]byte(tc.want))
			if err != nil {
				t.Fatalf("unmarshal golden: %v", err)
			}
			if !reflect.DeepEqual(decoded, tc.value) {
				t.Fatalf("golden bytes decode to %+v, want %+v", decoded, tc.value)
			}
		})
	}
}

func TestUnmarshalDisconnectCommand(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"valid", `{"daemon_id":"d","reason":"credential revoked","request_id":"r"}`, false},
		{"unknown fields are ignored", `{"daemon_id":"d","revoked_token_id":"t"}`, false},
		{"missing daemon_id", `{"reason":"credential revoked"}`, true},
		{"blank daemon_id", `{"daemon_id":"  "}`, true},
		{"junk", `not json`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := daemonwire.UnmarshalDisconnectCommand([]byte(tc.payload))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
