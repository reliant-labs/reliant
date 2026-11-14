// Copyright (c) 2025 Reliant Labs

package daemon

import (
	"context"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// ForceCredentialsNone returns c with every command it runs or starts forced
// to `credentials: none` (daemonpolicy.CredentialsNone), whatever the request
// asked for, and its environment neutralised (daemonpolicy.NeutralizeEnv) so
// a machine that predates the `credentials` field still applies the
// environment half. It is the one wrapper an org's forced `credentials: none`
// (control-plane docs/design/org-policies.md §4.6) puts on the daemon client
// a tool receives: a tool that builds a command, on the worker or on the
// machine, reaches it only through RunCommand or StartBackground.
//
// Everything else passes through to c. A nil c is returned as is.
func ForceCredentialsNone(c Client) Client {
	if c == nil {
		return nil
	}
	if _, already := c.(credentialsNoneClient); already {
		return c
	}
	return credentialsNoneClient{Client: c}
}

type credentialsNoneClient struct{ Client }

// Unwrap is the client underneath.
func (c credentialsNoneClient) Unwrap() Client { return c.Client }

func (c credentialsNoneClient) RunCommand(ctx context.Context, req *RunCommandRequest) (*CommandResult, error) {
	return c.Client.RunCommand(ctx, WithCredentialsNone(req))
}

func (c credentialsNoneClient) StartBackground(ctx context.Context, req *RunCommandRequest) (string, error) {
	return c.Client.StartBackground(ctx, WithCredentialsNone(req))
}

// WithCredentialsNone is a copy of req that runs with `credentials: none`
// and the neutral environment over its own. nil stays nil.
func WithCredentialsNone(req *RunCommandRequest) *RunCommandRequest {
	if req == nil {
		return nil
	}
	forced := *req
	forced.Credentials = daemonpolicy.CredentialsNone
	forced.Env = daemonpolicy.NeutralizeEnv(req.Env)
	return &forced
}

// IsRemote reports whether c reaches its machine over the network (a
// RemoteClient), looking through wrappers such as ForceCredentialsNone.
func IsRemote(c Client) bool {
	for c != nil {
		if _, remote := c.(*RemoteClient); remote {
			return true
		}
		w, ok := c.(interface{ Unwrap() Client })
		if !ok {
			return false
		}
		c = w.Unwrap()
	}
	return false
}
