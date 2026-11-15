package orm

import (
	"database/sql"
	"errors"
	"fmt"
)

// Common ORM errors
var (
	// ErrNoRows is returned when a query returns no rows
	ErrNoRows = sql.ErrNoRows

	// ErrTxDone is returned when a transaction is already committed or rolled back
	ErrTxDone = sql.ErrTxDone

	// ErrConnDone is returned when the connection is already closed
	ErrConnDone = sql.ErrConnDone

	// ErrNoPrimaryKey is returned when attempting operations on a message without a primary key
	ErrNoPrimaryKey = errors.New("orm: message has no primary key defined")

	// ErrInvalidDialect is returned when an invalid or unregistered dialect is specified
	ErrInvalidDialect = errors.New("orm: invalid or unregistered dialect")

	// ErrNilContext is returned when a nil context is provided
	ErrNilContext = errors.New("orm: nil context provided")

	// ErrSchemaValidationFailed is returned when schema validation fails
	ErrSchemaValidationFailed = errors.New("orm: schema validation failed")
)

// UnknownFieldError is returned by the generated Update<Entity>Masked
// helpers when an update_mask path names a column that is not in the
// entity's updatable set — unknown columns, the primary key, and
// immutable bookkeeping columns (created_at, deleted_at) all
// qualify. pkg/crud maps it to CodeInvalidArgument with a clean,
// SQL-free message naming the path.
type UnknownFieldError struct {
	Field string
}

func (e *UnknownFieldError) Error() string {
	return fmt.Sprintf("orm: unknown or immutable field %q in update_mask", e.Field)
}

// QueryError represents an error that occurred during a query execution
type QueryError struct {
	Query string
	Err   error
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("orm: query failed: %v\nQuery: %s", e.Err, e.Query)
}

func (e *QueryError) Unwrap() error {
	return e.Err
}

// NewQueryError creates a new QueryError
func NewQueryError(query string, err error) error {
	return &QueryError{
		Query: query,
		Err:   err,
	}
}

// TransactionError represents an error that occurred during a transaction
type TransactionError struct {
	Operation string
	Err       error
}

func (e *TransactionError) Error() string {
	return fmt.Sprintf("orm: transaction %s failed: %v", e.Operation, e.Err)
}

func (e *TransactionError) Unwrap() error {
	return e.Err
}

// NewTransactionError creates a new TransactionError
func NewTransactionError(operation string, err error) error {
	return &TransactionError{
		Operation: operation,
		Err:       err,
	}
}

// SchemaError represents a schema-related error
type SchemaError struct {
	Table   string
	Message string
	Err     error
}

func (e *SchemaError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("orm: schema error for table %s: %s: %v", e.Table, e.Message, e.Err)
	}
	return fmt.Sprintf("orm: schema error for table %s: %s", e.Table, e.Message)
}

func (e *SchemaError) Unwrap() error {
	return e.Err
}

// NewSchemaError creates a new SchemaError
func NewSchemaError(table, message string, err error) error {
	return &SchemaError{
		Table:   table,
		Message: message,
		Err:     err,
	}
}
