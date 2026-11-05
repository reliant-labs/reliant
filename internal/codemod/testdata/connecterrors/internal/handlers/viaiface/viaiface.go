// Package viaiface is a handler package that nothing imports and that has no
// reader of its own. internal/lister still reads its errors in process:
// lister declares Getter, which *Service implements, and composition hands
// it one. lister reads a *connect.Error there, so viaiface's rewrites stay
// *connect.Error — and the report says it is lister, through Getter.Get.
package viaiface

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// Service is the handler.
type Service struct{}

// Get fails the way a by-key get does.
func (s *Service) Get(context.Context, string) (string, error) {
	return "", connect.NewError(connect.CodeNotFound, errors.New("widget not found"))
}
