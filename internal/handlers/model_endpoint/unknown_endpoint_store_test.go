package model_endpoint_test

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/modelendpoints"
)

// unknownEndpointStore answers an endpoint lookup the way db.Repo does for an
// id the caller has no row for. The generated harness's stub answers (nil,
// nil) instead, which the manager reads as found. Every other method is
// unreachable from the rows that use it: the embedded nil Store makes calling
// one a panic, never a silent pass.
type unknownEndpointStore struct{ modelendpoints.Store }

func (unknownEndpointStore) GetModelEndpoint(context.Context, string, string) (*db.ModelEndpoint, error) {
	return nil, db.ErrModelEndpointNotFound
}
