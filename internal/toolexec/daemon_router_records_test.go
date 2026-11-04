// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// fakeDaemonRecords is the slice of reliant's daemon registry resolution
// reads: the user's daemon rows (with the lifecycle phase control-plane
// mirrors in) and which of them hold a fresh attachment lease.
type fakeDaemonRecords struct {
	DaemonRecords
	daemons  []*db.Daemon
	attached []string
}

func (f *fakeDaemonRecords) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	var out []*db.Daemon
	for _, d := range f.daemons {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDaemonRecords) IsDaemonAttached(context.Context, string, time.Duration) (bool, error) {
	return len(f.attached) > 0, nil
}

func (f *fakeDaemonRecords) IsDaemonIDAttached(_ context.Context, id string, _ time.Duration) (bool, error) {
	return slices.Contains(f.attached, id), nil
}

func (f *fakeDaemonRecords) ListAttachedDaemonIDsForUser(context.Context, string, time.Duration) ([]string, error) {
	return f.attached, nil
}

type resumeCall struct {
	token    string
	daemonID string
}

// fakeResumer stands in for controlplane.v1.DaemonService/ResumeDaemon. With
// boundTokens set it enforces what the real one does for a daemon:resume
// token: it acts on exactly the daemon it is bound to.
type fakeResumer struct {
	mu          sync.Mutex
	calls       []resumeCall
	boundTokens map[string]string // daemon id -> the only token allowed to resume it
	err         error
}

func (f *fakeResumer) ResumeDaemon(_ context.Context, token, daemonID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, resumeCall{token: token, daemonID: daemonID})
	if f.err != nil {
		return f.err
	}
	if f.boundTokens != nil && f.boundTokens[daemonID] != token {
		return connect.NewError(connect.CodePermissionDenied, nil)
	}
	return nil
}

func strPtr(s string) *string { return &s }

func recordsRouter(records *fakeDaemonRecords) *NATSDaemonRouter {
	return NewNATSDaemonRouter(nil, WithDatabase(records))
}

// A daemon whose record says it is provisioning is still coming up: the
// caller gets the retryable ErrDaemonPending, not an id that NATS can only
// answer with "no responders".
func TestResolveDaemonID_ProvisioningRecordIsPending(t *testing.T) {
	for _, phase := range []string{"provisioning", "cloning"} {
		t.Run(phase, func(t *testing.T) {
			router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{{
				ID: "daemon-coming-up", UserID: "u", LifecyclePhase: strPtr(phase),
			}}})

			_, err := router.resolveDaemonID(context.Background(), "u", &DaemonSelector{ID: "daemon-coming-up"})
			require.Error(t, err)
			assert.True(t, IsDaemonPending(err), "got: %v", err)
			assert.Contains(t, err.Error(), "no daemon connected",
				"isDaemonConnectingError keys on this marker")
		})
	}
}

// A suspended (or suspending) daemon is pending too, and resolution never
// wakes it — that is EnsureAwake's job alone.
func TestResolveDaemonID_SuspendedRecordIsPendingAndNeverResumed(t *testing.T) {
	for _, phase := range []string{"suspending", "suspended"} {
		t.Run(phase, func(t *testing.T) {
			resumer := &fakeResumer{}
			router := NewNATSDaemonRouter(nil, WithDatabase(&fakeDaemonRecords{daemons: []*db.Daemon{{
				ID: "daemon-parked", UserID: "u", LifecyclePhase: strPtr(phase),
			}}}), WithDaemonResumer(resumer))

			_, err := router.resolveDaemonID(context.Background(), "u", nil)
			require.Error(t, err)
			assert.True(t, IsDaemonPending(err), "got: %v", err)
			assert.Empty(t, resumer.calls, "resolution must never resume")
		})
	}
}

// Attachment wins over the mirrored phase, exactly as the registry composes
// status: a stream attached right now is observed, the phase is a mirror.
func TestResolveDaemonID_AttachedBeatsLifecyclePhase(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{
		daemons: []*db.Daemon{
			{ID: "daemon-parked", UserID: "u", LifecyclePhase: strPtr("suspended")},
			{ID: "daemon-a", UserID: "u", LifecyclePhase: strPtr("suspended")},
		},
		attached: []string{"daemon-a"},
	})

	id, err := router.resolveDaemonID(context.Background(), "u", nil)
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
}

