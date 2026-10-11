// Copyright (c) 2025 Reliant Labs

package daemonwire_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

var (
	goldenAt      = time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	goldenOOM     = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	goldenCreated = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	goldenChanged = time.Date(2026, 10, 7, 9, 15, 0, 0, time.UTC)
)

// TestGoldenWire pins the exact bytes each payload marshals to. Two services
// in two repositories decode these bytes, and a deploy never moves both at
// once, so a renamed field, a changed JSON tag, or a dropped omitempty is a
// wire break even when both sides compile. The literals below are the
// contract; if this test fails, the change is not a refactor.
//
// Each case also decodes the literal back and compares it with the value it
// came from, so a tag that only one direction honours fails too.
func TestGoldenWire(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name: "lifecycle event, every field populated",
			value: &daemonwire.Event{
				DaemonID:        "d-1",
				UserID:          "u-1",
				Type:            daemonwire.EventLifecycle,
				At:              goldenAt,
				DaemonType:      "managed",
				Phase:           daemonwire.LifecyclePhaseReady,
				Size:            "small",
				StatusMessage:   "image pull failed",
				LastOOMKilledAt: &goldenOOM,
				OOMKillCount:    2,
				Name:            "gpu-box",
				CreatedAt:       &goldenCreated,
			},
			want: `{"daemon_id":"d-1","user_id":"u-1","type":"lifecycle","at":"2026-10-07T09:30:00Z",` +
				`"daemon_type":"managed","phase":"ready","size":"small","status_message":"image pull failed",` +
				`"last_oom_killed_at":"2026-10-07T09:00:00Z","oom_kill_count":2,"name":"gpu-box",` +
				`"created_at":"2026-10-01T08:00:00Z"}`,
		},
		{
			name: "liveness event omits every lifecycle field",
			value: &daemonwire.Event{
				DaemonID:   "d-1",
				UserID:     "u-1",
				Type:       daemonwire.EventConnected,
				At:         goldenAt,
				DaemonType: "self_hosted",
			},
			want: `{"daemon_id":"d-1","user_id":"u-1","type":"connected","at":"2026-10-07T09:30:00Z","daemon_type":"self_hosted"}`,
		},
		{
			name: "activity event carries its reason",
			value: &daemonwire.Event{
				DaemonID:   "d-1",
				UserID:     "u-1",
				Type:       daemonwire.EventActivity,
				At:         goldenAt,
				DaemonType: "managed",
				Reason:     "tool call shell",
			},
			want: `{"daemon_id":"d-1","user_id":"u-1","type":"activity","at":"2026-10-07T09:30:00Z","daemon_type":"managed","reason":"tool call shell"}`,
		},
		{
			name:  "removed event keeps an empty user_id",
			value: &daemonwire.Event{DaemonID: "d-1", Type: daemonwire.EventRemoved, At: goldenAt},
			want:  `{"daemon_id":"d-1","user_id":"","type":"removed","at":"2026-10-07T09:30:00Z"}`,
		},
		{
			name: "registry snapshot, a managed and a self-hosted daemon",
			value: &daemonwire.RegistrySnapshot{
				UserID: "u-1",
				At:     goldenAt,
				Daemons: []daemonwire.RegistryDaemon{
					{
						DaemonID:        "d-1",
						Name:            "default",
						DaemonType:      "managed",
						CreatedAt:       goldenCreated,
						Phase:           daemonwire.LifecyclePhaseSuspended,
						PhaseChangedAt:  &goldenChanged,
						Size:            "small",
						StatusMessage:   "idle timeout",
						LastOOMKilledAt: &goldenOOM,
						OOMKillCount:    2,
					},
					{DaemonID: "d-2", DaemonType: "self_hosted", CreatedAt: goldenCreated},
				},
			},
			want: `{"user_id":"u-1","at":"2026-10-07T09:30:00Z","daemons":[` +
				`{"daemon_id":"d-1","name":"default","daemon_type":"managed","created_at":"2026-10-01T08:00:00Z",` +
				`"phase":"suspended","phase_changed_at":"2026-10-07T09:15:00Z","size":"small",` +
				`"status_message":"idle timeout","last_oom_killed_at":"2026-10-07T09:00:00Z","oom_kill_count":2},` +
				`{"daemon_id":"d-2","daemon_type":"self_hosted","created_at":"2026-10-01T08:00:00Z"}]}`,
		},
		{
			name:  "empty registry snapshot is an empty array, not null",
			value: &daemonwire.RegistrySnapshot{UserID: "u-1", At: goldenAt, Daemons: []daemonwire.RegistryDaemon{}},
			want:  `{"user_id":"u-1","at":"2026-10-07T09:30:00Z","daemons":[]}`,
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

			decoded := reflect.New(reflect.TypeOf(tc.value).Elem()).Interface()
			if err := json.Unmarshal([]byte(tc.want), decoded); err != nil {
				t.Fatalf("unmarshal golden: %v", err)
			}
			if !reflect.DeepEqual(decoded, tc.value) {
				t.Fatalf("golden bytes do not decode to the value they came from\n got: %+v\nwant: %+v", decoded, tc.value)
			}
		})
	}
}

