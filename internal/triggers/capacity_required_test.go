package triggers

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// The capacity store is a required role. A store that lacks it cannot be
// handed to a firer or the redriver (the build fails), and a nil one fails at
// construction, so a capped trigger can never run unadmitted because the store
// quietly did not implement admission.
func TestNarrowStoresDoNotSatisfyTheCapacityRoles(t *testing.T) {
	for name, narrow := range map[string]any{
		"the repository facade":   (*db.Repo)(nil),
		"the trigger Repo":        (Repo)(nil),
		"the inbound EventRepo":   (EventRepo)(nil),
		"an event repo, no slots": struct{ EventRepo }{},
	} {
		t.Run(name, func(t *testing.T) {
			_, fire := narrow.(FireRepo)
			_, events := narrow.(EventFireRepo)
			_, capacity := narrow.(CapacityStore)
			require.False(t, fire || events || capacity)
		})
	}
	var _ FireRepo = Stores{}
	var _ EventFireRepo = Stores{}
}

func TestConstructionFailsWithoutACapacityStore(t *testing.T) {
	require.PanicsWithValue(t, "triggers: NewFirer requires a CapacityStore", func() { NewFirer(nil, nil) })
	require.PanicsWithValue(t, "triggers: NewEventFirer requires a CapacityStore", func() { NewEventFirer(nil, nil) })
	require.PanicsWithValue(t, "triggers: NewRedriver requires a CapacityStore", func() { NewRedriver(nil, nil, "") })
	require.PanicsWithValue(t, "triggers: NewStores requires a repository", func() { NewStores(nil) })
}
