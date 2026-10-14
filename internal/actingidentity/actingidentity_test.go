package actingidentity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFromAnUnmarkedContextIsTheOwner(t *testing.T) {
	assert.Equal(t, Identity{}, From(context.Background()))
	assert.False(t, From(context.Background()).IsApp())
	//nolint:staticcheck // a nil context is exactly the case under test
	assert.Equal(t, Identity{}, From(nil))
}

func TestWithMarksAnAppIdentity(t *testing.T) {
	ctx := With(context.Background(), Identity{Kind: App, Automation: "pr-review"})
	got := From(ctx)
	assert.True(t, got.IsApp())
	assert.Equal(t, "pr-review", got.Automation)
}

// Absence is the one way to say "owner": an owner identity, an unknown kind and
// the zero value all leave the context unmarked, so a code path that mislabels
// can only under-claim and never speak as the bot by accident.
func TestOnlyTheAppIsEverMarked(t *testing.T) {
	for name, id := range map[string]Identity{
		"zero": {}, "owner": {Kind: "owner", Automation: "x"}, "unknown": {Kind: "admin"}, "wrong case": {Kind: "App"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := With(context.Background(), id)
			assert.Equal(t, Identity{}, From(ctx))
			assert.False(t, From(ctx).IsApp())
		})
	}
}

// A child context inherits the mark, and a later owner mark does not clear it:
// nothing below the activity can downgrade the run out of the identity its
// activation chose, nor raise itself into it.
func TestTheMarkSurvivesDerivedContexts(t *testing.T) {
	ctx := With(context.Background(), Identity{Kind: App, Automation: "a"})
	type k struct{}
	derived, cancel := context.WithCancel(context.WithValue(ctx, k{}, 1))
	defer cancel()
	assert.True(t, From(derived).IsApp())
	assert.True(t, From(With(derived, Identity{Kind: "owner"})).IsApp())
}
