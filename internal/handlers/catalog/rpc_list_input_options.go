// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListInputOptions is declared by the typed-inputs contract; the options
// service that answers it lands separately.
func (s *Service) ListInputOptions(ctx context.Context, req *connect.Request[reliantv1.ListInputOptionsRequest]) (*connect.Response[reliantv1.ListInputOptionsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListInputOptions is not implemented"))
}
