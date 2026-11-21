// Package pingsvc is a handler whose method set matches internal/cliapp's
// pinger interface. cliapp reads a *connect.Error, but it is only ever handed
// the generated client, so pingsvc's errors never reach it in process: it
// gets bare kinds.
package pingsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Service is the handler.
type Service struct{}

// Ping refuses.
func (s *Service) Ping(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("ping is down"))
}
