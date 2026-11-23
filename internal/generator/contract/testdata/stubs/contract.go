package stubs

import "context"

// Service defines the service contract.
type Service interface {
	Get(ctx context.Context, id string) (string, error)
	Set(ctx context.Context, key string, value string) error
	Delete(ctx context.Context, id string) error
}