// TestWireVocabulary pins the string value of every subject, event type and
// lifecycle phase. They travel on the wire as plain strings, so changing one
// is as much a wire break as renaming a JSON field.
func TestWireVocabulary(t *testing.T) {
	pins := []struct{ got, want string }{
		{daemonwire.SubjectPrefix, "daemon.v1.state."},
		{daemonwire.SubjectWildcard, "daemon.v1.state.*.*"},
		{daemonwire.SubjectRegistrySnapshot, "daemon.v1.registry.snapshot"},
		{daemonwire.Subject("d-1", daemonwire.EventActivity), "daemon.v1.state.d-1.activity"},
		{string(daemonwire.EventConnected), "connected"},
		{string(daemonwire.EventDisconnected), "disconnected"},
		{string(daemonwire.EventActivity), "activity"},
		{string(daemonwire.EventLifecycle), "lifecycle"},
		{string(daemonwire.EventRemoved), "removed"},
		{string(daemonwire.LifecyclePhaseProvisioning), "provisioning"},
		{string(daemonwire.LifecyclePhaseCloning), "cloning"},
		{string(daemonwire.LifecyclePhaseReady), "ready"},
		{string(daemonwire.LifecyclePhaseSuspending), "suspending"},
		{string(daemonwire.LifecyclePhaseSuspended), "suspended"},
		{string(daemonwire.LifecyclePhaseFailed), "failed"},
	}
	for _, p := range pins {
		if p.got != p.want {
			t.Errorf("wire string = %q, want %q", p.got, p.want)
		}
	}
}

var allEventTypes = []daemonwire.EventType{
	daemonwire.EventConnected,
	daemonwire.EventDisconnected,
	daemonwire.EventActivity,
	daemonwire.EventLifecycle,
	daemonwire.EventRemoved,
}

// TestSubjectMatchesWildcard checks that every subject a publisher can build
// is delivered to a consumer subscribed on SubjectWildcard, and that the two
// neighbouring subjects that must NOT reach a state consumer do not.
func TestSubjectMatchesWildcard(t *testing.T) {
	const daemonID = "0b9d6c3e-6f1a-4c55-9a43-2f7c2d1e8a90"
	for _, typ := range allEventTypes {
		subj := daemonwire.Subject(daemonID, typ)
		if !strings.HasPrefix(subj, daemonwire.SubjectPrefix) {
			t.Errorf("Subject(%q, %q) = %q, missing prefix %q", daemonID, typ, subj, daemonwire.SubjectPrefix)
		}
		if !natsSubjectMatches(daemonwire.SubjectWildcard, subj) {
			t.Errorf("Subject(%q, %q) = %q is not matched by %q", daemonID, typ, subj, daemonwire.SubjectWildcard)
		}
	}

	for _, sibling := range []string{
		"daemon.v1.state.managed", // the gateway's managed-daemon reconciler
		daemonwire.SubjectRegistrySnapshot,
	} {
		if natsSubjectMatches(daemonwire.SubjectWildcard, sibling) {
			t.Errorf("%q must not match %q", sibling, daemonwire.SubjectWildcard)
		}
	}
}

