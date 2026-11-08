// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// The real thing, as every DB-backed test reaches it.
func TestCheckServesADatabaseFromTheSharedServer(t *testing.T) {
	t.Cleanup(pgtest.Shutdown)
	if err := check(context.Background(), 150*time.Second, pgtest.New); err != nil {
		t.Fatal(failure(err))
	}
}

func TestCheckReportsAServerThatCannotServeADatabase(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	err := check(context.Background(), time.Second, func() (*sql.DB, func(), error) { return nil, nil, refused })
	if !errors.Is(err, refused) {
		t.Fatalf("check must return pgtest's error, got %v", err)
	}
	msg := failure(err)
	for _, want := range []string{"FAILED", "shared", "connection refused", "not this change"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure message must say %q:\n%s", want, msg)
		}
	}
}

func TestCheckGivesUpOnAServerThatNeverAnswers(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	err := check(context.Background(), 50*time.Millisecond, func() (*sql.DB, func(), error) {
		<-block
		return nil, nil, errors.New("unreachable")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("check must time out, got %v", err)
	}
	if msg := failure(err); !strings.Contains(msg, "did not answer in time") {
		t.Errorf("a timeout must say so:\n%s", msg)
	}
}
