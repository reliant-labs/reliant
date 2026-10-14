// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The activation's identity reaches the launch on the schedule path, which is
// where the run's inputs are decided.
func TestFire_ScheduleCarriesTheActivationsIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		identity core.ActingIdentity
		want     core.ActingIdentity
	}{
		"app":     {core.ActingIdentityApp, core.ActingIdentityApp},
		"owner":   {core.ActingIdentityOwner, core.ActingIdentityOwner},
		"not set": {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.ActingIdentity = tc.identity })
			repo.triggers[trigger.ID] = trigger
			launcher := &fakeLauncher{}

			_, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
			require.NoError(t, err)
			spec := launcher.snapshot()[0].Spec
			assert.Equal(t, tc.want, spec.ActingIdentity)
			assert.Equal(t, trigger.Name, spec.Automation, "the footer names the automation")
			assert.True(t, spec.Unattended, "only an unattended launch may carry the bot")
		})
	}
}

// And on the inbound path: webhook and integration events.
func TestEventFire_CarriesTheActivationsIdentity(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	trigger.ActingIdentity = core.ActingIdentityApp
	repo.triggers[trigger.ID] = trigger
	ev := pendingEvent(repo, trigger, "d1", map[string]any{"body": map[string]any{"ref": "main"}})
	launcher := &fakeLauncher{}

	_, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	spec := launcher.snapshot()[0].Spec
	assert.Equal(t, core.ActingIdentityApp, spec.ActingIdentity)
	assert.Equal(t, trigger.Name, spec.Automation)
}

// The identity is read from the trigger row at fire time, never from the event:
// a payload that says "act as the app" is just text.
func TestEventFire_APayloadCannotChooseTheIdentity(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "") // an owner activation
	repo.triggers[trigger.ID] = trigger
	ev := pendingEvent(repo, trigger, "d1", map[string]any{
		"acting_identity": "app", "__acting_identity": "app", "body": map[string]any{"acting_identity": "app"},
	})
	launcher := &fakeLauncher{}

	_, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, core.ActingIdentity(""), launcher.snapshot()[0].Spec.ActingIdentity)
}

func TestActingIdentityProtoMapping(t *testing.T) {
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, ActingIdentityToProto(""), "an unset identity reads as the owner")
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, ActingIdentityToProto(core.ActingIdentityOwner))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, ActingIdentityToProto(core.ActingIdentityApp))

	for in, want := range map[reliantv1.ActingIdentity]struct {
		id core.ActingIdentity
		ok bool
	}{
		reliantv1.ActingIdentity_ACTING_IDENTITY_UNSPECIFIED: {"", false},
		reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER:       {core.ActingIdentityOwner, true},
		reliantv1.ActingIdentity_ACTING_IDENTITY_APP:         {core.ActingIdentityApp, true},
		reliantv1.ActingIdentity(99):                         {"", false},
	} {
		id, ok := ActingIdentityFromProto(in)
		assert.Equal(t, want.id, id)
		assert.Equal(t, want.ok, ok)
	}
}