// natsSubjectMatches implements NATS subject matching for patterns that use
// only the single-token wildcard "*", which is all this contract uses.
func natsSubjectMatches(pattern, subject string) bool {
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	if len(pt) != len(st) {
		return false
	}
	for i := range pt {
		if st[i] == "" {
			return false
		}
		if pt[i] != "*" && pt[i] != st[i] {
			return false
		}
	}
	return true
}

func TestUnmarshalEvent(t *testing.T) {
	const at = `"at":"2026-05-30T12:00:00Z"`
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"valid", `{"daemon_id":"d","user_id":"u","type":"connected",` + at + `}`, false},
		{"valid lifecycle", `{"daemon_id":"d","type":"lifecycle","phase":"ready",` + at + `}`, false},
		{"valid removed without user_id", `{"daemon_id":"d","type":"removed",` + at + `}`, false},
		{"missing daemon_id", `{"user_id":"u","type":"connected",` + at + `}`, true},
		{"missing at", `{"daemon_id":"d","user_id":"u","type":"connected"}`, true},
		{"missing type", `{"daemon_id":"d","user_id":"u",` + at + `}`, true},
		{"bad type", `{"daemon_id":"d","user_id":"u","type":"weird",` + at + `}`, true},
		{"junk", `not json`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := daemonwire.UnmarshalEvent([]byte(tc.payload))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestUnmarshalEvent_AcceptsEveryType guards the type switch inside
// UnmarshalEvent: a new EventType that is published but not added there would
// be dropped by every consumer as "unknown type".
func TestUnmarshalEvent_AcceptsEveryType(t *testing.T) {
	for _, typ := range allEventTypes {
		in := daemonwire.Event{DaemonID: "d", Type: typ, At: goldenAt}
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got, err := daemonwire.UnmarshalEvent(body)
		if err != nil {
			t.Errorf("UnmarshalEvent rejected a %q event: %v", typ, err)
			continue
		}
		if !reflect.DeepEqual(got, in) {
			t.Errorf("UnmarshalEvent(%q) = %+v, want %+v", typ, got, in)
		}
	}
}

func TestHasIdentity(t *testing.T) {
	cases := []struct {
		name string
		evt  daemonwire.Event
		want bool
	}{
		{"owner and type", daemonwire.Event{UserID: "u", DaemonType: "managed"}, true},
		{"name is not required", daemonwire.Event{UserID: "u", DaemonType: "self_hosted", Name: ""}, true},
		{"no owner", daemonwire.Event{DaemonType: "managed", Name: "default"}, false},
		{"no type", daemonwire.Event{UserID: "u", Name: "default"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.evt.HasIdentity(); got != tc.want {
				t.Fatalf("HasIdentity() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidLifecyclePhase(t *testing.T) {
	for _, p := range []daemonwire.LifecyclePhase{
		daemonwire.LifecyclePhaseProvisioning,
		daemonwire.LifecyclePhaseCloning,
		daemonwire.LifecyclePhaseReady,
		daemonwire.LifecyclePhaseSuspending,
		daemonwire.LifecyclePhaseSuspended,
		daemonwire.LifecyclePhaseFailed,
	} {
		if !daemonwire.ValidLifecyclePhase(p) {
			t.Errorf("ValidLifecyclePhase(%q) = false, want true", p)
		}
	}
	for _, p := range []daemonwire.LifecyclePhase{"", "hibernating", "Pending", "READY"} {
		if daemonwire.ValidLifecyclePhase(p) {
			t.Errorf("ValidLifecyclePhase(%q) = true, want false", p)
		}
	}
}
