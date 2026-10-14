// Copyright (c) 2025 Reliant Labs
package lineage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

type reader map[string]*core.TriggerEvent

func (r reader) GetTriggerEventByChatID(_ context.Context, chatID string) (*core.TriggerEvent, error) {
	if ev, ok := r[chatID]; ok {
		return ev, nil
	}
	return nil, core.ErrTriggerEventNotFound
}

func ptr(s string) *string { return &s }

func TestExtendDoesNotMutateAndContainsFindsAnyAncestor(t *testing.T) {
	var root Lineage
	a := root.Extend("t-a", "r1")
	b := a.Extend("t-b", "r2")
	assert.Empty(t, root)
	assert.Len(t, a, 1)
	assert.Len(t, b, 2)
	assert.True(t, b.Contains("t-a"))
	assert.False(t, b.Contains("t-c"))
}

func TestBlockedByTheCycleRuleAndByTheCap(t *testing.T) {
	chain := Lineage{{TriggerID: "t-a"}, {TriggerID: "t-b"}}
	reason, blocked := chain.Blocked("t-a")
	assert.True(t, blocked)
	assert.Contains(t, reason, "descends from a run this trigger launched")

	_, blocked = chain.Blocked("t-new")
	assert.False(t, blocked)

	full := make(Lineage, MaxDepth)
	for i := range full {
		full[i] = Link{TriggerID: string(rune('a' + i))}
	}
	reason, blocked = full.Blocked("t-new")
	assert.True(t, blocked)
	assert.Contains(t, reason, "5 hops deep")
}

func TestAtCapIsTheOnlyGuardAnIntegrationLaunchIsHeldTo(t *testing.T) {
	// A chain may revisit a trigger: a workflow may deliberately re-trigger
	// itself. Only depth stops it.
	chain := Lineage{{TriggerID: "t-a"}, {TriggerID: "t-a"}}
	_, capped := chain.AtCap()
	assert.False(t, capped)

	chain = make(Lineage, MaxDepth)
	_, capped = chain.AtCap()
	assert.True(t, capped)
	_, capped = make(Lineage, MaxDepth-1).AtCap()
	assert.False(t, capped)
}

func TestPayloadRoundTripAndMalformedLineageIsAnError(t *testing.T) {
	l := Lineage{{TriggerID: "t-a", SourceRunID: "r1"}}
	got, err := FromPayload(map[string]any{PayloadKey: l.ToPayload()})
	require.NoError(t, err)
	assert.Equal(t, l, got)

	got, err = FromPayload(map[string]any{})
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = FromPayload(map[string]any{PayloadKey: "not a list"})
	assert.Error(t, err, "guessing would disable the guard")
	_, err = FromPayload(map[string]any{PayloadKey: []any{map[string]any{"source_run_id": "r"}}})
	assert.Error(t, err)
}

func TestOfReadsAWorkflowEventsPayloadLineage(t *testing.T) {
	l := Lineage{{TriggerID: "t-a", SourceRunID: "r1"}}
	r := reader{"chat": {Kind: core.TriggerEventKindWorkflowEvent, TriggerID: ptr("t-b"), Payload: map[string]any{PayloadKey: l.ToPayload()}}}
	got, err := Of(context.Background(), r, "chat")
	require.NoError(t, err)
	assert.Equal(t, l, got)
}

func TestOfDerivesAnIntegrationRunsLineageFromItsOrigin(t *testing.T) {
	r := reader{"chat": {
		Kind: core.TriggerEventKindIntegration, TriggerID: ptr("t-b"),
		Origin: &core.TriggerOrigin{
			CausedByReliant: true, RunID: "writer-run",
			Lineage: []core.OriginLink{{TriggerID: "t-a", SourceRunID: "r1"}}, Depth: 1,
		},
	}}
	got, err := Of(context.Background(), r, "chat")
	require.NoError(t, err)
	assert.Equal(t, Lineage{{TriggerID: "t-a", SourceRunID: "r1"}, {TriggerID: "t-b", SourceRunID: "writer-run"}}, got,
		"the chain that led to the writer, plus the hop this launch was")
}

func TestOfPadsAWithheldLineageSoItsDepthStillCounts(t *testing.T) {
	r := reader{"chat": {
		Kind: core.TriggerEventKindIntegration, TriggerID: ptr("t-b"),
		Origin: &core.TriggerOrigin{CausedByReliant: true, Foreign: true, Depth: 3},
	}}
	got, err := Of(context.Background(), r, "chat")
	require.NoError(t, err)
	assert.Len(t, got, 4, "three hops we may not see, plus this one")
	assert.False(t, got.Contains("t-a"), "anonymous hops match no trigger")
}

func TestOfIsEmptyForARunNothingOfOursCaused(t *testing.T) {
	for name, ev := range map[string]*core.TriggerEvent{
		"no origin":      {Kind: core.TriggerEventKindIntegration, TriggerID: ptr("t")},
		"not by reliant": {Kind: core.TriggerEventKindIntegration, TriggerID: ptr("t"), Origin: &core.TriggerOrigin{Confidence: core.OriginNone}},
		"a schedule":     {Kind: core.TriggerEventKindSchedule},
		"a chat":         {Kind: core.TriggerEventKindChatStart},
	} {
		got, err := Of(context.Background(), reader{"chat": ev}, "chat")
		require.NoError(t, err, name)
		assert.Empty(t, got, name)
	}
	got, err := Of(context.Background(), reader{}, "unknown-chat")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestOfPropagatesAStoreError(t *testing.T) {
	_, err := Of(context.Background(), failing{}, "chat")
	assert.Error(t, err)
}

type failing struct{}

func (failing) GetTriggerEventByChatID(context.Context, string) (*core.TriggerEvent, error) {
	return nil, errors.New("db down")
}

func TestCoreRoundTrip(t *testing.T) {
	l := Lineage{{TriggerID: "t-a", SourceRunID: "r1"}, {TriggerID: "t-b", SourceRunID: "r2"}}
	assert.Equal(t, l, FromCore(l.ToCore()))
	assert.Nil(t, Lineage(nil).ToCore())
	assert.Nil(t, FromCore(nil))
}
