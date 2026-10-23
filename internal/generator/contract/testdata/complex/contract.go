package complex

import (
	"context"
	"database/sql"
)

// Store defines a complex service contract with variadic args,
// pointer returns, and multi-package types.
type Store interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Subscribe(ctx context.Context, topic string) (<-chan []byte, error)
	Transform(ctx context.Context, fn func([]byte) ([]byte, error)) error
}
