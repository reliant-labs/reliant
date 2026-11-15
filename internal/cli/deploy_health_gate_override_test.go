package cli

import (
	"reflect"
	"testing"
)

// --override-health-gate is the acknowledged code plus the reason as the note:
// the request a control plane reads as the override, spelled for a human.
func TestWithHealthGateOverride(t *testing.T) {
	for name, tc := range map[string]struct {
		in       promoteCmdFlags
		wantAcks []string
		wantNote string
		wantErr  bool
	}{
		"no override": {
			in: promoteCmdFlags{version: "v1", note: "routine"}, wantNote: "routine",
		},
		"override alone": {
			in:       promoteCmdFlags{version: "v1", overrideHealthGate: " INC-7: the fix "},
			wantAcks: []string{healthGateAckCode}, wantNote: "health gate overridden: INC-7: the fix",
		},
		"override beside a note and other codes": {
			in:       promoteCmdFlags{version: "v1", note: "hotfix", acknowledgeDestructive: []string{"stateful_deletion"}, overrideHealthGate: "INC-7"},
			wantAcks: []string{"stateful_deletion", healthGateAckCode}, wantNote: "hotfix — health gate overridden: INC-7",
		},
		"the code already named by hand is not doubled": {
			in:       promoteCmdFlags{version: "v1", acknowledgeDestructive: []string{healthGateAckCode}, overrideHealthGate: "INC-7"},
			wantAcks: []string{healthGateAckCode}, wantNote: "health gate overridden: INC-7",
		},
		"no release: left for refusePromoteFlagsWithoutRelease": {
			in: promoteCmdFlags{overrideHealthGate: "INC-7"},
		},
		"a blank reason": {in: promoteCmdFlags{version: "v1", overrideHealthGate: "  "}, wantErr: true},
	} {
		got, err := tc.in.withHealthGateOverride()
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: want an error", name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got.acknowledgeDestructive, tc.wantAcks) || got.note != tc.wantNote {
			t.Errorf("%s: acks %v note %q, want %v %q", name, got.acknowledgeDestructive, got.note, tc.wantAcks, tc.wantNote)
		}
	}
	if err := refusePromoteFlagsWithoutRelease(promoteCmdFlags{overrideHealthGate: "INC-7"}); err == nil {
		t.Error("--override-health-gate with no release must be refused: there is no promotion to override on")
	}
}
