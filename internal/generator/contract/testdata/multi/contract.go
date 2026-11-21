package multi

import "context"

// Reader defines read operations.
type Reader interface {
	Get(ctx context.Context, key string) (string, error)
	List(ctx context.Context) ([]string, error)
}

// Writer defines write operations.
type Writer interface {
	Put(ctx context.Context, key string, value string) error
	Delete(ctx context.Context, key string) error
}
