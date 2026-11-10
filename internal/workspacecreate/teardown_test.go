// Copyright (c) 2026 Reliant Labs
package workspacecreate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/checkoutteardown"
	"github.com/reliant-labs/reliant/internal/db"
)

// statusMachine answers git.teardown_status with answer (or fails with err),
// recording the op ids it was asked about.
type statusMachine struct {
	answer string
	err    error
	asked  []string
}

func (m *statusMachine) Send(_ context.Context, commandType string, payload, resp any, _ int32) error {
	if commandType != checkoutteardown.StatusCommand {
		return errors.New("unexpected command " + commandType)
	}
	m.asked = append(m.asked, payload.(map[string]string)["op_id"])
	if m.err != nil {
		return m.err
	}
	return json.Unmarshal([]byte(m.answer), resp)
}

func pendingTeardown(created time.Time) *db.CheckoutTeardown {
	return &db.CheckoutTeardown{ID: "op-1", ProjectID: "p1", DaemonID: "d1", Kind: db.CheckoutTeardownReclone,
		Path: "/proj", State: db.CheckoutTeardownPending, CreatedAt: created}
}

// A workspace of a checkout an unconfirmed reclone may still be deleting:
// Insert asks the machine how the reclone ended, and creates only once it has.
func TestInsertSettlesAnUnconfirmedTeardownFirst(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		answer    string
		err       error
		created   time.Time
		wantState string
		wantErr   string // "" = the workspace is created
	}{
		{name: "it finished", answer: `{"op_id":"op-1","op_state":"done"}`, wantState: db.CheckoutTeardownDone},
		{name: "it failed", answer: `{"op_id":"op-1","op_state":"failed","error":"clone failed"}`, wantState: db.CheckoutTeardownFailed},
		{name: "it never arrived (fenced now)", answer: `{"op_id":"op-1","op_state":"fenced"}`, wantState: db.CheckoutTeardownNotRun},
		{
			name: "it is still running", answer: `{"op_id":"op-1","op_state":"running"}`,
			wantState: db.CheckoutTeardownPending, wantErr: "is still running",
		},
		{
			name: "the machine does not answer", err: errors.New("no daemon connected"),
			wantState: db.CheckoutTeardownPending, wantErr: "could not say how it ended",
		},
		{
			name: "an old machine, inside the grace", err: errors.New(`this machine is running an older version of Reliant (v1.0.0) that doesn't support "git.teardown_status"`),
			created: time.Now(), wantState: db.CheckoutTeardownPending, wantErr: "too old to confirm",
		},
		{
			name: "an old machine, past the grace", err: errors.New(`this machine is running an older version of Reliant (v1.0.0) that doesn't support "git.teardown_status"`),
			created: time.Now().Add(-checkoutteardown.LegacyGrace - time.Minute), wantState: db.CheckoutTeardownExpired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := &fakeStore{teardown: pendingTeardown(tc.created)}
			m := &statusMachine{answer: tc.answer, err: tc.err}
			wt, err := Insert(context.Background(), st, m, twoRepoReq())
			assert.Equal(t, []string{"op-1"}, m.asked, "the machine is asked about the op it was sent")
			assert.Equal(t, tc.wantState, st.teardown.State)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Contains(t, st.rows, wt.ID)
				return
			}
			var held *TeardownPendingError
			require.ErrorAs(t, err, &held)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), `a reclone of "demo"`)
			assert.Empty(t, st.rows, "no workspace row while the checkout may be going")
		})
	}
}

// With no machine to ask, the refusal stands.
func TestInsertWithoutAMachineRefusesWhileATeardownIsPending(t *testing.T) {
	t.Parallel()
	st := &fakeStore{teardown: pendingTeardown(time.Now())}
	_, err := Insert(context.Background(), st, nil, twoRepoReq())
	var held *TeardownPendingError
	require.ErrorAs(t, err, &held)
}