// A record with no lifecycle (every self-hosted daemon) and a stale lease is
// still handed to NATS: lease freshness is a decaying hint, and a
// connected-but-idle daemon must stay routable.
func TestResolveDaemonID_UnattachedRecordWithoutLifecycleFallsThroughToNATS(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "daemon-idle", UserID: "u"},
		{ID: "daemon-ready", UserID: "u", LifecyclePhase: strPtr("ready")},
	}})

	id, err := router.resolveDaemonID(context.Background(), "u", nil)
	require.NoError(t, err)
	assert.Equal(t, "daemon-idle", id)
}

// A user whose cloud machine is parked and who also has a stale self-hosted
// record: the parked machine is the one the request belongs to. Handing NATS
// the stale laptop's id would skip the wake entirely and fail on "no
// responders", so EnsureAwake wakes the managed machine and resolution waits
// for it.
func TestDaemonRecords_ParkedManagedMachineIsPreferredOverStaleSelfHosted(t *testing.T) {
	const userID = "user-laptop-and-cloud"
	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDatabase(&fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "old-laptop", UserID: userID},
		{ID: "cloud", UserID: userID, LifecyclePhase: strPtr("suspended")},
	}}), WithDaemonResumer(resumer), WithControlPlaneCredentials(credFunc(
		func(context.Context, string, string) (string, error) { return "jwt", nil })))

	_, err := router.resolveDaemonID(context.Background(), userID, nil)
	assert.True(t, IsDaemonPending(err), "got: %v", err)

	id, err := router.EnsureAwake(context.Background(), userID, nil)
	require.NoError(t, err)
	assert.Equal(t, "cloud", id)
	assert.Equal(t, []resumeCall{{token: "jwt", daemonID: "cloud"}}, resumer.calls)
}

// Typed and named selectors are evaluated against the records themselves —
// they used to be answered only by the control-plane lookup, and without it a
// typed selector matched nothing at all. Selectors speak "cloud"/"local",
// records "managed"/"self_hosted".
func TestDaemonRecords_SelectorMatchesTypeAndName(t *testing.T) {
	records := &fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "laptop", UserID: "u", DaemonType: strPtr("self_hosted"), Hostname: strPtr("mbp")},
		{ID: "cloud", UserID: "u", DaemonType: strPtr("managed"), Hostname: strPtr("box")},
	}, attached: []string{"laptop", "cloud"}}
	router := recordsRouter(records)

	for _, tc := range []struct {
		selector *DaemonSelector
		want     string
	}{
		{&DaemonSelector{Type: "cloud"}, "cloud"},
		{&DaemonSelector{Type: "local"}, "laptop"},
		{&DaemonSelector{Type: "any", Name: "box"}, "cloud"},
		{&DaemonSelector{Name: "mbp"}, "laptop"},
	} {
		id, err := router.resolveDaemonID(context.Background(), "u", tc.selector)
		require.NoError(t, err, "%+v", tc.selector)
		assert.Equal(t, tc.want, id, "%+v", tc.selector)
	}

	_, err := router.resolveDaemonID(context.Background(), "u", &DaemonSelector{Type: "cloud", Name: "mbp"})
	require.Error(t, err, "a selector no record satisfies must not fall back to some other daemon")
}

