package stubs_all

import "context"

// Service defines the service contract.
type Service interface {
	Get(ctx context.Context, id string) (string, error)
}
