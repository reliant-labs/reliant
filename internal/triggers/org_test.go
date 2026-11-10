// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Every chat a trigger launches belongs to the trigger's org, so its runs bill
// that org: the schedule path carries it to the launch.
func TestFire_ScheduleCarriesTheTriggersOrg(t *testing.T) {
	for name, tc := range map[string]struct {
		org  *string
		want string
	}{
		"an org's trigger":      {org: ptrTo("acme"), want: "acme"},
		"a trigger with no org": {org: nil, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = tc.org })
			repo.triggers[trigger.ID] = trigger
			launcher := &fakeLauncher{}

			_, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
			require.NoError(t, err)
			assert.Equal(t, tc.want, launcher.snapshot()[0].Spec.OrgID)
		})
	}
}

// And the inbound path: webhook and integration events.
func TestEventFire_CarriesTheTriggersOrg(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	trigger.OrgID = ptrTo("acme")
	repo.triggers[trigger.ID] = trigger
	ev := pendingEvent(repo, trigger, "d1", map[string]any{"body": map[string]any{"ref": "main"}})
	launcher := &fakeLauncher{}

	_, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, "acme", launcher.snapshot()[0].Spec.OrgID)
}

// The trigger's org reaches its clients: an assigned trigger and a legacy one
// are told apart.
func TestToProto_CarriesTheOrg(t *testing.T) {
	assigned := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = ptrTo("acme") })
	out, err := ToProto(assigned, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, out.OrgId)
	assert.Equal(t, "acme", out.GetOrgId())

	legacy := testTrigger(t, func(*core.Trigger, *core.ScheduleConfig) {})
	out, err = ToProto(legacy, nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, out.OrgId, "a trigger with no org has none on the wire")
}

func ptrTo(s string) *string { return &s }