// A workflow's `daemon:` shorthand puts whatever the author wrote in Name. A
// machine id written there selects that machine — an exact id first, then a
// name — and a named machine answers to the name its owner gave it, not only
// to its hostname. Both used to match nothing, so every run of a workflow
// pinned to `daemon: <machine id>` (or `daemon: cicd`) failed its preflight.
func TestDaemonRecords_NameSelectorTriesIDFirstThenName(t *testing.T) {
	const cicdID = "e164d47d-1288-468d-ad0b-c487af2da1c4"
	records := &fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "laptop", UserID: "u", DaemonType: strPtr("self_hosted"), Hostname: strPtr("mbp")},
		{ID: cicdID, UserID: "u", DaemonType: strPtr("managed"), Name: "cicd", Hostname: strPtr("ws-ws-e164d47d")},
		// Named after the cicd machine's id: the id must still win.
		{ID: "impostor", UserID: "u", DaemonType: strPtr("self_hosted"), Name: cicdID},
	}, attached: []string{"laptop", cicdID, "impostor"}}
	router := recordsRouter(records)

	for _, tc := range []struct {
		selector *DaemonSelector
		want     string
	}{
		{&DaemonSelector{Name: cicdID}, cicdID},
		{&DaemonSelector{Name: "cicd"}, cicdID},
		{&DaemonSelector{Name: "ws-ws-e164d47d"}, cicdID},
		{&DaemonSelector{Name: "mbp"}, "laptop"},
		{&DaemonSelector{Type: "cloud", Name: cicdID}, cicdID},
	} {
		id, err := router.resolveDaemonID(context.Background(), "u", tc.selector)
		require.NoError(t, err, "%+v", tc.selector)
		assert.Equal(t, tc.want, id, "%+v", tc.selector)
	}

	// An id the user does not own is not taken for one: it stays a name, and
	// no machine has that name.
	_, err := router.resolveDaemonID(context.Background(), "u", &DaemonSelector{Name: "someone-elses-id"})
	require.Error(t, err)
	// The id selected by id still honours the selector's other criteria.
	_, err = router.resolveDaemonID(context.Background(), "u", &DaemonSelector{Type: "local", Name: cicdID})
	require.Error(t, err, "a local selector must not route to the managed machine its name is the id of")
}

func TestConnectedResolver_NameSelectorTriesIDFirstThenName(t *testing.T) {
	resolver := NewConnectedDaemonResolver(connectedList{
		{DaemonID: "d-1", Name: "cicd", Type: "cloud"},
		{DaemonID: "d-2", Name: "d-1", Type: "local"},
	})
	for name, want := range map[string]string{"d-1": "d-1", "cicd": "d-1", "d-2": "d-2"} {
		got, err := resolver.ResolveDaemons(context.Background(), "u", &DaemonSelector{Name: name})
		require.NoError(t, err)
		require.Len(t, got, 1, name)
		assert.Equal(t, want, got[0].DaemonID, name)
	}
}

type connectedList []DaemonInfo

func (l connectedList) ListConnectedDaemons(string) []DaemonInfo { return l }

// Machine A is suspended, so the gateway's connected list lacks it, while a
// connected machine B is NAMED A's id. Only the user's complete registry knows
// A's id is an id: every resolution stage must read the selector against it
// first, or A's requests route to B.
func TestNameSelector_IDOfDisconnectedMachineNeverRoutesToNamesake(t *testing.T) {
	const aID = "e164d47d-1288-468d-ad0b-c487af2da1c4"
	records := &fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: aID, UserID: "u", DaemonType: strPtr("managed"), Name: "cicd", LifecyclePhase: strPtr("suspended")},
		{ID: "b", UserID: "u", DaemonType: strPtr("self_hosted"), Name: aID},
	}, attached: []string{"b"}}
	router := NewNATSDaemonRouter(nil, WithDatabase(records), WithResolver(NewConnectedDaemonResolver(connectedList{
		{DaemonID: "b", Name: aID, Type: "local"},
	})))
	ctx := context.Background()

	id, err := router.resolveDaemonID(ctx, "u", &DaemonSelector{Name: aID})
	assert.NotEqual(t, "b", id, "A's id must never resolve to the machine named after it")
	require.Error(t, err, "suspended A is pending, not B")

	state, err := router.MachineState(ctx, "u", &DaemonSelector{Name: aID})
	require.NoError(t, err)
	assert.Equal(t, aID, state.DaemonID)
	assert.True(t, state.Suspended)

	online, err := router.isSelectedDaemonOnline(ctx, "u", &DaemonSelector{Name: aID})
	require.NoError(t, err)
	assert.False(t, online, "B being connected says nothing about A")

	// B is still reachable by its own id.
	id, err = router.resolveDaemonID(ctx, "u", &DaemonSelector{Name: "b"})
	require.NoError(t, err)
	assert.Equal(t, "b", id)
}
