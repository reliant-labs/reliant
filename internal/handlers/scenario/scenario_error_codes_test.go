package scenario_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// readFailures is what a repository call can fail with, and the code the
// client reads for each: a classified error keeps its own code, and an
// opaque one is Internal with the handler's summary and none of its text.
var readFailures = []struct {
	name string
	err  error
	want connect.Code
}{
	{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
	{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
}

// The scenario reads (the project check, the workflow draft, the saved
// scenario) and writes pass a classified repository error through and redact
// an opaque one: an outage is no longer "workflow not found" or "scenario not
// found".
func TestScenarioRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range readFailures {
		t.Run("ListScenarios, checking the project: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newScenarioRepo(&fixture{})
			repo.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) { return nil, tc.err }
			svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: repo}))
			projectID := ownProjectID
			_, err := svc.ListScenarios(scenario.AuthedContext(t), connect.NewRequest(&pb.ListScenariosRequest{ProjectId: &projectID, WorkflowSlug: ownWorkflowSlug}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("CreateScenario, reading the workflow: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newScenarioRepo(&fixture{})
			repo.GetWorkflowDraftBySlugFunc = func(context.Context, string, string) (*db.WorkflowDraft, error) { return nil, tc.err }
			svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: repo}))
			_, err := svc.CreateScenario(scenario.AuthedContext(t), connect.NewRequest(&pb.CreateScenarioRequest{
				WorkflowSlug: ownWorkflowSlug, Scenario: &pb.ScenarioDefinition{Name: "s"},
			}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("DeleteScenario, reading the scenario: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newScenarioRepo(&fixture{})
			repo.GetWorkflowScenarioFunc = func(context.Context, string) (*db.WorkflowScenario, error) { return nil, tc.err }
			svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: repo}))
			_, err := svc.DeleteScenario(scenario.AuthedContext(t), connect.NewRequest(&pb.DeleteScenarioRequest{ScenarioId: ownScenarioID}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("DeleteScenario, deleting: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newScenarioRepo(&fixture{})
			repo.DeleteWorkflowScenarioFunc = func(context.Context, string) error { return tc.err }
			svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: repo}))
			_, err := svc.DeleteScenario(scenario.AuthedContext(t), connect.NewRequest(&pb.DeleteScenarioRequest{ScenarioId: ownScenarioID}))
			requireClientReads(t, err, tc.want)
		})
	}
}

// failingSender fails every daemon command with err.
type failingSender struct{ err error }

func (f failingSender) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return nil, f.err
}

// A scenario file the daemon cannot write is classified by the machine's state
// (daemonerr.Error), never Internal: the web's daemon-wait markers survive,
// and transport text does not reach the client.
func TestUploadScenario_DaemonFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		err      error
		want     connect.Code
		contains string
	}{
		{"a starting machine", toolexec.ErrDaemonPending, connect.CodeUnavailable, "no daemon connected"},
		{"no machine", toolexec.ErrNoDaemon, connect.CodeFailedPrecondition, "no daemon available"},
		{"a busy machine", toolexec.ErrDaemonTimeout, connect.CodeDeadlineExceeded, "did not answer in time"},
		{"a transport failure", errors.New("nats: subject reliant.secret.daemon"), connect.CodeUnavailable, "write the scenario file failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{
				Repo:   newScenarioRepo(&fixture{}),
				Router: failingSender{err: tc.err},
			}))
			_, err := svc.UploadScenario(scenario.AuthedContext(t), connect.NewRequest(&pb.UploadScenarioRequest{
				ProjectId: ownProjectID, WorkflowSlug: ownWorkflowSlug, Filename: "drafts.yaml", YamlContent: fixtureScenario,
			}))
			requireClientReads(t, err, tc.want)
			msg := svcerr.ToConnect(err).Message()
			require.True(t, strings.Contains(msg, tc.contains), "message %q lacks %q", msg, tc.contains)
		})
	}
}

// A scenario that does not parse is the caller's to fix, so the parser's
// own words stay in the InvalidArgument message.
func TestUploadScenario_InvalidYAMLKeepsTheParserText(t *testing.T) {
	t.Parallel()

	svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(&fixture{}), Router: failingSender{}}))
	_, err := svc.UploadScenario(scenario.AuthedContext(t), connect.NewRequest(&pb.UploadScenarioRequest{
		ProjectId: ownProjectID, WorkflowSlug: ownWorkflowSlug, Filename: "drafts.yaml", YamlContent: "events: [",
	}))
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	msg := svcerr.ToConnect(err).Message()
	require.True(t, strings.HasPrefix(msg, "invalid scenario YAML: "), "message = %q", msg)
	require.Greater(t, len(msg), len("invalid scenario YAML: "), "the parser's reason is kept")
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}
