package handlers

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"

	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// capturedLogger records every line the activity logs, with its key/value pairs.
type capturedLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLogger) add(level, msg string, kv []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf("%s %s %v", level, msg, kv))
}
func (c *capturedLogger) Debug(m string, kv ...interface{}) { c.add("DEBUG", m, kv) }
func (c *capturedLogger) Info(m string, kv ...interface{})  { c.add("INFO", m, kv) }
func (c *capturedLogger) Warn(m string, kv ...interface{})  { c.add("WARN", m, kv) }
func (c *capturedLogger) Error(m string, kv ...interface{}) { c.add("ERROR", m, kv) }
func (c *capturedLogger) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

var _ log.Logger = (*capturedLogger)(nil)

// The daemon's own words about a failure can echo the command's environment. They
// are masked before they reach any log line, and before the error is built.
func TestRunStepLogsAndErrorsAreMaskedOnTransportFailures(t *testing.T) {
	cases := map[string]*toolexec.TransportError{
		"result never came back": {
			Code: toolexec.ErrorCodeDaemonRoundTrip, Command: "deploy --key " + stagingKey,
			Detail: "no answer from the machine; env was TRADER_KEY=" + stagingKey,
		},
		"never dispatched, machine gone": {
			Code: toolexec.ErrorCodeDaemonUnreached, Command: "deploy",
			Detail: "tool request failed for env TRADER_KEY=" + stagingKey, NotDispatched: true, DaemonID: "daemon-abcdef123",
		},
	}
	for name, transportErr := range cases {
		t.Run(name, func(t *testing.T) {
			repo := setupTestRepo(t)
			chatID := createTestChat(t, repo)
			exec := &mockRunExecutor{responses: []mockRunResponse{{exitCode: -1, err: transportErr}}}
			a := NewExecuteRunStepActivity(repo, nil, exec).WithSecretResolver(traderResolver())
			// A machine that is not coming back, with a fake clock so the wait is instant.
			clock := newFakeClock()
			wait := defaultMachineWait()
			wait.now, wait.sleep = clock.Now, clock.Sleep
			a.machineWait = wait

			logs := &capturedLogger{}
			suite := &temporaltest.WorkflowTestSuite{}
			suite.SetLogger(logs)
			env := suite.NewTestActivityEnvironment()
			env.RegisterActivity(a.Execute)
			_, err := env.ExecuteActivity(a.Execute, runSecretInput(chatID, "deploy --key "+stepsecrets.Marker("TRADER_KEY")))

			require.Error(t, err)
			assert.NotContains(t, err.Error(), stagingKey, "the returned error")
			captured := logs.all()
			require.NotEmpty(t, captured, "the activity logged the failure")
			assert.NotContains(t, captured, stagingKey, "every log line")
			assert.Contains(t, captured, stepsecrets.Mask, "the line is there, with the value masked")
		})
	}
}
