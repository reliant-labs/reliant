// Package cliapp is a CLI. Its pinger interface is satisfied by
// *pingsvc.Service as well as by the generated client — a Connect client and
// handler share a method set — but all it is ever handed is the client. So
// its connect.CodeOf reads errors off the wire, and pingsvc is free to return
// bare kinds (reliant's cmd/reliant/cmd and its triggerDaemonLister).
package cliapp

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"example.com/cescratch/internal/wireclient/pingv1connect"
)

type pinger interface {
	Ping(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error)
}

// CLI talks to a server.
type CLI struct{ p pinger }

// New dials url.
func New(httpClient connect.HTTPClient, url string) *CLI {
	return &CLI{p: pingv1connect.NewPingServiceClient(httpClient, url)}
}

// Up pings the server.
func (c *CLI) Up(ctx context.Context) bool {
	_, err := c.p.Ping(ctx, connect.NewRequest(&emptypb.Empty{}))
	return !refused(err)
}

func refused(err error) bool { return connect.CodeOf(err) == connect.CodeUnavailable }
