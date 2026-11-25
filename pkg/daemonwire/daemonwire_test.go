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
	// A drain deadline arrives as unix milliseconds, so it keeps them.
	goldenDeadline = time.Date(2026, 10, 7, 9, 40, 0, 123_000_000, time.UTC)
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
			name: "connected event carries the build the daemon reported",
			value: &daemonwire.Event{
				DaemonID:   "d-1",
				UserID:     "u-1",
				Type:       daemonwire.EventConnected,
				At:         goldenAt,
				DaemonType: "managed",
				Build: &daemonwire.Build{
					Version:        "1.8.3-dev+b675d224",
					Commit:         "b675d22417c3",
					CommitTimeUnix: 1792800000,
					ForgeVersion:   "v0.1.47",
				},
			},
			want: `{"daemon_id":"d-1","user_id":"u-1","type":"connected","at":"2026-10-07T09:30:00Z","daemon_type":"managed",` +
				`"build":{"version":"1.8.3-dev+b675d224","commit":"b675d22417c3","commit_time_unix":1792800000,"forge_version":"v0.1.47"}}`,
		},
		{
			name: "upgrade event carries the daemon's upgrade state and no build",
			value: &daemonwire.Event{
				DaemonID:   "d-1",
				UserID:     "u-1",
				Type:       daemonwire.EventUpgrade,
				At:         goldenAt,
				DaemonType: "cloud",
				Upgrade: &daemonwire.Upgrade{
					State:            daemonwire.UpgradeStateDrained,
					RolloutID:        "roll-7",
					TargetVersion:    "1.9.0",
					Deadline:         &goldenDeadline,
					ToolCallsRunning: 1,
					Detail:           "drain deadline passed",
				},
			},
			want: `{"daemon_id":"d-1","user_id":"u-1","type":"upgrade","at":"2026-10-07T09:30:00Z","daemon_type":"cloud",` +
				`"upgrade":{"state":"drained","rollout_id":"roll-7","target_version":"1.9.0",` +
				`"deadline":"2026-10-07T09:40:00.123Z","tool_calls_running":1,"detail":"drain deadline passed"}}`,
		},
		{
			name: "upgrade state alone omits every optional field",
			value: &daemonwire.Event{
				DaemonID: "d-1", UserID: "u-1", Type: daemonwire.EventUpgrade, At: goldenAt,
				Upgrade: &daemonwire.Upgrade{State: daemonwire.UpgradeStateUpdating},
			},
			want: `{"daemon_id":"d-1","user_id":"u-1","type":"upgrade","at":"2026-10-07T09:30:00Z","upgrade":{"state":"updating"}}`,
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
		{string(daemonwire.EventUpgrade), "upgrade"},
		{daemonwire.Subject("d-1", daemonwire.EventUpgrade), "daemon.v1.state.d-1.upgrade"},
		{string(daemonwire.LifecyclePhaseProvisioning), "provisioning"},
		{string(daemonwire.LifecyclePhaseCloning), "cloning"},
		{string(daemonwire.LifecyclePhaseReady), "ready"},
		{string(daemonwire.LifecyclePhaseSuspending), "suspending"},
		{string(daemonwire.LifecyclePhaseSuspended), "suspended"},
		{string(daemonwire.LifecyclePhaseFailed), "failed"},
		{string(daemonwire.UpgradeStateCurrent), "current"},
		{string(daemonwire.UpgradeStateUpdating), "updating"},
		{string(daemonwire.UpgradeStateDrained), "drained"},
		{string(daemonwire.UpgradeStateAwaitingOwner), "awaiting_owner"},
		{string(daemonwire.UpgradeStateFailed), "failed"},
		{string(daemonwire.UpgradeStateStaged), "staged"},
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
	daemonwire.EventUpgrade,
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
		{"valid upgrade", `{"daemon_id":"d","type":"upgrade","upgrade":{"state":"drained"},` + at + `}`, false},
		{"upgrade without upgrade", `{"daemon_id":"d","type":"upgrade",` + at + `}`, true},
		{"upgrade without state", `{"daemon_id":"d","type":"upgrade","upgrade":{"rollout_id":"r"},` + at + `}`, true},
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

// typesBeforeUpgrade is the type switch of UnmarshalEvent as consumers built
// before EventUpgrade run it (control-plane at reliant e7aed083d1af, and every
// older reliant replica still running during a deploy). Frozen: never add to
// it.
var typesBeforeUpgrade = map[string]bool{
	"connected": true, "disconnected": true, "activity": true, "lifecycle": true, "removed": true,
}

// An old consumer drops an upgrade event at parse (it logs and acks: plain
// NATS, no redelivery) rather than reading it as something it knows. It must
// never look like a connect or activity: carrying a build, a reason, or one
// of their subjects would let an old consumer re-mirror a stale build or keep
// a machine awake.
func TestUpgradeEventIsDroppedNotMisreadByAnOldConsumer(t *testing.T) {
	in := daemonwire.Event{
		DaemonID: "d-1", UserID: "u-1", Type: daemonwire.EventUpgrade, At: goldenAt, DaemonType: "cloud",
		Upgrade: &daemonwire.Upgrade{State: daemonwire.UpgradeStateDrained, RolloutID: "roll-7", Deadline: &goldenDeadline},
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var old struct {
		Type   string          `json:"type"`
		Build  json.RawMessage `json:"build"`
		Reason string          `json:"reason"`
	}
	if err := json.Unmarshal(body, &old); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if typesBeforeUpgrade[old.Type] {
		t.Fatalf("an old consumer reads the upgrade event as a %q event", old.Type)
	}
	if old.Build != nil || old.Reason != "" {
		t.Fatalf("upgrade event carries connect or activity facts: %s", body)
	}
	subj := daemonwire.Subject(in.DaemonID, in.Type)
	for typ := range typesBeforeUpgrade {
		if subj == daemonwire.Subject(in.DaemonID, daemonwire.EventType(typ)) {
			t.Fatalf("upgrade subject %q is an existing type's subject", subj)
		}
	}
}

// UnmarshalEvent ignores a field it does not know, so a consumer built from
// this version reads events from a newer publisher. Making it strict would
// turn every future additive field into a wire break.
func TestUnmarshalEvent_IgnoresUnknownFields(t *testing.T) {
	body := `{"daemon_id":"d","user_id":"u","type":"upgrade","at":"2026-05-30T12:00:00Z",` +
		`"from_the_future":{"x":1},"upgrade":{"state":"drained","not_yet_named":true}}`
	got, err := daemonwire.UnmarshalEvent([]byte(body))
	if err != nil {
		t.Fatalf("UnmarshalEvent rejected unknown fields: %v", err)
	}
	if got.Upgrade == nil || got.Upgrade.State != daemonwire.UpgradeStateDrained {
		t.Fatalf("Upgrade = %+v, want state drained", got.Upgrade)
	}
}

// TestUnmarshalEvent_AcceptsEveryType guards the type switch inside
// UnmarshalEvent: a new EventType that is published but not added there would
// be dropped by every consumer as "unknown type".
func TestUnmarshalEvent_AcceptsEveryType(t *testing.T) {
	for _, typ := range allEventTypes {
		in := daemonwire.Event{DaemonID: "d", Type: typ, At: goldenAt}
		if typ == daemonwire.EventUpgrade {
			in.Upgrade = &daemonwire.Upgrade{State: daemonwire.UpgradeStateDrained}
		}
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
