// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"go.temporal.io/sdk/testsuite"
)

type capLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *capLogger) log(level, msg string, kv ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", level, msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " | %v=%v", kv[i], kv[i+1])
	}
	l.lines = append(l.lines, b.String())
}

func (l *capLogger) Debug(msg string, kv ...interface{}) { l.log("DEBUG", msg, kv...) }
func (l *capLogger) Info(msg string, kv ...interface{})  { l.log("INFO", msg, kv...) }
func (l *capLogger) Warn(msg string, kv ...interface{})  { l.log("WARN", msg, kv...) }
func (l *capLogger) Error(msg string, kv ...interface{}) { l.log("ERROR", msg, kv...) }

func TestZZDebug_ParallelCompete(t *testing.T) {
	wf := loadBuiltin(t, "parallel-compete")
	all := loadScenarios(t, "../../builtin/testdata/parallel-compete_scenarios.yaml")
	sc := findScenario(t, all, "parallel_compete_use_winner")

	var suite testsuite.WorkflowTestSuite
	lg := &capLogger{}
	suite.SetLogger(lg)
	env := suite.NewTestWorkflowEnvironment()

	rec := newRecorder()
	events := newEventTable(sc.Events)
	if err := (&Runner{workflow: wf}).registerActivities(env, rec, events); err != nil {
		t.Fatal(err)
	}

	env.ExecuteWorkflow(runtime.DynamicWorkflow, runtime.WorkflowInput{
		ChatID:       "scenario-chat",
		WorkflowName: wf.GetName(),
		Inputs:       map[string]interface{}{},
		ExecContext: &runtime.ExecutionContext{
			WorkflowID:   "scenario-wf",
			ChatID:       "scenario-chat",
			Thread:       "scenario-thread",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: wf.GetName(),
		},
	})

	t.Logf("REACHED: %v", rec.reached)
	t.Logf("COMPLETED: %v", rec.completed)
	if e := env.GetWorkflowError(); e != nil {
		t.Logf("WORKFLOW ERROR: %v", e)
	}
	for _, m := range events.unconsumed() {
		t.Logf("UNCONSUMED: %s", m)
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	for _, ln := range lg.lines {
		low := strings.ToLower(ln)
		if strings.Contains(low, "loop") || strings.Contains(low, "error") ||
			strings.Contains(low, "fail") || strings.Contains(low, "iteration") {
			t.Logf("LOG %s", ln)
		}
	}
}
