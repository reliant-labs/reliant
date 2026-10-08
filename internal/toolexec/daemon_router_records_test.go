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
	db.Repository
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
