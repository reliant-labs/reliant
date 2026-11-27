// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/machinepreflight"
)

// machine.preflight answers the check's verdict on this machine, a failure
// included, and refuses only a payload it cannot read.
func TestHandleMachinePreflight(t *testing.T) {
	orig := preflightEnv
	t.Cleanup(func() { preflightEnv = orig })
	preflightEnv = func() machinepreflight.Env {
		return machinepreflight.Env{LookPath: func(name string) (string, error) {
			if name == "git" {
				return "/usr/bin/git", nil
			}
			return "", errors.New("not found")
		}}
	}

	for _, tt := range []struct {
		name   string
		tools  []string
		wantOK bool
	}{
		{name: "passes", wantOK: true},
		{name: "a missing tool fails", tools: []string{"task"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(machinepreflight.Request{Spec: machinepreflight.Spec{Tools: tt.tools}})
			raw, err := DefaultRegistry().Handle(context.Background(), machinepreflight.Command, payload)
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var res machinepreflight.Result
			if err := json.Unmarshal(raw, &res); err != nil {
				t.Fatal(err)
			}
			if res.OK != tt.wantOK {
				t.Fatalf("got %+v, want ok %v", res, tt.wantOK)
			}
			if !tt.wantOK && res.Primary().Reason != machinepreflight.ReasonToolMissing {
				t.Fatalf("reason %s, want tool_missing", res.Primary().Reason)
			}
		})
	}
	if _, err := DefaultRegistry().Handle(context.Background(), machinepreflight.Command, []byte("{")); err == nil {
		t.Fatal("an unreadable payload was answered")
	}
}
