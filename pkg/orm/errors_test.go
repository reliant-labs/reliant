package orm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestQueryError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := NewQueryError("SELECT * FROM users", inner)

	// Error message should contain both query and error
	msg := err.Error()
	if !strings.Contains(msg, "SELECT * FROM users") {
		t.Errorf("error should contain query, got: %s", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("error should contain inner error, got: %s", msg)
	}

	// Should unwrap to inner error
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatal("expected error to be QueryError")
	}
	if qe.Query != "SELECT * FROM users" {
		t.Errorf("expected query, got: %s", qe.Query)
	}
	if !errors.Is(err, inner) {
		t.Error("expected Unwrap to return inner error")
	}
}

func TestTransactionError(t *testing.T) {
	inner := fmt.Errorf("deadlock detected")
	err := NewTransactionError("commit", inner)

	msg := err.Error()
	if !strings.Contains(msg, "commit") {
		t.Errorf("error should contain operation, got: %s", msg)
	}
	if !strings.Contains(msg, "deadlock detected") {
		t.Errorf("error should contain inner error, got: %s", msg)
	}

	var te *TransactionError
	if !errors.As(err, &te) {
		t.Fatal("expected error to be TransactionError")
	}
	if te.Operation != "commit" {
		t.Errorf("expected operation 'commit', got: %s", te.Operation)
	}
	if !errors.Is(err, inner) {
		t.Error("expected Unwrap to return inner error")
	}
}

func TestSchemaError(t *testing.T) {
	t.Run("with inner error", func(t *testing.T) {
		inner := fmt.Errorf("column missing")
		err := NewSchemaError("users", "migration failed", inner)

		msg := err.Error()
		if !strings.Contains(msg, "users") {
			t.Errorf("error should contain table name, got: %s", msg)
		}
		if !strings.Contains(msg, "migration failed") {
			t.Errorf("error should contain message, got: %s", msg)
		}
		if !strings.Contains(msg, "column missing") {
			t.Errorf("error should contain inner error, got: %s", msg)
		}

		var se *SchemaError
		if !errors.As(err, &se) {
			t.Fatal("expected error to be SchemaError")
		}
		if !errors.Is(err, inner) {
			t.Error("expected Unwrap to return inner error")
		}
	})

	t.Run("without inner error", func(t *testing.T) {
		err := NewSchemaError("orders", "table not found", nil)

		msg := err.Error()
		if !strings.Contains(msg, "orders") {
			t.Errorf("error should contain table name, got: %s", msg)
		}
		if !strings.Contains(msg, "table not found") {
			t.Errorf("error should contain message, got: %s", msg)
		}

		var se *SchemaError
		if !errors.As(err, &se) {
			t.Fatal("expected error to be SchemaError")
		}
		if se.Err != nil {
			t.Error("expected nil inner error")
		}
	})
}

func TestSentinelErrors(t *testing.T) {
	// Verify sentinel errors are distinct
	sentinels := []error{
		ErrNoPrimaryKey,
		ErrInvalidDialect,
		ErrNilContext,
		ErrSchemaValidationFailed,
	}

	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinel errors %d and %d should be distinct", i, j)
			}
		}
	}
}

func TestUnknownFieldError(t *testing.T) {
	err := &UnknownFieldError{Field: "nope"}

	// Message names the offending path and stays SQL-free.
	msg := err.Error()
	if !strings.Contains(msg, "nope") {
		t.Errorf("error should name the field, got: %s", msg)
	}
	for _, leak := range []string{"SELECT", "UPDATE", "sql:"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error message leaks SQL-ish text (%q): %s", leak, msg)
		}
	}

	// Must survive %w wrapping — pkg/crud classifies it via errors.As.
	wrapped := fmt.Errorf("update items: %w", err)
	var uf *UnknownFieldError
	if !errors.As(wrapped, &uf) {
		t.Fatal("expected errors.As to find UnknownFieldError through a %w wrap")
	}
	if uf.Field != "nope" {
		t.Errorf("Field = %q, want %q", uf.Field, "nope")
	}
}
