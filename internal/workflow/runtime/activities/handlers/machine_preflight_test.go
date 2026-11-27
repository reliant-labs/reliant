// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// preflightRouter answers machine.preflight with answer.
type preflightRouter struct {
	toolexectest.Stub
	answer func(payload []byte) ([]byte, error)
	sentTo []string
}

func (r *preflightRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType != machinepreflight.Command {
		return nil, errors.New("unexpected command " + commandType)
	}
	r.sentTo = append(r.sentTo, daemonID)
	return r.answer(payload)
}

func TestMachinePreflightActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := db.NewTestRepo(t)
	defer repo.Close()
	projectID := "p-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, Name: "P", Path: "/tmp/p", UserID: "u", CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: "u", Title: "c", ProjectID: projectID, State: db.ChatStateIdle, CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	result := func(r machinepreflight.Result) func([]byte) ([]byte, error) {
		return func([]byte) ([]byte, error) { return json.Marshal(r) }
	}
	for _, tt := range []struct {
		name       string
		answer     func([]byte) ([]byte, error)
		want       MachinePreflightOutput
		wantErr    bool
		wantInSpec string
	}{
		{
			name:       "a machine that passes",
			answer:     result(machinepreflight.Result{OK: true}),
			want:       MachinePreflightOutput{DaemonID: "test-daemon-id"},
			wantInSpec: `"tools":["git","task"]`,
		},
		{
			name: "a machine that fails is a verdict, with its reasons",
			answer: result(machinepreflight.Result{Failures: []machinepreflight.Failure{
				{Reason: machinepreflight.ReasonTmpUnusable, Message: "/tmp has mode 0755, not 1777 (drwxrwxrwt)"},
				{Reason: machinepreflight.ReasonLoadHigh, Message: "load is 3.00 per CPU"},
			}}),
			want: MachinePreflightOutput{Failed: true, Reason: "tmp_unusable", DaemonID: "test-daemon-id",
				Message: "/tmp has mode 0755, not 1777 (drwxrwxrwt); load is 3.00 per CPU"},
		},
		{
			name: "a daemon older than the check fails it, saying to update",
			answer: func([]byte) ([]byte, error) {
				return nil, errors.New(`this machine is running an older version of Reliant (v1.2.3) that doesn't support "machine.preflight" — update it to continue`)
			},
			want: MachinePreflightOutput{Failed: true, Reason: "daemon_outdated", DaemonID: "test-daemon-id",
				Message: "the machine's Reliant predates the preflight check; update it to run nodes that set preflight"},
		},
		{
			name:    "a check that could not run is an error for the retry policy",
			answer:  func([]byte) ([]byte, error) { return nil, errors.New("gateway round trip failed") },
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := &preflightRouter{answer: tt.answer}
			var payloads []string
			inner := router.answer
			router.answer = func(p []byte) ([]byte, error) { payloads = append(payloads, string(p)); return inner(p) }
			act := NewMachinePreflightActivity(repo, NewTriggerReads(repo.ORM()), workspaceRowsOf(repo), router)

			got, err := act.Execute(ctx, MachinePreflightInput{ChatID: chatID, WorkflowID: "wf", NodePath: "fix",
				Spec: machinepreflight.Spec{Tools: []string{"task"}}.WithDefaults()})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, []string{"test-daemon-id"}, router.sentTo)
			if tt.wantInSpec != "" {
				require.Len(t, payloads, 1)
				assert.Contains(t, payloads[0], tt.wantInSpec)
			}
		})
	}

	t.Run("no router has no machine to check", func(t *testing.T) {
		got, err := NewMachinePreflightActivity(repo, NewTriggerReads(repo.ORM()), workspaceRowsOf(repo), nil).Execute(ctx, MachinePreflightInput{ChatID: chatID})
		require.NoError(t, err)
		assert.False(t, got.Failed)
	})
}
