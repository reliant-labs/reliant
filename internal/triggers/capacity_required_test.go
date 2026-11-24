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

// The owner, receipt and root-status reads are required roles too: a store
// that lacks them is rejected by the compiler, not skipped at run time. The
// bare facade cannot be an Intake or Poll store any more, because the
// generated reads moved off it.
func TestRequiredRolesRejectTheBareFacade(t *testing.T) {
	var facade any = (*db.Repo)(nil)
	for name, role := range map[string]func(any) bool{
		"IntakeRepo":    func(v any) bool { _, ok := v.(IntakeRepo); return ok },
		"PollRepo":      func(v any) bool { _, ok := v.(PollRepo); return ok },
		"FireRepo":      func(v any) bool { _, ok := v.(FireRepo); return ok },
		"EventFireRepo": func(v any) bool { _, ok := v.(EventFireRepo); return ok },
	} {
		require.False(t, role(facade), "*db.Repo must not satisfy %s on its own", name)
	}
	var _ OwnerSource = (*db.Repo)(nil)
	var _ IntakeRepo = Stores{}
	var _ PollRepo = Stores{}
}

// The projection, alerting and syncer stores are typed roles, and the
// production Stores satisfies each; the facade alone no longer can.
func TestProjectionAndAlertingRolesNeedTheGeneratedStore(t *testing.T) {
	var facade any = (*db.Repo)(nil)
	_, projection := facade.(ProjectionStore)
	_, alerting := facade.(AlertingStore)
	require.False(t, projection || alerting, "*db.Repo must not satisfy ProjectionStore or AlertingStore on its own")
	var _ ProjectionStore = Stores{}
	var _ AlertingStore = Stores{}
	var _ Repo = Stores{}
}
