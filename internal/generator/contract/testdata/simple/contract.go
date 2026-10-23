package simple

import "context"

// Service defines the simple service contract.
type Service interface {
	Get(ctx context.Context, id string) (string, error)
	Set(ctx context.Context, key string, value string) error
}
