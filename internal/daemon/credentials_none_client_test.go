// Copyright (c) 2025 Reliant Labs

package daemon

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

type commandRecorder struct {
	Client
	reqs []RunCommandRequest
}

func (c *commandRecorder) RunCommand(_ context.Context, req *RunCommandRequest) (*CommandResult, error) {
	c.reqs = append(c.reqs, *req)
	return &CommandResult{}, nil
}

func (c *commandRecorder) StartBackground(_ context.Context, req *RunCommandRequest) (string, error) {
	c.reqs = append(c.reqs, *req)
	return "p1", nil
}

func TestForceCredentialsNoneForcesEveryCommand(t *testing.T) {
	rec := &commandRecorder{}
	c := ForceCredentialsNone(rec)
	asked := &RunCommandRequest{Command: "git push", Env: map[string]string{"GH_TOKEN": "ghp_x", "KEEP": "1"}}
	if _, err := c.RunCommand(context.Background(), asked); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartBackground(context.Background(), asked); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 2 {
		t.Fatalf("sent %d commands, want 2", len(rec.reqs))
	}
	for _, got := range rec.reqs {
		if got.Credentials != daemonpolicy.CredentialsNone {
			t.Errorf("Credentials = %q, want none", got.Credentials)
		}
		if got.Env["GH_TOKEN"] != "" || got.Env["KEEP"] != "1" || got.Env["GIT_TERMINAL_PROMPT"] != "0" {
			t.Errorf("env = %v: want the neutral environment over the caller's", got.Env)
		}
	}
	if asked.Credentials != "" || asked.Env["GH_TOKEN"] != "ghp_x" {
		t.Error("the caller's request was modified")
	}
	if ForceCredentialsNone(c) != c {
		t.Error("wrapping twice must not stack wrappers")
	}
	if ForceCredentialsNone(nil) != nil {
		t.Error("a nil client stays nil")
	}
}

func TestIsRemoteLooksThroughTheWrapper(t *testing.T) {
	remote := NewRemoteClient(nil, "u")
	if !IsRemote(remote) || !IsRemote(ForceCredentialsNone(remote)) {
		t.Fatal("a wrapped RemoteClient is still remote")
	}
	if IsRemote(ForceCredentialsNone(&commandRecorder{})) || IsRemote(nil) {
		t.Fatal("a local client is not remote")
	}
}
