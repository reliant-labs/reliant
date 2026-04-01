package compat

import (
	"errors"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err  error
		want ErrorKind
	}{
		{nil, ErrorKindNone},
		{errors.New("MCP error -32602: Invalid parameters"), ErrorKindInvalidParams},
		{errors.New("path [\"params\",\"application/json\"] Required"), ErrorKindSchemaMismatch},
		{errors.New("connection reset"), ErrorKindNonRetryable},
	}

	for _, c := range cases {
		if got := ClassifyError(c.err); got != c.want {
			t.Fatalf("ClassifyError(%v)=%v want %v", c.err, got, c.want)
		}
	}
}

func TestShouldRetry(t *testing.T) {
	if !ShouldRetry(ErrorKindInvalidParams, 0, 2) {
		t.Fatal("expected retry for invalid params")
	}
	if ShouldRetry(ErrorKindNonRetryable, 0, 2) {
		t.Fatal("did not expect retry for non-retryable")
	}
	if ShouldRetry(ErrorKindInvalidParams, 1, 2) {
		t.Fatal("did not expect retry on last attempt")
	}
}
