// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/rowreads"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// A box started with `reliant daemon start --label pool=pipeline` joins the
// pool on connect, with no one labelling it: the labels it declares are
// merged into its machine's durable labels on every registration. These run
// the real ConnectDaemon / RegisterOutboundConnection paths against the real
// SQL.

func machineLabels(t *testing.T, repo *db.Repo, daemonID string) map[string]string {
	t.Helper()
	d, err := store.GetDaemon(context.Background(), repo.ORM(), daemonID)
	require.NoError(t, err)
	return d.Labels
}

func labelledRegister(daemonID string, labels map[string]string) *reliantv1.DaemonRegister {
	return &reliantv1.DaemonRegister{DaemonId: daemonID, Hostname: "box", Platform: "linux", DaemonType: "local", MachineLabels: labels}
}

func TestConnectDaemonMergesDeclaredMachineLabels(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(rowreads.WithRepo(repo))
	t.Cleanup(svc.Close)
	ctx := context.Background()
	box := uuid.NewString()

	// First connect: the row is created and labelled.
	_, err := connectRegistering(t, svc, "user-A", box, labelledRegister("", map[string]string{"pool": "pipeline", "gate": "yes"}))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pool": "pipeline", "gate": "yes"}, machineLabels(t, repo, box))

	// The owner adds a label in the app; a reconnect declaring a changed
	// value sets that key and leaves the owner's label alone.
	ok, err := store.SetDaemonLabels(ctx, repo.ORM(), "user-A", box, map[string]string{"pool": "pipeline", "gate": "yes", "team": "ci"})
	require.NoError(t, err)
	require.True(t, ok)
	_, err = connectRegistering(t, svc, "user-A", box, labelledRegister(box, map[string]string{"pool": "gate"}))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pool": "gate", "gate": "yes", "team": "ci"}, machineLabels(t, repo, box))

	// A daemon that declares nothing (an old one, or no --label) writes
	// nothing; nor does an invalid set, and the connection still succeeds.
	_, err = connectRegistering(t, svc, "user-A", box, labelledRegister(box, nil))
	require.NoError(t, err)
	_, err = connectRegistering(t, svc, "user-A", box, labelledRegister(box, map[string]string{"Pool": "x"}))
	require.NoError(t, err, "invalid declared labels must not refuse the connection")
	require.Equal(t, map[string]string{"pool": "gate", "gate": "yes", "team": "ci"}, machineLabels(t, repo, box))
}

// The labels a daemon declares at connect land on the machine it connected
// as and nowhere else: a credential bound to one machine that asserts
// another's id labels its own (the bound id wins), an unbound credential
// cannot connect as, or label, a reserved machine, and another user's
// credential labels nothing of the owner's. This is the one label write that
// needs no person (DaemonRegistryService.SetMachineLabels does).
func TestConnectDaemonDeclaredLabelsTouchOnlyTheConnectingMachine(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(rowreads.WithRepo(repo))
	t.Cleanup(svc.Close)
	boxA, boxB := uuid.NewString(), uuid.NewString()

	_, err := connectRegistering(t, svc, "user-A", boxA, labelledRegister("", map[string]string{"pool": "a"}))
	require.NoError(t, err)
	_, err = connectRegistering(t, svc, "user-A", boxB, labelledRegister("", map[string]string{"pool": "b"}))
	require.NoError(t, err)

	// Bound to A, asserting B: A is the machine that connected.
	_, err = connectRegistering(t, svc, "user-A", boxA, labelledRegister(boxB, map[string]string{"pool": "stolen"}))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pool": "stolen"}, machineLabels(t, repo, boxA))
	require.Equal(t, map[string]string{"pool": "b"}, machineLabels(t, repo, boxB), "a credential bound to A relabelled B")

	// An unbound credential of the owner asserting reserved B is refused.
	_, err = connectRegistering(t, svc, "user-A", "", labelledRegister(boxB, map[string]string{"pool": "stolen"}))
	requireReservedRefusal(t, err)
	require.Equal(t, map[string]string{"pool": "b"}, machineLabels(t, repo, boxB), "an unbound credential relabelled a reserved machine")

	// Another user's credential, bound or asserting, labels nothing of A's.
	_, err = connectRegistering(t, svc, "user-B", boxB, labelledRegister("", map[string]string{"pool": "stolen"}))
	require.Error(t, err)
	_, err = connectRegistering(t, svc, "user-B", "", labelledRegister(boxB, map[string]string{"pool": "stolen"}))
	require.Error(t, err)
	require.Equal(t, map[string]string{"pool": "b"}, machineLabels(t, repo, boxB), "another user's credential relabelled the owner's machine")
}

// A merge that would leave the machine more labels than allowed is not
// written, and the connection carries on.
func TestConnectDaemonSkipsADeclaredMergePastTheLabelLimit(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(rowreads.WithRepo(repo))
	t.Cleanup(svc.Close)
	box := uuid.NewString()

	_, err := connectRegistering(t, svc, "user-A", box, labelledRegister("", nil))
	require.NoError(t, err)
	full := map[string]string{}
	for i := 0; i < 32; i++ {
		full[fmt.Sprintf("k%d", i)] = "v"
	}
	ok, err := store.SetDaemonLabels(context.Background(), repo.ORM(), "user-A", box, full)
	require.NoError(t, err)
	require.True(t, ok)

	_, err = connectRegistering(t, svc, "user-A", box, labelledRegister(box, map[string]string{"pool": "pipeline"}))
	require.NoError(t, err)
	require.Equal(t, full, machineLabels(t, repo, box))
}

// The gateway's dial-in to a managed pod declares labels the same way.
func TestRegisterOutboundConnectionMergesDeclaredMachineLabels(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(rowreads.WithRepo(repo))
	t.Cleanup(svc.Close)
	pod := uuid.NewString()

	out := dialInPodRegistering(t, svc, "user-A", pod, &reliantv1.DaemonRegister{
		Hostname: "ws-pod", Platform: "linux", DaemonType: "cloud", MachineLabels: map[string]string{"pool": "gate"},
	})
	t.Cleanup(out.Disconnect)
	require.Equal(t, map[string]string{"pool": "gate"}, machineLabels(t, repo, pod))
}
