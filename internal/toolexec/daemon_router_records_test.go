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
	// attachedAt is when each attached daemon's current session began.
	attachedAt map[string]time.Time
}

func (f *fakeDaemonRecords) ListFreshDaemonAttachmentsForUser(_ context.Context, userID string, _ time.Duration) ([]*db.DaemonAttachment, error) {
	var out []*db.DaemonAttachment
	for _, id := range f.attached {
		out = append(out, &db.DaemonAttachment{DaemonID: id, UserID: userID, AttachedAt: f.attachedAt[id]})
	}
	return out, nil
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

// A name selector is the name the owner gave the machine (daemons.name), not
// its hostname. A machine `cicd` runs on host `ws-ws-e164d47d`: before this was
// matched on the hostname, so `daemon: {name: cicd}` found no record while the
// machine was disconnected, preflight failed in 3s instead of waking it, and
// the machine stayed down while runs kept asking for it.
func TestDaemonRecords_NameSelectorMatchesTheMachineNameNotItsHostname(t *testing.T) {
	records := &fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "e164d47d", UserID: "u", Name: "cicd", Hostname: strPtr("ws-ws-e164d47d"), DaemonType: strPtr("managed"), LifecyclePhase: strPtr("suspended")},
		{ID: "2aab1465", UserID: "u", Name: "default", Hostname: strPtr("ws-ws-2aab1465"), DaemonType: strPtr("managed")},
	}}
	router := recordsRouter(records)

	rec, found, _, err := router.lookupDaemonRecord(context.Background(), "u", &DaemonSelector{Name: "cicd"})
	require.NoError(t, err)
	require.True(t, found, "the disconnected machine is found by its name")
	assert.Equal(t, "e164d47d", rec.id)
	assert.True(t, rec.wakeable(), "and, being suspended, can be woken")

	_, found, _, err = router.lookupDaemonRecord(context.Background(), "u", &DaemonSelector{Name: "ws-ws-e164d47d"})
	require.NoError(t, err)
	assert.False(t, found, "a named machine is not matched by its hostname")

	state, err := router.MachineState(context.Background(), "u", &DaemonSelector{Name: "cicd"})
	require.NoError(t, err)
	assert.True(t, state.Exists)
	assert.True(t, state.Suspended)
	assert.Equal(t, "e164d47d", state.DaemonID)
}

// A suspended machine named by a workflow is woken by the wake the run's
// preflight makes, for that machine's id.
func TestDaemonRecords_NameSelectorWakesTheSuspendedMachine(t *testing.T) {
	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDatabase(&fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "e164d47d", UserID: "u", Name: "cicd", Hostname: strPtr("ws-ws-e164d47d"), DaemonType: strPtr("managed"), LifecyclePhase: strPtr("suspended")},
	}}), WithDaemonResumer(resumer), WithControlPlaneCredentials(credFunc(
		func(context.Context, string, string) (string, error) { return "jwt", nil })))

	id, err := router.EnsureAwake(context.Background(), "u", &DaemonSelector{Name: "cicd"})
	require.NoError(t, err)
	assert.Equal(t, "e164d47d", id)
	assert.Equal(t, []resumeCall{{token: "jwt", daemonID: "e164d47d"}}, resumer.calls)
}

// A daemon the control plane has not named yet (a self-hosted one that
// registered first) is known by its hostname, which is the documented
// fallback; once it has a name, only the name answers.
func TestDaemonRecords_UnnamedDaemonFallsBackToItsHostname(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "laptop", UserID: "u", Hostname: strPtr("mbp")},
	}, attached: []string{"laptop"}})

	id, err := router.resolveDaemonID(context.Background(), "u", &DaemonSelector{Name: "mbp"})
	require.NoError(t, err)
	assert.Equal(t, "laptop", id)

	router = recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "laptop", UserID: "u", Name: "work laptop", Hostname: strPtr("mbp")},
	}, attached: []string{"laptop"}})
	_, err = router.resolveDaemonID(context.Background(), "u", &DaemonSelector{Name: "mbp"})
	require.Error(t, err, "named: the hostname no longer answers")
	id, err = router.resolveDaemonID(context.Background(), "u", &DaemonSelector{Name: "work laptop"})
	require.NoError(t, err)
	assert.Equal(t, "laptop", id)
}

// AttachedSince is when the session of the machine a selector names began:
// what lets delivery tell a machine that connected after it gave up from one
// that has simply stayed up.
func TestAttachedSince_IsTheStartOfTheMatchingMachinesSession(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	router := recordsRouter(&fakeDaemonRecords{
		daemons: []*db.Daemon{
			{ID: "cicd-id", UserID: "u", Name: "cicd", Hostname: strPtr("ws-ws-e164d47d")},
			{ID: "default-id", UserID: "u", Name: "default", Hostname: strPtr("ws-ws-2aab1465")},
			{ID: "gone-id", UserID: "u", Name: "gone"},
		},
		attached:   []string{"cicd-id", "default-id"},
		attachedAt: map[string]time.Time{"cicd-id": t0, "default-id": t0.Add(time.Hour)},
	})

	since, ok, err := router.AttachedSince(context.Background(), "u", &DaemonSelector{Name: "cicd"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, t0, since, "the session of cicd, not of the user's other machine")

	_, ok, err = router.AttachedSince(context.Background(), "u", &DaemonSelector{Name: "gone"})
	require.NoError(t, err)
	assert.False(t, ok, "a machine with no live session has no attach time")

	since, ok, err = router.AttachedSince(context.Background(), "u", &DaemonSelector{ID: "default-id"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, t0.Add(time.Hour), since)
}
