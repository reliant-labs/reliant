package stubs

import "context"

type service struct{}

// Get is already implemented.
func (s *service) Get(ctx context.Context, id string) (string, error) {
	return id, nil
}
