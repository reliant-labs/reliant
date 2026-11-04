// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// slowTerminalWritesSQL injects trouble into the first %[3]d writes of a
// terminal status to the story user's workflows: %[2]s, either a pause longer
// than one WorkflowStatus attempt used to be allowed (5s) or a refusal. The
// count lives in a sequence because nextval survives the rollback of the
// transaction it refused. Scoped to one user (owner_user_id) so the stories
// that share this database are untouched.
const slowTerminalWritesSQL = `
CREATE SEQUENCE %[1]s_seq;
CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.owner_user_id = '%[4]s' AND NEW.state = 3 AND NEW.stop_reason <> 3
	   AND nextval('%[1]s_seq') <= %[3]d THEN
		%[2]s;
	END IF;
	RETURN NEW;
END $$;
CREATE TRIGGER %[1]s BEFORE UPDATE ON workflows
	FOR EACH ROW EXECUTE FUNCTION %[1]s();
`

// Story 15: a run's terminal status lands however the database treats it.
// The run's last write to its workflow row is its terminal status, and
// nothing writes the row after it, so a lost one leaves the chat reading as
// running over a run that has ended (e2e story 05 in the 2026-10-26 stress
// runs: Temporal finished, the row stayed ACTIVE). It was sent with a 5s
// budget per attempt and three attempts, and a failed write was logged while
// the activity reported success; it is now retried until it lands.
func TestStory15_TerminalStatusLandsOnASlowOrRefusingDatabase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		trouble string // the plpgsql statement each injected write runs
		count   int
	}{
		// Longer than an attempt's old 5s budget, three times: every one
		// of the three attempts it used to get timed out.
		{name: "slow", trouble: "PERFORM pg_sleep(6)", count: 3},
		// More refusals than the old three attempts.
		{name: "refusing", trouble: "RAISE EXCEPTION 'injected: terminal status write refused'", count: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, NewScriptedLLM(Turn{Text: "Done."}))

			name := "e2e_terminal_" + strings.ReplaceAll(strings.ToLower(h.UserID), "-", "_")
			_, err := h.Stack.Repo.ORM().Bun().ExecContext(h.Ctx, fmt.Sprintf(slowTerminalWritesSQL, name, tc.trouble, tc.count, h.UserID))
			require.NoError(t, err, "inject the database trouble")
			t.Cleanup(func() {
				_, _ = h.Stack.Repo.ORM().Bun().ExecContext(h.Ctx, fmt.Sprintf(
					"DROP TRIGGER IF EXISTS %[1]s ON workflows; DROP FUNCTION IF EXISTS %[1]s(); DROP SEQUENCE IF EXISTS %[1]s_seq;", name))
			})

			created := h.StartChat("builtin://agent", "Say done", map[string]any{"mode": "auto"})
			workflowID := created.WorkflowId

			h.WaitTemporalWorkflowDone(workflowID)
			// The run does not end until its status is written, so it is
			// there the moment Temporal reports the run done.
			wf, err := h.Stack.Repo.GetWorkflow(h.Ctx, workflowID)
			require.NoError(t, err)
			require.Equal(t, db.Completed(), wf.Status, "the terminal status landed before the run ended")
		})
	}
}
